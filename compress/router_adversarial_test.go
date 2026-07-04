package compress

import (
	"strings"
	"testing"
)

// TestDetect_EdgeInputs probes the router with degenerate and ambiguous inputs.
// The key correctness property: Detect must never panic and must always return
// one of the known types.
func TestDetect_EdgeInputs(t *testing.T) {
	diffThatIsAlsoCode := `diff --git a/main.go b/main.go
@@ -1,4 +1,4 @@
-func old() {
+func new() {
 	return
 }`

	// Go test / grep style output: "file:line: message". This LOOKS like an
	// error log but the file:line: shape makes the SEARCH detector fire first.
	searchLikeErrorLog := `internal/db.go:42: ERROR connection refused
internal/db.go:88: ERROR retry exhausted
internal/api.go:13: FATAL panic in handler
cmd/run.go:7: ERROR shutting down`

	// Newline-delimited JSON objects (NDJSON) — structured but not a JSON array.
	ndjson := `{"level":"info","msg":"start"}
{"level":"error","msg":"boom"}
{"level":"warn","msg":"slow"}
{"level":"info","msg":"done"}`

	tests := []struct {
		name string
		in   string
		want ContentType
		note string
	}{
		{"empty", "", TypeProse, "empty must not panic"},
		{"whitespace_only", "   \n\t\n   ", TypeProse, "whitespace must not panic"},
		{"single_char", "x", TypeProse, ""},
		{"open_bracket_not_json", "[this is not, valid json at all", TypeProse, "bad JSON array falls through"},
		{"diff_that_is_also_code", diffThatIsAlsoCode, TypeDiff, "diff wins over code (first-match-wins)"},
		{"ndjson", ndjson, TypeJSON, "NDJSON is JSON-lines: detectJSONLines claims it before detectLog can steal it via level words"},
		{"search_like_error_log", searchLikeErrorLog, TypeSearch, "FINDING: error logs in file:line: form route to search, not log"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, conf := Detect(tt.in)
			if got != tt.want {
				t.Errorf("Detect()=%q (conf %.2f), want %q — %s", got, conf, tt.want, tt.note)
			}
		})
	}
}

// TestDetect_TimestampedLogMisroutedToSearch is a high-impact routing finding:
// real production logs are prefixed with an ISO-8601 timestamp containing
// HH:MM:SS. The search detector's regex `^.+:\d+:` matches the ":MM:" in the
// time, so timestamped logs classify as SEARCH (which has no compressor) and are
// never compressed. detectSearch runs before detectLog, so it wins.
func TestDetect_TimestampedLogMisroutedToSearch(t *testing.T) {
	log := `2024-01-01T12:30:01Z INFO starting up subsystem
2024-01-01T12:30:02Z INFO loaded configuration values
2024-01-01T12:30:03Z ERROR failed to connect to database
2024-01-01T12:30:04Z WARN retrying connection now
2024-01-01T12:30:05Z INFO recovered and continuing`
	got, conf := Detect(log)
	if got != TypeLog {
		t.Errorf("FINDING: ISO-8601 timestamped log misrouted to %q (conf %.2f), want %q. "+
			"searchLineRe `^.+:\\d+:` matches the HH:MM: in the timestamp; detectSearch precedes "+
			"detectLog, so most real logs never reach the LogCompressor.", got, conf, TypeLog)
	}
}

// TestDetect_NeverPanicsOnPathologicalInput throws nasty inputs at the router.
func TestDetect_NeverPanicsOnPathologicalInput(t *testing.T) {
	inputs := []string{
		strings.Repeat("\n", 100000),                  // 100k blank lines
		strings.Repeat("a", 100000),                   // one huge line
		"[" + strings.Repeat(`{"a":1},`, 10000) + "]", // huge valid JSON array
		"\x00\x01\x02 binary garbage \xff\xfe",        // control bytes
		strings.Repeat("|---|", 5000),                 // markdown-table-ish noise
		"```" + strings.Repeat("x", 50000),            // unterminated fence
	}
	for i, in := range inputs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("Detect panicked on input #%d: %v", i, r)
				}
			}()
			got, _ := Detect(in)
			if got == "" {
				t.Errorf("input #%d: Detect returned empty type", i)
			}
		}()
	}
}
