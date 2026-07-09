package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// /health is a local liveness endpoint (for `whittle status` / launchd). It must
// answer 200 here and NEVER be forwarded upstream — a GET to an unroutable path
// otherwise passes through to Anthropic, which would make health checks flap.
func TestProxy_HealthAnsweredLocally(t *testing.T) {
	upstreamHit := false
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHit = true
	}))
	defer up.Close()

	pol, _, err := Load([]byte(proxyPolicy))
	if err != nil {
		t.Fatal(err)
	}
	px := NewProxy(pol, nil, NewMemSessionStore(), nil)
	px.baseURL = up.URL

	rec := httptest.NewRecorder()
	px.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ok") {
		t.Fatalf("/health should be 200 ok locally, got %d %q", rec.Code, rec.Body.String())
	}
	if upstreamHit {
		t.Fatal("/health must NOT be forwarded upstream")
	}
}
