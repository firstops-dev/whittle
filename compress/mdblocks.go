package compress

import (
	"regexp"
	"strings"
)

// Structure-aware markdown segmentation (docs/compressor-opportunities.md,
// "markdown-structure-aware" follow-up to #1): split a de-line-numbered document
// into VERBATIM blocks (everything that must never reach the lossy prose model)
// and PROSE segments (paragraph text that may be compressed). Single source of
// truth shared by the router's doc gate (isMarkdownDoc) and the doc_read chain's
// MarkdownStructured compressor — if the two ever disagreed, content the gate
// judged safe could be compressed under different rules.
//
// The classification is deliberately over-inclusive toward VERBATIM: anything
// ambiguous stays verbatim (worst case: some prose is not compressed; never the
// reverse).

// MDSegment is one contiguous run of lines of a single kind.
type MDSegment struct {
	Verbatim bool
	Text     string // original lines joined with "\n", byte-exact
}

// MDStats summarizes a segmentation for the routing gate.
type MDStats struct {
	Headings   int // ATX/setext headings (verbatim, but counted as doc evidence)
	Sentences  int // prose lines that read as sentences (>=5 words ending '.')
	ProseChars int // total bytes classified prose
	TotalLines int // non-blank lines
	VetoLines  int // config/script-signature lines OUTSIDE fences (kv/assign/docker/code)
	Shebang    bool
}

var (
	mdFenceOpenRe = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})")
	mdSetextRe    = regexp.MustCompile(`^ {0,3}(=+|-{2,})\s*$`)
	mdThematicRe  = regexp.MustCompile(`^ {0,3}(-( *-){2,}|\*( *\*){2,}|_( *_){2,}) *$`)
	mdListRe      = regexp.MustCompile(`^ {0,3}([*+-]|\d{1,9}[.)])\s`)
	mdQuoteRe     = regexp.MustCompile(`^ {0,3}>`)
	mdLinkDefRe   = regexp.MustCompile(`^ {0,3}\[[^\]]+\]:\s`)
	mdHTMLRe      = regexp.MustCompile(`^ {0,3}<`)
	mdImageRe     = regexp.MustCompile(`^ {0,3}!\[`)
	mdIndentRe    = regexp.MustCompile(`^( {4,}|\t)\S`)
)

// SegmentMarkdown classifies lines into verbatim/prose segments and gathers the
// gate's evidence in one pass. Line-oriented CommonMark-lite; every rule that
// could go either way goes VERBATIM:
//
//   - fenced code: ``` / ~~~ open (<=3 leading spaces, any info string) through a
//     closing fence of the same char and >= length. An UNCLOSED fence makes the
//     REST OF THE DOCUMENT verbatim (truncated docs must not leak code to the
//     model). Everything inside is verbatim and exempt from veto counting.
//   - indented code (>=4 spaces or tab before non-space).
//   - ATX headings; setext headings (the underlined TEXT line is verbatim too);
//     thematic breaks; tables (>=2 '|'); blockquotes; list items; link-reference
//     definitions; HTML-leading and image-leading lines.
//   - config/script stragglers outside fences (kv/assign/docker/code-signal
//     lines): verbatim AND counted in VetoLines — sparse ones are normal in real
//     docs ("Magic: 0xE85250D6"), dominance means the file is config, not a doc
//     (the caller applies the budget).
//   - shebang: flagged; callers treat it as an absolute veto (scripts).
//   - blank lines: attached to the current prose run (paragraph separators the
//     model force-keeps); a blank inside a verbatim run splits it, which only
//     costs an extra sentinel.
func SegmentMarkdown(lines []string) ([]MDSegment, MDStats) {
	var segs []MDSegment
	var stats MDStats
	var cur []string
	curVerbatim := false

	flush := func() {
		if len(cur) > 0 {
			segs = append(segs, MDSegment{Verbatim: curVerbatim, Text: strings.Join(cur, "\n")})
			cur = nil
		}
	}
	add := func(line string, verbatim bool) {
		if verbatim != curVerbatim {
			flush()
			curVerbatim = verbatim
		}
		cur = append(cur, line)
	}

	inFence := false
	fenceChar := byte(0)
	fenceLen := 0

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		if inFence {
			add(line, true)
			if isClosingFence(line, fenceChar, fenceLen) {
				inFence = false
			}
			continue
		}

		if trimmed == "" {
			// blank: paragraph separator — ride with the prose stream
			add(line, false)
			continue
		}
		stats.TotalLines++

		switch {
		case mdFenceOpenRe.MatchString(line):
			m := mdFenceOpenRe.FindStringSubmatch(line)
			inFence, fenceChar, fenceLen = true, m[1][0], len(m[1])
			add(line, true)
		case strings.HasPrefix(trimmed, "#!"):
			stats.Shebang = true
			stats.VetoLines++
			add(line, true)
		case mdHeadingRe.MatchString(trimmed):
			stats.Headings++
			add(line, true)
		case i+1 < len(lines) && mdSetextRe.MatchString(lines[i+1]) && !mdListRe.MatchString(line) &&
			!strings.Contains(line, "|") && looksLikeTitle(trimmed):
			// setext heading: this text line + its underline are both verbatim
			stats.Headings++
			add(line, true)
			add(lines[i+1], true)
			i++
		case mdThematicRe.MatchString(line) || mdSetextRe.MatchString(line):
			add(line, true) // break, or a stray underline (never prose)
		case mdIndentRe.MatchString(line):
			add(line, true) // indented code
		case mdQuoteRe.MatchString(line), mdListRe.MatchString(line),
			mdLinkDefRe.MatchString(line), mdHTMLRe.MatchString(line),
			mdImageRe.MatchString(line), strings.Contains(line, "|"):
			add(line, true)
		case codeKeywordRe.MatchString(line) || codeCallRe.MatchString(line) ||
			assignLineRe.MatchString(line) || kvLineRe.MatchString(line) ||
			dockerDirectiveRe.MatchString(line):
			stats.VetoLines++
			add(line, true) // straggler config/code: never modeled, maybe vetoed
		case isProseLine(trimmed):
			stats.ProseChars += len(line)
			if strings.HasSuffix(trimmed, ".") && len(strings.Fields(trimmed)) >= 5 {
				stats.Sentences++
			}
			add(line, false)
		default:
			// PROSE IS OPT-IN (reviewer B1): a line the isProseLine allow-list cannot
			// positively identify as natural-language text stays VERBATIM. Unfenced
			// SQL/lisp/C-declaration lines dodge the five straggler regexes above and
			// previously fell through to the model — a runnable DELETE came back with
			// its WHERE clause corrupted, reported as a successful compression. The
			// failure direction must always be "some prose not compressed", never
			// "code reached the paraphraser".
			add(line, true)
		}
	}
	flush()
	return segs, stats
}

// isProseLine is the OPT-IN test for sending a line to the lossy model: it must
// affirmatively read as natural-language text. Everything ambiguous is verbatim
// (never modeled). Requirements:
//   - >= 3 words, first char a letter or quote;
//   - does not END in a code terminator (; { } ( = \ | &) — statement/decl shapes
//     like `int main(void);` or `struct node *next;`;
//   - no two consecutive ALL-CAPS words (SQL/shell keyword runs: SELECT ... FROM,
//     ORDER BY — prose almost never shouts twice in a row);
//   - low code-symbol density and high letter/space ratio, computed after
//     removing `inline code` spans (inline code is normal in doc prose; its
//     tokens are additionally entity-protected by the sidecar).
func isProseLine(trimmed string) bool {
	if len(strings.Fields(trimmed)) < 3 {
		return false
	}
	c := trimmed[0]
	if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '"' || c == '\'') {
		return false
	}
	switch trimmed[len(trimmed)-1] {
	case ';', '{', '}', '(', '=', '\\', '|', '&':
		return false
	}
	prevCaps := false
	for _, w := range strings.Fields(trimmed) {
		w = strings.Trim(w, ".,;:!?()\"'")
		caps := len(w) >= 2 && strings.ToUpper(w) == w && strings.ContainsAny(w, "ABCDEFGHIJKLMNOPQRSTUVWXYZ")
		if caps && prevCaps {
			return false // consecutive ALL-CAPS: keyword run, not prose
		}
		prevCaps = caps
	}
	// density checks on the line with inline-code spans removed
	s := stripInlineCode(trimmed)
	letters, symbols, total := 0, 0, 0
	for _, r := range s {
		total++
		switch {
		case r == ' ' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
			letters++
		case strings.ContainsRune(";{}()=<>|&*[]$#@~^%", r):
			symbols++
		}
	}
	if total == 0 {
		return false
	}
	return float64(symbols)/float64(total) <= 0.05 && float64(letters)/float64(total) >= 0.7
}

// stripInlineCode removes `...` spans (single line). Unterminated backticks leave
// the remainder in place (counted by the density checks — safe direction).
func stripInlineCode(s string) string {
	var b strings.Builder
	for {
		i := strings.IndexByte(s, '`')
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		j := strings.IndexByte(s[i+1:], '`')
		if j < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		s = s[i+1+j+1:]
	}
}

// isClosingFence: same fence char, run >= opening length, nothing but the fence
// (and whitespace) on the line, <=3 leading spaces.
func isClosingFence(line string, ch byte, minLen int) bool {
	s := strings.TrimLeft(line, " ")
	if len(line)-len(s) > 3 {
		return false
	}
	n := 0
	for n < len(s) && s[n] == ch {
		n++
	}
	return n >= minLen && strings.TrimSpace(s[n:]) == ""
}

// looksLikeTitle bounds what a setext heading's text line may be: short-ish,
// no sentence period — avoids consuming an ordinary paragraph line followed by
// a coincidental "---" thematic break as a "heading".
func looksLikeTitle(trimmed string) bool {
	return len(trimmed) <= 120 && !strings.HasSuffix(trimmed, ".")
}

// isMarkdownDoc reports whether de-numbered file-read content is a markdown/prose
// document whose PROSE the model may compress (verbatim blocks are protected by
// the doc_read chain's MarkdownStructured compressor and never reach the model).
// Judgment runs on the segmentation:
//   - shebang anywhere -> NO (a script, whatever its comments look like)
//   - config/script straggler lines above the budget (max(2, 5% of non-blank
//     lines)) -> NO (kv/assign-dominated files are configs: Ansible/compose/
//     Makefile; a real doc has only sparse stragglers, kept verbatim anyway)
//   - >= 2 headings AND >= 2 prose sentences (a doc is prose; scripts' comment
//     lines rarely form sentences)
//   - prose mass >= 200 bytes (else there is nothing worth a model call)
//   - prose-only character distribution >= 0.6 letters/spaces
func isMarkdownDoc(lines []string) bool {
	segs, stats := SegmentMarkdown(lines)
	if stats.Shebang {
		return false
	}
	budget := stats.TotalLines / 20
	if budget < 2 {
		budget = 2
	}
	if stats.VetoLines > budget {
		return false
	}
	if stats.Headings < 2 || stats.Sentences < 2 || stats.ProseChars < 200 {
		return false
	}
	letters, total := 0, 0
	for _, seg := range segs {
		if seg.Verbatim {
			continue
		}
		for _, r := range seg.Text {
			total++
			if r == ' ' || r == '\n' || r == '\t' ||
				(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				letters++
			}
		}
	}
	return total > 0 && float64(letters)/float64(total) >= 0.6
}
