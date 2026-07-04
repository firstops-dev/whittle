package server

// POST /hook — Claude Code PostToolUse over HTTP: receives the hook event,
// returns hookSpecificOutput.updatedToolOutput when compression wins, or an
// empty 200 body (fail-open: Claude Code proceeds with the original).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/firstops-dev/whittle/compress"
)

// lossyStrategy: strategies that REDUCE content (marked or model-lossy) get a
// retrieval hint; lossless transforms (json columnar, ansi strip) get NONE —
// nothing was lost, so nothing is offered (and no hint tokens are spent).
func lossyStrategy(strategy string) bool {
	for _, m := range []string{"llmlingua", "log_compressor", "md_structured"} {
		if strings.Contains(strategy, m) {
			return true
		}
	}
	return false
}

func hookHandler(p *compress.Pipeline, store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK) // ALWAYS 200: a hook must never error a tool call
		var ev struct {
			HookEventName string          `json:"hook_event_name"`
			SessionID     string          `json:"session_id"`
			ToolName      string          `json:"tool_name"`
			ToolOutput    string          `json:"tool_output"`
			ToolResponse  json.RawMessage `json:"tool_response"`
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
		if err != nil || json.Unmarshal(body, &ev) != nil || ev.HookEventName != "PostToolUse" {
			return
		}
		text := ev.ToolOutput
		if text == "" {
			text, _ = extractHookText(ev.ToolResponse)
		}
		if len(text) < 256 {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
		defer cancel()
		out := p.Compress(ctx, compress.Input{Content: text, ToolName: ev.ToolName, MinTokens: -1})
		if out.Action != "compressed" || len(out.Output) >= len(text) {
			return
		}
		// Docs-verified: the 10,000-char cap on updatedToolOutput applies to HTTP
		// hooks as well as command hooks. Fail open above ~9.5k until the content
		// store + retrieval pointer lands (PLAN P2).
		final := out.Output
		// Retrieval hint — ONLY where content was actually reduced. Copy is
		// deliberately discouraging: the summary is complete; raw is for
		// byte-exact needs. Alias integers cost ~2 tokens (measured).
		final, storeID := finalizeReplacement(text, out.Output, out.Strategy, store)
		if final == "" {
			return // no honest win once all costs are counted: fail open
		}
		logEvent(ev.SessionID, ev.ToolName, out.Strategy, storeID,
			compress.EstimateTokens(text), compress.EstimateTokens(final))
		_ = json.NewEncoder(w).Encode(map[string]any{
			"hookSpecificOutput": map[string]any{
				"hookEventName":     "PostToolUse",
				"updatedToolOutput": final,
			},
		})
	}
}

func extractHookText(raw json.RawMessage) (string, bool) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, true
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return "", false
	}
	for _, k := range []string{"stdout", "output", "result"} {
		if v, ok := obj[k]; ok && json.Unmarshal(v, &s) == nil && s != "" {
			return s, true
		}
	}
	if f, ok := obj["file"]; ok {
		var file struct {
			Content string `json:"content"`
		}
		if json.Unmarshal(f, &file) == nil && file.Content != "" {
			return file.Content, true
		}
	}
	return "", false
}

// getHandler serves originals back to the whittle_get MCP tool. Misses are
// honest 404s ("expired") — the agent can re-run the tool.
func getHandler(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
		if err != nil || store == nil {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}
		content, ok := store.Get(id)
		if ok {
			logEvent("", "whittle_get", "retrieve", id, 0, 0)
		}
		if !ok {
			http.Error(w, "expired — re-run the tool for fresh output", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, content)
	}
}

// logEvent appends one compression record to ~/.whittle/stats.jsonl — the local,
// never-transmitted audit trail behind `whittle stats`. One line per whittled
// output: when, which session, which tool, which strategy, token delta, and the
// retrieval id (0 = lossless, nothing stored). Users can inspect it directly;
// originals of reduced outputs are retrievable via whittle_get(id) while cached.
func logEvent(session, tool, strategy string, storeID int64, inTok, outTok int) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(home, ".whittle", "stats.jsonl"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	b, _ := json.Marshal(map[string]any{
		"ts": time.Now().Unix(), "session": session, "tool": tool,
		"strategy": strategy, "id": storeID, "in_tokens": inTok, "out_tokens": outTok,
	})
	f.Write(append(b, '\n'))
}

const hintFmt = "\n… [trimmed; content above is complete in substance. Raw original only if strictly required: whittle_get(%d)]"

// finalizeReplacement enforces the POST-HINT invariant (review B1): whatever is
// emitted — with hint or without — must be strictly smaller than the original in
// BOTH bytes and estimated tokens, and within Claude Code's 10k output cap.
// Order of preference: compressed+hint; compressed alone (marginal wins keep the
// win, just without retrieval); nothing (fail open). The store alias is only
// spent when the hint actually emits (review O6).
func finalizeReplacement(text, output, strategy string, store *Store) (string, int64) {
	fits := func(s string) bool {
		return len(s) < len(text) && len(s) <= 9500 &&
			compress.EstimateTokens(s) < compress.EstimateTokens(text)
	}
	if store != nil && lossyStrategy(strategy) {
		// probe with a worst-case-width alias before spending one
		if probe := output + fmt.Sprintf(hintFmt, int64(99999999)); fits(probe) {
			id := store.Put(text)
			final := output + fmt.Sprintf(hintFmt, id)
			if fits(final) {
				return final, id
			}
		}
	}
	if fits(output) {
		return output, 0
	}
	return "", 0
}
