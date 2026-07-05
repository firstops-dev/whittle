package compressors

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/firstops-dev/whittle/compress"
)

func stripAs(t *testing.T, ct compress.ContentType, in string) string {
	t.Helper()
	res, err := NewANSIStrip().Compress(context.Background(), compress.Input{Content: in, ContentType: ct})
	if err != nil {
		t.Fatal(err)
	}
	return res.Output
}

// TestCROverwriteCollapse pins opportunity #4: terminal CR-overwrite chains
// collapse to the final visible frame with true overlay semantics - and the
// collapse NEVER runs on non-terminal content.
func TestCROverwriteCollapse(t *testing.T) {
	t.Run("progress_bar_final_frame", func(t *testing.T) {
		var b strings.Builder
		for p := 1; p <= 100; p++ {
			b.WriteString("\rDownloading  ")
			b.WriteString(strings.Repeat("#", p/5))
		}
		b.WriteString("\n")
		out := stripAs(t, compress.TypeTerminal, b.String())
		if strings.Count(out, "Downloading") != 1 {
			t.Fatalf("expected single final frame, got: %q", out)
		}
		if !strings.Contains(out, strings.Repeat("#", 20)) {
			t.Fatalf("final frame must be the 100%% render: %q", out)
		}
	})

	t.Run("overlay_keeps_visible_tail", func(t *testing.T) {
		// terminal semantics: a shorter overwrite leaves the previous render's tail
		// visible - the classic stale-digit artifact ("100" overwritten by "99"
		// displays "990" absent an erase-line). Frames share the "counter: " label
		// so the rewrite-signature guard admits the collapse.
		if got := stripAs(t, compress.TypeTerminal, "counter: 100\rcounter: 99\n"); got != "counter: 990\n" {
			t.Fatalf("overlay wrong: %q, want %q", got, "counter: 990\n")
		}
	})

	t.Run("crlf_is_a_newline_not_an_overwrite", func(t *testing.T) {
		if got := stripAs(t, compress.TypeTerminal, "line one\r\nline two\r\n"); got != "line one\nline two\n" {
			t.Fatalf("CRLF mishandled: %q", got)
		}
	})

	t.Run("non_terminal_content_untouched", func(t *testing.T) {
		in := "prose with a stray\rcarriage return kept verbatim\n"
		for _, ct := range []compress.ContentType{compress.TypeProse, compress.TypeLog, compress.TypeJSON} {
			if got := stripAs(t, ct, in); got != in {
				t.Fatalf("collapse must not run for %s: %q", ct, got)
			}
		}
	})
}

// TestCROverwriteCollapse_UTF8 (reviewer B1): the overlay must be rune-based -
// multibyte runes at the overlay boundary must never be severed into invalid
// UTF-8. Block runes, accents, braille spinners are ordinary terminal output.
func TestCROverwriteCollapse_UTF8(t *testing.T) {
	cases := map[string]struct{ in, want string }{
		// similar frames (fixed label + changing region) -> collapse fires; the
		// overlay boundary lands inside multibyte territory and must stay valid.
		"block_progress": {"Progress: ██████████\rProgress: ██████ ok\n", "Progress: ██████ ok█\n"},
		"accent_frames":  {"Löading: ████ 40%\rLöading: done!!!!\n", "Löading: done!!!!\n"},
		"braille_spinner": {"⠋ building the project\r⠙ building the project\r⠹ building the project\n",
			"⠹ building the project\n"},
		"emoji_frames": {"🚀 launching the rocket\r✅ launching the rocket\n", "✅ launching the rocket\n"},
		// DISSIMILAR 2-frame chain: the rewrite-signature guard leaves it verbatim
		// (conservative - indistinguishable from 2 data records). Still valid UTF-8.
		"dissimilar_kept_verbatim": {"éé\rx\n", "éé\rx\n"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := stripAs(t, compress.TypeTerminal, tc.in)
			if !utf8.ValidString(got) {
				t.Fatalf("output is INVALID UTF-8: %q", got)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestCROverwriteCollapse_DataRecordsSafe (reviewer B2): `\r`-delimited DATA
// (classic-Mac text, `\r`-separated exports) must never be collapsed - neither
// via routing (must not detect as terminal) nor via the collapse itself when the
// content is terminal-typed for other reasons.
func TestCROverwriteCollapse_DataRecordsSafe(t *testing.T) {
	records := "alpha,1\rbravo,2\rcharlie,3\rdelta,4\recho,5\rfoxtrot,6\r"
	// collapse called directly on terminal-typed content: the rewrite-signature
	// guard must leave unrelated records verbatim.
	got := stripAs(t, compress.TypeTerminal, records)
	for _, rec := range []string{"alpha,1", "bravo,2", "charlie,3", "delta,4", "echo,5", "foxtrot,6"} {
		if !strings.Contains(got, rec) {
			t.Fatalf("record %q destroyed by collapse: %q", rec, got)
		}
	}
}
