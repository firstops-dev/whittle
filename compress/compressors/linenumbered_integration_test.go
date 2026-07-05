package compressors

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/firstops-dev/whittle/compress"
)

// TestLineNumberedReadPassesThroughEndToEnd pins the routing contract:
// a file read rendered with line-number prefixes (`N\t<line>`, from the Read tool)
// used to route to tabular (silent row-drop, 853 lines -> 41) or prose (llmlingua
// paraphrase). Through the REAL DefaultChains it must now pass through UNTOUCHED.
func TestLineNumberedReadPassesThroughEndToEnd(t *testing.T) {
	p := compress.NewPipeline(compress.NewRegistry(DefaultChains()), compress.DefaultGateConfig(), nil)

	var b strings.Builder
	for i := 1; i <= 80; i++ {
		fmt.Fprintf(&b, "%d\tline %d of the file with some descriptive content here\n", i, i)
	}
	in := compress.Input{Content: b.String(), MinTokens: 0}

	out := p.Compress(context.Background(), in)
	if out.Detected != compress.TypeCode {
		t.Fatalf("line-numbered read detected as %q, want code (passthrough)", out.Detected)
	}
	if out.Action != "skipped" || out.SkipReason != "no_compressor" {
		t.Fatalf("want skipped/no_compressor, got %q/%q", out.Action, out.SkipReason)
	}
	if out.Output != in.Content {
		t.Fatalf("line-numbered read must pass through byte-for-byte; output changed")
	}
}

// TestTabularLosslessOnly confirms tabular is now LOSSLESS-only: the row-dropping
// compressor is unwired (a plain table passes through byte-for-byte, no rows lost),
// while the leading ANSIStrip still de-colors a colored table losslessly without
// dropping any rows.
func TestTabularLosslessOnly(t *testing.T) {
	p := compress.NewPipeline(compress.NewRegistry(DefaultChains()), compress.DefaultGateConfig(), nil)

	// plain table (>maxRows=40): must NOT be row-dropped - passes through untouched.
	var b strings.Builder
	b.WriteString("NAME       STATUS    AGE   RESTARTS\n")
	for i := 0; i < 80; i++ {
		fmt.Fprintf(&b, "pod-%02d     Running   %dd    %d\n", i, i, i%3)
	}
	plain := b.String()
	out := p.Compress(context.Background(), compress.Input{Content: plain, MinTokens: 0})
	if out.Detected != compress.TypeTabular {
		t.Fatalf("table detected as %q, want tabular", out.Detected)
	}
	if out.Output != plain {
		t.Fatalf("plain table must pass through byte-for-byte (no row-drop); output changed")
	}

	// colored table: ANSIStrip removes escapes losslessly; every row survives.
	var c strings.Builder
	c.WriteString("\x1b[1mNAME\x1b[0m       \x1b[1mSTATUS\x1b[0m    \x1b[1mAGE\x1b[0m\n")
	for i := 0; i < 80; i++ {
		fmt.Fprintf(&c, "pod-%02d     \x1b[32mRunning\x1b[0m   %dd\n", i, i)
	}
	colored := c.String()
	out2 := p.Compress(context.Background(), compress.Input{Content: colored, MinTokens: 0})
	if strings.ContainsRune(out2.Output, 0x1b) {
		t.Fatalf("colored table: ANSI escapes not stripped")
	}
	if got, want := strings.Count(out2.Output, "\n"), strings.Count(colored, "\n"); got != want {
		t.Fatalf("colored table lost rows: got %d newlines, want %d", got, want)
	}
}
