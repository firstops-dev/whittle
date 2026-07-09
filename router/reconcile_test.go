package router

import (
	"net/http"
	"strings"
	"testing"
)

// mkReq builds a Request from a JSON body and an optional anthropic-beta header.
func mkReq(t *testing.T, body, beta string) *Request {
	t.Helper()
	h := http.Header{}
	if beta != "" {
		h.Set("anthropic-beta", beta)
	}
	r, err := ParseRequest([]byte(body), h)
	if err != nil {
		t.Fatalf("ParseRequest: %v", err)
	}
	return r
}

func has(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// When thinking is disabled on the target, EVERY thinking beta must go — not just
// the known prefixes. A lone dependent beta (clear_thinking_…, confirmed live)
// 400s with "requires thinking to be enabled".
func TestReconcile_StripsOpenEndedThinkingBetas(t *testing.T) {
	r := mkReq(t, `{"model":"claude-opus-4-8","thinking":{"type":"enabled"},"messages":[{"role":"user","content":"hi"}]}`,
		"context-1m-2025-08-07,clear_thinking_20251015,interleaved-thinking-2025-05-14,thinking-token-count-2025-01-01")
	Reconcile(r, "claude-sonnet-4-5-20250929") // sonnet family floor: thinking unsupported
	if beta := r.Headers.Get("anthropic-beta"); strings.Contains(strings.ToLower(beta), "thinking") {
		t.Errorf("every thinking beta must be stripped, got %q", beta)
	}
	if _, has := r.Body["thinking"]; has {
		t.Error("thinking config must be removed")
	}
}

// A context_management edit that REQUIRES thinking (clear_thinking_…, confirmed
// live via headless claude) must be dropped when thinking is disabled — a leftover
// one 400s. Non-thinking edits stay.
func TestReconcile_StripsThinkingContextEdit(t *testing.T) {
	r := mkReq(t, `{"model":"claude-opus-4-8","thinking":{"type":"enabled"},
	  "context_management":{"edits":[{"type":"clear_thinking_20251015"},{"type":"clear_tool_uses_20250919"}]},
	  "messages":[{"role":"user","content":"hi"}]}`, "")
	Reconcile(r, "claude-sonnet-4-5-20250929") // sonnet floor: thinking off
	cm, ok := r.Body["context_management"].(map[string]any)
	if !ok {
		t.Fatal("a surviving non-thinking edit should keep context_management")
	}
	edits := cm["edits"].([]any)
	if len(edits) != 1 || edits[0].(map[string]any)["type"] != "clear_tool_uses_20250919" {
		t.Errorf("only the thinking edit should be dropped, got %v", edits)
	}
}

// If every edit required thinking, context_management is removed entirely (an
// empty edits array could itself be rejected).
func TestReconcile_DropsContextMgmtWhenAllThinking(t *testing.T) {
	r := mkReq(t, `{"model":"claude-opus-4-8","thinking":{"type":"enabled"},
	  "context_management":{"edits":[{"type":"clear_thinking_20251015"}]},
	  "messages":[{"role":"user","content":"hi"}]}`, "")
	Reconcile(r, "claude-sonnet-4-5-20250929")
	if _, has := r.Body["context_management"]; has {
		t.Error("context_management with only thinking edits should be removed")
	}
}

// B1: an UNKNOWN model is fully capable — Reconcile strips nothing, only sets
// the model. The zero-value trap (strip everything / unroutable) must not happen.
func TestReconcile_UnknownModelStripsNothing(t *testing.T) {
	r := mkReq(t, `{"model":"claude-opus-4-8","thinking":{"type":"enabled"},
	  "output_config":{"effort":"high"},"messages":[]}`,
		"context-1m-2025-08-07,effort-2025-11-24")
	stripped := Reconcile(r, "claude-neo-9-99") // not in the table
	if len(stripped) != 0 {
		t.Fatalf("unknown model should strip nothing, stripped: %v", stripped)
	}
	if r.model() != "claude-neo-9-99" {
		t.Fatalf("model not set: %q", r.model())
	}
	if _, ok := r.Body["thinking"]; !ok {
		t.Error("thinking should be untouched for a fully-capable unknown model")
	}
	if r.Headers.Get("anthropic-beta") == "" {
		t.Error("beta header should be untouched for an unknown model")
	}
}

// Down-routing to Haiku (known to reject all four) strips every feature present.
func TestReconcile_DownrouteHaikuStripsAll(t *testing.T) {
	r := mkReq(t, `{"model":"claude-opus-4-8",
	  "thinking":{"type":"enabled"},
	  "output_config":{"effort":"high"},
	  "messages":[{"role":"system","content":"be terse"},{"role":"user","content":"hi"}]}`,
		"context-1m-2025-08-07,effort-2025-11-24,mid-conversation-system-2026-04-07,other-beta")
	stripped := Reconcile(r, "claude-haiku-4-5")
	for _, want := range []string{"context-1m", "effort", "thinking", "midconv-system"} {
		if !has(stripped, want) {
			t.Errorf("expected %q stripped, got %v", want, stripped)
		}
	}
	if _, ok := r.Body["thinking"]; ok {
		t.Error("thinking config not removed")
	}
	if _, ok := r.Body["output_config"]; ok {
		t.Error("output_config should be gone (only had effort)")
	}
	beta := r.Headers.Get("anthropic-beta")
	if strings.Contains(beta, "context-1m") || strings.Contains(beta, "effort-") || strings.Contains(beta, "mid-conversation-system") {
		t.Errorf("stripped beta tokens still present: %q", beta)
	}
	if !strings.Contains(beta, "other-beta") {
		t.Errorf("unrelated beta token must be preserved: %q", beta)
	}
}

// Up-routing (target MORE capable) strips nothing.
func TestReconcile_UprouteStripsNothing(t *testing.T) {
	r := mkReq(t, `{"model":"claude-haiku-4-5","thinking":{"type":"enabled"},"messages":[]}`, "context-1m-2025-08-07")
	stripped := Reconcile(r, "claude-opus-4-8")
	if len(stripped) != 0 {
		t.Fatalf("up-route should strip nothing, got %v", stripped)
	}
}

// Degenerate: stripping the ONLY beta token removes the header entirely (no empty value).
func TestReconcile_EmptyBetaHeaderRemoved(t *testing.T) {
	r := mkReq(t, `{"model":"m","messages":[]}`, "context-1m-2025-08-07")
	r.removeBetaPrefix("context-1m")
	if _, ok := r.Headers["Anthropic-Beta"]; ok {
		t.Errorf("beta header should be deleted when empty, got %q", r.Headers.Get("anthropic-beta"))
	}
}

// Degenerate: an assistant message emptied by thinking-strip is dropped.
func TestReconcile_EmptiedAssistantMessageDropped(t *testing.T) {
	r := mkReq(t, `{"model":"m","thinking":{"type":"enabled"},"messages":[
	  {"role":"user","content":"go"},
	  {"role":"assistant","content":[{"type":"thinking","thinking":"..."}]},
	  {"role":"user","content":"and then"}
	]}`, "")
	Reconcile(r, "claude-haiku-4-5")
	msgs := r.messages()
	// The thinking-only assistant is dropped; the two adjacent users that
	// result are coalesced by repairAlternation into one — no adjacency, no
	// thinking survives.
	for i := 1; i < len(msgs); i++ {
		if msgRole(msgs[i]) == msgRole(msgs[i-1]) {
			t.Fatalf("adjacent same-role after thinking-drop repair at %d", i)
		}
	}
	for _, m := range msgs {
		for _, b := range contentBlocksOf(m) {
			if blockType(b) == "thinking" {
				t.Error("a thinking block survived history strip")
			}
		}
	}
}

// A real assistant turn that carries a tool_use (not thinking-only) must NOT be
// dropped by the thinking strip — dropping it would orphan the following
// tool_result. Only its thinking block is removed; the tool_use stays.
func TestReconcile_ToolUseAssistantNotDropped(t *testing.T) {
	r := mkReq(t, `{"model":"m","thinking":{"type":"enabled"},"messages":[
	  {"role":"user","content":"run it"},
	  {"role":"assistant","content":[{"type":"thinking","thinking":"..."},{"type":"tool_use","id":"t","name":"Bash","input":{}}]},
	  {"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"ok"}]}
	]}`, "")
	Reconcile(r, "claude-haiku-4-5")
	msgs := r.messages()
	if len(msgs) != 3 {
		t.Fatalf("tool_use assistant must survive (only thinking stripped); got %d messages", len(msgs))
	}
	// The assistant keeps its tool_use; the tool_result stays paired after it.
	if msgRole(msgs[1]) != "assistant" {
		t.Fatalf("message[1] should still be the assistant tool_use turn, got %s", msgRole(msgs[1]))
	}
	foundToolUse := false
	for _, b := range contentBlocksOf(msgs[1]) {
		switch blockType(b) {
		case "thinking":
			t.Error("thinking block should be stripped from the tool_use assistant")
		case "tool_use":
			foundToolUse = true
		}
	}
	if !foundToolUse {
		t.Error("tool_use block was lost")
	}
}

// B3: converting a mid-conversation system message must not leave adjacent
// same-role messages — coalescing guarantees valid alternation.
func TestReconcile_MidConvSystemCoalesces(t *testing.T) {
	r := mkReq(t, `{"model":"m","messages":[
	  {"role":"user","content":"hello"},
	  {"role":"system","content":"be terse"},
	  {"role":"assistant","content":"ok"}
	]}`, "mid-conversation-system-2026-04-07")
	Reconcile(r, "claude-haiku-4-5")
	msgs := r.messages()
	// user + (system→user) coalesce into one user; then assistant → 2 messages.
	if len(msgs) != 2 {
		t.Fatalf("expected coalesced to 2 messages, got %d", len(msgs))
	}
	if msgRole(msgs[0]) != "user" || msgRole(msgs[1]) != "assistant" {
		t.Fatalf("roles should alternate user,assistant; got %s,%s", msgRole(msgs[0]), msgRole(msgs[1]))
	}
	// No two adjacent same-role.
	for i := 1; i < len(msgs); i++ {
		if msgRole(msgs[i]) == msgRole(msgs[i-1]) {
			t.Fatalf("adjacent same-role messages at %d", i)
		}
	}
	// The coalesced first message carries both texts.
	var texts []string
	for _, b := range contentBlocksOf(msgs[0]) {
		if bm, ok := b.(map[string]any); ok {
			if s, _ := bm["text"].(string); s != "" {
				texts = append(texts, s)
			}
		}
	}
	joined := strings.Join(texts, " ")
	if !strings.Contains(joined, "hello") || !strings.Contains(joined, "be terse") {
		t.Errorf("coalesced content lost text: %q", joined)
	}
}

// effort with a multi-key output_config keeps the other keys.
func TestReconcile_EffortKeepsOtherOutputConfig(t *testing.T) {
	r := mkReq(t, `{"model":"m","output_config":{"effort":"high","format":"json"},"messages":[]}`, "")
	Reconcile(r, "claude-haiku-4-5")
	oc, ok := r.Body["output_config"].(map[string]any)
	if !ok {
		t.Fatal("output_config should remain (still has format)")
	}
	if _, has := oc["effort"]; has {
		t.Error("effort should be removed")
	}
	if oc["format"] != "json" {
		t.Error("unrelated output_config key must be preserved")
	}
}

// CanServe applies the 0.9 margin and treats unknown models as unbounded.
func TestCanServe(t *testing.T) {
	// Haiku window 200k, margin → 180k.
	if !CanServe("claude-haiku-4-5", 179_000) {
		t.Error("179k should fit within the 180k margin")
	}
	if CanServe("claude-haiku-4-5", 190_000) {
		t.Error("190k should exceed the margin and be rejected")
	}
	if !CanServe("claude-unknown-model", 5_000_000) {
		t.Error("unknown model is unbounded — should always serve")
	}
}

// Serialize round-trips a reconciled body back to valid JSON with the new model.
func TestReconcile_SerializeRoundTrip(t *testing.T) {
	r := mkReq(t, `{"model":"claude-opus-4-8","output_config":{"effort":"high"},"messages":[]}`, "effort-2025-11-24")
	Reconcile(r, "claude-haiku-4-5")
	out, err := r.Serialize()
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, `"claude-haiku-4-5"`) {
		t.Errorf("serialized body missing new model: %s", s)
	}
	if strings.Contains(s, "effort") {
		t.Errorf("serialized body still has effort: %s", s)
	}
}
