package compress

import (
	"context"
	"strings"
)

// GateConfig bounds what the gate will accept.
type GateConfig struct {
	MinTokens int
	MaxChars  int // global ceiling: skip before classify; deterministic compressors handle the rest
	// ProseMaxChars caps ONLY the prose path (LLMLingua). 0 disables the cap.
	ProseMaxChars int
}

// DefaultGateConfig returns the ported gate.py / app.py defaults.
func DefaultGateConfig() GateConfig {
	return GateConfig{MinTokens: DefaultMinTokens, MaxChars: DefaultMaxChars, ProseMaxChars: DefaultProseMaxChars}
}

// Logger is the minimal logging surface the pipeline needs (satisfied by the
// stdlib *log.Logger). Kept tiny so callers aren't forced into a logging dep.
type Logger interface {
	Printf(format string, args ...any)
}

// Outcome is the full result of running the pipeline once.
type Outcome struct {
	Output     string
	Action     string // "compressed" | "skipped"
	SkipReason string // "" when compressed
	Strategy   string // "+"-joined compressor names that ran
	Detected   ContentType
	GateKlass  string
	GateSignal string
	InChars    int
	OutChars   int
}

// Pipeline ties the gate, router and registry together. It is safe for
// concurrent use: it holds no mutable state.
type Pipeline struct {
	registry *Registry
	gate     GateConfig
	log      Logger
}

// NewPipeline wires a pipeline. log may be nil.
func NewPipeline(r *Registry, gate GateConfig, log Logger) *Pipeline {
	return &Pipeline{registry: r, gate: gate, log: log}
}

// Compress runs gate → route → chain → guardrail. It NEVER returns an error:
// every failure is fail-open (the original content passes through, action
// "skipped"). The contract mirrors the Python service so the edge-server caller
// - which reads only Output(compressed) + Action - is unchanged.
func (p *Pipeline) Compress(ctx context.Context, in Input) (outcome Outcome) {
	content := in.Content
	inChars := len(content)

	// Fail-open against panics: a panic in classify/route/chain must never
	// propagate to the caller (the contract is "Compress NEVER returns an
	// error"). Recover to a passthrough skip.
	defer func() {
		if r := recover(); r != nil {
			if p.log != nil {
				p.log.Printf("textcompress: recovered panic: %v (in=%d)", r, inChars)
			}
			outcome = Outcome{Output: content, Action: "skipped", SkipReason: "error", InChars: inChars, OutChars: inChars}
		}
	}()

	minTokens := in.MinTokens
	if minTokens < 0 { // explicit 0 means "no floor"; only a negative falls back
		minTokens = p.gate.MinTokens
	}

	base := Outcome{
		Output:   content,
		InChars:  inChars,
		OutChars: inChars,
	}

	// Global size gate runs BEFORE any classification/parse: an oversized body is
	// rejected before paying for Detect / looksStructured on an unbounded input.
	// This is a generous safety bound, NOT the prose limit - deterministic
	// structural compressors handle large output, and the prose-only ceiling is
	// applied later (see the prose-path guards). Never compress only part of an
	// oversized input (matches app.py): skip the whole thing, don't drop the tail.
	if p.gate.MaxChars > 0 && inChars > p.gate.MaxChars {
		base.Action = "skipped"
		base.SkipReason = "too_large"
		return base
	}

	action, klass, signal, reason := Decide(content, estTokens(content), in.ToolName, in.MIME, in.ContentClass, minTokens)
	base.GateKlass = klass
	base.GateSignal = signal
	if action == "skip" {
		base.Action = "skipped"
		base.SkipReason = reason
		return base
	}

	detected := in.ContentType
	if detected == "" || detected == TypeUnknown {
		detected, _ = Detect(content)
	}
	base.Detected = detected

	// Prose-path guards. The prose chain delegates to LLMLingua (Python model);
	// these limits are specific to it and must NOT apply to the deterministic
	// structural compressors (json, log, ...), which route by type regardless.
	if detected == TypeProse {
		// (a) Safety: code/structured that fell through the router to prose (a
		// fallback) must never reach the prose model - it would corrupt it.
		// Deliberately NOT applied to TypeDocRead: prose is a FALLBACK (weak
		// evidence, so the gate's metadata vote wins), while doc_read is a
		// POSITIVE zero-tolerance classification (isMarkdownDoc) - and every
		// Read-tool output carries klass=code_structured from the tool-name
		// vote, which would otherwise veto every doc read. Downstream defense for
		// doc_read: the adapter sends NO content_class override, so the Python
		// sidecar's classify() code-vetoes run independently, plus its
		// identifier-dense + fidelity guards (reviewer B2).
		if klass == "code_structured" {
			base.Action = "skipped"
			base.SkipReason = "code_structured"
			return base
		}
	}
	if detected == TypeProse || detected == TypeDocRead {
		// (b) Size: the prose model pays per-token inference and has a finite
		// context window, so model-bound content above the prose-only ceiling is
		// skipped. This is the cap that used to be global; deterministic
		// compressors are bound only by MaxChars above.
		if p.gate.ProseMaxChars > 0 && inChars > p.gate.ProseMaxChars {
			base.Action = "skipped"
			base.SkipReason = "too_large_prose"
			return base
		}
	}

	chain := p.registry.Chain(detected)
	if len(chain) == 0 {
		base.Action = "skipped"
		base.SkipReason = "no_compressor"
		return base
	}

	cur := content
	var strategies []string
	for _, c := range chain {
		if !c.Handles(detected) {
			continue
		}
		res, err := c.Compress(ctx, Input{
			Content:      cur,
			ContentType:  detected,
			ToolName:     in.ToolName,
			MIME:         in.MIME,
			ContentClass: in.ContentClass,
			Rate:         in.Rate,
			MinTokens:    minTokens,
		})
		if err != nil { // fail-open: log + passthrough the ORIGINAL
			if p.log != nil {
				p.log.Printf("textcompress: compressor %s failed: %v (in=%d type=%s)", c.Name(), err, inChars, detected)
			}
			base.Action = "skipped"
			base.SkipReason = "error"
			base.Output = content
			base.OutChars = inChars
			return base
		}
		if res.Skipped { // clean skip (e.g. sidecar shed load / gated) - NOT an error
			base.Action = "skipped"
			base.SkipReason = res.SkipReason
			base.Output = content
			base.OutChars = inChars
			return base
		}
		cur = res.Output
		strategies = append(strategies, c.Name())
	}

	// Empty-output guardrail: a compressor that reduced non-empty input to the
	// empty string is total data loss, not compression. Fail open with the
	// original rather than reporting a "successful" 100%-loss compression.
	if cur == "" && content != "" {
		base.Action = "skipped"
		base.SkipReason = "empty_output"
		base.Output = content
		base.OutChars = inChars
		return base
	}

	// Expansion guardrail - token-honest AND byte-honest: the consumer pays
	// tokens (bytes diverge from tokens by up to ~4x on whitespace-heavy content,
	// docs/compressor-opportunities.md #3), so output must be strictly smaller on
	// BOTH axes; else passthrough. Conservative as ESTIMATED: the token side uses
	// EstimateTokens (MAE ~8%, biased to overestimate), so a borderline true-token
	// regression inside estimator noise can slip through - for our lossless
	// transforms that is an efficiency miss, never data loss.
	if len(cur) >= inChars || EstimateTokens(cur) >= EstimateTokens(content) {
		base.Action = "skipped"
		base.SkipReason = "guardrail_expansion"
		base.Output = content
		base.OutChars = inChars
		return base
	}

	base.Output = cur
	base.Action = "compressed"
	base.SkipReason = ""
	base.Strategy = strings.Join(strategies, "+")
	base.OutChars = len(cur)
	return base
}

// estTokens is the gate's token floor counter - now the calibrated estimator
// (tokens.go) rather than the old chars/4, which was off -27..-48% on structured
// content and would admit under-length inputs.
func estTokens(s string) int { return EstimateTokens(s) }
