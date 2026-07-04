package compress

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// fakeCompressor is a configurable test double.
type fakeCompressor struct {
	name string
	fn   func(in Input) (Result, error)
}

func (f fakeCompressor) Name() string             { return f.name }
func (f fakeCompressor) Handles(ContentType) bool { return true }
func (f fakeCompressor) Compress(_ context.Context, in Input) (Result, error) {
	return f.fn(in)
}

// proseInput builds a long, non-structured prose Input that clears the gate and
// forces routing to TypeLog (override) so we exercise a known chain.
func proseInput() Input {
	body := strings.Repeat("the team reviewed the plan and agreed on the next milestone today. ", 8)
	return Input{Content: body, ContentType: TypeLog, MinTokens: DefaultMinTokens}
}

func newPipelineWith(c Compressor) *Pipeline {
	chains := map[ContentType][]Compressor{TypeLog: {c}}
	return NewPipeline(NewRegistry(chains), DefaultGateConfig(), nil)
}

func TestPipelineCompressed(t *testing.T) {
	in := proseInput()
	shrink := fakeCompressor{name: "shrink", fn: func(in Input) (Result, error) {
		return Result{Output: "short"}, nil
	}}
	out := newPipelineWith(shrink).Compress(context.Background(), in)
	if out.Action != "compressed" || out.Output != "short" {
		t.Fatalf("got action=%q output=%q", out.Action, out.Output)
	}
	if out.Strategy != "shrink" {
		t.Fatalf("strategy=%q want shrink", out.Strategy)
	}
}

func TestPipelineFailOpen(t *testing.T) {
	in := proseInput()
	boom := fakeCompressor{name: "boom", fn: func(in Input) (Result, error) {
		return Result{}, errors.New("boom")
	}}
	out := newPipelineWith(boom).Compress(context.Background(), in)
	if out.Action != "skipped" || out.SkipReason != "error" {
		t.Fatalf("got action=%q reason=%q, want skipped/error", out.Action, out.SkipReason)
	}
	if out.Output != in.Content {
		t.Fatalf("fail-open must passthrough original; got %q", out.Output)
	}
}

func TestPipelineGuardrailExpansion(t *testing.T) {
	in := proseInput()
	grow := fakeCompressor{name: "grow", fn: func(in Input) (Result, error) {
		return Result{Output: in.Content + " extra extra extra"}, nil
	}}
	out := newPipelineWith(grow).Compress(context.Background(), in)
	if out.Action != "skipped" || out.SkipReason != "guardrail_expansion" {
		t.Fatalf("got action=%q reason=%q, want skipped/guardrail_expansion", out.Action, out.SkipReason)
	}
	if out.Output != in.Content {
		t.Fatalf("guardrail must passthrough original; got %q", out.Output)
	}
}

func TestPipelineGateSkip(t *testing.T) {
	shrink := fakeCompressor{name: "shrink", fn: func(in Input) (Result, error) {
		return Result{Output: "short"}, nil
	}}
	out := newPipelineWith(shrink).Compress(context.Background(), Input{Content: "too short", MinTokens: DefaultMinTokens})
	if out.Action != "skipped" || out.SkipReason != "too_short" {
		t.Fatalf("got action=%q reason=%q, want skipped/too_short", out.Action, out.SkipReason)
	}
}

func TestPipelineNoCompressor(t *testing.T) {
	in := proseInput()
	in.ContentType = TypeCode // no chain registered for code
	p := NewPipeline(NewRegistry(map[ContentType][]Compressor{}), DefaultGateConfig(), nil)
	out := p.Compress(context.Background(), in)
	if out.Action != "skipped" || out.SkipReason != "no_compressor" {
		t.Fatalf("got action=%q reason=%q, want skipped/no_compressor", out.Action, out.SkipReason)
	}
}

func TestPipelineTooLarge(t *testing.T) {
	shrink := fakeCompressor{name: "shrink", fn: func(in Input) (Result, error) {
		return Result{Output: "short"}, nil
	}}
	// Above the global MaxChars (256 KiB): skip before classify regardless of type.
	in := Input{Content: strings.Repeat("a b c d ", 40000), ContentType: TypeLog, MinTokens: DefaultMinTokens}
	out := newPipelineWith(shrink).Compress(context.Background(), in)
	if out.Action != "skipped" || out.SkipReason != "too_large" {
		t.Fatalf("got action=%q reason=%q, want skipped/too_large", out.Action, out.SkipReason)
	}
}

// TestPipelineLargeDeterministicCompresses pins the behavior change: structural
// content well above the old 30k cap (but under the global MaxChars) now routes
// to its deterministic compressor instead of being skipped as too_large.
func TestPipelineLargeDeterministicCompresses(t *testing.T) {
	shrink := fakeCompressor{name: "shrink", fn: func(in Input) (Result, error) {
		return Result{Output: "short"}, nil
	}}
	in := Input{Content: strings.Repeat("a b c d ", 5000), ContentType: TypeLog, MinTokens: DefaultMinTokens} // 40k chars
	out := newPipelineWith(shrink).Compress(context.Background(), in)
	if out.Action != "compressed" || out.Output != "short" {
		t.Fatalf("got action=%q reason=%q, want compressed (large deterministic must not skip)", out.Action, out.SkipReason)
	}
}

// TestPipelineProseTooLarge pins the prose-only ceiling: prose above
// ProseMaxChars is skipped (the LLMLingua model can't take it), without
// affecting the deterministic paths above.
func TestPipelineProseTooLarge(t *testing.T) {
	body := strings.Repeat("the team reviewed the plan and agreed on the next milestone today. ", 600) // ~40k chars prose
	in := Input{Content: body, ContentType: TypeProse, MinTokens: DefaultMinTokens}
	p := NewPipeline(NewRegistry(map[ContentType][]Compressor{}), DefaultGateConfig(), nil)
	out := p.Compress(context.Background(), in)
	if out.Action != "skipped" || out.SkipReason != "too_large_prose" {
		t.Fatalf("got action=%q reason=%q, want skipped/too_large_prose", out.Action, out.SkipReason)
	}
}

// docReadInput is a line-numbered markdown doc (>=2 headings, no code signals)
// large enough to clear the token gate.
func docReadInput() Input {
	var b strings.Builder
	lines := []string{"# Title", "", "Intro paragraph describing the system for readers today.", "", "## Section",
		"More prose explaining the memory layout in plain sentences for everyone involved."}
	n := 1
	for r := 0; r < 8; r++ {
		for _, l := range lines {
			fmt.Fprintf(&b, "%d\t%s\n", n, l)
			n++
		}
	}
	return Input{Content: b.String(), ToolName: "read", MinTokens: 0}
}

// TestPipelineDocReadNotVetoedByToolName: every Read-tool output carries
// klass=code_structured from the tool-name vote; that veto applies to the prose
// FALLBACK, not to the router's POSITIVE doc_read classification — otherwise no
// doc read could ever compress.
func TestPipelineDocReadNotVetoedByToolName(t *testing.T) {
	shrink := fakeCompressor{name: "fake_prose", fn: func(in Input) (Result, error) {
		return Result{Output: "compressed doc"}, nil
	}}
	p := NewPipeline(NewRegistry(map[ContentType][]Compressor{TypeDocRead: {shrink}}), DefaultGateConfig(), nil)
	out := p.Compress(context.Background(), docReadInput())
	if out.Detected != TypeDocRead {
		t.Fatalf("detected=%q, want doc_read", out.Detected)
	}
	if out.Action != "compressed" {
		t.Fatalf("doc_read vetoed: action=%q reason=%q", out.Action, out.SkipReason)
	}
}

// TestPipelineDocReadHonorsProseCeiling: doc_read is model-bound, so the prose
// latency ceiling applies to it exactly as to prose.
func TestPipelineDocReadHonorsProseCeiling(t *testing.T) {
	shrink := fakeCompressor{name: "fake_prose", fn: func(in Input) (Result, error) {
		return Result{Output: "x"}, nil
	}}
	cfg := DefaultGateConfig()
	cfg.ProseMaxChars = 100 // force the ceiling below the input size
	p := NewPipeline(NewRegistry(map[ContentType][]Compressor{TypeDocRead: {shrink}}), cfg, nil)
	out := p.Compress(context.Background(), docReadInput())
	if out.Action != "skipped" || out.SkipReason != "too_large_prose" {
		t.Fatalf("want skipped/too_large_prose, got %q/%q", out.Action, out.SkipReason)
	}
}
