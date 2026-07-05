package compressors

import (
	"fmt"
	"strings"
	"testing"
)

// TestLogCompressor_OmissionMarker: dropped lines must be MARKED, never silently
// removed. A noisy log compresses, the output carries "... [N lines omitted]"
// markers, and the accounting is exact - kept data lines + summed omitted counts
// equal the input line count, so no line is silently unaccounted for.
func TestLogCompressor_OmissionMarker(t *testing.T) {
	lines := []string{"ERROR startup failed: bad config"}
	for i := 0; i < 100; i++ {
		lines = append(lines, "2024-01-01 INFO tick handler ok")
	}
	lines = append(lines, "ERROR shutdown: connection reset")
	in := strings.Join(lines, "\n") // 102 lines, no trailing newline

	out := runLog(t, DefaultLogConfig(), in)
	if !strings.Contains(out, "lines omitted]") {
		t.Fatalf("dropped lines must be marked, got no marker:\n%s", out)
	}
	if !strings.Contains(out, "startup failed") || !strings.Contains(out, "shutdown: connection reset") {
		t.Errorf("both errors must survive:\n%s", out)
	}
	kept, omitted := 0, 0
	for _, ln := range strings.Split(out, "\n") {
		var n int
		if _, err := fmt.Sscanf(ln, "... [%d lines omitted]", &n); err == nil {
			omitted += n
		} else {
			kept++
		}
	}
	if omitted == 0 {
		t.Errorf("marker present but omitted count is zero")
	}
	if kept+omitted != len(lines) {
		t.Errorf("marker accounting off: kept=%d + omitted=%d = %d, want %d",
			kept, omitted, kept+omitted, len(lines))
	}
}

// TestLogCompressor_PureInfoLog_TotalContentLoss is the headline log finding:
// a log with zero error/warn/summary/stack lines selects NOTHING and returns an
// empty string. The pipeline would then report this as a successful compression
// with 100% content loss.
func TestLogCompressor_PureInfoLog_TotalContentLoss(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 40; i++ {
		b.WriteString("2024-01-01 INFO routine heartbeat tick, all systems nominal here\n")
	}
	in := b.String()
	out := runLog(t, DefaultLogConfig(), in)

	if strings.TrimSpace(out) == "" {
		t.Errorf("FINDING: pure-INFO log compressed to EMPTY output (%d bytes -> %d bytes). "+
			"All content is dropped; downstream the pipeline reports action=compressed with total data loss.",
			len(in), len(out))
	}
}

// TestLogCompressor_PureDebugLog_TotalContentLoss - same with DEBUG noise.
func TestLogCompressor_PureDebugLog_TotalContentLoss(t *testing.T) {
	in := strings.Repeat("2024-01-01 DEBUG cache probe key=abc value=def hit=true\n", 40)
	out := runLog(t, DefaultLogConfig(), in)
	if strings.TrimSpace(out) == "" {
		t.Errorf("FINDING: pure-DEBUG log compressed to EMPTY output (%d -> %d bytes).", len(in), len(out))
	}
}

// TestLogCompressor_SingleError_IsKept - exactly one error line, buried in noise,
// must survive.
func TestLogCompressor_SingleError_IsKept(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 60; i++ {
		b.WriteString("2024-01-01 INFO tick\n")
	}
	b.WriteString("2024-01-01 ERROR the one and only fatal database failure\n")
	for i := 0; i < 60; i++ {
		b.WriteString("2024-01-01 INFO tick\n")
	}
	out := runLog(t, DefaultLogConfig(), b.String())
	if !strings.Contains(out, "the one and only fatal database failure") {
		t.Fatalf("single error line was dropped:\n%s", out)
	}
}

// TestLogCompressor_TwoDistinctErrors_NotCollapsed - dedup must not collapse two
// genuinely different errors that share a similar shape.
func TestLogCompressor_TwoDistinctErrors_NotCollapsed(t *testing.T) {
	var b strings.Builder
	b.WriteString("start of run\n")
	b.WriteString("2024-01-01 ERROR failed to open file: /etc/passwd permission denied\n")
	b.WriteString("2024-01-01 ERROR failed to open file: /var/log/app.log no such file\n")
	for i := 0; i < 30; i++ {
		b.WriteString("2024-01-01 INFO tick\n")
	}
	out := runLog(t, DefaultLogConfig(), b.String())
	if !strings.Contains(out, "permission denied") {
		t.Errorf("first distinct error dropped:\n%s", out)
	}
	if !strings.Contains(out, "no such file") {
		t.Errorf("second distinct error dropped (errors must not be deduped):\n%s", out)
	}
}

// TestLogCompressor_AllErrors_CapBehavior - every line is an error; the cap and
// first/last selection should keep a bounded, non-empty subset that includes the
// first and last error.
func TestLogCompressor_AllErrors_CapBehavior(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 200; i++ {
		fmtLine := "2024-01-01 ERROR failure #" + strings.Repeat("x", 1) + string(rune('A'+i%26)) + "\n"
		b.WriteString(fmtLine)
	}
	in := b.String()
	cfg := DefaultLogConfig()
	out := runLog(t, cfg, in)
	if strings.TrimSpace(out) == "" {
		t.Fatal("all-error log produced empty output")
	}
	nLines := len(strings.Split(strings.TrimRight(out, "\n"), "\n"))
	if nLines > cfg.MaxTotalLines+2 {
		t.Errorf("cap not honored: %d lines > MaxTotalLines=%d", nLines, cfg.MaxTotalLines)
	}
	// This all-error log is also near-all-summary, so almost every line is kept; with
	// omission markers the compressor correctly declines to expand and hands the
	// input back. The invariant is non-expansion (a compressor must never grow output).
	if len(out) > len(in) {
		t.Errorf("log compressor expanded: in=%d out=%d", len(in), len(out))
	}
}

// TestLogCompressor_StackTraceSplitByBlankLine - a stack trace interrupted by a
// blank line. Both halves should be retained near the error.
func TestLogCompressor_StackTraceSplitByBlankLine(t *testing.T) {
	in := strings.Repeat("INFO warmup\n", 20) +
		"ERROR panic: runtime error: index out of range\n" +
		"goroutine 1 [running]:\n" +
		"main.handler(0x1, 0x2)\n" +
		"\tmain.go:42 +0x1f\n" +
		"\n" + // blank line splits the trace
		"\tserver.go:88 +0x3a\n" +
		"main.main()\n" +
		strings.Repeat("INFO cooldown\n", 20)
	out := runLog(t, DefaultLogConfig(), in)
	if !strings.Contains(out, "index out of range") {
		t.Errorf("error line dropped:\n%s", out)
	}
	if !strings.Contains(out, "main.go:42") {
		t.Errorf("first stack segment dropped:\n%s", out)
	}
}

// TestLogCompressor_CRLF - Windows line endings must not crash and must keep
// the error.
func TestLogCompressor_CRLF(t *testing.T) {
	in := strings.Repeat("INFO tick\r\n", 30) + "ERROR boom on windows\r\n" + strings.Repeat("INFO tick\r\n", 30)
	out := runLog(t, DefaultLogConfig(), in)
	if !strings.Contains(out, "boom on windows") {
		t.Errorf("CRLF error line dropped:\n%q", out)
	}
}

// TestLogCompressor_SingleHugeLine - one 30k-char line must not crash. (It will
// not shrink because there is nothing to drop; the pipeline guardrail handles
// the no-shrink case.)
func TestLogCompressor_SingleHugeLine(t *testing.T) {
	in := "ERROR " + strings.Repeat("a", 30000)
	out := runLog(t, DefaultLogConfig(), in)
	if !strings.Contains(out, "ERROR") {
		t.Errorf("huge error line dropped")
	}
}

// TestLogCompressor_InterleavedLevels - mixed levels; errors/warns kept, info
// noise dropped, output shrinks.
func TestLogCompressor_InterleavedLevels(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 50; i++ {
		switch i % 5 {
		case 0:
			b.WriteString("ERROR thing broke number ")
		case 1, 2, 3:
			b.WriteString("INFO mundane event number ")
		case 4:
			b.WriteString("WARN careful about thing number ")
		}
		b.WriteString(string(rune('A' + i%26)))
		b.WriteByte('\n')
	}
	in := b.String()
	out := runLog(t, DefaultLogConfig(), in)
	if !strings.Contains(out, "thing broke") {
		t.Errorf("errors dropped from interleaved log")
	}
	if len(out) >= len(in) {
		t.Errorf("interleaved log did not shrink: in=%d out=%d", len(in), len(out))
	}
}

// Bench-corpus regression: "HTTP 200 OK" matched the \d+\s+ok summary form, so
// EVERY line of a health-check log was kept as a "summary" (0.15% reduction on
// 200 near-identical lines). Status phrases are not test summaries.
func TestLogCompressor_HTTPStatusIsNotASummary(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&b, "2024-07-04T08:00:%02dZ [INFO] GET /health -> 200 OK in 3ms\n", i%60)
	}
	b.WriteString("2024-07-04T08:03:21Z [ERROR] GET /health -> 503 upstream timeout\n")
	out := runLog(t, DefaultLogConfig(), b.String())
	if !strings.Contains(out, "503 upstream timeout") {
		t.Fatal("the one real error must survive")
	}
	if len(out) > len(b.String())/3 {
		t.Fatalf("repetitive health-check log barely compressed: %d of %d bytes kept", len(out), b.Len())
	}
}
