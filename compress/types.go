// Package compress is the content-aware tool-output compressor library: a gate
// decides whether content is safe to compress, a router classifies it, and a
// pipeline dispatches to specialized compressors. Everything on this path runs
// on every tool call, so all regexes are precompiled package vars and the hot
// path avoids per-call allocation where practical.
package compress

import "context"

// ContentType is the router's classification of a piece of content.
type ContentType string

const (
	TypeJSON    ContentType = "json"
	TypeCode    ContentType = "code"
	TypeLog     ContentType = "log"
	TypeDiff    ContentType = "diff"
	TypeHTML    ContentType = "html"
	TypeSearch  ContentType = "search"
	TypeTabular ContentType = "tabular"
	// TypeDocRead is a line-numbered file read (`N\t<line>`, Read tool / cat -n)
	// whose STRIPPED content is unmistakably a markdown/prose document
	// (isMarkdownDoc). Routed to the prose model after LineNumberStrip; every
	// other line-numbered read stays TypeCode (passthrough) — code must never
	// reach the paraphraser.
	TypeDocRead  ContentType = "doc_read"
	TypeProse    ContentType = "prose"
	TypeTerminal ContentType = "terminal"
	TypeUnknown  ContentType = "unknown"
)

// Input is the unit of work handed to the pipeline and each compressor.
//
// ContentType is an OPTIONAL routing override: when empty the router detects
// the type from Content. MIME and ContentClass carry the raw request gating
// signals that gate.py consumes (HTTP content_type MIME hint and the explicit
// prose|code_structured override); they are not part of the Headroom Input but
// are required to port the gate faithfully.
type Input struct {
	Content      string
	ContentType  ContentType
	ToolName     string
	MIME         string
	ContentClass string
	Rate         float64
	MinTokens    int
}

// Result is what a single compressor returns.
type Result struct {
	Output   string
	Strategy string
	InChars  int
	OutChars int
	// Skipped is a clean, non-error skip (the compressor chose not to compress,
	// e.g. an upstream sidecar shed load or gated the input). The pipeline treats
	// it as a passthrough skip with SkipReason, NOT as the error fail-open path —
	// so legitimate skips do not pollute the error rate.
	Skipped    bool
	SkipReason string
}

// Compressor transforms content of the types it Handles. Implementations must
// be safe for concurrent use (the pipeline holds one instance per type).
type Compressor interface {
	Name() string
	Handles(ContentType) bool
	Compress(ctx context.Context, in Input) (Result, error)
}
