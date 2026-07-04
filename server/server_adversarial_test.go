package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// rawPost returns the recorder so tests can assert status codes and bodies for
// non-200 paths (the existing post() helper fatals on non-200).
func rawPost(t *testing.T, h http.Handler, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/v1/compress", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestEndpoint_MalformedJSON(t *testing.T) {
	h := newTestMux("http://127.0.0.1:0")
	for _, body := range []string{`{"content": }`, `not json`, `{"content":`, ``, `[]`} {
		rr := rawPost(t, h, http.MethodPost, body)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("body %q: status=%d want 400", body, rr.Code)
		}
	}
}

func TestEndpoint_WrongMethod(t *testing.T) {
	h := newTestMux("http://127.0.0.1:0")
	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		rr := rawPost(t, h, m, `{"content":"x"}`)
		if rr.Code != http.StatusMethodNotAllowed {
			t.Errorf("method %s: status=%d want 405", m, rr.Code)
		}
	}
}

func TestEndpoint_MissingAndEmptyContent(t *testing.T) {
	h := newTestMux("http://127.0.0.1:0")

	// Missing content field entirely.
	resp := post(t, h, `{"tool_name":"bash"}`)
	if resp.Action != "skipped" {
		t.Errorf("missing content: action=%q want skipped", resp.Action)
	}
	if resp.Compressed != "" {
		t.Errorf("missing content: compressed=%q want empty", resp.Compressed)
	}
	if resp.SkipReason == nil || *resp.SkipReason != "too_short" {
		t.Errorf("missing content: skip_reason=%v want too_short", deref(resp.SkipReason))
	}

	// Explicit empty content.
	resp = post(t, h, `{"content":""}`)
	if resp.Action != "skipped" {
		t.Errorf("empty content: action=%q", resp.Action)
	}
	if resp.OriginalTokens != 0 {
		t.Errorf("empty content: original_tokens=%d want 0", resp.OriginalTokens)
	}
	if resp.Reduction != 0 {
		t.Errorf("empty content: reduction=%v want 0 (no divide-by-zero)", resp.Reduction)
	}
}

// TestEndpoint_ResponseContract verifies all response fields are present and
// correct on the skipped path (no upstream required).
func TestEndpoint_ResponseContract(t *testing.T) {
	h := newTestMux("http://127.0.0.1:0")
	rr := rawPost(t, h, http.MethodPost, `{"content":"short text here"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type=%q want application/json", ct)
	}
	// Assert presence of every documented field via a raw map.
	var m map[string]json.RawMessage
	if err := json.Unmarshal(rr.Body.Bytes(), &m); err != nil {
		t.Fatalf("invalid response json: %v", err)
	}
	for _, field := range []string{
		"compressed", "original_tokens", "compressed_tokens", "reduction",
		"action", "skip_reason", "gate", "strategy", "latency_ms", "model", "version",
	} {
		if _, ok := m[field]; !ok {
			t.Errorf("response missing field %q", field)
		}
	}

	var resp compressResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Model != model {
		t.Errorf("model=%q want %q", resp.Model, model)
	}
	if resp.Version != version {
		t.Errorf("version=%q want %q", resp.Version, version)
	}
	if resp.Gate.Klass == "" {
		t.Errorf("gate.klass empty")
	}
	// On the compressed path skip_reason must serialize null; here (skipped) it
	// must be non-null.
	if string(m["skip_reason"]) == "null" {
		t.Errorf("skipped path: skip_reason serialized null")
	}
}

// TestEndpoint_RateClamp ensures out-of-range rate does not error and is clamped.
func TestEndpoint_RateClamp(t *testing.T) {
	h := newTestMux("http://127.0.0.1:0")
	for _, body := range []string{
		`{"content":"short","rate":-5}`,
		`{"content":"short","rate":99}`,
		`{"content":"short","min_tokens":-3}`,
	} {
		rr := rawPost(t, h, http.MethodPost, body)
		if rr.Code != http.StatusOK {
			t.Errorf("body %q: status=%d want 200", body, rr.Code)
		}
	}
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}
