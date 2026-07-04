package compress

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode"
)

// Gate defaults. There are two distinct ceilings, and conflating them was wrong:
//   - DefaultMaxChars is a GLOBAL safety bound: content larger than this is
//     skipped before classification, purely to cap classify/regex cost on a
//     pathological input. Deterministic structural compressors (Log/JSON/ANSI)
//     crush large tool output cheaply, so this is generous, not the prose limit.
//   - DefaultProseMaxChars is a LATENCY-BUDGET ceiling for the prose path, NOT a
//     model-context one. Measured end-to-end (prod, llmlingua-2 xlm-roberta-large
//     on 4 vCPU, single-flight): latency ≈ 0.3s + 0.3s/KB, so 4.5KB ≈ 1.5s,
//     6KB ≈ 2.1s (breaches the edge hook's 2s budget), ≥9KB always times out.
//     The old 30000 value admitted 5x more than the budget could serve — every
//     6-30KB prose burned the full 2s and surfaced as an error. Above this
//     ceiling prose skips CLEANLY upfront. Raise only after a faster model
//     (bert-base ≈ 3x throughput) or chunked-parallel inference lands.
const (
	DefaultMinTokens     = 64
	DefaultMaxChars      = 262144 // 256 KiB (~64k tokens): global, deterministic-safe
	DefaultProseMaxChars = 4500   // LLMLingua-only ceiling: what fits the 2s hook budget
	minProseRatio        = 0.55
	// heuristicProseMax gates the weak codeSignal backstop: above this letter/space
	// ratio the text is clearly prose, so a few incidental file/command tokens do
	// not flag it as code. Truly structured content is symbol-heavy and scores well
	// below this; tables/code are detected directly by the router regardless.
	heuristicProseMax = 0.78
	// strongProseMin / longProseChars carve out the one case where even a definitive
	// code token (a ``` fence, a diff header) is incidental: a long, strongly-prose
	// document that merely quotes a snippet (e.g. a Claude Code session summary).
	// Such content is predominantly prose and the extractive model compresses it
	// safely, as the baseline did. Short or lower-prose content stays structured.
	strongProseMin = 0.80
	longProseChars = 2000
)

// Structural / code-signal regexes. All precompiled — the gate runs on every
// tool call. Direct ports of gate.py.
var (
	markupRe    = regexp.MustCompile(`(?is)^\s*<(\?xml|!doctype|html|svg|[a-zA-Z][\w:-]*[\s/>])`)
	jsonShapeRe = regexp.MustCompile(`^[{\[]\s*["{\[]`)

	codeFenceRe  = regexp.MustCompile("```")
	diffSigRe    = regexp.MustCompile(`(?m)^(@@ |diff --git |index [0-9a-f]+\.\.|[+\-]{3} [ab]/)`)
	shellRe      = regexp.MustCompile(`(?m)^\s*(\$ |sudo |npm |pip |go (run|build|test|mod)|git |make |cd |ls |cat |grep |docker |kubectl )`)
	pathExtRe    = regexp.MustCompile(`[\w./\-]+\.(go|py|ts|tsx|js|jsx|java|rs|c|cpp|h|hpp|rb|php|sh|sql|yaml|yml|json|toml|proto|css|html)\b`)
	codeSyntaxRe = regexp.MustCompile(`(?m)(^\s*(func |def |class |import |package |from \w+ import|const |let |var |public |private |return |if \(|for \(|while \()|[{};]\s*$|=>|::|\bself\.|\b#include\b)`)
	jsAssignRe   = regexp.MustCompile(`(?m)^\s*(const|let|var)\s+[\w$]+\s*=`)
	jsxRe        = regexp.MustCompile(`(=\{\{|style=\{|className=|</[A-Za-z]|<[A-Za-z]\w*\s+\w+=)`)

	// gateGrepRe matches grep/ripgrep "path:line:" lines WITHOUT requiring a file
	// extension. The router's searchLineRe is stricter (needs an extension, to
	// avoid stealing timestamps for routing); this looser shape is safe HERE
	// because it only votes "do not paraphrase", never a route.
	gateGrepRe = regexp.MustCompile(`^[\w.\\/\-]+:\d+:`)
)

// Metadata that can only vote SKIP (a prose MIME / tool name is never trusted
// to promote structured data to compressible).
var (
	mimeStruct = []string{"json", "xml", "yaml", "csv", "x-python", "javascript", "typescript", "x-sh", "sql", "octet-stream", "x-c", "x-java", "x-go", "x-rust", "x-ruby", "x-php"}
	toolStruct = []string{"read", "edit", "write", "bash", "grep", "glob", "notebook", "exec", "shell", "sql", "file", "cat"}
)

// looksStructured is strong, cheap evidence that text is JSON/markup, not prose.
func looksStructured(text string) bool {
	s := strings.TrimLeft(text, " \t\r\n")
	if s == "" {
		return false
	}
	c := s[0]
	if c == '{' || c == '[' {
		if json.Valid([]byte(s)) { // valid JSON: definitive, no object-graph alloc
			return true
		}
		// truncated/partial but unmistakably JSON-shaped
		if jsonShapeRe.MatchString(s) || strings.Count(s, `":`) >= 3 {
			return true
		}
	}
	if c == '<' && markupRe.MatchString(s) {
		return true
	}
	return false
}

// proseRatio is the fraction of letters+spaces in a 4000-char sample. Structured
// data is symbol-heavy and scores low.
func proseRatio(text string) float64 {
	if text == "" {
		return 1.0
	}
	sample := text
	if len(sample) > 4000 {
		sample = sample[:4000]
	}
	alpha, total := 0, 0
	for _, ch := range sample {
		total++
		if unicode.IsLetter(ch) || unicode.IsSpace(ch) {
			alpha++
		}
	}
	if total == 0 {
		return 1.0
	}
	return float64(alpha) / float64(total)
}

// codeSignal is the weighted code heuristic from gate.py (fallback signal). It is
// the sum of strong (definitive: fence/diff/assignment/jsx) and weak (incidental:
// shell/path/syntax) evidence.
func codeSignal(text string) int {
	strong, weak := codeSignals(text)
	return strong + weak
}

// codeSignals splits the heuristic into STRONG evidence (a code fence, a diff
// header, a JS assignment, JSX) that is definitive regardless of how prose-like the
// surrounding text is, and WEAK evidence (a shell command, a file path, a stray
// code-syntax token) that accumulates incidentally in long genuine prose and so
// must be discounted when the text is clearly prose.
func codeSignals(text string) (strong, weak int) {
	if text == "" {
		return 0, 0
	}
	if codeFenceRe.MatchString(text) {
		strong += 2
	}
	if diffSigRe.MatchString(text) {
		strong += 2
	}
	if jsAssignRe.MatchString(text) {
		strong += 2
	}
	if jsxRe.MatchString(text) {
		strong += 2
	}
	if shellRe.MatchString(text) {
		weak++
	}
	if pathExtRe.MatchString(text) {
		weak++
	}
	if codeSyntaxRe.MatchString(text) {
		weak++
	}
	return strong, weak
}

// classify returns (klass, signal). Order is fail-safe: every rule can only push
// toward code_structured (SKIP). Default prose only after surviving all five.
func classify(content, toolName, mime string) (klass, signal string) {
	if looksStructured(content) {
		return "code_structured", "content_structural"
	}
	if mime != "" {
		lm := strings.ToLower(mime)
		for _, x := range mimeStruct {
			if strings.Contains(lm, x) {
				return "code_structured", "mime"
			}
		}
	}
	if toolName != "" {
		lt := strings.ToLower(toolName)
		for _, k := range toolStruct {
			if strings.Contains(lt, k) {
				return "code_structured", "tool_name"
			}
		}
	}
	// Strong code evidence (fence / diff / JS assignment / JSX) is definitive,
	// except when it is an incidental snippet inside a long, strongly-prose document.
	strong, weak := codeSignals(content)
	if strong >= 2 && !(len(content) >= longProseChars && proseRatio(content) >= strongProseMin) {
		return "code_structured", "heuristic"
	}
	// Weak incidental tokens (a file path, a command name) accumulate in long
	// genuine prose — e.g. Claude Code session summaries that mention `rank.py` — so
	// only let the combined heuristic veto when the text is not clearly prose by
	// character distribution. The router detects real code/tables/JSON directly.
	if strong+weak >= 2 && proseRatio(content) < heuristicProseMax {
		return "code_structured", "heuristic"
	}
	// Structural backstop: machine-shaped tool output that slips past codeSignal
	// (grep path:line: output, stack traces) is labeled code_structured so the
	// pipeline's prose-safety guard skips it instead of paraphrasing it. This is
	// defense-in-depth — even if the router misroutes such content to prose, the
	// gate independently refuses to send it to the lossy model.
	if structuralSignal(content) {
		return "code_structured", "structural"
	}
	if proseRatio(content) < minProseRatio {
		return "code_structured", "low_prose_ratio"
	}
	return "prose", "default"
}

// structuralSignal is the gate's last-line defense for content that is clearly
// machine-structured but scores as prose: unambiguous crash/traceback markers, or
// a majority of grep-shaped "path:line:" lines. Sampled, cheap, line-anchored.
func structuralSignal(content string) bool {
	if logDefiniteRe.MatchString(content) { // panic: / traceback / goroutine header
		return true
	}
	grep, total := 0, 0
	for _, ln := range strings.Split(content, "\n") {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		total++
		if total > 50 {
			break
		}
		if gateGrepRe.MatchString(ln) {
			grep++
		}
	}
	return total > 0 && float64(grep)/float64(total) >= 0.5
}

// Decide is the gate verdict, ported from gate.py decide(). action ∈
// {compress, skip}. too_short takes precedence over code_structured, matching
// gate.py exactly.
func Decide(content string, nTokens int, toolName, mime, contentClass string, minTokens int) (action, klass, signal, reason string) {
	if contentClass == "prose" || contentClass == "code_structured" {
		klass, signal = contentClass, "override"
	} else {
		klass, signal = classify(content, toolName, mime)
	}
	if nTokens < minTokens {
		return "skip", klass, signal, "too_short"
	}
	// Structured content is NO LONGER skipped here. With content-type routing,
	// "structured" means "route to a structural compressor" (e.g. JSON ->
	// JSONCrusher), not "skip". The only hard skips at the gate are size-based
	// (too_short here, too_large in the pipeline). `klass` is still returned so
	// the pipeline can use it as a prose-safety guard — code/structured content
	// that falls through the router to prose must never reach the LLMLingua
	// (prose) compressor, which would corrupt it.
	return "compress", klass, signal, ""
}
