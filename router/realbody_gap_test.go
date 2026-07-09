package router

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// buildRealBody reconstructs a request shaped like the captured Claude Code
// traffic (experiment/captures_gate1/req_0004.json body_top_keys): system as an
// array of text blocks with a cache_control breakpoint, 30 tools, thinking config,
// output_config.effort, metadata, context_management, a large max_tokens, stream.
// Message history includes a mid-conversation system message and a
// tool_use/tool_result pair with a thinking block, to exercise all four strips at
// once.
func buildRealBody(t *testing.T) []byte {
	t.Helper()
	tools := make([]any, 0, 30)
	for i := 0; i < 30; i++ {
		tools = append(tools, map[string]any{
			"name":         fmt.Sprintf("tool_%02d", i),
			"description":  fmt.Sprintf("does thing %d", i),
			"input_schema": map[string]any{"type": "object", "properties": map[string]any{}},
		})
	}
	body := map[string]any{
		"model":  "claude-opus-4-8",
		"stream": true,
		"system": []any{
			map[string]any{"type": "text", "text": "You are a Claude agent."},
			map[string]any{"type": "text", "text": "Be concise.", "cache_control": map[string]any{"type": "ephemeral"}},
		},
		"tools":              tools,
		"thinking":           map[string]any{"type": "enabled", "budget_tokens": 10000},
		"output_config":      map[string]any{"effort": "high"},
		"metadata":           map[string]any{"user_id": "session-911c4d7e"},
		"context_management": map[string]any{"edits": []any{}},
		"max_tokens":         64000,
		"messages": []any{
			map[string]any{"role": "user", "content": "list the files"},
			map[string]any{"role": "system", "content": "prefer ripgrep"},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "thinking", "thinking": "I'll call a tool"},
				map[string]any{"type": "text", "text": "Listing now."},
				map[string]any{"type": "tool_use", "id": "toolu_01", "name": "tool_00", "input": map[string]any{}},
			}},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "toolu_01", "content": "a.go b.go"},
			}},
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// WHY: end-to-end on a realistic captured-shape body with the REAL beta header.
// Down-routing Opus->Haiku must (1) strip all four features across body+headers,
// (2) leave every UNRELATED part byte-identical (system array + its cache_control,
// all 30 tools, metadata, context_management, big max_tokens), (3) preserve every
// history text/tool block while fixing alternation, and (4) re-serialize to valid
// JSON. This is the highest-fidelity guard that reconciliation is surgical.
func TestRealBody_DownrouteToHaikuIsSurgical(t *testing.T) {
	raw := buildRealBody(t)
	h := http.Header{}
	h.Set("anthropic-beta", realBetaHeader)
	r, err := ParseRequest(raw, h)
	if err != nil {
		t.Fatal(err)
	}

	// Snapshots of the parts reconciliation must NOT touch.
	snap := func(key string) string { b, _ := json.Marshal(r.Body[key]); return string(b) }
	beforeSystem := snap("system")
	beforeTools := snap("tools")
	beforeMeta := snap("metadata")
	beforeCM := snap("context_management")
	beforeHistoryTexts, beforeTU, beforeTR := collectBlocks(r.messages())

	stripped := Reconcile(r, "claude-haiku-4-5")

	// (1) all four stripped.
	for _, want := range []string{"context-1m", "effort", "thinking", "midconv-system"} {
		if !has(stripped, want) {
			t.Errorf("expected %q stripped; got %v", want, stripped)
		}
	}
	if r.model() != "claude-haiku-4-5" {
		t.Errorf("model = %q", r.model())
	}
	if _, ok := r.Body["thinking"]; ok {
		t.Error("thinking config not removed")
	}
	if _, ok := r.Body["output_config"]; ok {
		t.Error("output_config had only effort; should be gone")
	}

	// (2) unrelated parts byte-identical.
	if snap("system") != beforeSystem {
		t.Errorf("system array mutated:\n before %s\n after  %s", beforeSystem, snap("system"))
	}
	if snap("tools") != beforeTools {
		t.Error("tools array mutated")
	}
	if snap("metadata") != beforeMeta {
		t.Error("metadata mutated")
	}
	if snap("context_management") != beforeCM {
		t.Error("context_management mutated")
	}

	// beta header: exact surviving token list, in order.
	wantBeta := "claude-code-20250219,oauth-2025-04-20,context-management-2025-06-27,prompt-caching-scope-2026-01-05,advisor-tool-2026-03-01,structured-outputs-2025-12-15"
	if got := r.Headers.Get("anthropic-beta"); got != wantBeta {
		t.Errorf("surviving beta wrong:\n got  %q\n want %q", got, wantBeta)
	}

	// (3) history: thinking block gone, but the answer text + tool pairing kept,
	// and no adjacent same-role after the system-strip coalesce.
	afterTexts, afterTU, afterTR := collectBlocks(r.messages())
	// The dropped thinking block is not a text block, so texts are unchanged.
	if !sortedEq(beforeHistoryTexts, afterTexts) {
		t.Errorf("history text lost/added: before=%v after=%v", beforeHistoryTexts, afterTexts)
	}
	if !sortedEq(beforeTU, afterTU) || !sortedEq(beforeTR, afterTR) {
		t.Errorf("tool pairing disturbed: tu %v->%v  tr %v->%v", beforeTU, afterTU, beforeTR, afterTR)
	}
	for _, m := range r.messages() {
		for _, b := range contentBlocksOf(m) {
			if bt := blockType(b); bt == "thinking" || bt == "redacted_thinking" {
				t.Error("thinking residue survived in history")
			}
		}
	}
	if i := firstAdjacentSameRole(r.messages()); i != -1 {
		t.Errorf("adjacent same-role at %d after reconcile: roles=%v", i, rolesOf(r.messages()))
	}

	// (4) re-serializes to valid JSON with the big max_tokens intact.
	out, err := r.Serialize()
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("reconciled body is not valid JSON: %v", err)
	}
	if fmt.Sprint(back["max_tokens"]) != "64000" {
		t.Errorf("max_tokens changed: %v", back["max_tokens"])
	}
}
