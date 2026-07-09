package router

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestProxy_ModeB_RetryAlsoFails_SingleRelayNoPanic covers the retry-that-also-
// fails branch the existing suite skips.
//
// WHY: a routed 400 triggers a Mode-B retry of the ORIGINAL. If that retry ALSO
// 400s, the client must get exactly ONE 400 relayed — no third attempt, no
// second WriteHeader (which would panic once relay already wrote the head), and
// exactly two upstream calls. Every upstream response here is a 400, so a bug
// that loops or double-writes shows up as either a call count != 2 or a panic.
func TestProxy_ModeB_RetryAlsoFails_SingleRelayNoPanic(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		fmt.Fprintf(w, `{"type":"error","error":{"type":"invalid_request_error","message":"call-%d"}}`, n)
	}))
	t.Cleanup(srv.Close)

	px := testProxy(t, proxyPolicy, srv)
	resp := doPost(t, px, "/v1/messages",
		`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"hello"}]}`, "")

	if resp.StatusCode != 400 {
		t.Fatalf("a retry that also 400s must relay a single 400, got %d", resp.StatusCode)
	}
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Errorf("expected exactly 2 upstream calls (routed 400 + one original retry), got %d", n)
	}
	body, _ := io.ReadAll(resp.Body)
	// The RETRY's body (call-2) is what the client should see — the retry response
	// is relayed, the buffered routed-400 (call-1) body is discarded.
	if !strings.Contains(string(body), "call-2") {
		t.Errorf("client should receive the retry's 400 body (call-2), got %q", string(body))
	}
}

// scriptedRT returns a 400 on the first call and a transport error on the second,
// letting a test drive the Mode-B branch where the RETRY cannot even be sent.
type scriptedRT struct{ calls int32 }

func (s *scriptedRT) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Body != nil {
		io.Copy(io.Discard, r.Body)
		r.Body.Close()
	}
	if atomic.AddInt32(&s.calls, 1) == 1 {
		body := `{"type":"error","error":{"type":"invalid_request_error","message":"ROUTED_400_BUFFERED"}}`
		return &http.Response{
			StatusCode: 400,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	}
	return nil, fmt.Errorf("simulated retry transport failure")
}

// TestProxy_ModeB_RetryTransportFailure_RelaysBufferedOriginal covers the branch
// where the Mode-B retry's SEND fails (not a 4xx — a transport error).
//
// WHY: when the routed request 400s but the retry of the original cannot be sent
// (upstream drops between the two calls), the proxy must fall back to relaying the
// BUFFERED original 4xx verbatim — never a hang, never a synthetic 502, never a
// double write. This exercises modeBRetry's relayBytes fallback (otherwise 0%
// covered). A scripted transport returns 400 then a transport error.
func TestProxy_ModeB_RetryTransportFailure_RelaysBufferedOriginal(t *testing.T) {
	srv, _ := mockUpstream(t) // only to satisfy testProxy; the client is overridden below
	px := testProxy(t, proxyPolicy, srv)
	rt := &scriptedRT{}
	px.client = &http.Client{Transport: rt}

	resp := doPost(t, px, "/v1/messages",
		`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"hello"}]}`, "")

	if resp.StatusCode != 400 {
		t.Fatalf("retry-send-failure should relay the buffered original 400, got %d", resp.StatusCode)
	}
	if n := atomic.LoadInt32(&rt.calls); n != 2 {
		t.Errorf("expected exactly 2 upstream attempts (routed 400 + one failed retry), got %d", n)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "ROUTED_400_BUFFERED") {
		t.Errorf("client should receive the buffered original 400 body, got %q", string(body))
	}
	if r := resp.Header.Get("X-Whittle-Reason"); !strings.Contains(r, "mode-b:relay(retry-failed)") {
		t.Errorf("reason should record the retry-failed relay: %q", r)
	}
}

// TestProxy_ModeB_RetrySuccessBodyAndHeadersAreRetrys asserts the provenance of
// the relayed response AND the commit-point invariant in one shot.
//
// WHY: when the routed request 400s and the retried original 200s, the client
// must see the RETRY's status, headers, and streamed body — never any byte of the
// buffered 400. The routed 400 carries a distinctive header and body marker; if
// either leaks to the client, the commit point was violated (a partial write
// happened before the retry decision) or the wrong response was relayed.
func TestProxy_ModeB_RetrySuccessBodyAndHeadersAreRetrys(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(b, &body)
		if body.Model == "claude-haiku-4-5" { // the routed (down-route) target rejects
			w.Header().Set("X-From", "routed-400")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"ROUTED_400_LEAK"}}`)
			return
		}
		w.Header().Set("X-From", "retry-200")
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		io.WriteString(w, "event: message_start\ndata: {\"src\":\"RETRY_STREAM_BODY\"}\n\n")
	}))
	t.Cleanup(srv.Close)

	px := testProxy(t, proxyPolicy, srv)
	resp := doPost(t, px, "/v1/messages",
		`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"hello"}]}`, "")

	if resp.StatusCode != 200 {
		t.Fatalf("Mode B should recover via the original retry → 200, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("X-From"); got != "retry-200" {
		t.Errorf("relayed headers must be the RETRY's; X-From = %q, want retry-200 (the buffered 400's headers leaked)", got)
	}
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), "ROUTED_400_LEAK") {
		t.Errorf("commit-point violated: the buffered routed-400 body reached the client: %q", string(body))
	}
	if !strings.Contains(string(body), "RETRY_STREAM_BODY") {
		t.Errorf("client should receive the retry's streamed body, got %q", string(body))
	}
}
