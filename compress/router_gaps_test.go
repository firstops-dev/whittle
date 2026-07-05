package compress

import (
	"context"
	"sync/atomic"
	"testing"
)

// This file stress-tests the content-type ROUTING/DETECTION layer against a
// corpus of REAL coding-agent tool output. It was written after the detectJSON
// bug (objects never matched, only arrays) to find every other gap of that
// class. Failing assertions here are INTENTIONAL and are the deliverable: each
// red case documents a routing hole. Do not "fix" them by relaxing the want -
// fix router.go/gate.go, or move the case to the guard set if behavior is
// deliberately correct.
//
// Two layers are probed:
//   1. TestRouterGaps_Detection - what Detect() returns vs. what it SHOULD.
//   2. TestRouterGaps_NoStructuredLeakToProseModel - the dangerous consequence:
//      structured content that the gate labels klass=prose AND the router lands
//      on TypeProse reaches the LLMLingua *paraphrasing* model => corruption.
//      The prose-safety guard only fires for klass==code_structured, so anything
//      structured that classify() calls "prose" slips through.

// gapCorpus is shared by both tests. Inputs are short but representative; the
// routing logic is line-shape based, not length based, so size is irrelevant to
// what Detect returns.
func gapCorpus() map[string]string {
	return map[string]string{
		// ---- diff ----
		"git_diff_basic": "diff --git a/foo.go b/foo.go\n" +
			"index 1234567..89abcde 100644\n--- a/foo.go\n+++ b/foo.go\n" +
			"@@ -1,3 +1,3 @@\n-old line\n+new line\n context line here",
		"git_diff_stat": " foo.go  | 12 ++++++------\n bar.go  |  4 ++--\n" +
			" baz.go  | 20 ++++++++++++++++----\n 3 files changed, 24 insertions(+), 12 deletions(-)",
		"diff_after_echo": "$ git diff\ndiff --git a/foo.go b/foo.go\n@@ -1,2 +1,2 @@\n-a\n+b",

		// ---- search / grep ----
		"grep_rn": "src/foo.go:12:func main() {\nsrc/bar.go:44:return nil\n" +
			"internal/baz.go:7:package baz\ncmd/x.go:99:log.Println(\"x\")",
		"ripgrep_heading": "src/foo.go\n12:func main() {\n44:\treturn nil\n\nsrc/bar.go\n7:package bar",
		"grep_no_ext":     "Makefile:12:build:\nDockerfile:4:FROM golang\nLICENSE:1:MIT License",
		"grep_win_path":   "C:\\src\\foo.cs:12:public void Main()\nC:\\src\\bar.cs:44:return null;",

		// ---- log ----
		"app_log_dateonly": "2024-01-01 INFO starting up\n2024-01-01 INFO loaded config\n" +
			"2024-01-01 ERROR failed to connect\n2024-01-01 WARN retrying",
		"npm_build_log": "npm warn deprecated foo@1.0.0\nnpm error code ELIFECYCLE\n" +
			"npm error errno 1\n> build failed with exit code 1",
		"go_test_log":     "=== RUN   TestFoo\n--- FAIL: TestFoo (0.00s)\n    foo_test.go:12: expected 1 got 2\nFAIL\nexit status 1",
		"java_stacktrace": "Exception in thread \"main\" java.lang.NullPointerException\n\tat com.example.Main.run(Main.java:42)\n\tat com.example.Main.main(Main.java:12)",
		"go_panic": "panic: runtime error: index out of range [3] with length 3\n\n" +
			"goroutine 1 [running]:\nmain.main()\n\t/src/main.go:12 +0x1d\nexit status 2",
		"python_traceback": "Traceback (most recent call last):\n  File \"app.py\", line 12, in <module>\n    main()\n" +
			"  File \"app.py\", line 8, in main\n    raise ValueError(\"boom\")\nValueError: boom",
		"pure_info_log": "2024-01-01 server listening on port 8080\n2024-01-01 connection accepted from 10.0.0.1\n" +
			"2024-01-01 request handled in 12ms\n2024-01-01 connection closed cleanly",

		// ---- tabular ----
		"csv": "id,name,score\n1,alice,90\n2,bob,85\n3,carol,77",
		"tsv": "id\tname\tscore\n1\talice\t90\n2\tbob\t85\n3\tcarol\t77",
		"kubectl_get": "NAME                     READY   STATUS    RESTARTS   AGE\n" +
			"web-7d9f8c-abcde         1/1     Running   0          5d\n" +
			"api-5c6b7a-fghij         1/1     Running   2          3d\n" +
			"db-9a8b7c-klmno          0/1     Pending   0          1h",
		"docker_ps": "CONTAINER ID   IMAGE     COMMAND     STATUS         PORTS     NAMES\n" +
			"abc123         nginx     \"nginx\"     Up 2 hours     80/tcp    web\n" +
			"def456         redis     \"redis\"     Up 5 minutes   6379/tcp  cache",
		"ls_l": "total 24\n-rw-r--r--  1 user  staff   1234 Jan  1 12:00 foo.go\n" +
			"-rw-r--r--  1 user  staff   5678 Jan  1 12:01 bar.go\n" +
			"drwxr-xr-x  3 user  staff     96 Jan  1 12:02 internal",
		"psql_table": " id |  name  | score\n----+--------+-------\n  1 | alice  |    90\n  2 | bob    |    85\n  3 | carol  |    77",

		// ---- line-numbered file reads (Read tool / cat -n): must NOT route to
		// tabular (row-drop); detectLineNumbered claims them. Code stays TypeCode
		// (passthrough); an unmistakable markdown DOC routes to doc_read (prose
		// model after LineNumberStrip).
		"linenum_code": "1\tpackage main\n2\t\n3\tfunc main() {\n4\t\tfor i := 0; i < 10; i++ {\n5\t\t\tdoWork(i)\n6\t\t}\n7\t}",
		"linenum_doc":  "1\t# NephilimOS\n2\t\n3\tFull reference for kernel internals and the syscall ABI for readers.\n4\t\n5\t## Boot Sequence\n6\tThe bootloader initializes the descriptor tables then jumps to the kernel entry point.\n7\tIt verifies the magic value before handing control to the scheduler afterwards.\n8\t\n9\t## Memory Layout\n10\tThe kernel reserves the first megabyte for firmware structures and data.\n11\tThe remainder is mapped linearly for the frame allocator to divide up.",
		// gate guards: these stay TypeCode - the fenced case and the stray-code
		// case carry too little prose to clear the prose-mass/sentence floors, and
		// one heading is never enough evidence. (Fences themselves no longer veto:
		// rich fenced docs route to doc_read and MarkdownStructured masks the code.)
		"linenum_doc_with_fence":  "1\t# Guide\n2\t\n3\tIntro text for the guide.\n4\t\n5\t## Usage\n6\t```\n7\tfunc main() {}\n8\t```\n9\tMore prose here.",
		"linenum_doc_with_code":   "1\t# Notes\n2\t\n3\tSome prose describing the setup steps.\n4\timport os\n5\t\n6\t## More\n7\tAnother paragraph of prose text.",
		"linenum_doc_one_heading": "1\t# Title\n2\t\n3\tJust one heading and some plain prose following it here.\n4\tAnother sentence of ordinary text continues the document.",

		// ---- html / xml ----
		"full_html":     "<!doctype html><html><head><title>x</title></head><body><div><p>hi</p></div></body></html>",
		"html_fragment": "<div class=\"card\"><span>Hello</span><a href=\"/x\">link</a></div>",
		"xml":           "<?xml version=\"1.0\"?>\n<root>\n  <item id=\"1\">alpha</item>\n  <item id=\"2\">beta</item>\n</root>",
		"svg":           "<svg xmlns=\"http://www.w3.org/2000/svg\" width=\"100\" height=\"100\"><circle cx=\"50\" cy=\"50\" r=\"40\"/></svg>",

		// ---- code ----
		"go_code":           "package main\n\nfunc main() {\n\tfor i := 0; i < 10; i++ {\n\t\tif i > 5 {\n\t\t\treturn\n\t\t}\n\t}\n}",
		"python_code":       "def foo(x):\n    if x > 0:\n        return x * 2\n    return 0\n\nclass Bar:\n    def __init__(self):\n        self.x = 1",
		"minified_js":       "const a=1;const b=2;function f(){return a+b};const c=f();var d={x:1,y:2,z:3};",
		"jsx_code":          "export function Card() {\n  return <div className=\"card\"><span>{title}</span></div>;\n}",
		"code_few_keywords": "x = compute(a, b)\ny = transform(x)\nz = combine(y, w)\nresult = finalize(z)",

		// ---- json ----
		"json_object":          "{\"id\":42,\"name\":\"alpha\",\"items\":[1,2,3],\"nested\":{\"a\":1}}",
		"json_array":           "[{\"id\":1,\"name\":\"a\"},{\"id\":2,\"name\":\"b\"}]",
		"json_pretty":          "{\n  \"id\": 42,\n  \"name\": \"alpha\",\n  \"tags\": [\"x\", \"y\"]\n}",
		"ndjson":               "{\"level\":\"info\",\"msg\":\"start\"}\n{\"level\":\"debug\",\"msg\":\"tick\"}\n{\"level\":\"trace\",\"msg\":\"slow\"}\n{\"level\":\"info\",\"msg\":\"done\"}",
		"ndjson_no_levelwords": "{\"a\":1,\"b\":\"x\"}\n{\"a\":2,\"b\":\"y\"}\n{\"a\":3,\"b\":\"z\"}",
		"json_with_trail_log":  "{\"id\":42,\"name\":\"alpha\"}\n2024-01-01 INFO done writing",

		// ---- prose (genuine - these are regression guards, must stay correct) ----
		"plain_prose":    "The quick brown fox jumps over the lazy dog. This is an ordinary paragraph of English text with nothing structured about it at all, just words and sentences.",
		"markdown_prose": "## Heading\n\nThis is some **markdown** prose with a [link](http://x.com) and some _emphasis_. It reads like a document, not code.",
	}
}

// --- Layer 1: detection gaps -------------------------------------------------

func TestRouterGaps_Detection(t *testing.T) {
	corpus := gapCorpus()
	type tc struct {
		name     string
		want     ContentType
		guard    bool   // true => currently correct, kept as a regression guard
		severity string // for gaps only
		why      string
	}
	cases := []tc{
		// --- regression guards: routing is correct today, keep it red-proof ---
		{"git_diff_basic", TypeDiff, true, "", ""},
		{"diff_after_echo", TypeDiff, true, "", ""},
		{"grep_rn", TypeSearch, true, "", ""},
		{"app_log_dateonly", TypeLog, true, "", ""},
		{"npm_build_log", TypeLog, true, "", ""},
		{"go_test_log", TypeLog, true, "", ""},
		{"java_stacktrace", TypeLog, true, "", ""},
		{"csv", TypeTabular, true, "", ""},
		{"tsv", TypeTabular, true, "", ""},
		// line-numbered reads are claimed by detectLineNumbered: code -> TypeCode
		// (passthrough, never tabular row-drop or prose paraphrase); an unmistakable
		// markdown doc -> TypeDocRead (strip line numbers, prose model). The
		// zero-tolerance guards keep anything code-adjacent in TypeCode.
		{"linenum_code", TypeCode, true, "", ""},
		{"linenum_doc", TypeDocRead, true, "", ""},
		{"linenum_doc_with_fence", TypeCode, true, "", ""},
		{"linenum_doc_with_code", TypeCode, true, "", ""},
		{"linenum_doc_one_heading", TypeCode, true, "", ""},
		{"full_html", TypeHTML, true, "", ""},
		{"go_code", TypeCode, true, "", ""},
		{"python_code", TypeCode, true, "", ""},
		{"minified_js", TypeCode, true, "", ""},
		{"jsx_code", TypeCode, true, "", ""},
		{"json_object", TypeJSON, true, "", ""},
		{"json_array", TypeJSON, true, "", ""},
		{"json_pretty", TypeJSON, true, "", ""},
		{"plain_prose", TypeProse, true, "", ""},
		{"markdown_prose", TypeProse, true, "", ""},

		// --- KNOWN GAPS (expected to FAIL) ---

		// Stack traces / tracebacks are log-class debugging output. detectLog
		// keys off level/build words; a panic or traceback has at most one such
		// word across many lines, so the ratio never clears conf>=0.5. They fall
		// to prose -> LLMLingua paraphrase => CORRUPTION of the exact lines an
		// agent needs verbatim. This is the headline gap.
		{"go_panic", TypeLog, false, "CORRUPTION", "panic/stack trace has too few level words for detectLog (ratio<0.2+1.5*r>=0.5); lands on prose"},
		{"python_traceback", TypeLog, false, "CORRUPTION", "only 'Traceback' matches logBuildRe across 6 lines; ratio too low; lands on prose"},

		// Level-less server logs: no error/warn/info/fatal token at all. Same
		// failure as above. Common for access logs / app stdout.
		{"pure_info_log", TypeLog, false, "CORRUPTION", "no log-level keyword on any line; detectLog conf=0.2; prose"},

		// Space/column-aligned CLI tables. detectTabular only understands ',' and
		// '\t'. kubectl/docker/ls/psql align with spaces => not tabular, fall to
		// prose -> LLMLingua paraphrases pod names, statuses, sizes => CORRUPTION.
		{"kubectl_get", TypeTabular, false, "CORRUPTION", "detectTabular only checks comma/tab delimiters; space-aligned columns missed"},
		{"docker_ps", TypeTabular, false, "CORRUPTION", "space-aligned columns; detectTabular misses; prose"},
		{"ls_l", TypeTabular, false, "CORRUPTION", "space-aligned columns; leading '-' does not satisfy diffHeaderRe; prose"},
		{"psql_table", TypeTabular, false, "CORRUPTION", "pipe table without leading/trailing '|' so mdTableRe misses; no comma/tab; prose"},

		// NDJSON / jsonlines. Real structured tool output (kubectl logs -o json,
		// jq -c, bunyan). With level words it routes to log (wrong compressor);
		// without them, identical comma-count per line routes to tabular -> NO
		// compressor => SKIPPED. The latter is the exact JSON-object-bug class:
		// compressible structured data silently dropped.
		{"ndjson", TypeJSON, false, "MISROUTE", "JSON-lines matched by detectLog via level words before any JSON-aware check"},
		{"ndjson_no_levelwords", TypeJSON, false, "LOST_VALUE", "equal comma count per line => detectTabular fires => tabular has no compressor => skipped"},

		// keyword-light code: assignments/calls with no func/def/class/keyword.
		// codeKeywordRe and codeSignal both miss => prose => CORRUPTION.
		{"code_few_keywords", TypeCode, false, "CORRUPTION", "no code keyword tokens; codeKeywordRe & codeSignal both 0; prose"},

		// grep variants the searchLineRe shape misses: extensionless files and
		// Windows backslash paths. Lower frequency; search has no compressor so
		// today the cost is only that some land on prose (extensionless/grep_win).
		{"grep_no_ext", TypeSearch, false, "COSMETIC", "searchLineRe requires '.<ext>:<n>:'; Makefile/Dockerfile/LICENSE have no ext"},
		{"grep_win_path", TypeSearch, false, "CORRUPTION", "searchLineRe char class [\\w./-] excludes backslash; Windows paths miss; prose"},

		// ripgrep grouped/--heading output: filename on its own line, then
		// 'n:line'. Neither line satisfies searchLineRe. Caught by gate as
		// code_structured (safe skip), so cosmetic, but still a detection miss.
		{"ripgrep_heading", TypeSearch, false, "COSMETIC", "heading format splits path and line:content across lines; searchLineRe needs both on one line"},

		// git diff --stat: no diff/@@/+++ headers, just the bar-graph summary.
		// detectDiff needs >=1 header line. Diff has no compressor so cosmetic now.
		{"git_diff_stat", TypeDiff, false, "COSMETIC", "no diffHeaderRe line (no 'diff --git'/'@@'/'+++ b/'); only bar-graph rows"},

		// HTML fragments and XML/SVG without a recognized strong tag. detectHTML's
		// htmlTagRe whitelist misses xml/svg/custom elements. Caught by the gate's
		// looksStructured (markupRe) => safe skip, so cosmetic for corruption, but
		// the router itself never identifies them as markup.
		{"html_fragment", TypeHTML, false, "COSMETIC", "htmlStrongRe needs doctype/html/head/body; 3 whitelisted tags only reach conf 0.15<0.7"},
		{"xml", TypeHTML, false, "COSMETIC", "no XML/SVG detector in router; htmlTagRe whitelist excludes root/item; gate looksStructured catches it instead"},
		{"svg", TypeHTML, false, "COSMETIC", "no SVG detector; htmlTagRe excludes svg/circle"},

		// JSON with a trailing log line (common: a write followed by a status
		// line). Whole blob is invalid JSON so detectJSON bails; level word routes
		// it to log (wrong compressor for the JSON body).
		{"json_with_trail_log", TypeJSON, false, "MISROUTE", "trailing non-JSON line makes json.Valid false; 'INFO' routes to log"},
	}

	// Deferred cosmetic routing-label gaps: these route to a NO-compressor type
	// (search), so the routing label is cosmetic, and fixing detection cleanly
	// would mean loosening search/log detection in ways that risk stealing
	// timestamped logs. Their SAFETY (never paraphrased) is guaranteed elsewhere:
	// grep_no_ext by the gate's structuralSignal backstop, ripgrep_heading by the
	// existing codeSignal (pathExt + trailing brace) - see the leak test.
	deferred := map[string]string{
		"grep_no_ext":     "extensionless grep routing deferred; safety via gate structuralSignal",
		"ripgrep_heading": "multi-line grouped grep routing deferred; safe code_structured skip today",
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if reason, ok := deferred[c.name]; ok {
				t.Skip("deferred: " + reason)
			}
			in, ok := corpus[c.name]
			if !ok {
				t.Fatalf("corpus missing input %q", c.name)
			}
			got, conf := Detect(in)
			if got == c.want {
				return // pass: guard holds or (rare) gap got fixed
			}
			if c.guard {
				t.Errorf("REGRESSION: %s routed to %q (conf %.2f), want %q - a previously-correct case broke",
					c.name, got, conf, c.want)
				return
			}
			t.Errorf("GAP[%s]: %s routed to %q (conf %.2f), should be %q. root-cause: %s",
				c.severity, c.name, got, conf, c.want, c.why)
		})
	}
}

// --- Layer 2: the dangerous consequence - structured content reaching the
// LLMLingua prose (paraphrasing) model -----------------------------------------

// gapProseSpy is a stand-in for the LLMLingua adapter registered on TypeProse.
// If Compress is ever called, structured content reached the paraphraser.
type gapProseSpy struct{ calls int32 }

func (s *gapProseSpy) Name() string             { return "PROSE-LLMLINGUA" }
func (s *gapProseSpy) Handles(ContentType) bool { return true }
func (s *gapProseSpy) Compress(_ context.Context, in Input) (Result, error) {
	atomic.AddInt32(&s.calls, 1)
	return Result{Output: "PARAPHRASED"}, nil
}

// gapPipeline mirrors DefaultChains coverage exactly: Log, JSON, Prose have a
// compressor; code/diff/html/search/tabular/unknown have none (=> no_compressor
// skip). The Prose slot is the spy so we can detect corruption leaks.
func gapPipeline(spy *gapProseSpy) *Pipeline {
	shrink := fakeCompressor{name: "shrink", fn: func(in Input) (Result, error) {
		return Result{Output: "SHRUNK"}, nil
	}}
	chains := map[ContentType][]Compressor{
		TypeLog:   {shrink},
		TypeJSON:  {shrink},
		TypeProse: {spy},
	}
	return NewPipeline(NewRegistry(chains), DefaultGateConfig(), nil)
}

// TestRouterGaps_NoStructuredLeakToProseModel asserts the safety invariant: NO
// structured content (logs, tables, code, JSON, stack traces) may reach the
// prose paraphrasing model. The prose-safety guard in pipeline.go only blocks
// klass==code_structured; classify() returns klass=prose for plenty of
// structured-but-prose-shaped content, which then routes to TypeProse and is
// handed to LLMLingua. Each failing case below is a corruption leak.
func TestRouterGaps_NoStructuredLeakToProseModel(t *testing.T) {
	corpus := gapCorpus()

	// Inputs that are STRUCTURED and must never be paraphrased.
	mustNotLeak := []struct {
		name string
		why  string
	}{
		{"go_panic", "Go panic / stack trace - line numbers and frames must stay verbatim"},
		{"python_traceback", "Python traceback - file/line/exception must stay verbatim"},
		{"pure_info_log", "level-less server log - IPs/ports/timings must stay verbatim"},
		{"kubectl_get", "kubectl table - pod names/statuses/restart counts must stay verbatim"},
		{"docker_ps", "docker ps table - container ids/ports must stay verbatim"},
		{"ls_l", "ls -l - permissions/sizes/dates must stay verbatim"},
		{"psql_table", "psql result set - column values must stay verbatim"},
		{"code_few_keywords", "assignment-heavy code - identifiers/calls must stay verbatim"},
		{"grep_no_ext", "grep on extensionless files - path:line:match must stay verbatim"},
		{"grep_win_path", "grep with Windows paths - path:line:match must stay verbatim"},
	}
	for _, c := range mustNotLeak {
		t.Run("leak/"+c.name, func(t *testing.T) {
			spy := &gapProseSpy{}
			out := gapPipeline(spy).Compress(context.Background(), Input{Content: corpus[c.name], MinTokens: 0})
			if atomic.LoadInt32(&spy.calls) != 0 {
				t.Errorf("CORRUPTION LEAK: %s reached the prose paraphraser (detected=%q klass=%q action=%q). %s. "+
					"The prose-safety guard only blocks klass==code_structured; classify() called this prose.",
					c.name, out.Detected, out.GateKlass, out.Action, c.why)
			}
		})
	}

	// Inputs that ARE genuine prose and SHOULD reach the prose model - guards so
	// a future fix doesn't over-correct and starve real prose compression.
	mustLeak := []string{"plain_prose", "markdown_prose"}
	for _, name := range mustLeak {
		t.Run("guard/"+name, func(t *testing.T) {
			spy := &gapProseSpy{}
			out := gapPipeline(spy).Compress(context.Background(), Input{Content: corpus[name], MinTokens: 0})
			if atomic.LoadInt32(&spy.calls) == 0 {
				t.Errorf("REGRESSION: genuine prose %q did NOT reach the prose model (detected=%q klass=%q action=%q reason=%q)",
					name, out.Detected, out.GateKlass, out.Action, out.SkipReason)
			}
		})
	}
}

// TestRouterGaps_LostValueSkips pins the false-negative class: real compressible
// structured content that the router lands on a NO-compressor type and silently
// drops (the JSON-object bug pattern). These assert the content is NOT skipped
// with no_compressor; failing => lost value.
func TestRouterGaps_LostValueSkips(t *testing.T) {
	corpus := gapCorpus()
	cases := []struct {
		name string
		why  string
	}{
		{"ndjson_no_levelwords", "NDJSON misrouted to tabular (no compressor) and dropped - JSON-lines is compressible structured data"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			spy := &gapProseSpy{}
			out := gapPipeline(spy).Compress(context.Background(), Input{Content: corpus[c.name], MinTokens: 0})
			if out.Action == "skipped" && out.SkipReason == "no_compressor" {
				t.Errorf("LOST VALUE: %s skipped/no_compressor (detected=%q). %s",
					c.name, out.Detected, c.why)
			}
		})
	}
}

// TestRouter_FailureThemedProseIsNotLog pins the fidelity-eval finding: prose
// ABOUT failures (postmortems, error investigations, architecture docs that
// say "fails open") is saturated with error/failed/info words, and the old
// unanchored word-match detectors routed it to the line-deduping LogCompressor
// - which destroyed it (entity retention down to 14%). These samples come from
// the fidelity corpus (fidelity/corpus/); prose must NEVER route to log.
func TestRouter_FailureThemedProseIsNotLog(t *testing.T) {
	samples := map[string]string{
		"postmortem": "Postmortem: compressor sidecar outage, June 30. Between 01:15 and 04:41 UTC " +
			"the Python inference sidecar was down while the Go front stayed healthy, so every prose " +
			"compression request failed with connection refused - 1,874 failures in the window. Impact " +
			"was limited to lost savings because the hook fails open; no tool output was corrupted.\n\n" +
			"Root cause: the sidecar was killed by the kernel OOM killer. The process died with SIGKILL, " +
			"which produces no Python traceback, and the health check only probed the Go front, so the " +
			"task was never marked unhealthy and was not replaced.",
		"error_investigation": "Investigating the elevated 500 rate on the savings read path. The error " +
			"string was always the same: connection refused. Writes kept succeeding, which ruled out a " +
			"dead service and pointed at brief listener gaps. Log filtering showed the ledger process " +
			"restarting twice within the hour. The synchronous read API surfaces one failed dial as a " +
			"user-visible error because it has no retry, while the batched emitter retries and hides " +
			"the same blips. Short term we added a retry with backoff to the read call.",
		"failure_semantics_doc": "Authentication runs first and fails closed: a request without a valid " +
			"proof is rejected even if the network path is trusted. Policy evaluation runs third and is " +
			"split by effect. Getting this order wrong produces either false denials, if scrubbing runs " +
			"before a block rule that needed the original content, or silent bypasses. The final stage " +
			"is the audit write, which is asynchronous and fails open: losing an audit record must " +
			"never block a permitted request, and a failure returns 503 only under compliance_strict.",
		"readme_with_commands_prose": "Run the login command to open a browser window; after signing in, " +
			"the CLI exchanges your session for a token pair. The scanner reads configuration files and " +
			"reports each server with its transport. A typical first sync finds between 4 and 12 " +
			"servers. If the scan misses a custom location, pass the config path explicitly. Version " +
			"0.4.2 fixed a hang on machines with more than 64 network interfaces.",
	}
	for name, text := range samples {
		ct, conf := Detect(text)
		if ct == TypeLog {
			t.Errorf("%s: failure-themed prose routed to log (conf %.2f) - LogCompressor would destroy it", name, conf)
		}
	}
}

// TestRouter_LogLevelDocsAndTestProse_AreNotLog pins the reviewer-reproduced
// precision holes: line-wrapped documentation ABOUT log levels, and prose that
// mentions test counts, must not route to log (whole-document prose veto).
func TestRouter_LogLevelDocsAndTestProse_AreNotLog(t *testing.T) {
	samples := map[string]string{
		"level_reference_doc": "ERROR indicates an operation that failed and requires attention.\n" +
			"WARN indicates a recoverable anomaly worth investigating later.\n" +
			"INFO records routine lifecycle events for the service.\n" +
			"DEBUG is verbose diagnostic output disabled in production.\n" +
			"Choose the lowest level that still captures the incident.",
		"test_result_prose": "We saw 2 passed and 1 failed in the morning run of the suite.\n" +
			"Another run showed 3 errors remained after the rollback completed.\n" +
			"The team agreed the flaky cases needed quarantine before release.",
		"ok_prose": "ok so 3s later the worker recovered and drained the queue.\n" +
			"The retry policy behaved exactly as designed during the incident.",
	}
	for name, text := range samples {
		if ct, conf := Detect(text); ct == TypeLog {
			t.Errorf("%s: prose routed to log (conf %.2f)", name, conf)
		}
	}
}

// TestRouter_LevelLessLogFormats_StillLog pins the recall regressions the
// shaped rewrite introduced and logShapeRe closes: BSD syslog, klog/glog, and
// logfmt lines carry no level word but are unmistakably logs.
func TestRouter_LevelLessLogFormats_StillLog(t *testing.T) {
	samples := map[string]string{
		"bsd_syslog": "Jul  1 10:00:00 host sshd[123]: error: kex_exchange_identification\n" +
			"Jul  1 10:00:01 host sshd[123]: Connection closed by 10.0.0.1\n" +
			"Jul  1 10:00:05 host systemd[1]: Started Session 42 of user deploy.",
		"klog": "E0701 10:00:00.123456       1 server.go:214] leader lost\n" +
			"I0701 10:00:01.000001       1 controller.go:88] resync queued\n" +
			"W0701 10:00:02.500000       1 reflector.go:324] watch closed",
		"logfmt_no_level": "ts=2026-07-01T10:00:00Z msg=\"connection error\" status=500 upstream=ledger\n" +
			"ts=2026-07-01T10:00:01Z msg=\"retrying\" attempt=2 backoff=200ms\n" +
			"ts=2026-07-01T10:00:02Z msg=\"recovered\" status=200 latency_ms=41",
	}
	for name, text := range samples {
		if ct, _ := Detect(text); ct != TypeLog {
			t.Errorf("%s: log routed to %q - recall regression", name, ct)
		}
	}
}
