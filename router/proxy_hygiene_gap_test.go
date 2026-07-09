package router

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestProxy_HopByHopHeadersNotForwarded verifies per-connection headers are
// stripped on the way upstream while ordinary headers pass through.
//
// WHY: forwarding hop-by-hop headers (they belong to THIS connection, not the
// next hop) corrupts the upstream connection semantics. copyUpstreamHeaders must
// drop them but forward everything else (the codebase's "strip the few, never
// allowlist" principle). Keep-Alive and Proxy-Authorization are the two hop-by-hop
// headers the transport does NOT re-manage, so they cleanly exercise the filter.
func TestProxy_HopByHopHeadersNotForwarded(t *testing.T) {
	got := make(chan http.Header, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		got <- r.Header.Clone()
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)

	px := testProxy(t, proxyPolicy, srv)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages",
		strings.NewReader(`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Claude-Code-Session-Id", "s1")
	req.Header.Set("Keep-Alive", "timeout=5")
	req.Header.Set("Proxy-Authorization", "secret-proxy-cred")
	req.Header.Set("X-Custom-Passthrough", "keepme")
	px.ServeHTTP(httptest.NewRecorder(), req)

	up := <-got
	if v := up.Get("Keep-Alive"); v != "" {
		t.Errorf("hop-by-hop Keep-Alive must not reach upstream, got %q", v)
	}
	if v := up.Get("Proxy-Authorization"); v != "" {
		t.Errorf("hop-by-hop Proxy-Authorization must not reach upstream, got %q", v)
	}
	if v := up.Get("X-Custom-Passthrough"); v != "keepme" {
		t.Errorf("ordinary headers must be forwarded (never allowlist), X-Custom-Passthrough = %q", v)
	}
}

// TestProxy_StaleContentLengthReplacedForReconciledBody verifies the client's
// Content-Length is never forwarded stale onto a reconciled (different-length)
// body.
//
// WHY: down-routing rewrites the model and strips features, so the reconciled body
// is a DIFFERENT length than the client sent. If the client's original
// Content-Length were forwarded, the upstream would read the wrong number of bytes
// (truncated body or a hang waiting for bytes that never come). copyUpstreamHeaders
// drops the client Content-Length and sendUpstream recomputes it from the
// reconciled body.
func TestProxy_StaleContentLengthReplacedForReconciledBody(t *testing.T) {
	type cap struct {
		cl   int64
		blen int
	}
	got := make(chan cap, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- cap{cl: r.ContentLength, blen: len(b)}
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)

	px := testProxy(t, proxyPolicy, srv)
	// Down-route opus->haiku AND strip the context-1m beta → body length changes.
	req := httptest.NewRequest(http.MethodPost, "/v1/messages",
		strings.NewReader(`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Claude-Code-Session-Id", "s1")
	req.Header.Set("anthropic-beta", "context-1m-2025-08-07")
	req.Header.Set("Content-Length", "99999") // deliberately wrong/stale
	px.ServeHTTP(httptest.NewRecorder(), req)

	c := <-got
	if c.cl == 99999 {
		t.Error("stale client Content-Length was forwarded onto the reconciled body")
	}
	if c.cl != int64(c.blen) {
		t.Errorf("upstream Content-Length (%d) must match the reconciled body length (%d)", c.cl, c.blen)
	}
}

// TestProxy_AcceptEncodingIdentityOnBothPaths verifies Accept-Encoding is forced
// to identity on BOTH the routed (sendUpstream) and passthrough (sendUpstreamRaw)
// upstream calls.
//
// WHY: GATE-1 — a gzip-compressed SSE stream breaks the per-event framing the
// client relies on, so the proxy must force identity so the body is plaintext,
// regardless of what the client's Accept-Encoding was. The two code paths set it
// independently, so both must be checked.
func TestProxy_AcceptEncodingIdentityOnBothPaths(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		mu.Lock()
		seen = append(seen, r.Header.Get("Accept-Encoding"))
		mu.Unlock()
		w.WriteHeader(200)
	}))
	t.Cleanup(srv.Close)

	px := testProxy(t, proxyPolicy, srv)

	// Routed path (down-route) — client asks for gzip.
	routed := httptest.NewRequest(http.MethodPost, "/v1/messages",
		strings.NewReader(`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"hello"}]}`))
	routed.Header.Set("Content-Type", "application/json")
	routed.Header.Set("X-Claude-Code-Session-Id", "s1")
	routed.Header.Set("Accept-Encoding", "gzip")
	px.ServeHTTP(httptest.NewRecorder(), routed)

	// Passthrough path (unroutable count_tokens, body streamed raw) — also gzip.
	pass := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens",
		strings.NewReader(`{"model":"claude-opus-4-8","messages":[]}`))
	pass.Header.Set("Content-Type", "application/json")
	pass.Header.Set("Accept-Encoding", "gzip")
	px.ServeHTTP(httptest.NewRecorder(), pass)

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 {
		t.Fatalf("expected 2 upstream calls, got %d", len(seen))
	}
	for i, ae := range seen {
		if ae != "identity" {
			t.Errorf("upstream call %d Accept-Encoding = %q, want identity", i, ae)
		}
	}
}

// TestReloadFile_ValidReloadTakesEffectNextRequest verifies a successful hot
// reload is applied to the very next request.
//
// WHY: the daemon edits its policy file live; a valid reload must swap the active
// routing without a restart. The first request routes hello->fast(haiku); after
// reloading a policy that routes hello->smart(opus), the next request must route
// per the NEW policy (opus, a no-op passthrough of the original opus request).
func TestReloadFile_ValidReloadTakesEffectNextRequest(t *testing.T) {
	srv, capd := mockUpstream(t)
	px := testProxy(t, proxyPolicy, srv)

	doPost(t, px, "/v1/messages",
		`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"hello"}]}`, "")
	if capd.model != "claude-haiku-4-5" {
		t.Fatalf("pre-reload: hello should route to haiku, upstream saw %q", capd.model)
	}

	alt := strings.Replace(proxyPolicy, `"to":"fast"`, `"to":"smart"`, 1)
	dir := t.TempDir()
	p := filepath.Join(dir, "p.json")
	if err := os.WriteFile(p, []byte(alt), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := px.ReloadFile(p); err != nil {
		t.Fatalf("valid reload should succeed: %v", err)
	}

	doPost(t, px, "/v1/messages",
		`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"hello"}]}`, "")
	if capd.model != "claude-opus-4-8" {
		t.Errorf("post-reload: hello should route to smart(opus) → no-op, upstream saw %q (reload did not take effect)", capd.model)
	}
}
