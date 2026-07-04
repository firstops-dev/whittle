package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/firstops-dev/whittle/compress"
	"github.com/firstops-dev/whittle/compress/compressors"
)

// newTestMux wires a pipeline whose prose path points at the given mock URL, so
// tests never require the real Python LLMLingua service.
func newTestMux(proseURL string) http.Handler {
	chains := map[compress.ContentType][]compress.Compressor{
		compress.TypeProse: {compressors.NewLLMLinguaAdapterWithURL(proseURL)},
	}
	p := compress.NewPipeline(compress.NewRegistry(chains), compress.DefaultGateConfig(), nil)
	return NewMux(p)
}

func post(t *testing.T, h http.Handler, body string) compressResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/compress", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	var resp compressResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v\n%s", err, rr.Body.String())
	}
	return resp
}

func TestCompressEndpointHappyPath(t *testing.T) {
	// Mock Python service: returns a shorter compressed body.
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req["content_class"] != "prose" {
			t.Errorf("adapter must send content_class=prose, got %v", req["content_class"])
		}
		_, _ = io.WriteString(w, `{"compressed":"short summary","action":"compressed"}`)
	}))
	defer mock.Close()

	h := newTestMux(mock.URL)
	prose := strings.Repeat("the team discussed the quarterly roadmap and the hiring plan at length. ", 8)
	body, _ := json.Marshal(compressRequest{Content: prose})

	resp := post(t, h, string(body))
	if resp.Action != "compressed" {
		t.Fatalf("action=%q want compressed (skip_reason=%v)", resp.Action, resp.SkipReason)
	}
	if resp.Compressed != "short summary" {
		t.Fatalf("compressed=%q", resp.Compressed)
	}
	if resp.Strategy != "llmlingua" {
		t.Fatalf("strategy=%q", resp.Strategy)
	}
	if resp.Reduction <= 0 {
		t.Fatalf("reduction=%v want >0", resp.Reduction)
	}
}

func TestCompressEndpointSkipShort(t *testing.T) {
	// No upstream needed: gate skips before any compressor runs.
	h := newTestMux("http://127.0.0.1:0")
	body, _ := json.Marshal(compressRequest{Content: "just a little text"})

	resp := post(t, h, string(body))
	if resp.Action != "skipped" {
		t.Fatalf("action=%q want skipped", resp.Action)
	}
	if resp.SkipReason == nil || *resp.SkipReason != "too_short" {
		t.Fatalf("skip_reason=%v want too_short", resp.SkipReason)
	}
	// Passthrough: original returned unchanged.
	if resp.Compressed != "just a little text" {
		t.Fatalf("compressed=%q", resp.Compressed)
	}
}

func TestCompressEndpointFailOpenUpstreamDown(t *testing.T) {
	// Upstream returns 500 → adapter errors → pipeline fails open (skipped/error).
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer mock.Close()

	h := newTestMux(mock.URL)
	prose := strings.Repeat("the team discussed the quarterly roadmap and the hiring plan at length. ", 8)
	body, _ := json.Marshal(compressRequest{Content: prose})

	resp := post(t, h, string(body))
	if resp.Action != "skipped" || resp.SkipReason == nil || *resp.SkipReason != "error" {
		t.Fatalf("action=%q reason=%v want skipped/error", resp.Action, resp.SkipReason)
	}
	if resp.Compressed != prose {
		t.Fatal("fail-open must return original content")
	}
}

func TestHealth(t *testing.T) {
	rr := httptest.NewRecorder()
	healthHandler(rr, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"ok"`) {
		t.Fatalf("body=%s", rr.Body.String())
	}
}
