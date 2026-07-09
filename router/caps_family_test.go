package router

import "testing"

// The family fallback is what fixes the silent down-route 400: a model not in
// baselineCaps but of a known family gets the conservative family floor, so
// reconciliation still strips features the target rejects.
func TestCapsFor_FamilyFallback(t *testing.T) {
	// An unrecognized sonnet must NOT claim the context-1m beta — otherwise an
	// opus[1m] request's beta is forwarded to it and 400s on every down-route.
	if capsFor("claude-sonnet-4-5-20250929").supports(CapLongContext) {
		t.Error("unknown sonnet must not claim CapLongContext (family fallback)")
	}
	// ...but it keeps the widely-supported features.
	if !capsFor("claude-sonnet-4-5-20250929").supports(CapThinking) {
		t.Error("sonnet family should support thinking")
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
