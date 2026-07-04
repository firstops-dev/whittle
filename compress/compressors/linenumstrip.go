package compressors

import (
	"context"

	"github.com/firstops-dev/whittle/compress"
)

// LineNumberStrip removes `N\t` line-number prefixes from a doc_read (a
// line-numbered file read the router positively classified as a markdown/prose
// document) before the prose model runs. The prefixes are Read-tool render
// furniture, pure token cost, and would skew llmlingua's token scoring.
//
// Deterministic; the numbering is mechanically re-derivable (lines are numbered
// contiguously), but the downstream prose model is lossy anyway, so this chain is
// lossy end-to-end by design — it only runs on content the router proved is a doc.
//
// TODO(textcompress): CCR-style recovery — when the recovery store lands, stash
// the ORIGINAL doc_read content under a content hash and append a retrieval
// marker, so an agent that needs the exact file (or its line numbers, e.g. to
// quote a line reference) can recover it. Deferred with the other CCR work; see
// docs/smartcrusher-gap-analysis.md and docs/compressor-opportunities.md (#1).
type LineNumberStrip struct{}

func NewLineNumberStrip() LineNumberStrip { return LineNumberStrip{} }

func (LineNumberStrip) Name() string { return "linenum_strip" }

func (LineNumberStrip) Handles(ct compress.ContentType) bool { return ct == compress.TypeDocRead }

func (LineNumberStrip) Compress(_ context.Context, in compress.Input) (compress.Result, error) {
	out := compress.StripLineNumbers(in.Content)
	return compress.Result{Output: out, Strategy: "linenum_strip", InChars: len(in.Content), OutChars: len(out)}, nil
}
