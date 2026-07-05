package compressors

import (
	"context"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/firstops-dev/whittle/compress"
)

// Per-line classification regexes (each call sees one line, so ^/$ anchor to the
// line). Precompiled - the log path is hot.
var (
	reError   = regexp.MustCompile(`(?i)\b(error|fatal|panic|exception)\b`)
	reFail    = regexp.MustCompile(`(?i)\b(fail(ed|ure)?)\b`)
	reWarn    = regexp.MustCompile(`(?i)\b(warn(ing)?)\b`)
	reInfo    = regexp.MustCompile(`(?i)\b(info|notice)\b`)
	reDebug   = regexp.MustCompile(`(?i)\b(debug)\b`)
	reTrace   = regexp.MustCompile(`(?i)\b(trace)\b`)
	reStack   = regexp.MustCompile(`(?i)(^\s+at\s|^\s+file "|^\s*goroutine\s|^\s+\.\.\.|^\s+[\w./$-]+\.(go|py|js|java|rb|rs):\d+|^traceback|^panic:)`)
	reSummary = regexp.MustCompile(`(?i)(\b\d+\s+(passed|failed|error|errors|warning|warnings|tests?|skipped)\b|tests?\s+run|test\s+summary|^=+$|^-{3,}\s|build (succeeded|failed|successful)|\bsummary\b|\d+\s+of\s+\d+)`)

	// Stack-trace CONTINUATION frames that sit at column 0 (so reStack, which is
	// indentation-anchored, misses them): Go function frames like
	// `main.handler(0x1, 0x2)` / `main.main()`, and any `file.ext:line` reference.
	frameCallRe = regexp.MustCompile(`^\s*[\w./$<>*-]+\([^)]*\)\s*$`)
	frameFileRe = regexp.MustCompile(`(?i)[\w./\-]+\.(go|py|js|java|rb|rs|ts|c|cpp|cc):\d+`)

	reHex  = regexp.MustCompile(`0x[0-9a-fA-F]+`)
	rePath = regexp.MustCompile(`/[\w./\-]+`)
	// reDigitOrVersion collapses counter-like digit runs to N for warning dedup,
	// but PRESERVES version tokens (v2, v3) so genuinely distinct messages such as
	// "use v2" / "use v3" are not merged.
	reDigitOrVersion = regexp.MustCompile(`(?i)\bv\d+\b|\d+`)
)

type level int

const (
	lvUnknown level = iota
	lvTrace
	lvDebug
	lvInfo
	lvWarn
	lvFail
	lvError
)

func baseScore(lv level) float64 {
	switch lv {
	case lvError, lvFail:
		return 1.0
	case lvWarn:
		return 0.5
	case lvInfo:
		return 0.1
	case lvDebug:
		return 0.05
	case lvTrace:
		return 0.02
	default:
		return 0.1
	}
}

// LogConfig bounds the selection. Zero values are NOT defaults - use
// DefaultLogConfig.
type LogConfig struct {
	MaxErrors          int
	MaxWarnings        int
	MaxStackTraces     int
	StackTraceMaxLines int
	MaxTotalLines      int
}

func DefaultLogConfig() LogConfig {
	return LogConfig{
		MaxErrors:          20,
		MaxWarnings:        20,
		MaxStackTraces:     5,
		StackTraceMaxLines: 30,
		MaxTotalLines:      200,
	}
}

// LogCompressor keeps the signal in build/test/runtime logs: errors, fails,
// deduped warnings, stack traces and summary lines (plus a line of neighbor
// context), dropping the INFO/DEBUG noise. Ported from Headroom log_compressor.
type LogCompressor struct{ cfg LogConfig }

func NewLogCompressor(cfg LogConfig) LogCompressor { return LogCompressor{cfg: cfg} }

func (LogCompressor) Name() string { return "log_compressor" }

func (LogCompressor) Handles(ct compress.ContentType) bool {
	// Terminal output, once ANSI-stripped, is log-shaped - keep errors/results, drop
	// the noise.
	return ct == compress.TypeLog || ct == compress.TypeTerminal
}

type lineMeta struct {
	lvl     level
	stack   bool
	summary bool
	score   float64
}

func (l LogCompressor) Compress(_ context.Context, in compress.Input) (compress.Result, error) {
	lines := strings.Split(in.Content, "\n")
	n := len(lines)
	meta := make([]lineMeta, n)
	for i, ln := range lines {
		meta[i] = classifyLine(ln)
	}
	extendStacks(lines, meta)

	selected := make([]bool, n)
	selectFirstLastTop(collectLevel(meta, lvError), meta, l.cfg.MaxErrors, selected)
	selectFirstLastTop(collectLevel(meta, lvFail), meta, l.cfg.MaxErrors, selected)
	selectWarnings(lines, meta, l.cfg.MaxWarnings, selected)
	selectStacks(meta, l.cfg.MaxStackTraces, l.cfg.StackTraceMaxLines, selected)
	for i := range meta {
		if meta[i].summary {
			selected[i] = true
		}
	}
	addNeighbors(selected)
	enforceCap(meta, l.cfg.MaxTotalLines, selected)
	floorSelection(selected)

	// Emit kept lines in order, replacing each contiguous run of dropped lines with
	// a "... [N lines omitted]" marker so the drop is never silent - an agent reading
	// the output can see where and how much was elided (matches TabularCompressor).
	out := make([]string, 0, n)
	gap := 0
	flushGap := func() {
		if gap > 0 {
			out = append(out, "... ["+strconv.Itoa(gap)+" lines omitted]")
			gap = 0
		}
	}
	for i := 0; i < n; i++ {
		if selected[i] {
			flushGap()
			out = append(out, lines[i])
		} else {
			gap++
		}
	}
	flushGap() // trailing dropped run
	res := strings.Join(out, "\n")
	// Never expand: an all-signal log (little dropped) plus omission markers can end
	// up larger than the input. In that case there is nothing to compress - hand back
	// the original so the pipeline's guardrail skips it, rather than returning a
	// larger, marker-littered output.
	if len(res) >= len(in.Content) {
		return compress.Result{Output: in.Content, Strategy: l.Name(), InChars: len(in.Content), OutChars: len(in.Content)}, nil
	}
	return compress.Result{Output: res, Strategy: l.Name(), InChars: len(in.Content), OutChars: len(res)}, nil
}

func classifyLine(ln string) lineMeta {
	lv := detectLevel(ln)
	stack := reStack.MatchString(ln)
	summary := reSummary.MatchString(ln)
	s := baseScore(lv)
	if stack {
		s += 0.3
	}
	if summary {
		s += 0.4
	}
	if s > 1.0 {
		s = 1.0
	}
	return lineMeta{lvl: lv, stack: stack, summary: summary, score: s}
}

func detectLevel(ln string) level {
	switch {
	case reError.MatchString(ln):
		return lvError
	case reFail.MatchString(ln):
		return lvFail
	case reWarn.MatchString(ln):
		return lvWarn
	case reInfo.MatchString(ln):
		return lvInfo
	case reDebug.MatchString(ln):
		return lvDebug
	case reTrace.MatchString(ln):
		return lvTrace
	default:
		return lvUnknown
	}
}

func collectLevel(meta []lineMeta, lv level) []int {
	var out []int
	for i := range meta {
		if meta[i].lvl == lv {
			out = append(out, i)
		}
	}
	return out
}

// selectFirstLastTop keeps the first, last, and highest-scoring lines of a
// category, up to cap total.
func selectFirstLastTop(idx []int, meta []lineMeta, cap int, selected []bool) {
	if len(idx) == 0 || cap <= 0 {
		return
	}
	chosen := map[int]bool{idx[0]: true, idx[len(idx)-1]: true}
	rest := make([]int, len(idx))
	copy(rest, idx)
	sort.SliceStable(rest, func(a, b int) bool { return meta[rest[a]].score > meta[rest[b]].score })
	for _, i := range rest {
		if len(chosen) >= cap {
			break
		}
		chosen[i] = true
	}
	for i := range chosen {
		selected[i] = true
	}
}

// selectWarnings keeps deduped warnings (conservative normalize) up to cap.
func selectWarnings(lines []string, meta []lineMeta, cap int, selected []bool) {
	if cap <= 0 {
		return
	}
	seen := map[string]bool{}
	count := 0
	for i := range meta {
		if meta[i].lvl != lvWarn {
			continue
		}
		key := normalizeLine(lines[i])
		if seen[key] {
			continue
		}
		seen[key] = true
		selected[i] = true
		if count++; count >= cap {
			break
		}
	}
}

// selectStacks keeps up to maxStacks consecutive stack-trace blocks, each
// truncated to maxLines.
func selectStacks(meta []lineMeta, maxStacks, maxLines int, selected []bool) {
	if maxStacks <= 0 {
		return
	}
	blocks, i, n := 0, 0, len(meta)
	for i < n {
		if !meta[i].stack {
			i++
			continue
		}
		j := i
		for j < n && meta[j].stack {
			j++
		}
		if blocks < maxStacks {
			limit := j - i
			if maxLines > 0 && limit > maxLines {
				limit = maxLines
			}
			for k := i; k < i+limit; k++ {
				selected[k] = true
			}
		}
		blocks++
		i = j
	}
}

// addNeighbors widens each selected line by one line of context on each side.
// Snapshots first so context does not cascade.
func addNeighbors(selected []bool) {
	n := len(selected)
	var add []int
	for i := 0; i < n; i++ {
		if selected[i] {
			if i-1 >= 0 {
				add = append(add, i-1)
			}
			if i+1 < n {
				add = append(add, i+1)
			}
		}
	}
	for _, i := range add {
		selected[i] = true
	}
}

// enforceCap drops the lowest-scoring selected lines if the total exceeds cap.
func enforceCap(meta []lineMeta, cap int, selected []bool) {
	if cap <= 0 {
		return
	}
	var idx []int
	for i := range selected {
		if selected[i] {
			idx = append(idx, i)
		}
	}
	if len(idx) <= cap {
		return
	}
	sort.SliceStable(idx, func(a, b int) bool { return meta[idx[a]].score > meta[idx[b]].score })
	keep := make(map[int]bool, cap)
	for _, i := range idx[:cap] {
		keep[i] = true
	}
	for i := range selected {
		selected[i] = keep[i]
	}
}

// normalizeLine splits at the first ':' or '=' and normalizes ONLY the suffix
// (digits→N except version tokens, 0x..→ADDR, /path/→/PATH/). Conservative:
// lines without a delimiter dedup only on exact match.
func normalizeLine(ln string) string {
	i := strings.IndexAny(ln, ":=")
	if i < 0 {
		return ln
	}
	return ln[:i+1] + normalizeSuffix(ln[i+1:])
}

func normalizeSuffix(s string) string {
	s = reHex.ReplaceAllString(s, "ADDR")
	s = rePath.ReplaceAllString(s, "/PATH/")
	// Collapse counter-like digit runs but keep version tokens distinct so
	// "use v2" / "use v3" do not dedup into one another.
	s = reDigitOrVersion.ReplaceAllStringFunc(s, func(m string) string {
		if m[0] == 'v' || m[0] == 'V' {
			return m
		}
		return "N"
	})
	return s
}

// extendStacks bridges stack-trace frames that the per-line reStack misses
// because they sit at column 0 (Go function frames, `main.main()`). Once a stack
// block has started, consecutive frame-like lines stay in the SAME block until a
// clearly-non-trace line (blank, or a line carrying an explicit log level).
func extendStacks(lines []string, meta []lineMeta) {
	inStack := false
	for i, ln := range lines {
		switch {
		case meta[i].stack:
			inStack = true
		case inStack && isTraceContinuation(ln):
			meta[i].stack = true
		default:
			inStack = false
		}
	}
}

func isTraceContinuation(ln string) bool {
	t := strings.TrimRight(ln, "\r")
	if strings.TrimSpace(t) == "" {
		return false
	}
	// An explicit log level marks a fresh log entry, ending the trace.
	if reError.MatchString(t) || reWarn.MatchString(t) || reInfo.MatchString(t) || reDebug.MatchString(t) {
		return false
	}
	return frameCallRe.MatchString(t) || frameFileRe.MatchString(t)
}

// floorSelection guarantees non-empty output for non-empty input: if nothing
// scored high enough to be kept, retain the first and last few lines so the
// compressor never returns a total-data-loss empty string.
func floorSelection(selected []bool) {
	for _, v := range selected {
		if v {
			return
		}
	}
	const keep = 5
	n := len(selected)
	for i := 0; i < keep && i < n; i++ {
		selected[i] = true
	}
	for i := n - keep; i < n; i++ {
		if i >= 0 {
			selected[i] = true
		}
	}
}
