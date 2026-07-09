package router

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// captured records what the mock upstream received.
type captured struct {
	path  string
	model string
	beta  string
	body  string
	host  string
}

// mockUpstream returns a test server that records the request and streams a
// small SSE-shaped body, plus a pointer to the capture.
func mockUpstream(t *testing.T) (*httptest.Server, *captured) {
	t.Helper()
	cap := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		cap.body = string(b)
		cap.path = r.URL.RequestURI()
		cap.beta = r.Header.Get("anthropic-beta")
		cap.host = r.Host
		var body struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(b, &body)
		cap.model = body.Model
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\"}\n\n")
		io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv, cap
}

func testProxy(t *testing.T, policyJSON string, srv *httptest.Server) *Proxy {
	t.Helper()
	var pol *Policy
	if policyJSON != "" {
		p, _, err := Load([]byte(policyJSON))
		if err != nil {
			t.Fatalf("policy: %v", err)
		}
		pol = p
	}
	px := NewProxy(pol, nil, NewMemSessionStore(), nil)
	px.baseURL = srv.URL // point upstream at the mock
	return px
}

const proxyPolicy = `{
  "version":1,
  "tiers":[{"name":"fast","model":"claude-haiku-4-5"},{"name":"smart","model":"claude-opus-4-8"}],
  "default":"fast","inspect":{"scope":"full"},
  "routes":[{"name":"cheap","when":{"keywords":["hello"]},"to":"fast"}]
}`

func doPost(t *testing.T, px *Proxy, path, body, beta string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Claude-Code-Session-Id", "s1")
	if beta != "" {
		req.Header.Set("anthropic-beta", beta)
	}
	rec := httptest.NewRecorder()
	px.ServeHTTP(rec, req)
	return rec.Result()
}

// Happy path: a down-route rewrites the model and strips an unsupported beta
// before forwarding; the verdict header is set; the response streams back.
func TestProxy_DownrouteRewritesAndStrips(t *testing.T) {
	srv, cap := mockUpstream(t)
	px := testProxy(t, proxyPolicy, srv)
	resp := doPost(t, px, "/v1/messages",
		`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"hello there"}]}`,
		"context-1m-2025-08-07")

	if cap.model != "claude-haiku-4-5" {
		t.Errorf("upstream model = %q, want rewritten claude-haiku-4-5", cap.model)
	}
	if strings.Contains(cap.beta, "context-1m") {
		t.Errorf("context-1m beta should be stripped for haiku, got %q", cap.beta)
	}
	if got := resp.Header.Get("X-Whittle-Route"); got != "fast" {
		t.Errorf("X-Whittle-Route = %q, want fast", got)
	}
	if !strings.Contains(resp.Header.Get("X-Whittle-Reason"), "route:cheap") {
		t.Errorf("reason should name the route: %q", resp.Header.Get("X-Whittle-Reason"))
	}
	sb, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(sb), "message_start") {
		t.Errorf("response body not streamed back: %q", string(sb))
	}
}

// No-op: the request already asks for the tier it routes to → forwarded byte-for
// byte, no rewrite, no reconciliation.
func TestProxy_NoOpPassthrough(t *testing.T) {
	srv, cap := mockUpstream(t)
	px := testProxy(t, proxyPolicy, srv)
	// "hello" routes to fast(haiku); request already asks for haiku → no-op.
	orig := `{"model":"claude-haiku-4-5","messages":[{"role":"user","content":"hello there"}]}`
	resp := doPost(t, px, "/v1/messages", orig, "context-1m-2025-08-07")
	if cap.body != orig {
		t.Errorf("no-op should forward original body unchanged:\n got  %q\n want %q", cap.body, orig)
	}
	if !strings.Contains(cap.beta, "context-1m") {
		t.Error("no-op must NOT strip anything (beta should survive)")
	}
	if !strings.HasPrefix(resp.Header.Get("X-Whittle-Reason"), "no-op") {
		t.Errorf("reason should be no-op: %q", resp.Header.Get("X-Whittle-Reason"))
	}
}

// Unknown path (count_tokens) passes through untouched.
func TestProxy_UnknownPathPassthrough(t *testing.T) {
	srv, cap := mockUpstream(t)
	px := testProxy(t, proxyPolicy, srv)
	orig := `{"model":"claude-opus-4-8","messages":[]}`
	doPost(t, px, "/v1/messages/count_tokens", orig, "")
	if cap.body != orig || cap.model != "claude-opus-4-8" {
		t.Errorf("count_tokens must pass through unrouted; got model=%q body=%q", cap.model, cap.body)
	}
}

// Mode A: a malformed body is forwarded as-is (never 500 from our parse error).
func TestProxy_MalformedBodyFailsOpen(t *testing.T) {
	srv, cap := mockUpstream(t)
	px := testProxy(t, proxyPolicy, srv)
	bad := `{"model":"claude-opus-4-8","messages":[`
	resp := doPost(t, px, "/v1/messages", bad, "")
	if resp.StatusCode != 200 {
		t.Errorf("malformed body must fail-open (200 from upstream), got %d", resp.StatusCode)
	}
	if cap.body != bad {
		t.Errorf("Mode A must forward the ORIGINAL malformed body, got %q", cap.body)
	}
	if !strings.HasPrefix(resp.Header.Get("X-Whittle-Reason"), "fail-open") {
		t.Errorf("reason should be fail-open: %q", resp.Header.Get("X-Whittle-Reason"))
	}
}

// No policy (cold start / bad config) → transparent passthrough.
func TestProxy_NoPolicyPassthrough(t *testing.T) {
	srv, cap := mockUpstream(t)
	px := testProxy(t, "", srv) // nil policy
	orig := `{"model":"claude-opus-4-8","messages":[{"role":"user","content":"hello"}]}`
	resp := doPost(t, px, "/v1/messages", orig, "")
	if cap.body != orig {
		t.Errorf("no-policy must pass through unchanged, got %q", cap.body)
	}
	if !strings.Contains(resp.Header.Get("X-Whittle-Reason"), "no-policy") {
		t.Errorf("reason should be no-policy: %q", resp.Header.Get("X-Whittle-Reason"))
	}
}
