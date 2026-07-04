package compressors

import (
	"context"
	"strings"
	"testing"

	"github.com/firstops-dev/whittle/compress"
)

func runLog(t *testing.T, cfg LogConfig, in string) string {
	t.Helper()
	res, err := NewLogCompressor(cfg).Compress(context.Background(), compress.Input{Content: in})
	if err != nil {
		t.Fatal(err)
	}
	return res.Output
}

func TestLogCompressorKeepsErrorsDropsNoise(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 50; i++ {
		b.WriteString("2024-01-01 INFO routine heartbeat tick\n")
	}
	b.WriteString("2024-01-01 ERROR database connection refused\n")
	for i := 0; i < 50; i++ {
		b.WriteString("2024-01-01 DEBUG cache lookup\n")
	}
	out := runLog(t, DefaultLogConfig(), b.String())

	if !strings.Contains(out, "ERROR database connection refused") {
		t.Fatalf("error line dropped:\n%s", out)
	}
	if strings.Count(out, "heartbeat") > 5 {
		t.Fatalf("INFO noise not dropped (got %d):\n%s", strings.Count(out, "heartbeat"), out)
	}
	if len(out) >= len(b.String()) {
		t.Fatalf("expected shrink")
	}
}

func TestLogCompressorDedupesWarnings(t *testing.T) {
	var b strings.Builder
	b.WriteString("start\n")
	for i := 0; i < 30; i++ {
		// same warning, varying only in a numeric suffix after ':'
		b.WriteString("2024-01-01 WARN deprecated call at offset: ")
		b.WriteByte(byte('0' + i%10))
		b.WriteString("\n")
	}
	b.WriteString("end\n")
	out := runLog(t, DefaultLogConfig(), b.String())

	warns := strings.Count(out, "deprecated call")
	if warns == 0 {
		t.Fatalf("all warnings dropped:\n%s", out)
	}
	if warns > 3 {
		t.Fatalf("warnings not deduped (kept %d):\n%s", warns, out)
	}
}

func TestLogCompressorKeepsSummary(t *testing.T) {
	in := strings.Repeat("INFO step done\n", 40) + "=== 5 passed, 2 failed in 3.1s ===\n"
	out := runLog(t, DefaultLogConfig(), in)
	if !strings.Contains(out, "5 passed, 2 failed") {
		t.Fatalf("summary line dropped:\n%s", out)
	}
}

func TestLogCompressorCapHonored(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 200; i++ {
		b.WriteString("2024-01-01 ERROR failure number here\n")
	}
	cfg := DefaultLogConfig()
	cfg.MaxTotalLines = 20
	out := runLog(t, cfg, b.String())
	lines := strings.Count(out, "\n")
	if lines > 22 { // 20 + small slack for trailing newline handling
		t.Fatalf("cap not honored: %d lines", lines)
	}
}
