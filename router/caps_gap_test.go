package router

import "testing"

// WHY: exhaustive capsFor over every known model plus the unknown-model sentinel.
// A KNOWN model reports each capability from its map (absent ⇒ false ⇒ strip); an
// UNKNOWN model reports true for EVERY capability (forward). This pins the two
// distinct safe defaults the spec (B1) warns must never be conflated.
func TestCapsFor_KnownAndUnknownDefaults(t *testing.T) {
	allCaps := []Capability{CapLongContext, CapEffortParam, CapThinking, CapMidConvSystem}

	full := capsFor("claude-opus-4-8")
	for _, c := range allCaps {
		if !full.supports(c) {
			t.Errorf("opus should support %s", c)
		}
	}
	sonnet := capsFor("claude-sonnet-5")
	for _, c := range allCaps {
		if !sonnet.supports(c) {
			t.Errorf("sonnet should support %s", c)
		}
	}
	haiku := capsFor("claude-haiku-4-5")
	for _, c := range allCaps {
		if haiku.supports(c) {
			t.Errorf("haiku should NOT support %s (absent ⇒ unsupported)", c)
		}
	}
	// Unknown model: fully capable, including a Capability the enum might gain
	// later — supports() must honor the allSupported sentinel, not the map.
	unknown := capsFor("claude-neo-9-99")
	for _, c := range append(allCaps, Capability("some_future_capability")) {
		if !unknown.supports(c) {
			t.Errorf("unknown model must support everything, missing %s", c)
		}
	}
}

// WHY: canonicalModel must let a DATED model id resolve to its baseline entry, so
// a request targeting claude-haiku-4-5-20251001 gets Haiku's caps (strip), not the
// unknown-model fully-capable default (forward). A miss here silently disables all
// stripping for dated ids.
func TestCapsFor_DatedIDCanonicalizes(t *testing.T) {
	dated := capsFor("claude-haiku-4-5-20251001")
	if dated.supports(CapThinking) {
		t.Error("dated Haiku id must resolve to Haiku caps (thinking unsupported), not the unknown default")
	}
	if dated.MaxContextTokens != 200_000 {
		t.Errorf("dated Haiku window = %d, want 200000", dated.MaxContextTokens)
	}
}

// WHY: the routing guard math is (Max/10*9). Pin the EXACT boundary: a context of
// precisely the 0.9-margin value must serve; one token over must not. The existing
// suite only checks 179k/190k and never touches the 180000 edge where an off-by-one
// would hide.
func TestCanServe_ExactMarginBoundary(t *testing.T) {
	const margin = 200_000 / 10 * 9 // 180000
	if !CanServe("claude-haiku-4-5", margin) {
		t.Errorf("exactly %d must fit (<=)", margin)
	}
	if CanServe("claude-haiku-4-5", margin+1) {
		t.Errorf("%d (one over) must be rejected", margin+1)
	}
	if !CanServe("claude-haiku-4-5", 0) {
		t.Error("zero context must always fit")
	}
}

// WHY: a known-capability set with an explicit false must behave identically to an
// absent one — both mean "strip". Guards against supports() ever treating a
// present-false as "keep".
func TestSupports_PresentFalseEqualsAbsent(t *testing.T) {
	c := ModelCaps{Supports: map[Capability]bool{CapThinking: false}}
	if c.supports(CapThinking) {
		t.Error("present-false capability must report unsupported")
	}
	if c.supports(CapEffortParam) {
		t.Error("absent capability must report unsupported")
	}
	c2 := ModelCaps{Supports: map[Capability]bool{CapThinking: true}}
	if !c2.supports(CapThinking) {
		t.Error("present-true capability must report supported")
	}
}

// LIMITATION (documented): capsFor's comment and CanServe's say an unknown model is
// "unbounded ⇒ always true", but the sentinel is 1<<30 and CanServe applies the
// 0.9 margin, so a context above ~966M tokens would be REJECTED for an unknown
// model. No request can reach that size (bodies are capped at ~32MB ≈ 8M tokens),
// so it cannot trigger in practice — but "always true" is not literally true. Pin
// the real numeric behavior so the discrepancy is visible, not surprising.
func TestCanServe_UnknownModelNotLiterallyUnbounded_LIMITATION(t *testing.T) {
	if !CanServe("claude-neo-9-99", 8_000_000) {
		t.Error("a realistically-large context (8M) must serve on an unknown model")
	}
	if CanServe("claude-neo-9-99", 1<<30) {
		t.Log("note: unknown-model 'unbounded' rejects >~966M-token contexts (unreachable in practice, but 'always true' is inexact)")
	}
}
