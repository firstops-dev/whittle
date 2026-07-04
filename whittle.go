// Package whittle carves agent tool outputs down to what matters — and never
// cuts what doesn't come back.
//
// Whittle is a content-aware compressor for the text an AI agent reads: tool
// outputs, file reads, logs, JSON, terminal streams. It routes each input to a
// type-specific strategy and holds one hard line: structural compression is
// LOSSLESS or CLEARLY MARKED, code never reaches a lossy model, and any anomaly
// fails open to the original bytes.
//
// The deterministic strategies (JSON columnar reshaping, log selection with
// omission markers, terminal CR-overwrite collapse, ANSI stripping) work with no
// dependencies. The optional ML prose path (LLMLingua-2, see model/) activates
// only when Options.ModelURL (or WHITTLE_MODEL_URL) points at a running model
// sidecar.
//
//	eng := whittle.New(whittle.Options{})
//	res := eng.Compress(ctx, toolOutput)
//	if res.Action == "compressed" {
//	    use(res.Output) // res.SkipReason explains any skip
//	}
package whittle

import (
	"context"

	"github.com/firstops-dev/whittle/compress"
	"github.com/firstops-dev/whittle/compress/compressors"
)

// Options configures an Engine. The zero value is production-safe: deterministic
// compressors only, default gates.
type Options struct {
	// ModelURL enables the ML prose path (an LLMLingua sidecar, see model/).
	// Empty = prose and doc-read content pass through untouched.
	ModelURL string
	// MinTokens skips inputs shorter than this (default 64; compression isn't
	// worth it below that). Use -1 to keep the default; 0 disables the floor.
	MinTokens int
	// MaxChars is the global safety ceiling; larger inputs skip before
	// classification (default 256 KiB).
	MaxChars int
	// ProseMaxChars caps only the ML prose path (default 4500 — a latency
	// budget, not a correctness bound).
	ProseMaxChars int
	// Logger receives pipeline diagnostics (nil = silent).
	Logger compress.Logger
}

// Result is the outcome of one compression: Output (compressed or original),
// Action ("compressed"|"skipped"), SkipReason, Strategy, Detected type, sizes.
type Result = compress.Outcome

// Engine is a reusable, concurrency-safe compressor.
type Engine struct {
	p         *compress.Pipeline
	minTokens int
}

// New builds an Engine from opts.
func New(opts Options) *Engine {
	gate := compress.DefaultGateConfig()
	if opts.MaxChars > 0 {
		gate.MaxChars = opts.MaxChars
	}
	if opts.ProseMaxChars > 0 {
		gate.ProseMaxChars = opts.ProseMaxChars
	}
	minTokens := -1 // pipeline: negative falls back to the gate default
	if opts.MinTokens >= 0 {
		minTokens = opts.MinTokens
	}
	p := compress.NewPipeline(
		compress.NewRegistry(compressors.ChainsWithModel(opts.ModelURL)),
		gate,
		opts.Logger,
	)
	return &Engine{p: p, minTokens: minTokens}
}

// Compress routes content to its type-specific strategy. It never returns an
// error and never corrupts: every failure or guard trip returns the original
// content with Action "skipped" and a reason.
func (e *Engine) Compress(ctx context.Context, content string) Result {
	return e.p.Compress(ctx, compress.Input{Content: content, MinTokens: e.minTokens})
}

// CompressInput is the full-control variant (routing override, tool-name /
// MIME gating hints, prose keep-rate).
func (e *Engine) CompressInput(ctx context.Context, in compress.Input) Result {
	if in.MinTokens == 0 {
		in.MinTokens = e.minTokens
	}
	return e.p.Compress(ctx, in)
}

// EstimateTokens is whittle's calibrated token estimator (MAE ~8% vs tiktoken
// o200k_base) — the same accounting the engine's accept gates use.
func EstimateTokens(s string) int { return compress.EstimateTokens(s) }
