package router

import "testing"

// The family fallback is what fixes the silent down-route 400: a model not in
// baselineCaps but of a known family gets the conservative family floor, so
// reconciliation still strips features the target rejects.
func TestCapsFor_FamilyFallback(t *testing.T) {
	// An unrecognized sonnet must claim NOTHING optional — an opus[1m] request
	// carries the context-1m beta AND adaptive thinking, both of which an older
	// sonnet rejects; the aggressive floor strips them so the down-route succeeds.
	for _, cap := range []Capability{CapLongContext, CapThinking, CapEffortParam, CapMidConvSystem} {
		if capsFor("claude-sonnet-4-5-20250929").supports(cap) {
			t.Errorf("unknown sonnet must not claim %s (aggressive family floor)", cap)
		}
	}
	// An unrecognized opus keeps long-context.
	if !capsFor("claude-opus-4-7").supports(CapLongContext) {
		t.Error("opus family should support CapLongContext")
	}
	// Haiku supports nothing.
	if capsFor("claude-haiku-4-5-20251001").supports(CapThinking) {
		t.Error("haiku supports nothing")
	}
	// A genuinely unknown family stays fully capable (forward, let Mode B catch).
	if !capsFor("some-vendor-model-x").supports(CapLongContext) {
		t.Error("unknown family should be fully capable")
	}
}
