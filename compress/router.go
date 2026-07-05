package compress

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Detection regexes, precompiled (hot path). Ported from Headroom's router.
var (
	diffHeaderRe = regexp.MustCompile(`^(diff --git|@@|index [0-9a-f]+\.\.|[+-]{3} [ab]/)`)
	diffChangeRe = regexp.MustCompile(`^[+-]`)
	// diffStatRe matches a `git diff --stat` summary ("N files changed, ...") which
	// carries no unified-diff headers.
	diffStatRe = regexp.MustCompile(`(?m)^\s*\d+ files? changed`)

	// markupLeadRe is the definitive markup signal: content that STARTS with an
	// xml/doctype/svg/html structural tag. Anchoring on the leading tag avoids
	// stealing JSX/code that merely contains tags mid-stream.
	markupLeadRe = regexp.MustCompile(`(?i)^<(\?xml|!doctype|svg|html|head|body|rss|feed)\b`)
	htmlTagRe    = regexp.MustCompile(`(?i)</?(div|span|p|a|ul|ol|li|table|tr|td|th|h[1-6]|br|img|script|style|head|body|html|meta|link|form|input|button|nav|section|article|footer|header)\b`)

	// searchLineRe matches grep/ripgrep "path:line:" output. It requires a filename
	// WITH extension before the line number so it does NOT match the bare "HH:MM:"
	// of a timestamp. An optional drive letter + backslashes admit Windows paths.
	searchLineRe = regexp.MustCompile(`^(?:[A-Za-z]:)?[\w.\\/\-]+\.\w+:\d+:`)

	// Log-level and build/test-status evidence must appear in LOG-SHAPED
	// POSITIONS, never as mere word presence: engineering prose is saturated
	// with lowercase "error"/"failed"/"info" mid-sentence ("the process failed
	// with connection refused", "fails open"), and the old unanchored \b-word
	// forms routed failure-themed prose paragraphs to the line-deduping
	// LogCompressor - destroying them (found by the fidelity eval: 4/12 prose
	// samples misrouted, entity retention down to 14%). Log-shaped means:
	//   - an UPPERCASE level token in the line's prefix region (timestamp/level
	//     headers: "2026-07-01 10:00:00 INFO ...", "[ERROR] ..."), or
	//   - a level key-value ("level=error", "level":"warn"), or
	//   - a line-leading "Error:"/"panic:" style marker.
	//   - a CLI-logger prefix ("npm error ...", "yarn warn ..." - the npm-style
	//     "tool level message" family), matched against a bounded tool list so
	//     prose like "The error string was..." can never qualify.
	logLevelRe = regexp.MustCompile(`^.{0,48}\b(ERROR|WARN(ING)?|INFO|FATAL|DEBUG|TRACE|PANIC|SEVERE|CRITICAL|NOTICE|ALERT)\b|(?i)\blevel["':=]+\s*"?(error|warn(ing)?|info|fatal|debug|trace|panic|critical|notice)\b|(?i)^\s*(error|warning|fatal|panic|severe|critical)\s*:`)
	// logColorLevelRe: an SGR color code immediately before a level token
	// ("\x1b[31mERROR..."). Machine output by definition - no one authors prose
	// with escape codes - so it counts as STRUCTURAL evidence, exempt from the
	// prose veto. (Needs its own pattern anyway: the SGR terminator is the
	// letter 'm', which glues onto the token and defeats \b.)
	logColorLevelRe = regexp.MustCompile("\x1b\\[[0-9;]*m(ERROR|WARN(ING)?|INFO|FATAL|DEBUG|TRACE|PANIC|SEVERE|CRITICAL)\\b")
	// logCliRe is the npm-style "tool level message" CLI-logger family. Shape-
	// anchored against a bounded tool list, so it counts as STRUCTURAL evidence
	// (exempt from the prose veto): "npm error code ELIFECYCLE" is a log line no
	// matter how prose-y the surrounding document scores, while "The error
	// string was..." can never match.
	logCliRe = regexp.MustCompile(`(?i)^\s*(npm|yarn|pnpm|pip|gem|cargo|apt|brew|docker|kubectl|systemd)\s+(error|warn(ing)?|info|notice|verbose|silly|debug)\b`)
	// Build/test status: runner-shaped lines ("--- FAIL: TestX", "ok  pkg 1.2s",
	// "2 passed, 1 failed", "FAILED tests/x.py"), or exception leads
	// ("ValueError: ...") - not prose that talks about failures. The go-test "ok"
	// form requires the line to END at the duration so prose like "ok so 3s
	// later..." cannot qualify.
	logBuildRe = regexp.MustCompile(`^(--- )?(FAIL|PASS|SKIP|FAILED|PASSED)\b|^ok\s+[\w./\-]+\s+([\d.]+m?s|\(cached\))\s*$|(?i)\b\d+\s+(passed|failed|skipped|errors?)\b|(?i)^\s*[\w.]*(exception|error):\s`)
	// logShapeRe matches structural log-line formats that carry no level word at
	// all - evidence the OLD bare-word detector caught incidentally and the
	// shaped rewrite must keep:
	//   - BSD syslog:  "Jul  1 10:00:00 host sshd[123]: ..."
	//   - klog/glog:   "E0701 10:00:00.123456   1 server.go:214] ..."
	//   - logfmt:      "ts=2026-07-01T10:00:00Z msg=... status=500" (>=2 leading k=v)
	logShapeRe = regexp.MustCompile(`^(Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)\s+\d{1,2}\s+\d{2}:\d{2}:\d{2}\b|^[IWEF]\d{4}\s+\d{2}:\d{2}:\d{2}|^\s*(\w+=\S+\s+){2,}`)
	// logDefiniteRe is an unambiguous runtime-crash / traceback marker: its mere
	// presence routes to log regardless of line ratio (a panic or traceback has few
	// "level word" lines but must never be paraphrased). Go panics included - a gap
	// Headroom's classifier leaves open.
	logDefiniteRe = regexp.MustCompile(`(?i)(^panic:|^traceback \(most recent call last\):|^\s*goroutine\s+\d+\s+\[)`)
	// logStackRe matches individual stack-frame lines (indented `at`, `File "..."`,
	// goroutine headers, hex PC offsets, `file.ext:line` frames).
	logStackRe = regexp.MustCompile(`(?i)(^panic:|^traceback|^\s+at\s|^\s*file ".*", line \d+|^\s*goroutine\s|\+0x[0-9a-f]+|^\s*[\w./$\-]+\.(go|py|js|java|rb|rs|ts):\d+)`)
	// logTimeRe matches a leading timestamp (date or HH:MM:SS) - the structural
	// signal for level-less logs (access logs, app stdout).
	logTimeRe = regexp.MustCompile(`^\s*\[?(\d{4}-\d{2}-\d{2}|\d{2}:\d{2}:\d{2})`)

	mdTableRe = regexp.MustCompile(`(?m)^\s*\|.*\|\s*$`)
	mdSepRe   = regexp.MustCompile(`(?m)^\s*\|?[ :|]*-{2,}[-: |]*$`)
	// pipeSepRe matches an ascii/psql table separator row (---+---, |---|).
	pipeSepRe = regexp.MustCompile(`(?m)^\s*[:|+-]{2,}[-+|:\s]*$`)
	// colGapRe splits on internal multi-space runs - the column-gap signal for
	// space-aligned fixed-width tables (kubectl/docker/ls -l). Headroom has this
	// parser but never wires a detector to it; we do.
	colGapRe = regexp.MustCompile(` {2,}`)

	// ansiRe matches the escape sequences a terminal emits, beyond plain CSI color:
	// OSC (window titles, OSC-8 hyperlinks; BEL- or ST-terminated), DCS/SOS/PM/APC
	// string sequences, CSI (7-bit and 8-bit C1, plus a truncated trailing one),
	// charset designations, and single-char Fe/Fp escapes. Used for both stripping
	// (lossless visible text) and terminal-density detection. Order matters: the
	// multi-char forms are listed before the single-char catch-all (leftmost-first).
	// NOTE: 8-bit C1 introducers (raw 0x9b/0x9d) are NOT matched - a raw 0x9b is
	// invalid UTF-8 and cannot live in a UTF-8 regex pattern, and such bytes would
	// already be mangled by JSON transport before reaching us. Terminals overwhelm-
	// ingly emit the 7-bit ESC forms below.
	ansiRe = regexp.MustCompile(
		"\x1b\\][\\s\\S]*?(?:\x07|\x1b\\\\)" + // OSC ... BEL|ST
			"|\x1b[PX^_][\\s\\S]*?\x1b\\\\" + // DCS/SOS/PM/APC ... ST
			"|\x1b\\[[0-9;:<=>?]*[ -/]*[@-~]" + // CSI 7-bit
			"|\x1b\\[[0-9;:<=>?]*[ -/]*$" + // truncated CSI at end of buffer
			"|\x1b[ -/][0-~]" + // charset / designation (ESC ( 0, ESC ( B, ...)
			"|\x1b[<-~]") // single-char Fe/Fp (ESC M, ESC =, ESC >, stray introducer)

	codeKeywordRe = regexp.MustCompile(`(?m)^\s*(func |def |class |import |package |return |const |let |var |public |private |if \(|for \(|while \(|}\s*$|{\s*$)`)
	// codeCallRe matches keyword-light code: `lhs = call(...)` assignments and bare
	// `call(...)` statements that carry no func/def/class keyword.
	codeCallRe = regexp.MustCompile(`^\s*[\w.\[\]]+\s*=\s*[\w.]+\s*\(|^\s*[\w.]+\([^)]*\)\s*;?\s*$`)
	// codeSnippetRe: evidence shapes that dominate SMALL/PARTIAL code reads, which
	// the keyword/call regexes miss (auditor: 200-900-token Python snippets routed
	// to prose and lossily compressed - deleted "from .packages import chardet",
	// "_ver = sys.version_info"). Dotted-from imports, block headers ending in ":",
	// decorators, bare assignments, and flow statements.
	codeSnippetRe = regexp.MustCompile(`^\s*from\s+[.\w]+\s+import\b|^\s*(try|except|finally|elif|else|with|for|while|if)\b[^\n]*:\s*$|^\s*@\w|^\s*(pass|raise|return|yield)\b|^\s*[\w.\[\]]+\s*[+\-*/|&]?=\s*\S`)

	// lineNumberedRe matches a file read rendered with a leading line-number column
	// (`   42\t<line>`, from the Read tool / `cat -n`). Exactly one tab per line makes
	// such a read look like a 2-column TSV to detectTabular; detectLineNumbered claims
	// it first so it passes through untouched instead of being row-dropped/paraphrased.
	lineNumberedRe = regexp.MustCompile(`^\s*\d+\t`)
)

// maxDetectLines bounds the line slice the detectors share. The gate caps body
// size (MaxChars) upstream, so this is a defense-in-depth bound, not the primary
// limiter.
const maxDetectLines = 500

// Detect classifies content. Ordered, first-match-wins: each detector returns a
// confidence and is taken only if it clears its gate. Falls back to prose.
//
// The content is split into lines ONCE (capped) and that slice is shared by
// every line-based detector - re-splitting per detector was the hot-path cost.
func Detect(content string) (ContentType, float64) {
	// Classify on the de-ANSI'd text: a colored diff/log/grep carries escape codes
	// before its line-start markers, which would break the structured detectors and
	// mis-route it to terminal. Stripping first lets it reach its real type;
	// compression still runs on the original (every chain strips).
	work := content
	if hasANSI(content) {
		work = StripANSI(content)
	}
	if ct, conf, ok := detectJSON(work); ok {
		return ct, conf
	}
	lines := splitOnce(work)
	if ct, conf, ok := detectDiff(lines); ok {
		return ct, conf
	}
	if ct, conf, ok := detectHTML(work); ok {
		return ct, conf
	}
	nonEmpty := nonEmptyLines(lines, 100)
	if ct, conf, ok := detectSearch(nonEmpty); ok {
		return ct, conf
	}
	if ct, conf, ok := detectLog(lines); ok {
		return ct, conf
	}
	// Line-numbered file reads are claimed AFTER the structured detectors (json/
	// diff/html/search/log). `^\d+\t` alone cannot tell a `cat -n` read from a
	// tab-separated stream whose first column is a bare integer (epoch, PID,
	// sequence id) - a genuine log of that shape must reach detectLog FIRST and be
	// compressed. Only content no structured detector claimed - a real file read -
	// falls here and passes through, instead of being row-dropped (tabular) or
	// paraphrased (prose fallback).
	if ct, conf, ok := detectLineNumbered(work, nonEmpty); ok {
		return ct, conf
	}
	if ct, conf, ok := detectTabular(work, nonEmpty); ok {
		return ct, conf
	}
	if ct, conf, ok := detectCode(lines); ok {
		return ct, conf
	}
	// ANSI-heavy terminal output that no structured detector claimed (a colored TUI
	// dump, a progress/status screen). Runs LAST so a colored log/diff/json - whose
	// keywords or shape survive the interspersed escapes - routes to its real type
	// first; only structure-less escape-heavy content falls through to here.
	if ct, conf, ok := detectANSI(content); ok {
		return ct, conf
	}
	return TypeProse, 0.0
}

// ansiMinEscapes / ansiMinFraction tune detectANSI's precision: it fires only when
// there are several escape sequences AND they are a meaningful fraction of the
// bytes (so a stray reset code in prose, or one colored word, never triggers it).
const (
	ansiMinEscapes  = 5
	ansiMinFraction = 0.08
	// ansiMinCountAbs catches sparsely-but-pervasively colored output (e.g. a
	// git-log with one or two SGR codes per line over many lines): the byte fraction
	// stays under ansiMinFraction, but this many real escape sequences is itself
	// unambiguous terminal output. Real prose/code contain zero, so precision holds.
	ansiMinCountAbs = 12
)

// detectANSI classifies raw terminal output by the density of ANSI escape codes,
// or by carriage-return overwrite density (a colorless progress bar / spinner
// stream carries no escapes at all - its terminal signature is many lone CRs and
// few newlines). The win is the lossless strip + CR-overwrite collapse; the chain
// then log-compresses what remains.
func detectANSI(content string) (ContentType, float64, bool) {
	if len(content) == 0 {
		return "", 0, false
	}
	// CR-overwrite signature: >= ansiMinEscapes lone CRs AND more overwrite frames
	// than newlines (a log with a couple of stray CR artifacts never qualifies;
	// a progress stream is dominated by them).
	if loneCR := strings.Count(content, "\r") - strings.Count(content, "\r\n"); loneCR >= ansiMinEscapes &&
		loneCR > strings.Count(content, "\n") &&
		crContentLooksLikeRewrites(content, 200) {
		// The rewrite-signature check keeps `\r`-delimited DATA (classic-Mac text,
		// `\r`-separated exports) out of the terminal chain, whose CR-overwrite
		// collapse would destroy all but the final record (reviewer B2).
		return TypeTerminal, 0.7, true
	}
	locs := ansiRe.FindAllStringIndex(content, -1)
	if len(locs) < ansiMinEscapes {
		return "", 0, false
	}
	esc := 0
	for _, m := range locs {
		esc += m[1] - m[0]
	}
	frac := float64(esc) / float64(len(content))
	if frac >= ansiMinFraction || len(locs) >= ansiMinCountAbs {
		return TypeTerminal, clamp1(0.5 + frac), true
	}
	return "", 0, false
}

// hasANSI reports whether s contains a 7-bit ESC escape introducer.
func hasANSI(s string) bool {
	return strings.IndexByte(s, 0x1b) >= 0
}

// StripANSI removes terminal escape sequences (CSI, OSC, DCS, charset, single-char)
// and is the single source of truth for both detection and the ANSIStrip
// compressor. Lossless on visible text. Returns s unchanged when it has no escapes.
func StripANSI(s string) string {
	if !hasANSI(s) {
		return s
	}
	return ansiRe.ReplaceAllString(s, "")
}

func detectJSON(content string) (ContentType, float64, bool) {
	s := strings.TrimSpace(content)
	// Match both objects and arrays - the gate's looksStructured already treats
	// '{' and '[' identically, so routing must too, or JSON objects (the common
	// case: API responses, `kubectl -o json`) fall through to prose and get
	// skipped by the prose-safety guard instead of reaching JSONCrusher.
	if s == "" || (s[0] != '{' && s[0] != '[') {
		return "", 0, false
	}
	if json.Valid([]byte(s)) {
		return TypeJSON, 1.0, true
	}
	return detectJSONLines(s)
}

// detectJSONLines recognizes NDJSON / json-lines (one JSON value per line) and a
// JSON document followed by trailing noise: the first non-empty line is a valid
// JSON value and a majority of non-empty lines parse as JSON values. Runs as part
// of detectJSON (first in the order) so a level-bearing NDJSON stream is claimed
// here, before detectLog/detectTabular can poach it. Headroom has no such path.
func detectJSONLines(s string) (ContentType, float64, bool) {
	total, valid, firstOK := 0, 0, false
	for _, ln := range strings.Split(s, "\n") {
		t := strings.TrimSpace(ln)
		if t == "" {
			continue
		}
		total++
		isJSON := (t[0] == '{' || t[0] == '[') && json.Valid([]byte(t))
		if total == 1 {
			firstOK = isJSON
		}
		if isJSON {
			valid++
		}
	}
	if total < 2 || !firstOK {
		return "", 0, false
	}
	if r := float64(valid) / float64(total); r >= 0.5 {
		return TypeJSON, clamp1(0.5 + 0.5*r), true
	}
	return "", 0, false
}

func detectDiff(lines []string) (ContentType, float64, bool) {
	headers, changes := 0, 0
	for _, ln := range lines {
		if diffHeaderRe.MatchString(ln) {
			headers++
			continue
		}
		if diffChangeRe.MatchString(ln) {
			changes++
		}
	}
	if headers > 0 {
		conf := clamp1(0.5 + 0.2*float64(headers) + 0.05*float64(changes))
		if conf >= 0.7 {
			return TypeDiff, conf, true
		}
	}
	// `git diff --stat`: a "N files changed" summary with bar-graph rows but no
	// unified-diff headers.
	if diffStatRe.MatchString(strings.Join(lines, "\n")) {
		return TypeDiff, 0.8, true
	}
	return "", 0, false
}

func detectHTML(content string) (ContentType, float64, bool) {
	s := strings.TrimLeft(content, " \t\r\n")
	// Markup must START with a tag. This anchor is what separates HTML/XML/SVG from
	// JSX/code that merely contains tags mid-stream (e.g. `return <div .../>`).
	if s == "" || s[0] != '<' {
		return "", 0, false
	}
	if markupLeadRe.MatchString(s) {
		return TypeHTML, 0.95, true // xml / svg / doctype / html - definitive
	}
	sample := s
	if len(sample) > 3000 {
		sample = sample[:3000]
	}
	if tags := len(htmlTagRe.FindAllStringIndex(sample, -1)); tags >= 2 {
		return TypeHTML, clamp1(0.5 + 0.1*float64(tags)), true
	}
	return "", 0, false
}

func detectSearch(nonEmpty []string) (ContentType, float64, bool) {
	if len(nonEmpty) == 0 {
		return "", 0, false
	}
	matched := 0
	for _, ln := range nonEmpty {
		if searchLineRe.MatchString(ln) {
			matched++
		}
	}
	ratio := float64(matched) / float64(len(nonEmpty))
	if ratio < 0.3 {
		return "", 0, false
	}
	conf := 0.4 + 0.6*ratio
	if conf >= 0.6 {
		return TypeSearch, conf, true
	}
	return "", 0, false
}

func detectLog(lines []string) (ContentType, float64, bool) {
	if len(lines) > 200 {
		lines = lines[:200]
	}
	if len(lines) == 0 {
		return "", 0, false
	}
	// Whole-document prose veto for WORD-based evidence (level/status tokens):
	// line-wrapped docs ABOUT log levels ("ERROR indicates an operation that
	// failed.") and test-result prose ("we saw 2 passed and 1 failed") hit the
	// word forms on enough lines to clear the ratio. Two signals must agree
	// before the veto fires, because character distribution alone cannot
	// separate "ERROR indicates..." (doc) from "ERROR failed to connect to db"
	// (level-prefixed app log):
	//   1. prose-shaped character distribution (letters+spaces >= 0.82), AND
	//   2. sentence punctuation - at least half the lines end with . ! or ?
	//      (prose is written in sentences; log lines almost never end with a
	//      period).
	// STRUCTURAL evidence (timestamps, syslog/klog/logfmt shapes, stack frames,
	// CLI-logger and SGR-colored level prefixes) is never vetoed.
	proseVeto := proseRatio(strings.Join(lines, "\n")) >= 0.82 && sentenceLineRatio(lines) >= 0.5
	matched := 0
	for _, ln := range lines {
		// Unambiguous crash/traceback markers route immediately - a panic or
		// traceback has too few "level word" lines to clear the ratio, but it must
		// never reach the paraphraser.
		if logDefiniteRe.MatchString(ln) {
			return TypeLog, 0.95, true
		}
		switch {
		case logStackRe.MatchString(ln), logShapeRe.MatchString(ln),
			logCliRe.MatchString(ln), logColorLevelRe.MatchString(ln):
			matched++
		case logTimeRe.MatchString(ln) && strings.Count(ln, ",") < 2:
			// Level-less timestamped log line - but not a CSV row (a date-first CSV
			// would otherwise be misread as a log).
			matched++
		case !proseVeto && (logLevelRe.MatchString(ln) || logBuildRe.MatchString(ln)):
			matched++
		}
	}
	ratio := float64(matched) / float64(len(lines))
	conf := clamp1(0.2 + 1.5*ratio)
	if conf >= 0.5 {
		return TypeLog, conf, true
	}
	return "", 0, false
}

// sentenceLineRatio is the fraction of nonempty lines that end with sentence
// punctuation - the second prong of detectLog's prose veto.
func sentenceLineRatio(lines []string) float64 {
	ended, total := 0, 0
	for _, ln := range lines {
		s := strings.TrimSpace(ln)
		if s == "" {
			continue
		}
		total++
		switch s[len(s)-1] {
		case '.', '!', '?':
			ended++
		}
	}
	if total == 0 {
		return 0
	}
	return float64(ended) / float64(total)
}

// detectLineNumbered recognizes a file read rendered with line-number prefixes
// (`N\t<line>`). Such reads were the single largest misroute: the prefix made them
// look like a 2-column TSV to detectTabular (which silently row-dropped them) and
// broke code detection (falling through to the prose paraphraser). Classifying them
// as TypeCode - which has no compressor in DefaultChains - passes them through
// UNCOMPRESSED. That is the correct default: a line-numbered read is a file an agent
// acts on line-by-line (where silent loss is worst), and its underlying content
// (code/config/prose/log) is not reliably any single compressible type. A strong
// majority is required so a genuine numbered-column table is not misclaimed.
//
// The FIRST line must also be numbered: a file read is numbered from its first
// emitted line, whereas a data table (a TSV whose first column happens to be a
// numeric id) leads with a non-numeric header row. That single check separates
// `id\tname\tscore\n1\talice\t90…` (a real table) from `1\t<line>\n2\t<line>…`
// (a line-numbered read).
// One carve-out (docs/compressor-opportunities.md #1): if the STRIPPED content is
// unmistakably a markdown/prose document, the read routes to TypeDocRead (line
// numbers stripped, then the prose model - measured ~11% of corpus tokens were doc
// reads skipped here). The doc check is zero-tolerance: any code fence, any
// code-signal line, or a weak prose ratio keeps the read TypeCode (passthrough).
// The filename is not available on this API, so the gate is content-shape, not
// extension; ambiguity always loses to passthrough.
func detectLineNumbered(work string, nonEmpty []string) (ContentType, float64, bool) {
	if len(nonEmpty) < 3 || !lineNumberedRe.MatchString(nonEmpty[0]) {
		return "", 0, false
	}
	matched := 0
	for _, ln := range nonEmpty {
		if lineNumberedRe.MatchString(ln) {
			matched++
		}
	}
	if float64(matched)/float64(len(nonEmpty)) < 0.7 {
		return "", 0, false
	}
	// The markdown-doc check scans EVERY line of the full body, not the capped
	// nonEmpty sample (reviewer C1: the model compresses the whole content, so
	// code hiding beyond a detection window must also be able to veto). Cost is
	// bounded: this branch only runs for confirmed line-numbered reads, and the
	// global MaxChars gate bounds the body upstream.
	// Judge the EXACT line sequence the chain will segment (blanks preserved -
	// reviewer C1: the gate and MarkdownStructured must run SegmentMarkdown on
	// identical inputs or their verbatim/prose decisions could diverge).
	if isMarkdownDoc(strings.Split(StripLineNumbers(work), "\n")) {
		return TypeDocRead, 0.9, true
	}
	return TypeCode, 0.9, true
}

// stripLineNumbers removes the `N\t` prefix from each line that carries one.
func stripLineNumbers(lines []string) []string {
	out := make([]string, len(lines))
	for i, ln := range lines {
		out[i] = lineNumberedRe.ReplaceAllString(ln, "")
	}
	return out
}

// StripLineNumbers removes `N\t` line-number prefixes from a whole document.
// Single source of truth with detectLineNumbered's regex (the StripANSI pattern),
// used by the doc_read chain's LineNumberStrip compressor.
func StripLineNumbers(content string) string {
	lines := strings.Split(content, "\n")
	for i, ln := range lines {
		lines[i] = lineNumberedRe.ReplaceAllString(ln, "")
	}
	return strings.Join(lines, "\n")
}

var (
	mdHeadingRe = regexp.MustCompile(`^#{1,6}\s\S`)
	// assignLineRe matches bare assignment lines - Makefile/shell/TOML/INI style
	// (`CXX := clang++`, `ENV="$1"`, `port = 8080`). These files' `# comments`
	// mimic markdown headings and can even read as sentences, but markdown prose
	// essentially never opens a line with an identifier assignment. Found via a
	// real-corpus Makefile that slipped past the heading+sentence checks.
	assignLineRe = regexp.MustCompile(`^\s*[A-Za-z_][A-Za-z0-9_]*\s*:?=`)
	// kvLineRe matches YAML/config `key: value` lines (incl. `- name: ...` list
	// entries). Reviewer B1: prose-heavy YAML (Ansible/CI/compose with doc
	// comments + sentence-like task names) dodged the `=`-keyed assignLineRe and
	// was paraphrased - a playbook lost its `become: yes` directive. Markdown
	// prose CAN open a line "Note: ..." - rejecting those too is the accepted
	// zero-tolerance cost (a missed doc over an eaten config).
	kvLineRe = regexp.MustCompile(`^\s*(-\s+)?[A-Za-z_][\w.-]*:(\s|$)`)
	// dockerDirectiveRe: Dockerfile directives dodge all three code detectors
	// (reviewer O1) - a heavily-commented Dockerfile would otherwise qualify.
	dockerDirectiveRe = regexp.MustCompile(`^\s*(FROM|RUN|COPY|ADD|ENV|ARG|WORKDIR|EXPOSE|ENTRYPOINT|CMD|USER|VOLUME|LABEL|HEALTHCHECK|ONBUILD|STOPSIGNAL|SHELL)\s`)
)

func detectTabular(content string, nonEmpty []string) (ContentType, float64, bool) {
	if mdTableRe.MatchString(content) && mdSepRe.MatchString(content) {
		return TypeTabular, 0.9, true
	}
	if len(nonEmpty) < 3 {
		return "", 0, false
	}
	for _, delim := range []string{"\t", ","} {
		first := strings.Count(nonEmpty[0], delim)
		if first == 0 {
			continue
		}
		same := 0
		for _, ln := range nonEmpty {
			if strings.Count(ln, delim) == first {
				same++
			}
		}
		ratio := float64(same) / float64(len(nonEmpty))
		if ratio >= 0.9 {
			return TypeTabular, clamp1(0.6 + 0.3*ratio), true
		}
	}
	// Pipe table (psql / ascii): a separator row plus >=2 piped rows.
	if pipeSepRe.MatchString(content) {
		piped := 0
		for _, ln := range nonEmpty {
			if strings.Contains(ln, "|") {
				piped++
			}
		}
		if piped >= 2 {
			return TypeTabular, 0.85, true
		}
	}
	// Space-aligned fixed-width columns (kubectl/docker/ls -l): a stable modal
	// column count >=3 across most rows. A prose-guard (Headroom's idea) rejects
	// sentence text so prose with stray double-spaces is not mistaken for a table.
	if tabularLooksProse(nonEmpty) {
		return "", 0, false
	}
	counts := map[int]int{}
	for _, ln := range nonEmpty {
		counts[len(colGapRe.Split(strings.TrimSpace(ln), -1))]++
	}
	mode, freq := 0, 0
	for f, c := range counts {
		if c > freq || (c == freq && f > mode) {
			mode, freq = f, c
		}
	}
	if mode >= 3 {
		if r := float64(freq) / float64(len(nonEmpty)); r >= 0.7 {
			return TypeTabular, clamp1(0.55 + 0.4*r), true
		}
	}
	return "", 0, false
}

// tabularLooksProse rejects sentence-shaped text from the space-aligned path:
// columnar rows do not end in sentence punctuation. Ported from Headroom's
// _looks_like_prose tabular guard.
func tabularLooksProse(lines []string) bool {
	if len(lines) == 0 {
		return false
	}
	sentence, total := 0, 0
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if t == "" {
			continue
		}
		total++
		if c := t[len(t)-1]; c == '.' || c == '!' || c == '?' {
			sentence++
		}
	}
	return total > 0 && float64(sentence)/float64(total) >= 0.5
}

func detectCode(lines []string) (ContentType, float64, bool) {
	if len(lines) == 0 {
		return "", 0, false
	}
	hits := 0
	for _, ln := range lines {
		if codeKeywordRe.MatchString(ln) || codeCallRe.MatchString(ln) || codeSnippetRe.MatchString(ln) ||
			kvLineRe.MatchString(ln) || dockerDirectiveRe.MatchString(ln) {
			// kv/config lines (yaml `key: value`, `- name: x`, Dockerfile directives)
			// count as code evidence: raw YAML slipped to prose and was lossily
			// compressed (bench corpus caught sample_deploy.yaml at 40% loss).
			// Prose crosses the 25%% hit ratio only if kv-shaped lines dominate -
			// and then passthrough is the safe direction anyway.
			hits++
		}
	}
	conf := clamp1(2.0 * float64(hits) / float64(len(lines)))
	if conf >= 0.5 {
		return TypeCode, conf, true
	}
	return "", 0, false
}

func clamp1(f float64) float64 {
	if f > 1.0 {
		return 1.0
	}
	return f
}

// splitOnce splits content into at most maxDetectLines lines, allocating a
// bounded slice regardless of input size (the gate caps MaxChars upstream).
func splitOnce(content string) []string {
	lines := strings.SplitN(content, "\n", maxDetectLines+1)
	if len(lines) > maxDetectLines {
		lines = lines[:maxDetectLines]
	}
	return lines
}

// nonEmptyLines returns up to limit non-blank lines from the shared slice.
func nonEmptyLines(lines []string, limit int) []string {
	out := make([]string, 0, limit)
	for _, ln := range lines {
		if len(out) >= limit {
			break
		}
		if strings.TrimSpace(ln) != "" {
			out = append(out, ln)
		}
	}
	return out
}
