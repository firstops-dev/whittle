package compress_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/firstops-dev/whittle/compress"
	"github.com/firstops-dev/whittle/compress/compressors"
)

// Benchmarks for the deterministic (no-model) strategies — the in-path budget a
// PostToolUse hook pays per tool output. Run: go test -bench . -benchmem ./compress/

func pipeline() *compress.Pipeline {
	return compress.NewPipeline(compress.NewRegistry(compressors.ChainsWithModel("")), compress.DefaultGateConfig(), nil)
}

func benchInput(kind string) string {
	switch kind {
	case "json":
		rows := make([]map[string]any, 200)
		for i := range rows {
			rows[i] = map[string]any{"name": fmt.Sprintf("pod-%03d", i), "namespace": "production", "status": "Running", "restarts": i % 3}
		}
		b, _ := json.MarshalIndent(rows, "", "  ")
		return string(b)
	case "log":
		var b strings.Builder
		b.WriteString("ERROR boot failed: bad config\n")
		for i := 0; i < 800; i++ {
			fmt.Fprintf(&b, "2026-07-04T10:%02d:%02dZ INFO request served path=/v1/x id=%d status=200\n", i/60, i%60, i)
		}
		b.WriteString("ERROR shutdown: connection reset\n")
		return b.String()
	case "terminal":
		var b strings.Builder
		for p := 1; p <= 300; p++ {
			fmt.Fprintf(&b, "\rDownloading model.bin  %3d%% [%s]", p/3, strings.Repeat("#", p/15))
		}
		b.WriteString("\n")
		return b.String()
	}
	return ""
}

func benchKind(b *testing.B, kind string) {
	p := pipeline()
	in := compress.Input{Content: benchInput(kind), MinTokens: 0}
	b.SetBytes(int64(len(in.Content)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out := p.Compress(context.Background(), in)
		if out.Action != "compressed" {
			b.Fatalf("expected compression, got %s/%s", out.Action, out.SkipReason)
		}
	}
}

func BenchmarkJSON(b *testing.B)     { benchKind(b, "json") }
func BenchmarkLog(b *testing.B)      { benchKind(b, "log") }
func BenchmarkTerminal(b *testing.B) { benchKind(b, "terminal") }
