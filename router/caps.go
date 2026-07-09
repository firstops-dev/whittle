package router

// Capability is a model feature that a request may use and a target model may or
// may not support. Reconciliation strips a feature only when the target is
// KNOWN to lack the capability (blocklist, not allowlist — see
// docs/ROUTER_RECONCILIATION.md).
type Capability string

const (
	CapLongContext   Capability = "long_context"  // context-1m beta
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
// unbounded.
var fullyCapable = ModelCaps{allSupported: true, MaxContextTokens: unboundedContext}

// capsFor returns the capabilities of a model. The critical rule (review B1): a
// lookup MISS (an unknown model — Anthropic ships a new id, or a user pins one
// we don't know) returns the FULLY-CAPABLE, UNBOUNDED sentinel — strip nothing,
// forward as-is, let Mode B catch any real 400. The zero ModelCaps value would
// do the opposite (support nothing, MaxContextTokens 0 ⇒ unroutable), so we must
// never return it on a miss. Two distinct safe defaults, never conflated:
//
//	unknown CAPABILITY on a KNOWN model → false (strip)
//	entirely UNKNOWN model              → fully capable (forward)
func capsFor(model string) ModelCaps {
	if c, ok := baselineCaps[canonicalModel(model)]; ok {
		return c
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
