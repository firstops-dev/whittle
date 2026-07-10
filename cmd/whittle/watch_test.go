package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var plain = newPalette(true)

func TestRenderRouterLine(t *testing.T) {
	// A real down-route verdict renders reason, family move, tokens, signals.
	line := `2026/07/10 01:22:07 {"tier":"fast","requested":"claude-opus-4-8","model":"claude-haiku-4-5-20251001","reason":"route:casual-easy stripped:context-1m","signals":"cplx:reasoning=-0.273 dom=other@0.995","status":200,"latency_ms":3896,"ctx_tokens":52848,"in_tokens":2225,"out_tokens":158,"session":"3db8cd74"}`
	out := renderRouterLine(line, plain)
	for _, want := range []string{"01:22:07", "casual-easy", "opus→haiku", "52,848 tok", "3896ms", "dom=other@0.995"} {
		if !strings.Contains(out, want) {
			t.Errorf("router render missing %q in %q", want, out)
		}
	}

	// A no-op keeps the family with a "kept" marker, no arrow.
	noop := `2026/07/10 12:54:33 {"tier":"-","requested":"claude-opus-4-8","model":"claude-opus-4-8","reason":"no-op:default:requested","signals":"","status":200,"latency_ms":2214,"ctx_tokens":52846,"in_tokens":3169,"out_tokens":20,"session":"x"}`
	out = renderRouterLine(noop, plain)
	if !strings.Contains(out, "kept") || strings.Contains(out, "→") {
		t.Errorf("no-op should render kept without an arrow: %q", out)
	}

	// Non-JSON startup lines pass through dim, never dropped.
	out = renderRouterLine("2026/07/10 12:54:20 router: smart mode ON (classifier sidecar = http://127.0.0.1:45872)", plain)
	if !strings.Contains(out, "smart mode ON") {
		t.Errorf("startup lines must pass through: %q", out)
	}

	// Non-200 statuses are surfaced.
	bad := `2026/07/10 01:00:00 {"tier":"main","requested":"claude-opus-4-8","model":"claude-sonnet-4-5","reason":"default","signals":"","status":429,"latency_ms":601,"ctx_tokens":78,"in_tokens":0,"out_tokens":0,"session":"x"}`
	if out = renderRouterLine(bad, plain); !strings.Contains(out, "429") {
		t.Errorf("non-200 status must be visible: %q", out)
	}
}

func TestRenderCarveLine(t *testing.T) {
	line := `{"id":1,"in_tokens":1107,"out_tokens":145,"session":"s","strategy":"ansi_strip+log_compressor","tool":"Bash","ts":1783666113}`
	out := renderCarveLine(line, plain)
	for _, want := range []string{"🪓", "Bash", "1,107", "145", "−86%", "ansi_strip+log_compressor"} {
		if !strings.Contains(out, want) {
			t.Errorf("carve render missing %q in %q", want, out)
		}
	}
	// Retrievals render as their own event type.
	if out = renderCarveLine(`{"strategy":"retrieve","ts":1783666113}`, plain); !strings.Contains(out, "retrieve") {
		t.Errorf("retrieve event missing: %q", out)
	}
	// Junk lines are dropped silently.
	if out = renderCarveLine("not json", plain); out != "" {
		t.Errorf("junk should render empty, got %q", out)
	}
}

// The follower delivers only complete new lines, buffers partials, and
// recovers from truncation (rotation).
func TestFollower(t *testing.T) {
	p := filepath.Join(t.TempDir(), "log")
	f := newFollower(p)

	if got := f.readNew(); got != nil {
		t.Fatalf("missing file should yield nil, got %v", got)
	}
	os.WriteFile(p, []byte("old1\nold2\n"), 0o644)
	if got := f.readNew(); len(got) != 0 {
		t.Fatalf("first sighting must start at end (live-only), got %v", got)
	}
	// Append a complete line and a partial one.
	fh, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	fh.WriteString("new1\npart")
	fh.Close()
	if got := f.readNew(); len(got) != 1 || got[0] != "new1" {
		t.Fatalf("want [new1], got %v", got)
	}
	// Complete the partial.
	fh, _ = os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	fh.WriteString("ial\n")
	fh.Close()
	if got := f.readNew(); len(got) != 1 || got[0] != "partial" {
		t.Fatalf("partial line must be joined, got %v", got)
	}
	// Rotation: replace with a smaller file; follower restarts from zero.
	os.WriteFile(p, []byte("fresh\n"), 0o644)
	if got := f.readNew(); len(got) != 1 || got[0] != "fresh" {
		t.Fatalf("rotation must reopen from start, got %v", got)
	}
}

func TestBacklogMergesByTime(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "logs"), 0o755)
	rp := filepath.Join(dir, "logs", "router.log")
	sp := filepath.Join(dir, "stats.jsonl")
	// carve at 10:00:00 local-equivalent unix; route at a later wall clock
	os.WriteFile(rp, []byte(`2026/07/10 23:59:59 {"tier":"fast","requested":"claude-opus-4-8","model":"claude-haiku-4-5","reason":"route:casual-easy","signals":"","status":200,"latency_ms":10,"ctx_tokens":5,"in_tokens":1,"out_tokens":1,"session":"x"}`+"\n"), 0o644)
	os.WriteFile(sp, []byte(`{"in_tokens":100,"out_tokens":10,"strategy":"log_compressor","tool":"Bash","ts":1}`+"\n"), 0o644)
	events := backlogEvents(newFollower(rp), newFollower(sp), 8, plain)
	if len(events) != 2 {
		t.Fatalf("want 2 backlog events, got %d: %v", len(events), events)
	}
	if !strings.Contains(events[0], "🪓") || !strings.Contains(events[1], "casual-easy") {
		t.Errorf("expected carve (ts=1) before route (2026), got %v", events)
	}
}

func TestFmtInt(t *testing.T) {
	for n, want := range map[int]string{0: "0", 999: "999", 1000: "1,000", 52848: "52,848", 1234567: "1,234,567"} {
		if got := fmtInt(n); got != want {
			t.Errorf("fmtInt(%d)=%q want %q", n, got, want)
		}
	}
}
