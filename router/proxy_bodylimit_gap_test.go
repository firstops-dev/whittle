package router

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestProxy_OversizedBodyForwardedWhole pins a real bug (left RED intentionally).
//
// WHY: R18 says a body over the 32MB cap must "stream-passthrough UNBUFFERED
// (can't route; forward UNTOUCHED), not 413". The proxy instead calls
// readBounded, which reads only maxBodyBytes+1 bytes via io.LimitReader and then
// closes r.Body; the tooLarge branch forwards THAT truncated buffer with
// sendUpstream. Every byte past 32MB+1 is silently dropped and the body is
// already drained, so the upstream receives a truncated, structurally-invalid
// request — an upstream 400 the client would NOT have gotten going direct.
// sendUpstreamRaw's own doc says it exists "for the body-too-large case where we
// never read it", but that path is unreachable because readBounded already read
// (and truncated) the body.
//
// This test asserts the upstream receives the FULL body. It FAILS today, pinning
// the truncation bug; the fix is to stream the oversized body through unbuffered
// (e.g. sendUpstreamRaw over io.MultiReader(bytes.NewReader(buffered), r.Body)).
func TestProxy_OversizedBodyForwardedWhole(t *testing.T) {
	gotLen := make(chan int, 1)
	gotAE := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotLen <- len(b)
		gotAE <- r.Header.Get("Accept-Encoding")
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)

	px := testProxy(t, proxyPolicy, srv)

	// A valid JSON body just over the 32MB cap.
	filler := strings.Repeat("x", maxBodyBytes+4096)
	body := `{"model":"claude-opus-4-8","messages":[{"role":"user","content":"` + filler + `"}]}`

	resp := doPost(t, px, "/v1/messages", body, "")

	if resp.StatusCode != 200 {
		t.Errorf("oversized body must pass through (never rejected), got status %d", resp.StatusCode)
	}
	if ae := <-gotAE; ae != "identity" {
		t.Errorf("Accept-Encoding must be identity on the passthrough path, got %q", ae)
	}
	if n := <-gotLen; n != len(body) {
		t.Errorf("upstream received %d bytes, want the full %d: the >32MB body was TRUNCATED (R18 requires forwarding it untouched)", n, len(body))
	}
}
