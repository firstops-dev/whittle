package compressors

import (
	"context"
	"strings"

	"github.com/firstops-dev/whittle/compress"
)

// MDBlockSentinel is the line that stands in for one verbatim block in the
// masked document sent to the prose model. Requirements: single wordpiece-shaped
// token (like fidelity.py's FKEEPX), never plausibly present in real docs, and
// registered in the Python sidecar's FORCE_TOKENS (belt) while fidelity.protect
// also masks it as an ALL-CAPS entity (suspenders) and EXCLUDES it from
// identifier-density accounting (86 sentinels must not trigger identifier_dense).
const MDBlockSentinel = "MDBLKX"

// MarkdownStructured compresses a markdown doc_read STRUCTURE-AWARE: verbatim
// blocks (fenced/indented code, headings, tables, lists, quotes, HTML, link
// defs, config stragglers - everything compress.SegmentMarkdown marks verbatim)
// are swapped for MDBlockSentinel lines; ONLY the prose+sentinel document goes to
// the LLMLingua sidecar (one call - latency stays flat); the sentinels are then
// restored to the original blocks BYTE-EXACT, positionally.
//
// Fail-open everywhere: sentinel collision in the input, a lost/duplicated
// sentinel in the model output, or a sidecar skip/error all return the original
// content as a clean skip. Restore is count-checked (the fidelity.py restore
// design): extractive models delete in order, so the i-th surviving sentinel is
// block i; any count mismatch aborts rather than risking a mis-splice.
//
// This is what lets the router admit real technical docs (README/API/DOCS.md
// full of code fences) to doc_read: their code is protected HERE, structurally,
// not by refusing the whole document.
//
// TODO(textcompress): CCR-style recovery for the original doc (line numbers +
// uncompressed prose) once the recovery store lands - see linenumstrip.go TODO.
type MarkdownStructured struct {
	llm *LLMLinguaAdapter
}

// NewMarkdownStructuredWith allows injecting the model adapter (tests).
func NewMarkdownStructuredWith(llm *LLMLinguaAdapter) *MarkdownStructured {
	return &MarkdownStructured{llm: llm}
}

func (*MarkdownStructured) Name() string { return "md_structured" }

func (*MarkdownStructured) Handles(ct compress.ContentType) bool {
	return ct == compress.TypeDocRead
}

func (m *MarkdownStructured) Compress(ctx context.Context, in compress.Input) (compress.Result, error) {
	skip := func(reason string) (compress.Result, error) {
		return compress.Result{Skipped: true, SkipReason: reason,
			Output: in.Content, InChars: len(in.Content), OutChars: len(in.Content)}, nil
	}

	if strings.Contains(in.Content, MDBlockSentinel) {
		return skip("md_sentinel_collision") // cannot mask safely
	}

	segs, _ := compress.SegmentMarkdown(strings.Split(in.Content, "\n"))
	var masked strings.Builder
	var blocks []string
	for i, seg := range segs {
		if i > 0 {
			masked.WriteString("\n")
		}
		if seg.Verbatim {
			blocks = append(blocks, seg.Text)
			masked.WriteString(MDBlockSentinel)
		} else {
			masked.WriteString(seg.Text)
		}
	}

	// Nothing verbatim: a pure-prose doc - hand the whole thing to the model
	// directly (the masking machinery would be a no-op).
	if len(blocks) == 0 {
		return m.llm.Compress(ctx, in)
	}

	res, err := m.llm.Compress(ctx, compress.Input{
		Content:     masked.String(),
		ContentType: in.ContentType, // doc_read: adapter omits content_class (sidecar classify runs)
		Rate:        in.Rate,
		MinTokens:   0, // the pipeline's gate already ran on the full doc
	})
	if err != nil || res.Skipped {
		if err != nil {
			return compress.Result{}, err // pipeline fails open
		}
		return skip(res.SkipReason)
	}

	restored, ok := restoreBlocks(res.Output, blocks)
	if !ok {
		// A sentinel was lost or duplicated by the model - mis-splicing could put
		// code under the wrong section. Fail open.
		return skip("md_structure_guard")
	}
	return compress.Result{Output: restored, Strategy: "md_structured",
		InChars: len(in.Content), OutChars: len(restored)}, nil
}

// restoreBlocks replaces the i-th sentinel occurrence with blocks[i]. The count
// must match exactly. Sentinels can come back glued to detokenizer punctuation
// ("MDBLKX." / "wordMDBLKX"); occurrences are found by substring scan, and a
// newline is ensured on each side of the spliced block so a glued neighbor never
// lands on a code/heading line.
func restoreBlocks(compressed string, blocks []string) (string, bool) {
	if strings.Count(compressed, MDBlockSentinel) != len(blocks) {
		return "", false
	}
	var out strings.Builder
	rest := compressed
	for _, block := range blocks {
		i := strings.Index(rest, MDBlockSentinel)
		before := rest[:i]
		out.WriteString(before)
		if !strings.HasSuffix(before, "\n") && out.Len() > 0 {
			out.WriteString("\n")
		}
		out.WriteString(block)
		rest = rest[i+len(MDBlockSentinel):]
		if !strings.HasPrefix(rest, "\n") && strings.TrimSpace(rest) != "" {
			out.WriteString("\n")
		}
	}
	out.WriteString(rest)
	return out.String(), true
}
