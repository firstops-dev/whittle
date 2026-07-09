package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Signals is the heuristic view of a request that the decision engine routes on.
// Every field here is cheap (no model call). The ML fields (Intent/IntentConf)
// are filled lazily by the engine only if a route or classify needs them.
type Signals struct {
	RequestedModel string // body.model, canonicalized (date/-latest stripped)
	ContextTokens  int    // whole-body bytes/4 — the cost/cache scale, ~20k floor
	LastUserText   string // most recent user-authored text (never tool output)
	RecentText     string // user text across the inspect window (classifier input)
	ToolLoop       bool    // last message is role:user AND carries a tool_result block
	MessageCount   int
	HasTools       bool
	SessionID      string // X-Claude-Code-Session-Id; "" ⇒ caller skips stickiness

	// Filled lazily by the engine (not by Extract):
	Intent     string
	IntentConf float64
}

// anthropicReq is the minimal projection of the Anthropic Messages body we read.
// Unknown fields are ignored (this is NOT strict decoding — the request has many
// fields we don't model). Content/System are RawMessage because each may be a
// bare string OR an array of typed blocks.
type anthropicReq struct {
	Model    string            `json:"model"`
	Stream   bool              `json:"stream"`
	System   json.RawMessage   `json:"system"`
	Messages []anthropicMsg    `json:"messages"`
	Tools    []json.RawMessage `json:"tools"`
}

type anthropicMsg struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Extract parses an Anthropic Messages request body once into Signals. It never
// panics on malformed input — a parse error is returned so the caller takes the
// Mode-A path (forward the original untouched). `sessionID` is the
// X-Claude-Code-Session-Id header value (the proxy layer reads it, keeping this
// core HTTP-free); "" is fine (stickiness is skipped downstream).
func Extract(body []byte, sessionID string, ins InspectCfg) (Signals, error) {
	s := Signals{
		// ContextTokens uses the RAW body length (system + tools + messages) ÷ 4
		// — the scale that actually drives cost and prompt-cache size. Author
		// thresholds against this whole-request scale (a ~20k floor is normal).
		ContextTokens: len(body) / 4,
		SessionID:     sessionID,
	}

	var req anthropicReq
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&req); err != nil {
		return s, fmt.Errorf("parse request body: %w", err)
	}

	s.RequestedModel = canonicalModel(req.Model)
	s.MessageCount = len(req.Messages)
	s.HasTools = len(req.Tools) > 0
	s.ToolLoop = lastMessageIsToolResult(req.Messages)

	last, recent := extractUserText(req.Messages, ins)
	s.LastUserText = last
	s.RecentText = recent
	return s, nil
}

// lastMessageIsToolResult implements the PRECISE ToolLoop predicate: the final
// message has role:user and contains at least one tool_result block. This is an
// agent-loop continuation. It is deliberately NOT "any assistant ever emitted a
// tool_use" (true from turn ~2 of every session — would pin every session).
func lastMessageIsToolResult(msgs []anthropicMsg) bool {
	if len(msgs) == 0 {
		return false
	}
	last := msgs[len(msgs)-1]
	if last.Role != "user" {
		return false
	}
	for _, b := range decodeBlocks(last.Content) {
		if b.Type == "tool_result" {
			return true
		}
	}
	return false
}

// extractUserText returns (lastUserText, recentText). Only role:user `text`
// blocks contribute — tool_result / tool_use / thinking / image blocks are
// excluded so the classifier never sees tool output or model scratchpad. A
// trailing tool_result-only user turn is skipped when finding LastUserText
// (walk back to genuine human text).
//
// recentText honors InspectCfg: last_user_turn ⇒ the single latest user text;
// recent_turns ⇒ the last N user messages' text; full ⇒ all user text.
func extractUserText(msgs []anthropicMsg, ins InspectCfg) (last, recent string) {
	// Collect per-user-message text, oldest→newest, skipping empties.
	var userTexts []string
	for _, m := range msgs {
		if m.Role != "user" {
			continue
		}
		txt := userTextOf(m.Content)
		if strings.TrimSpace(txt) == "" {
			continue // e.g. a tool_result-only turn contributes no human text
		}
		userTexts = append(userTexts, txt)
	}
	if len(userTexts) == 0 {
		return "", ""
	}
	last = userTexts[len(userTexts)-1]

	switch ins.Scope {
	case "last_user_turn":
		recent = last
	case "recent_turns":
		n := ins.Turns
		if n <= 0 || n > len(userTexts) {
			n = len(userTexts)
		}
		recent = strings.Join(userTexts[len(userTexts)-n:], "\n")
	default: // "full" or unset
		recent = strings.Join(userTexts, "\n")
	}
	return last, recent
}

// userTextOf flattens the `text` blocks of one message's content. Content may be
// a bare string (⇒ itself) or an array of blocks (⇒ join text blocks only).
func userTextOf(content json.RawMessage) string {
	if s, ok := decodeString(content); ok {
		return s
	}
	var parts []string
	for _, b := range decodeBlocks(content) {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// decodeString returns the string value of a RawMessage if it is a JSON string.
func decodeString(raw json.RawMessage) (string, bool) {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || t[0] != '"' {
		return "", false
	}
	var s string
	if err := json.Unmarshal(t, &s); err != nil {
		return "", false
	}
	return s, true
}

// decodeBlocks returns the typed content blocks if the RawMessage is an array;
// nil otherwise (bare-string content, or malformed — callers treat nil as "no
// blocks", which is safe).
func decodeBlocks(raw json.RawMessage) []contentBlock {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || t[0] != '[' {
		return nil
	}
	var blocks []contentBlock
	if err := json.Unmarshal(t, &blocks); err != nil {
		return nil
	}
	return blocks
}
