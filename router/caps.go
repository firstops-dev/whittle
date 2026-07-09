package router

import "strings"

// Capability is a model feature that a request may use and a target model may or
// may not support. Reconciliation strips a feature only when the target is
// KNOWN to lack the capability (blocklist, not allowlist — see
// docs/ROUTER_RECONCILIATION.md).
type Capability string

const (
	CapLongContext   Capability = "long_context"   // context-1m beta
	CapEffortParam   Capability = "effort_param"   // output_config.effort
	CapThinking      Capability = "thinking"       // adaptive thinking config
	CapMidConvSystem Capability = "midconv_system" // a mid-conversation role:"system" message
)

// ModelCaps describes what a model supports. On a KNOWN model an absent
// capability means unsupported (safe: strip). See capsFor for the crucial
// unknown-MODEL default, which is the opposite (assume fully capable, forward).
//
// allSupported is the unknown-model sentinel flag: when true, supports() reports
// true for EVERY capability, including ones added to the enum later — so a new
// Capability constant can never make the unknown-model default silently start
// stripping. Known models leave it false and rely on the Supports map.
type ModelCaps struct {
	Supports         map[Capability]bool
	MaxContextTokens int
	allSupported     bool
}

func (c ModelCaps) supports(cap Capability) bool {
	return c.allSupported || c.Supports[cap]
}

// unboundedContext is the sentinel window for an unknown model — we never reject
// it on length; if the real window is smaller, Mode B (retry-original) recovers.
const unboundedContext = 1 << 30

// baselineCaps is the compiled-in capability table, canonical-model-keyed.
// Windows are deliberately CONSERVATIVE (under-stated) pending a systematic
// per-model probe: under-stating starves a tier (safe), over-stating routes
// oversized contexts that 400. Seeded from the GATE-1 experiment (Haiku rejected
// all four features); this is the single place to extend as models ship.
var baselineCaps = map[string]ModelCaps{
	"claude-opus-4-8": {
		Supports:         map[Capability]bool{CapLongContext: true, CapEffortParam: true, CapThinking: true, CapMidConvSystem: true},
		MaxContextTokens: 200_000,
	},
	"claude-sonnet-5": {
		Supports:         map[Capability]bool{CapLongContext: true, CapEffortParam: true, CapThinking: true, CapMidConvSystem: true},
		MaxContextTokens: 200_000,
	},
	"claude-haiku-4-5": {
		Supports:         map[Capability]bool{}, // rejects all four (observed)
		MaxContextTokens: 200_000,
	},
}

// fullyCapable is the unknown-model sentinel: every capability supported, window
// unbounded. Reached only when a model matches no known FAMILY either.
var fullyCapable = ModelCaps{allSupported: true, MaxContextTokens: unboundedContext}

// familyCaps is the conservative per-FAMILY fallback for a model not in
// baselineCaps — Anthropic ships versioned ids faster than we enumerate, and the
// common case is DOWN-routing to a cheaper same-family model. Ordered
// most-specific first.
//
// The floor is deliberately AGGRESSIVE: an unrecognized haiku/sonnet supports
// NOTHING optional, so every request rewritten to it is a plain request every
// model accepts. Over-stripping on a down-route is safe (the request runs, just
// without the betas — which the cheaper tier may not honor anyway); UNDER-
// stripping 400s (confirmed live: an unknown sonnet rejected both the context-1m
// beta AND adaptive thinking). Only opus — the top tier, reached by UP-routing,
// where the source had fewer features to begin with — keeps the full set. A model
// matching no family at all still gets the fully-capable sentinel (forward, let
// Mode B catch a genuine 400).
var familyCaps = []struct {
	sub  string
	caps ModelCaps
}{
	{"haiku", ModelCaps{Supports: map[Capability]bool{}, MaxContextTokens: 200_000}},
	{"sonnet", ModelCaps{Supports: map[Capability]bool{}, MaxContextTokens: 200_000}},
	{"opus", ModelCaps{Supports: map[Capability]bool{CapLongContext: true, CapEffortParam: true, CapThinking: true, CapMidConvSystem: true}, MaxContextTokens: 200_000}},
}

// capsFor returns the capabilities of a model. Resolution order:
//
//	exact canonical id in baselineCaps → its caps
//	else a known FAMILY (haiku/sonnet/opus) → conservative family floor (strip)
//	else entirely unknown → fully capable (forward, let Mode B catch a real 400)
//
// The family step is what fixes the silent-down-route-400: an unrecognized but
// same-family model no longer masquerades as fully capable.
func capsFor(model string) ModelCaps {
	canon := canonicalModel(model)
	if c, ok := baselineCaps[canon]; ok {
		return c
	}
	for _, f := range familyCaps {
		if strings.Contains(canon, f.sub) {
			return f.caps
		}
	}
	return fullyCapable
}

// CanServe reports whether a target model can hold a request of ctxTokens. Used
// as the routing guard so an oversized context is not down-routed below a
// target's window. The estimate is bytes/4 and under-counts, so a 0.9 margin is
// applied (reconciliation spec M1). An unknown model is unbounded ⇒ always true.
func CanServe(model string, ctxTokens int) bool {
	return ctxTokens <= capsFor(model).MaxContextTokens/10*9
}
