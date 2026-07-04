package compress

import (
	"context"
	"strings"
	"testing"
)

// TestEstimateTokens_Sanity pins the estimator's behavioral contract (exact
// values are calibration-dependent; these invariants are not).
func TestEstimateTokens_Sanity(t *testing.T) {
	if EstimateTokens("") != 0 {
		t.Fatal("empty string must be 0 tokens")
	}
	// Whitespace padding must cost far less than its byte count (the len/4
	// failure mode): 100 spaces is a handful of tokens, not 25.
	if got := EstimateTokens(strings.Repeat(" ", 100)); got > 20 {
		t.Fatalf("100 spaces estimated at %d tokens (whitespace overcount)", got)
	}
	// Unicode is ~1/rune, far more than len/4 of its UTF-8 bytes.
	cjk := strings.Repeat("日本語", 10) // 30 runes, 90 bytes
	if got := EstimateTokens(cjk); got < 25 {
		t.Fatalf("CJK underestimated: %d tokens for 30 runes", got)
	}
	// Monotonic-ish: prose of 2x length estimates strictly more.
	p := "the quick brown fox jumps over the lazy dog. "
	if EstimateTokens(strings.Repeat(p, 8)) <= EstimateTokens(strings.Repeat(p, 4)) {
		t.Fatal("longer prose must estimate more tokens")
	}
}

// TestPipelineTokenGuardrail: byte-smaller but token-not-smaller output must be
// rejected as guardrail_expansion (the consumer pays tokens, not bytes).
func TestPipelineTokenGuardrail(t *testing.T) {
	in := proseInput() // long ASCII prose: many bytes, comparatively few tokens
	// Output: fewer BYTES than the prose but dense CJK -> at least as many tokens.
	dense := strings.Repeat("日本語の圧縮結果", 20) // 504 bytes (< input), est tokens >= input
	fake := fakeCompressor{name: "densifier", fn: func(in Input) (Result, error) {
		return Result{Output: dense}, nil
	}}
	out := newPipelineWith(fake).Compress(context.Background(), in)
	if len(dense) >= len(in.Content) {
		t.Fatal("test setup broken: output must be byte-smaller")
	}
	if EstimateTokens(dense) < EstimateTokens(in.Content) {
		t.Skip("calibration shifted; construct denser output")
	}
	if out.Action != "skipped" || out.SkipReason != "guardrail_expansion" {
		t.Fatalf("token-expanding output must be rejected: got %q/%q", out.Action, out.SkipReason)
	}
}
