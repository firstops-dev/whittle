package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Request is a mutable view of an outbound Anthropic request during
// reconciliation. Body is the parsed JSON as a generic map so strip transforms
// can edit arbitrary fields and messages; Headers is the outbound header set
// (anthropic-beta lives here). The proxy serializes Body back to bytes and
// recomputes Content-Length AFTER reconciliation (docs/ROUTER_RECONCILIATION.md
// "Strip mechanics": parse → strip → re-serialize, accepting the prompt-cache
// miss on routed requests only).
type Request struct {
	Body    map[string]any
	Headers http.Header
}

// ParseRequest builds a Request from a raw body + headers. A parse error leaves
// the caller to take the Mode-A path (forward original untouched).
//
// CONTRACT (review C3): hdr MUST be a canonical net/http.Header (the beta-token
// helpers use Get/Set/Del, which canonicalize "anthropic-beta" → "Anthropic-Beta").
// Passing http.Request.Header satisfies this. If the proxy ever builds Headers by
// raw lowercase map assignment (e.g. straight from HTTP/2 frames), Get misses and
// ALL four strips silently no-op → 400s. The proxy must canonicalize on the way in.
func ParseRequest(body []byte, hdr http.Header) (*Request, error) {
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber() // preserve integer token counts etc. across a round-trip
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("parse request for reconcile: %w", err)
	}
	if hdr == nil {
		hdr = http.Header{}
	}
	return &Request{Body: m, Headers: hdr}, nil
}

// Serialize marshals the (possibly mutated) body back to JSON. The caller sets
// Content-Length from len(result).
func (r *Request) Serialize() ([]byte, error) {
	return json.Marshal(r.Body)
}

func (r *Request) model() string {
	s, _ := r.Body["model"].(string)
	return s
}

func (r *Request) setModel(m string) { r.Body["model"] = m }

// ---- anthropic-beta header token helpers ----
//
// The header is a comma-separated token list. We remove individual tokens by
// prefix (a feature owns one or more beta tokens) and drop the header entirely
// if nothing remains — never leave an empty or comma-dangling value (review M2).

const betaHeader = "anthropic-beta"

func (r *Request) betaTokens() []string {
	raw := r.Headers.Get(betaHeader)
	if raw == "" {
		return nil
	}
	var out []string
	for _, t := range strings.Split(raw, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// hasBetaPrefix reports whether any beta token starts with prefix.
func (r *Request) hasBetaPrefix(prefix string) bool {
	for _, t := range r.betaTokens() {
		if strings.HasPrefix(t, prefix) {
			return true
		}
	}
	return false
}

// removeBetaPrefix drops every beta token starting with prefix, re-joins the
// rest, and removes the header if empty. Returns whether anything was removed.
func (r *Request) removeBetaPrefix(prefix string) bool {
	toks := r.betaTokens()
	if len(toks) == 0 {
		return false
	}
	kept := toks[:0:0]
	removed := false
	for _, t := range toks {
		if strings.HasPrefix(t, prefix) {
			removed = true
			continue
		}
		kept = append(kept, t)
	}
	if !removed {
		return false
	}
	if len(kept) == 0 {
		r.Headers.Del(betaHeader)
	} else {
		r.Headers.Set(betaHeader, strings.Join(kept, ","))
	}
	return true
}

// hasBetaContaining reports whether any beta token contains substr (case-insensitive).
func (r *Request) hasBetaContaining(substr string) bool {
	substr = strings.ToLower(substr)
	for _, t := range r.betaTokens() {
		if strings.Contains(strings.ToLower(t), substr) {
			return true
		}
	}
	return false
}

// removeBetaContaining drops every beta token containing substr (case-insensitive)
// and removes the header if empty. Used to strip an OPEN-ENDED family of dependent
// betas — e.g. every "thinking" beta (interleaved-thinking, thinking-token-count,
// clear_thinking_…) when the thinking capability is disabled — since a lone
// dependent beta whose feature we removed 400s ("requires thinking to be enabled").
func (r *Request) removeBetaContaining(substr string) bool {
	toks := r.betaTokens()
	if len(toks) == 0 {
		return false
	}
	substr = strings.ToLower(substr)
	kept := toks[:0:0]
	removed := false
	for _, t := range toks {
		if strings.Contains(strings.ToLower(t), substr) {
			removed = true
			continue
		}
		kept = append(kept, t)
	}
	if !removed {
		return false
	}
	if len(kept) == 0 {
		r.Headers.Del(betaHeader)
	} else {
		r.Headers.Set(betaHeader, strings.Join(kept, ","))
	}
	return true
}

// ---- message/content helpers (generic map[string]any shape) ----

// messages returns the mutable []any of message maps, or nil.
func (r *Request) messages() []any {
	m, _ := r.Body["messages"].([]any)
	return m
}

func msgRole(m any) string {
	mm, _ := m.(map[string]any)
	s, _ := mm["role"].(string)
	return s
}

// contentBlocksOf normalizes a message's content to a slice of block maps. A
// bare string becomes a single text block; an array is returned as-is; anything
// else yields nil.
func contentBlocksOf(m any) []any {
	mm, _ := m.(map[string]any)
	switch c := mm["content"].(type) {
	case string:
		return []any{map[string]any{"type": "text", "text": c}}
	case []any:
		return c
	default:
		return nil
	}
}

func blockType(b any) string {
	bm, _ := b.(map[string]any)
	s, _ := bm["type"].(string)
	return s
}
