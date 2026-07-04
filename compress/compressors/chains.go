package compressors

import (
	"os"

	"github.com/firstops-dev/whittle/compress"
)

// DefaultChains wires the per-type compressor chains. Types with no entry (code,
// diff, html, search, tabular, unknown) pass through (the pipeline reports them as
// skipped/no_compressor).
//
// TypeTabular is deliberately reduced to the LOSSLESS ANSIStrip only —
// NewTabularCompressor (row-dropping) is NOT wired. Unlike JSON, a table's headers
// aren't repeated, so there is no lossless key-hoisting win to prefer; tabular's
// only real lever on raw text was lossy row-dropping, and silent row loss misleads
// an agent the same way JSON truncation did. On real developer-agent output the
// tabular route was also ~95% misrouted line-numbered file reads (now caught by
// detectLineNumbered) with near-zero genuine tables. Keeping ANSIStrip means a
// colored table is still de-ANSI'd losslessly and a plain table passes through
// untouched (ANSIStrip is a no-op → the expansion guardrail skips it) — never
// row-dropped. NewTabularCompressor is kept + tested for a future domain-gated,
// lossless-only reintroduction; it must never silently drop rows if re-wired.
// DefaultChains wires chains from the environment: deterministic compressors
// always; the ML prose path only when WHITTLE_MODEL_URL points at a running
// model sidecar (see model/). Whittle works out of the box without it.
func DefaultChains() map[compress.ContentType][]compress.Compressor {
	return ChainsWithModel(os.Getenv("WHITTLE_MODEL_URL"))
}

// ChainsWithModel wires the per-type chains. modelURL=="" omits the prose and
// doc_read chains entirely (that content passes through untouched).
func ChainsWithModel(modelURL string) map[compress.ContentType][]compress.Compressor {
	chains := map[compress.ContentType][]compress.Compressor{
		// ANSIStrip leads every compressible chain: any tool output may be colored,
		// and the strip is lossless + cheap (no-op when there are no escapes). It
		// also ensures colored content routed to its real type (detection strips
		// first) is actually de-ANSI'd before its compressor runs on the original.
		compress.TypeLog:      {NewANSIStrip(), NewLogCompressor(DefaultLogConfig())},
		compress.TypeJSON:     {NewANSIStrip(), NewJSONCrusher()},
		compress.TypeTabular:  {NewANSIStrip()},
		compress.TypeTerminal: {NewANSIStrip(), NewLogCompressor(DefaultLogConfig())},

		// doc_read: a line-numbered file read the router POSITIVELY classified as a
		// markdown/prose document (isMarkdownDoc). Line numbers are stripped (render
		// furniture, pure token cost), then MarkdownStructured compresses ONLY the
		// prose: fenced/indented code, headings, tables, lists etc. are masked out,
		// never reach the model, and are restored byte-exact. All other line-numbered
		// reads stay TypeCode -> passthrough.
	}
	if modelURL != "" {
		llm := NewLLMLinguaAdapterWithURL(modelURL)
		chains[compress.TypeProse] = []compress.Compressor{NewANSIStrip(), llm}
		chains[compress.TypeDocRead] = []compress.Compressor{NewANSIStrip(), NewLineNumberStrip(), NewMarkdownStructuredWith(llm)}
	}
	return chains
}
