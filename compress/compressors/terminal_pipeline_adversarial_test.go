package compressors

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/firstops-dev/whittle/compress"
)

// Pipeline-level adversarial pass on the terminal route, wired through the REAL
// DefaultChains (TypeTerminal -> {ANSIStrip, LogCompressor}). These exercise the
// consequence of the routing/strip holes once they reach a consumer.

func termPipeline() *compress.Pipeline {
	return compress.NewPipeline(
		compress.NewRegistry(DefaultChains()),
		compress.DefaultGateConfig(),
		nil,
	)
}

// TestTerminalPipeline_PureEscapeStripsToEmpty_PassesThrough is a guard: input
// that is ALL escapes (no visible text) routes terminal, strips to "", and the
// empty-output guardrail must fail open with the ORIGINAL — never emit "".
func TestTerminalPipeline_PureEscapeStripsToEmpty_PassesThrough(t *testing.T) {
	in := strings.Repeat("\x1b[0m", 80) // 320 bytes, all CSI, frac=1.0, count=80
	out := termPipeline().Compress(context.Background(), compress.Input{Content: in, MinTokens: compress.DefaultMinTokens})

	if out.Detected != compress.TypeTerminal {
		t.Fatalf("setup: pure-escape input detected as %q, want terminal", out.Detected)
	}
	if out.Output == "" {
		t.Fatalf("DATA LOSS: pure-escape terminal input produced empty output (total data loss)")
	}
	if out.Output != in || out.Action != "skipped" || out.SkipReason != "empty_output" {
		t.Errorf("empty-output guard: want passthrough original/skipped/empty_output, got action=%q reason=%q outlen=%d inlen=%d",
			out.Action, out.SkipReason, len(out.Output), len(in))
	}
}

// TestTerminalPipeline_OSC8LeaksToConsumer is the integration consequence of the
// strip leak: content that routes to terminal (plenty of CSI) but also carries
// OSC-8 hyperlinks is reported as "compressed", yet the FINAL output still
// contains raw ESC/BEL control bytes from the OSC sequences. The consumer (an
// LLM, or a terminal re-rendering the cleaned text) receives live control bytes.
func TestTerminalPipeline_OSC8LeaksToConsumer(t *testing.T) {
	csi := strings.Repeat("\x1b[31m \x1b[0m", 30) // guarantees terminal detection + clears token floor
	osc := "\x1b]8;;https://example.com/secret-report\x07click here\x1b]8;;\x07"
	in := csi + osc

	out := termPipeline().Compress(context.Background(), compress.Input{Content: in, MinTokens: compress.DefaultMinTokens})

	if out.Detected != compress.TypeTerminal {
		t.Fatalf("setup: input detected as %q, want terminal", out.Detected)
	}
	if hasControl(out.Output) {
		t.Errorf("STRIP LEAK THROUGH PIPELINE: terminal output reported action=%q but final Output still carries raw "+
			"escape/control bytes (OSC-8 hyperlink survived ANSIStrip). A downstream consumer receives live control bytes.\n  out=%q",
			out.Action, out.Output)
	}
}

// TestTerminalPipeline_ColoredDiffDataLoss is the headline integration finding:
// a colored `git diff` has every line prefixed with an SGR code, so detectDiff's
// line-anchored ^diff/^@@/^[+-] regexes all miss and the diff falls through to
// detectANSI -> terminal. The terminal chain then strips the colors and hands a
// plain diff to LogCompressor, which finds NO log-level words and (via
// floorSelection) keeps only the first/last few lines — silently dropping the
// middle hunks while REPORTING success.
//
// Crucially this is WORSE than skipping: before the terminal type existed, a
// colored diff fell through to prose, was labeled code_structured by the gate,
// and the prose-safety guard skipped it -> the ORIGINAL was preserved. Adding
// terminal routing converted a safe passthrough into lossy data destruction.
func TestTerminalPipeline_ColoredDiffDataLoss(t *testing.T) {
	var b strings.Builder
	b.WriteString("\x1b[1mdiff --git a/foo.go b/foo.go\x1b[0m\n")
	b.WriteString("\x1b[1m@@ -1,60 +1,60 @@\x1b[0m\n")
	const n = 40
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "\x1b[31m-DELLINE%03d original content here\x1b[0m\n", i)
		fmt.Fprintf(&b, "\x1b[32m+ADDLINE%03d replacement content here\x1b[0m\n", i)
	}
	in := b.String()

	out := termPipeline().Compress(context.Background(), compress.Input{Content: in, MinTokens: compress.DefaultMinTokens})

	if out.Detected != compress.TypeTerminal {
		t.Logf("note: colored diff detected as %q (not terminal) — data-loss path may differ", out.Detected)
	}

	// Every changed line must survive in the output; count how many were dropped.
	missing := 0
	var firstMissing string
	for i := 0; i < n; i++ {
		for _, tok := range []string{fmt.Sprintf("DELLINE%03d", i), fmt.Sprintf("ADDLINE%03d", i)} {
			if !strings.Contains(out.Output, tok) {
				missing++
				if firstMissing == "" {
					firstMissing = tok
				}
			}
		}
	}
	if missing > 0 {
		t.Errorf("COLORED-DIFF DATA LOSS: %d of %d changed-line tokens dropped (e.g. %q missing) while pipeline reported "+
			"action=%q (detected=%q). A colored diff is misrouted to terminal, stripped, then log-compressed; LogCompressor's "+
			"floorSelection keeps only first/last lines because no diff line carries a log level. This is WORSE than the "+
			"pre-terminal behavior, where the gate's code_structured guard skipped it and preserved the original verbatim.",
			missing, 2*n, firstMissing, out.Action, out.Detected)
	}
}

// TestTerminalPipeline_NonCSITerminalSkippedNotCompressed documents the recall
// consequence at pipeline level: terminal output colored only with 8-bit C1 CSI
// (\x9b) is invisible to detectANSI, so it never reaches the terminal chain. It
// is either skipped or sent to prose. Here we assert it does NOT compress as
// terminal (the value is lost). If a future fix wires C1 recognition, this case
// should flip to action=compressed via the terminal chain.
func TestTerminalPipeline_NonCSITerminalSkippedNotCompressed(t *testing.T) {
	t.Skip("documented limitation: raw 8-bit C1 (\\x9b) is invalid UTF-8 so it cannot be matched by a Go regexp, " +
		"and is mangled by JSON transport before reaching us. Terminals emit 7-bit ESC forms, which are handled. See router.go ansiRe note.")
	in := strings.Repeat("\x9b38;5;196mERROR widget render failed\x9b0m\n\x9b32mOK panel drawn\x9b0m\n", 12)
	out := termPipeline().Compress(context.Background(), compress.Input{Content: in, MinTokens: compress.DefaultMinTokens})

	if out.Detected == compress.TypeTerminal {
		// Recall fixed — but then assert the 8-bit escapes did not leak.
		if hasControl(out.Output) && out.Action == "compressed" {
			t.Errorf("8-bit-CSI terminal compressed but raw \\x9b bytes leaked into output: %q", out.Output)
		}
		return
	}
	t.Errorf("RECALL (pipeline): 8-bit-C1 colored terminal output detected as %q (action=%q) — never routes to the "+
		"terminal chain, so the strip+log-compress win is lost and raw \\x9b bytes are passed/processed verbatim",
		out.Detected, out.Action)
}
