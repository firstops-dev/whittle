package router

import "testing"

// ---------------------------------------------------------------------------
// thinking-history strip
// ---------------------------------------------------------------------------

// WHY: a mixed assistant turn (thinking + text + tool_use) must have ONLY the
// thinking block removed — the text and tool_use are the real response and must
// survive, and the message must NOT be dropped. Regression guard on the "empty ⇒
// drop" rule over-firing.
func TestThinking_MixedBlockKeepsTextAndToolUse(t *testing.T) {
	r := mkReq(t, `{"model":"m","thinking":{"type":"enabled"},"messages":[
	  {"role":"user","content":"q"},
	  {"role":"assistant","content":[
	    {"type":"thinking","thinking":"secret"},
	    {"type":"text","text":"answer"},
	    {"type":"tool_use","id":"id1","name":"ls","input":{}}
	  ]},
	  {"role":"user","content":[{"type":"tool_result","tool_use_id":"id1","content":"ok"}]}
	]}`, "")
	Reconcile(r, "claude-haiku-4-5")
	msgs := r.messages()
	if len(msgs) != 3 {
		t.Fatalf("no message should be dropped, got %d", len(msgs))
	}
	for _, b := range contentBlocksOf(msgs[1]) {
		if blockType(b) == "thinking" {
			t.Error("thinking block survived strip")
		}
	}
	texts, tu, tr := collectBlocks(msgs)
	// "q" is the user turn; "answer" is the assistant text that must survive the
	// thinking strip. The thinking block ("secret") must be gone.
	if !sortedEq(texts, []string{"q", "answer"}) {
		t.Errorf("text lost/added: %v", texts)
	}
	for _, s := range texts {
		if s == "secret" {
			t.Error("thinking text survived as a block")
		}
	}
	if !sortedEq(tu, []string{"id1"}) || !sortedEq(tr, []string{"id1"}) {
		t.Errorf("tool pairing disturbed tu=%v tr=%v", tu, tr)
	}
}

// WHY: redacted_thinking blocks must be stripped exactly like thinking blocks
// (spec: "delete thinking config; also strip thinking blocks... redacted"). A
// non-thinking target rejects redacted_thinking residue in history.
func TestThinking_RedactedThinkingStripped(t *testing.T) {
	r := mkReq(t, `{"model":"m","thinking":{"type":"enabled"},"messages":[
	  {"role":"user","content":"q"},
	  {"role":"assistant","content":[
	    {"type":"redacted_thinking","data":"xyz"},
	    {"type":"text","text":"hi"}
	  ]}
	]}`, "")
	Reconcile(r, "claude-haiku-4-5")
	for _, m := range r.messages() {
		for _, b := range contentBlocksOf(m) {
			if bt := blockType(b); bt == "redacted_thinking" {
				t.Error("redacted_thinking survived strip")
			}
		}
	}
}

// WHY: a message whose content is a bare string has no thinking blocks and must
// pass through untouched (the strip only walks array content). Guards the
// isArray branch.
func TestThinking_StringContentUntouched(t *testing.T) {
	r := mkReq(t, `{"model":"m","thinking":{"type":"enabled"},"messages":[
	  {"role":"user","content":"plain string"},
	  {"role":"assistant","content":"reply string"}
	]}`, "")
	Reconcile(r, "claude-haiku-4-5")
	msgs := r.messages()
	if len(msgs) != 2 {
		t.Fatalf("string-content messages must survive, got %d", len(msgs))
	}
	if c, _ := msgs[0].(map[string]any); c["content"] != "plain string" {
		t.Errorf("string content mutated: %v", c["content"])
	}
}

// WHY: a non-map element in the messages array (defensive: malformed input) must
// not panic and must be preserved as-is by the thinking strip.
func TestThinking_NonMapMessageElementPreserved(t *testing.T) {
	r := mkReq(t, `{"model":"m","thinking":{"type":"enabled"},"messages":[
	  "i am not a map",
	  {"role":"user","content":"q"}
	]}`, "")
	Reconcile(r, "claude-haiku-4-5") // must not panic
	msgs := r.messages()
	if len(msgs) != 2 {
		t.Fatalf("non-map element should be preserved, got %d msgs", len(msgs))
	}
	if s, _ := msgs[0].(string); s != "i am not a map" {
		t.Errorf("non-map element mutated/dropped: %#v", msgs[0])
	}
}

// RED — PINNED BUG. WHY: reconciliation's core contract is to make a rewritten
// request ACCEPTED (no 400). The thinking strip drops an assistant turn that is
// emptied to [] (spec "degenerate empty turn is invalid"), but it performs NO
// alternation repair afterward. Coalescing (the repair) only runs on the
// mid-conv-system strip path, and ONLY when a role:"system" message is present to
// trigger it. RESOLVED in the M2-hardening pass: repairAlternation now runs as a
// shared post-pass whenever any message-mutating strip fires (not only the
// system-strip path), so dropping a thinking-only assistant turn between two user
// turns coalesces them instead of leaving an invalid [user, user] adjacency. This
// test pins that repair (input roles [user, assistant, user] -> single coalesced
// user, no adjacency).
func TestThinking_DropRepairsAlternation(t *testing.T) {
	r := mkReq(t, `{"model":"m","thinking":{"type":"enabled"},"messages":[
	  {"role":"user","content":"a"},
	  {"role":"assistant","content":[{"type":"thinking","thinking":"..."}]},
	  {"role":"user","content":"b"}
	]}`, "")
	Reconcile(r, "claude-haiku-4-5")
	msgs := r.messages()
	if i := firstAdjacentSameRole(msgs); i != -1 {
		t.Fatalf("thinking-strip drop left adjacent same-role at index %d; roles=%v (repair failed)", i, rolesOf(msgs))
	}
}
