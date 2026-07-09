package router

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// modelAwareUpstream 400s/403s when it sees the given model (simulating a target
// that rejects the rewritten request), and 200s otherwise. It counts calls and
// records the last model seen.
func modelAwareUpstream(t *testing.T, rejectModel string, status int, errType string) (*httptest.Server, *int32, *string) {
	t.Helper()
	var calls int32
	lastModel := new(string)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		b, _ := io.ReadAll(r.Body)
		var body struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(b, &body)
		*lastModel = body.Model
		if body.Model == rejectModel {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			io.WriteString(w, `{"type":"error","error":{"type":"`+errType+`","message":"nope"}}`)
			return
		}
		w.WriteHeader(200)
		io.WriteString(w, "event: message_start\ndata: {}\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv, &calls, lastModel
}

// Mode B: a rewrite-caused 400 retries the ORIGINAL model once and relays its 200.
func TestProxy_ModeB_RetryOriginalOn400(t *testing.T) {
	// Reject haiku (the routed target) with a 400; opus (original) succeeds.
	srv, calls, last := modelAwareUpstream(t, "claude-haiku-4-5", 400, "invalid_request_error")
	px := testProxy(t, proxyPolicy, srv)
	resp := doPost(t, px, "/v1/messages",
		`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"hello"}]}`, "")

	if resp.StatusCode != 200 {
		t.Fatalf("Mode B should recover via original → 200, got %d", resp.StatusCode)
	}
	if *last != "claude-opus-4-8" {
		t.Errorf("retry should use the ORIGINAL model, last upstream model = %q", *last)
	}
	if n := atomic.LoadInt32(calls); n != 2 {
		t.Errorf("expected exactly 2 upstream calls (routed 400 + original retry), got %d", n)
	}
	if !strings.Contains(resp.Header.Get("X-Whittle-Reason"), "mode-b:retried-original") {
		t.Errorf("reason should note the retry: %q", resp.Header.Get("X-Whittle-Reason"))
	}
}

// Mode B: a 403 permission_error blocks the tier account-globally — the NEXT
// request does not even attempt the blocked tier.
func TestProxy_ModeB_403BlocksTier(t *testing.T) {
	srv, calls, _ := modelAwareUpstream(t, "claude-haiku-4-5", 403, "permission_error")
	px := testProxy(t, proxyPolicy, srv)

	// First request: routes to haiku → 403 → block → retry opus → 200.
	_ = doPost(t, px, "/v1/messages", `{"model":"claude-opus-4-8","messages":[{"role":"user","content":"hello"}]}`, "")
	if !px.tierBlocked("fast") {
		t.Fatal("a 403 permission_error should have blocked the fast tier")
	}
	firstCalls := atomic.LoadInt32(calls)

	// Second request would route to haiku again — but the tier is blocked, so we
	// pass the ORIGINAL through with NO retry loop (a single upstream call).
	resp := doPost(t, px, "/v1/messages", `{"model":"claude-opus-4-8","messages":[{"role":"user","content":"hello"}]}`, "")
	if got := resp.Header.Get("X-Whittle-Reason"); !strings.Contains(got, "entitlement-blocked") {
		t.Errorf("second request should be guarded as entitlement-blocked: %q", got)
	}
	if delta := atomic.LoadInt32(calls) - firstCalls; delta != 1 {
		t.Errorf("blocked-tier request should make exactly 1 upstream call (no re-attempt+retry), got %d", delta)
	}
}

// A no-op / passthrough request that 400s is NOT retried (it would loop on the
// same input) — relayed verbatim.
func TestProxy_NoOp400NotRetried(t *testing.T) {
	// Reject haiku; the request already asks for haiku (no-op) and gets a 400.
	srv, calls, _ := modelAwareUpstream(t, "claude-haiku-4-5", 400, "invalid_request_error")
	px := testProxy(t, proxyPolicy, srv)
	resp := doPost(t, px, "/v1/messages",
		`{"model":"claude-haiku-4-5","messages":[{"role":"user","content":"hello"}]}`, "")
	if resp.StatusCode != 400 {
		t.Errorf("no-op 400 should relay verbatim, got %d", resp.StatusCode)
	}
	if n := atomic.LoadInt32(calls); n != 1 {
		t.Errorf("no-op must not retry: expected 1 upstream call, got %d", n)
	}
}

// 429 (quota) on a rewritten request is relayed verbatim, never retried.
func TestProxy_429NotRetried(t *testing.T) {
	srv, calls, _ := modelAwareUpstream(t, "claude-haiku-4-5", 429, "rate_limit_error")
	px := testProxy(t, proxyPolicy, srv)
	resp := doPost(t, px, "/v1/messages",
		`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"hello"}]}`, "")
	if resp.StatusCode != 429 {
		t.Errorf("429 should relay verbatim, got %d", resp.StatusCode)
	}
	if n := atomic.LoadInt32(calls); n != 1 {
		t.Errorf("429 must not be retried: expected 1 call, got %d", n)
	}
}

// The context-length guard: an oversized context is not down-routed to a
// smaller-window tier — the original is passed through.
func TestProxy_ContextGuardKeepsOriginal(t *testing.T) {
	srv, _, last := modelAwareUpstream(t, "never", 200, "")
	px := testProxy(t, proxyPolicy, srv)
	// Build a body big enough that bytes/4 exceeds haiku's 180k margin (~720KB).
	big := strings.Repeat("x", 800_000)
	body := `{"model":"claude-opus-4-8","messages":[{"role":"user","content":"hello ` + big + `"}]}`
	resp := doPost(t, px, "/v1/messages", body, "")
	if *last != "claude-opus-4-8" {
		t.Errorf("oversized context must keep the original model, upstream saw %q", *last)
	}
	if !strings.Contains(resp.Header.Get("X-Whittle-Reason"), "context-too-large") {
		t.Errorf("reason should be the context guard: %q", resp.Header.Get("X-Whittle-Reason"))
	}
}

// Mode C: a transport failure (dead upstream) → synthetic 502, never a hang.
func TestProxy_ModeC_TransportError(t *testing.T) {
	srv, _ := mockUpstream(t)
	px := testProxy(t, proxyPolicy, srv)
	px.baseURL = "http://127.0.0.1:1" // nothing listening
	resp := doPost(t, px, "/v1/messages",
		`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"hello"}]}`, "")
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("transport error should yield 502, got %d", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("X-Whittle-Reason"), "mode-c") {
		t.Errorf("reason should be mode-c: %q", resp.Header.Get("X-Whittle-Reason"))
	}
}
