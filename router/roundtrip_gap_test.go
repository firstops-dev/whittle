package router

import (
	"encoding/json"
	"strings"
	"testing"
)

// WHY: ParseRequest uses json.UseNumber so large integer token counts survive a
// parse -> serialize round-trip WITHOUT float64 precision loss. A 19-digit int
// must reappear byte-identical (float64 would round it). Pins the UseNumber
// contract that §2.1 relies on.
func TestRoundTrip_LargeIntegerPreserved(t *testing.T) {
	const big = "1234567890123456789" // 19 digits, unrepresentable exactly as float64
	r := mkReq(t, `{"model":"claude-opus-4-8","max_tokens":`+big+`,"messages":[]}`, "")
	Reconcile(r, "claude-haiku-4-5")
	out, err := r.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"max_tokens":`+big) {
		t.Errorf("large int not preserved exactly; body=%s", out)
	}
}

// WHY: unicode (multibyte + explicit escapes) in body text must survive the
// round-trip. Re-decoding must yield the original runes.
func TestRoundTrip_UnicodePreserved(t *testing.T) {
	r := mkReq(t, `{"model":"m","messages":[{"role":"user","content":"café ☕ 日本語 é"}]}`, "")
	Reconcile(r, "claude-haiku-4-5")
	out, err := r.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}
	msgs := back["messages"].([]any)
	got := msgs[0].(map[string]any)["content"].(string)
	if got != "café ☕ 日本語 é" {
		t.Errorf("unicode mangled: %q", got)
	}
}

// WHY: nested cache_control on a content block is plain body data — reconciliation
// (which touches only model/thinking/output_config/messages-on-system-strip/beta)
// must NOT strip or alter it. (The spec accepts prompt-cache POSITION drift from
// re-serialization; the cache_control FIELD itself must remain.)
func TestRoundTrip_CacheControlFieldPreserved(t *testing.T) {
	r := mkReq(t, `{"model":"claude-opus-4-8","messages":[
	  {"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}]}
	]}`, "")
	Reconcile(r, "claude-haiku-4-5")
	out, err := r.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"cache_control":{"type":"ephemeral"}`) {
		t.Errorf("cache_control field dropped: %s", out)
	}
}

// WHY: unrelated top-level fields (system, tools, metadata) must be byte-identical
// after reconciliation — no strip touches them. Compares a canonical JSON encoding
// of each before/after so a stray mutation is caught exactly.
func TestRoundTrip_UnrelatedTopLevelFieldsUntouched(t *testing.T) {
	body := `{"model":"claude-opus-4-8","thinking":{"type":"enabled"},
	  "system":[{"type":"text","text":"you are terse","cache_control":{"type":"ephemeral"}}],
	  "tools":[{"name":"ls","description":"list","input_schema":{"type":"object"}}],
	  "metadata":{"user_id":"u-123"},
	  "context_management":{"edits":[]},
	  "messages":[]}`
	r := mkReq(t, body, "")

	snap := func(key string) string {
		b, _ := json.Marshal(r.Body[key])
		return string(b)
	}
	beforeSystem, beforeTools, beforeMeta, beforeCM := snap("system"), snap("tools"), snap("metadata"), snap("context_management")

	Reconcile(r, "claude-haiku-4-5")

	if snap("system") != beforeSystem {
		t.Errorf("system mutated: %s", snap("system"))
	}
	if snap("tools") != beforeTools {
		t.Errorf("tools mutated: %s", snap("tools"))
	}
	if snap("metadata") != beforeMeta {
		t.Errorf("metadata mutated: %s", snap("metadata"))
	}
	if snap("context_management") != beforeCM {
		t.Errorf("context_management mutated: %s", snap("context_management"))
	}
	// And thinking WAS stripped (sanity that Reconcile actually ran).
	if _, ok := r.Body["thinking"]; ok {
		t.Error("thinking should be stripped")
	}
}
