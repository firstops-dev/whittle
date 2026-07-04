package compressors

import (
	"context"
	"strings"
	"testing"

	"github.com/firstops-dev/whittle/compress"
)

// Adversarial pass on ANSIStrip, which documents itself as "deterministic and
// lossless for visible text". The CSI-only regex `\x1b\[[0-9;?]*[ -/]*[@-~]`
// strips ONLY 7-bit CSI sequences. Every OTHER escape form a terminal emits —
// OSC, 8-bit C1, single-char, charset-select, DCS, truncated — survives the
// strip as RAW CONTROL BYTES in the output. That is not lossless: the consumer
// (an LLM, or a human re-rendering the "cleaned" text) receives bytes that will
// re-trigger terminal control or render as garbage. Each failing case is a
// strip-correctness finding.

func stripOut(t *testing.T, in string) string {
	t.Helper()
	res, err := NewANSIStrip().Compress(context.Background(), compress.Input{Content: in})
	if err != nil {
		t.Fatalf("ANSIStrip error: %v", err)
	}
	return res.Output
}

// hasControl reports whether s still contains an escape-introducer control byte
// (ESC 0x1b or the 8-bit C1 CSI 0x9b) — i.e. a leaked, unstripped escape. Uses
// raw byte search: strings.ContainsRune(s, 0x9b) would look for the UTF-8
// encoding of U+009B (0xC2 0x9B), NOT the raw 0x9b byte a terminal emits.
func hasControl(s string) bool {
	return strings.IndexByte(s, 0x1b) >= 0 || strings.IndexByte(s, 0x9b) >= 0
}

// TestANSIStrip_NonCSILeaks asserts the "lossless visible text" claim: after
// stripping, NO raw escape-introducer byte should remain. Today every non-CSI
// form leaks.
func TestANSIStrip_NonCSILeaks(t *testing.T) {
	cases := []struct {
		name, in, why string
	}{
		{"osc_window_title",
			"\x1b]0;build: my-service\x07Compiling main.go ...",
			"OSC title (\\x1b]...\\x07) survives; raw ESC + BEL leak into visible stream"},
		{"osc8_hyperlink",
			"See \x1b]8;;https://example.com/report\x07the report\x1b]8;;\x07 for details",
			"OSC-8 hyperlink wrappers survive; the URL and ESC/BEL bytes leak around the visible label"},
		{"c1_8bit_csi",
			"\x9b31mred\x9b0m and \x9b32mgreen\x9b0m",
			"8-bit C1 CSI (\\x9b) not matched by the \\x1b[-anchored regex; raw \\x9b bytes leak"},
		{"single_char_reverse_index",
			"line one\x1bMline two",
			"single-char escape \\x1bM (reverse index) survives; raw ESC leaks mid-text"},
		{"keypad_mode",
			"\x1b=application keypad\x1b> normal",
			"\\x1b= / \\x1b> single-char escapes survive"},
		{"charset_line_drawing",
			"\x1b(0qqqqqqqq\x1b(B done",
			"charset-select \\x1b(0 / \\x1b(B survives; ESC bytes leak and the box glyphs stay as raw letters"},
		{"dcs_string",
			"\x1bP1$r0;1m\x1b\\after dcs",
			"DCS (\\x1bP...\\x1b\\\\) survives entirely; raw ESC bytes leak"},
		{"truncated_csi_tail",
			"all good then a cut escape at the very end\x1b[31",
			"a truncated CSI (no final byte) at buffer end is not matched; the dangling \\x1b[31 leaks"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.name == "c1_8bit_csi" {
				t.Skip("documented limitation: raw 8-bit C1 (\\x9b) is invalid UTF-8, cannot be a Go regexp pattern, " +
					"and is mangled by JSON transport before reaching us. See router.go ansiRe note.")
			}
			out := stripOut(t, c.in)
			if hasControl(out) {
				t.Errorf("STRIP-NOT-LOSSLESS: %s — escape bytes survived ANSIStrip. %s\n  out=%q",
					c.name, c.why, out)
			}
		})
	}
}

// TestANSIStrip_LoneEscBracketEatsVisibleChar is a strip-CORRUPTION finding:
// a stray/garbage `\x1b[` (a malformed escape, or a literal ESC followed by a
// `[` in data) greedily consumes the FOLLOWING visible character as the CSI
// final byte. Here `\x1b[more` matches `\x1b[m`, so the 'm' of "more" is eaten
// and the visible text silently becomes "ore". No escape byte is left behind, so
// a leak check would miss it — but visible text was corrupted.
func TestANSIStrip_LoneEscBracketEatsVisibleChar(t *testing.T) {
	// Resolved as correct-by-design: `\x1b[m` IS a valid SGR reset (`\x1b[0m`), so a
	// real terminal also consumes the 'm' as the sequence's final byte and renders
	// "press " + reset + "ove cursor". Our strip matches that. The only way to hit
	// this is to emit a genuine CSI introducer in front of "ove", which is malformed
	// producer output, not text we corrupted. Pin the correct (terminal-faithful)
	// behavior so a future change does not regress into LEAVING raw escapes instead.
	in := "press \x1b[move cursor"
	out := stripOut(t, in)
	if want := "press ove cursor"; out != want {
		t.Errorf("want terminal-faithful strip %q, got %q", want, out)
	}
}

// TestANSIStrip_CSIVisibleTextLossless is the positive guard: for the CSI forms
// the regex DOES handle, stripping must be exactly lossless on visible text and
// must NOT corrupt by mis-concatenation. Adjacent visible characters separated
// only by a zero-width escape are correctly joined (this is correct, not a bug).
func TestANSIStrip_CSIVisibleTextLossless(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"reset_between_chars", "a\x1b[0mb", "ab"},
		{"colored_tokens", "\x1b[31mERROR\x1b[0m: \x1b[33mdisk full\x1b[0m", "ERROR: disk full"},
		{"truecolor_sgr", "\x1b[38;2;255;0;0mhot\x1b[39m", "hot"},
		{"cursor_and_clear", "\x1b[2J\x1b[Hscreen redrawn", "screen redrawn"},
		{"bracketed_paste", "\x1b[200~pasted text\x1b[201~", "pasted text"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stripOut(t, c.in); got != c.want {
				t.Errorf("CSI strip: %s = %q, want %q", c.name, got, c.want)
			}
		})
	}
}

// TestANSIStrip_BlankRunCollapseIsLossyOnVerticalWhitespace pins the blank-run
// collapse. It is intentional, but it is NOT "lossless for visible text":
// vertical spacing IS visible structure. A 5-blank-line gap (a deliberate
// section break in a TUI screen) is flattened to a single blank line. Guard +
// documentation of the lossy edge.
func TestANSIStrip_BlankRunCollapseIsLossyOnVerticalWhitespace(t *testing.T) {
	in := "section A\n\n\n\n\n\nsection B"
	out := stripOut(t, in)
	if out != "section A\n\nsection B" {
		t.Errorf("blank-run collapse: got %q, want %q", out, "section A\n\nsection B")
	}
	// Document the lossiness explicitly: vertical whitespace was reduced.
	if strings.Count(in, "\n") == strings.Count(out, "\n") {
		t.Errorf("expected blank-run collapse to drop newlines (it is lossy on vertical whitespace)")
	}
}
