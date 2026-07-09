package router

import "testing"

// WHY: reconciliation must be idempotent for the same target. A second Reconcile
// to the same model must strip NOTHING further (every feature already gone) and
// must not double-mutate messages (no re-coalesce, no duplicated content). Guards
// against a strip whose detector still fires on its own output.
func TestReconcile_IdempotentSameTarget(t *testing.T) {
	body := `{"model":"claude-opus-4-8","thinking":{"type":"enabled"},
	  "output_config":{"effort":"high"},
	  "messages":[{"role":"user","content":"hi"},{"role":"system","content":"be terse"},{"role":"assistant","content":"ok"}]}`
	r := mkReq(t, body, "context-1m-x,effort-1,mid-conversation-system-1")

	first := Reconcile(r, "claude-haiku-4-5")
	if len(first) == 0 {
		t.Fatal("first reconcile should strip features")
	}
	msgsAfterFirst := rolesOf(r.messages())
	textsAfterFirst, _, _ := collectBlocks(r.messages())

	second := Reconcile(r, "claude-haiku-4-5")
	if len(second) != 0 {
		t.Errorf("second reconcile must be a no-op, stripped: %v", second)
	}
	// Messages must be byte-stable across the idempotent second pass.
	if got := rolesOf(r.messages()); len(got) != len(msgsAfterFirst) {
		t.Errorf("message count changed on second reconcile: %v -> %v", msgsAfterFirst, got)
	}
	textsAfterSecond, _, _ := collectBlocks(r.messages())
	if !sortedEq(textsAfterFirst, textsAfterSecond) {
		t.Errorf("content duplicated/lost on second reconcile: %v -> %v", textsAfterFirst, textsAfterSecond)
	}
	if _, ok := r.Body["thinking"]; ok {
		t.Error("thinking reappeared")
	}
}

// WHY: calling Reconcile with the SAME model as the body already carries is
// normally short-circuited by Decide, but Reconcile itself must still be sound.
// Target == a KNOWN narrow model (haiku) strips features by CAPABILITY regardless
// of whether the id changed — this documents that Reconcile keys on caps, not on
// "did the model string change".
func TestReconcile_SameModelStillStripsByCapability(t *testing.T) {
	r := mkReq(t, `{"model":"claude-haiku-4-5","thinking":{"type":"enabled"},"messages":[]}`, "")
	stripped := Reconcile(r, "claude-haiku-4-5")
	if !has(stripped, "thinking") {
		t.Errorf("haiko target must strip thinking even when model unchanged, got %v", stripped)
	}
	if _, ok := r.Body["thinking"]; ok {
		t.Error("thinking not removed")
	}
}

// WHY: target == a fully-capable model equal to the request's own model strips
// nothing — pins that a no-op-shaped call to a capable target does not mutate the
// body.
func TestReconcile_SameCapableModelStripsNothing(t *testing.T) {
	r := mkReq(t, `{"model":"claude-opus-4-8","thinking":{"type":"enabled"},"output_config":{"effort":"high"},"messages":[]}`, "context-1m-x")
	stripped := Reconcile(r, "claude-opus-4-8")
	if len(stripped) != 0 {
		t.Errorf("capable same-model target must strip nothing, got %v", stripped)
	}
	if _, ok := r.Body["thinking"]; !ok {
		t.Error("thinking must remain")
	}
	if r.Headers.Get("anthropic-beta") != "context-1m-x" {
		t.Error("beta must remain")
	}
}
