package router

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestProxy_StreamsIncrementally is the single most important M3 guard.
//
// WHY: Claude Code consumes the SSE event stream incrementally. If the proxy
// buffers the whole upstream body before flushing anything to the client, the
// client hangs for the entire (minutes-long) generation — the GATE-1 "Claude
// Code hangs" failure the experiment's per-chunk flush exists to prevent. The
// rest of the suite drives the proxy through httptest.NewRecorder, which buffers
// the whole response and therefore CANNOT observe incremental delivery. This test
// runs the proxy on a REAL server and makes the upstream BLOCK after the first
// SSE chunk until the client proves it already received it. If the proxy buffers,
// the client never sees chunk 1, the upstream never unblocks, and the stall is
// caught by the deadline (turned into a failure, not a hang).
func TestProxy_StreamsIncrementally(t *testing.T) {
	releaseSecond := make(chan struct{})

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		rc := http.NewResponseController(w)
		io.WriteString(w, "event: message_start\ndata: {\"chunk\":1}\n\n")
		_ = rc.Flush()
		<-releaseSecond // hold the second chunk until the client has the first
		io.WriteString(w, "event: message_stop\ndata: {\"chunk\":2}\n\n")
		_ = rc.Flush()
	}))
	t.Cleanup(upstream.Close)

	px := testProxy(t, proxyPolicy, upstream) // points px.baseURL at upstream
	front := httptest.NewServer(px)
	t.Cleanup(front.Close)

	req, _ := http.NewRequest(http.MethodPost, front.URL+"/v1/messages",
		strings.NewReader(`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Claude-Code-Session-Id", "stream-sess")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })

	gotFirst := make(chan struct{})
	go func() {
		br := bufio.NewReader(resp.Body)
		sawFirst := false
		for {
			line, err := br.ReadString('\n')
			if !sawFirst && strings.Contains(line, `"chunk":1`) {
				sawFirst = true
				close(gotFirst)
			}
			if err != nil {
				return
			}
		}
	}()

	select {
	case <-gotFirst:
		// chunk 1 reached the client BEFORE chunk 2 was ever written upstream:
		// delivery is genuinely incremental.
	case <-time.After(3 * time.Second):
		close(releaseSecond) // let the upstream finish so servers can close cleanly
		t.Fatal("client did not receive the first SSE chunk before the second was produced: the proxy is buffering the stream — Claude Code would hang")
	}
	close(releaseSecond)
}
