// Package compressors holds the concrete Compressor implementations and wires
// the default per-type chains. It imports the compress package for the core
// types; the compress package never imports back (no cycle).
package compressors

import (
	"context"
	"regexp"
	"strings"

	"github.com/firstops-dev/whittle/compress"
)

// blankRunRe matches a run of 3+ newlines (blank lines, optional indent).
var blankRunRe = regexp.MustCompile(`\n[ \t]*(\n[ \t]*){2,}`)

// ANSIStrip removes ANSI escape codes and collapses runs of blank lines. It is
// deterministic and lossless for visible text. Used as the first link in the log,
// tabular, terminal, json and prose chains (any tool output may be colored). The
// escape stripping itself is delegated to compress.StripANSI so detection and
// compression share one definition of "an escape".
//
// For TERMINAL-classified content ONLY it additionally collapses carriage-return
// overwrite chains (progress bars, spinners: `frame1\rframe2\r...`) to what the
// terminal actually displayed - CR returns the cursor to column 0 and subsequent
// output overwrites in place, so earlier frames were never persistently visible.
// Lossless by terminal-emulation semantics (measured 99% on a progress stream,
// docs/compressor-opportunities.md #4). Gated to TypeTerminal so a lone \r inside
// prose/JSON/log data is never touched.
type ANSIStrip struct{}

func NewANSIStrip() ANSIStrip { return ANSIStrip{} }

func (ANSIStrip) Name() string { return "ansi_strip" }

// Handles returns true for every type - stripping escape codes is always safe.
func (ANSIStrip) Handles(compress.ContentType) bool { return true }

func (a ANSIStrip) Compress(_ context.Context, in compress.Input) (compress.Result, error) {
	out := compress.StripANSI(in.Content)
	if in.ContentType == compress.TypeTerminal {
		out = collapseCROverwrites(out)
	}
	out = blankRunRe.ReplaceAllString(out, "\n\n")
	return compress.Result{Output: out, Strategy: a.Name(), InChars: len(in.Content), OutChars: len(out)}, nil
}

// collapseCROverwrites renders each line's CR-overwrite chain to its final visible
// state, as a terminal would: `\r` moves the cursor to column 0 and the next
// segment overwrites IN PLACE - characters of the previous render beyond the new
// segment's length remain visible ("abcdef\rxy" displays "xycdef"). A trailing
// `\r` before `\n` (CRLF) leaves the buffer unchanged, so Windows line endings
// normalize to `\n` (the terminal-displayed reality for terminal output).
//
// The overlay is RUNE-based (reviewer B1: byte-offset splicing cut multibyte
// runes in half and emitted invalid UTF-8 - progress bars use block/braille
// runes, and re-rendered accented text straddles boundaries). Rune==cell is an
// approximation (wide CJK cells differ), but it can never corrupt the encoding.
//
// A line's chain is collapsed ONLY when its segments look like REWRITE FRAMES of
// each other (reviewer B2: a `\r`-record data file - classic-Mac text, `\r`-
// separated exports - otherwise collapses to its last record, total silent data
// loss). Progress frames share long prefixes ("...4%" / "...5%") or suffixes
// (spinner-rune + fixed label); unrelated data records share neither. Lines whose
// segments don't qualify are left VERBATIM - never destroyed.
//
// NOTE: an ANSI erase-line (ESC[K) was already removed by StripANSI, so a frame
// that relied on it may retain stale trailing characters from a longer earlier
// frame. That errs on KEEPING more than the terminal showed - never losing.
func collapseCROverwrites(s string) string {
	if !strings.Contains(s, "\r") {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if !strings.Contains(line, "\r") {
			continue
		}
		segs := strings.Split(line, "\r")
		if !compress.CRSegmentsLookLikeRewrites(segs) {
			continue // not overwrite frames: leave the line untouched
		}
		buf := []rune(segs[0])
		for _, seg := range segs[1:] {
			if seg == "" {
				continue // bare CR (incl. the CR of CRLF): cursor move only
			}
			r := []rune(seg)
			if len(r) >= len(buf) {
				buf = r
			} else {
				buf = append(r, buf[len(r):]...) // overwrite prefix, keep visible tail
			}
		}
		lines[i] = string(buf)
	}
	return strings.Join(lines, "\n")
}
