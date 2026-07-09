package router

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestProxy_OneLogLinePerRequestEveryPath verifies the observability contract on
// EVERY routing path: exactly one structured log line per request, the correct
// status for that path, and NEVER any prompt text.
//
// WHY: the log line is the only per-request record (review C1 forbids persisting
// request content). A path that logs zero lines is a blind spot; a path that logs
// two double-counts; a path that logs the prompt leaks user content to disk. The
// existing suite checks only the routed path — this sweeps routed, no-op,
// passthrough(unroutable), no-policy, mode-b, and mode-c. Each request carries the
// marker SECRET_PROMPT_XYZ in its content, which must never appear in the log.
func TestProxy_OneLogLinePerRequestEveryPath(t *testing.T) {
	const secret = "SECRET_PROMPT_XYZ"
	msg := func(model string) string {
		return `{"model":"` + model + `","messages":[{"role":"user","content":"hello ` + secret + `"}]}`
	}

	// streamUpstream 200s and streams a tiny SSE body.
	streamUpstream := func(t *testing.T) *httptest.Server {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.Copy(io.Discard, r.Body)
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			io.WriteString(w, "event: message_stop\ndata: {}\n\n")
		}))
		t.Cleanup(s.Close)
		return s
	}

	newLogged := func(policyJSON, baseURL string) (*Proxy, *capLogger) {
		var pol *Policy
		if policyJSON != "" {
			p, _, err := Load([]byte(policyJSON))
			if err != nil {
				t.Fatal(err)
			}
			pol = p
		}
		lg := &capLogger{}
		px := NewProxy(pol, nil, NewMemSessionStore(), lg)
		px.baseURL = baseURL
		return px, lg
	}

	post := func(px *Proxy, path, body string) *http.Response {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Claude-Code-Session-Id", "log-sess")
		rec := httptest.NewRecorder()
		px.ServeHTTP(rec, req)
		return rec.Result()
	}

	type pathCase struct {
		name       string
		build      func(t *testing.T) (*Proxy, *capLogger)
		do         func(px *Proxy) *http.Response
		wantStatus int
		reasonHas  string
	}

	cases := []pathCase{
		{
			name:       "routed",
			build:      func(t *testing.T) (*Proxy, *capLogger) { return newLogged(proxyPolicy, streamUpstream(t).URL) },
			do:         func(px *Proxy) *http.Response { return post(px, "/v1/messages", msg("claude-opus-4-8")) },
			wantStatus: 200, reasonHas: "route:cheap",
		},
		{
			name:       "no-op",
			build:      func(t *testing.T) (*Proxy, *capLogger) { return newLogged(proxyPolicy, streamUpstream(t).URL) },
			do:         func(px *Proxy) *http.Response { return post(px, "/v1/messages", msg("claude-haiku-4-5")) },
			wantStatus: 200, reasonHas: "no-op",
		},
		{
			name:       "passthrough-unroutable",
			build:      func(t *testing.T) (*Proxy, *capLogger) { return newLogged(proxyPolicy, streamUpstream(t).URL) },
			do:         func(px *Proxy) *http.Response { return post(px, "/v1/messages/count_tokens", msg("claude-opus-4-8")) },
			wantStatus: 200, reasonHas: "passthrough:unroutable-path",
		},
		{
			name:       "no-policy",
			build:      func(t *testing.T) (*Proxy, *capLogger) { return newLogged("", streamUpstream(t).URL) },
			do:         func(px *Proxy) *http.Response { return post(px, "/v1/messages", msg("claude-opus-4-8")) },
			wantStatus: 200, reasonHas: "no-policy",
		},
		{
			name: "mode-b",
			build: func(t *testing.T) (*Proxy, *capLogger) {
				srv, _, _ := modelAwareUpstream(t, "claude-haiku-4-5", 400, "invalid_request_error")
				return newLogged(proxyPolicy, srv.URL)
			},
			do:         func(px *Proxy) *http.Response { return post(px, "/v1/messages", msg("claude-opus-4-8")) },
			wantStatus: 200, reasonHas: "mode-b:retried-original",
		},
		{
			name: "mode-c",
			build: func(t *testing.T) (*Proxy, *capLogger) {
				px, lg := newLogged(proxyPolicy, "http://127.0.0.1:1") // nothing listening
				return px, lg
			},
			do:         func(px *Proxy) *http.Response { return post(px, "/v1/messages", msg("claude-opus-4-8")) },
			wantStatus: http.StatusBadGateway, reasonHas: "mode-c",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			px, lg := tc.build(t)
			resp := tc.do(px)

			// The observability contract is the STRUCTURED JSON verdict line (R22 "JSON
			// log schema"); exactly one must be emitted per request on every path.
			var structured []string
			for _, l := range lg.lines {
				if strings.HasPrefix(strings.TrimSpace(l), `{"tier"`) {
					structured = append(structured, l)
				}
				if strings.Contains(l, secret) {
					t.Fatalf("LOG LEAKED PROMPT TEXT on the %s path: %s", tc.name, l)
				}
			}
			if len(structured) != 1 {
				t.Fatalf("expected exactly ONE structured verdict line on the %s path, got %d: %v", tc.name, len(structured), lg.lines)
			}
			line := structured[0]
			if resp.StatusCode != tc.wantStatus {
				t.Errorf("%s: response status = %d, want %d", tc.name, resp.StatusCode, tc.wantStatus)
			}
			if !strings.Contains(line, tc.reasonHas) {
				t.Errorf("%s: log reason should contain %q: %s", tc.name, tc.reasonHas, line)
			}
		})
	}
}

// TestProxy_ModeC_EmitsSingleLogLine pins a real (low-severity) finding, left RED.
//
// WHY: the task's contract is "exactly one log line per request on EVERY path".
// Every path honors this EXCEPT mode-c: modeC calls p.log.Printf("router: upstream
// transport error: %v", err) in ADDITION to the one structured JSON verdict line
// ServeHTTP emits. So a transport failure logs TWO lines — one ad-hoc/unstructured,
// one JSON — which breaks one-line-per-request log ingestion and any metric derived
// from the JSON log schema (R22). The transport-error detail belongs folded into
// the structured line's reason, not on a separate Printf. Left RED to pin the
// deviation; the fix is to drop the extra Printf (or carry the detail in-schema).
func TestProxy_ModeC_EmitsSingleLogLine(t *testing.T) {
	pol, _, err := Load([]byte(proxyPolicy))
	if err != nil {
		t.Fatal(err)
	}
	lg := &capLogger{}
	px := NewProxy(pol, nil, NewMemSessionStore(), lg)
	px.baseURL = "http://127.0.0.1:1" // nothing listening → transport error

	req := httptest.NewRequest(http.MethodPost, "/v1/messages",
		strings.NewReader(`{"model":"claude-opus-4-8","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Claude-Code-Session-Id", "log-sess")
	px.ServeHTTP(httptest.NewRecorder(), req)

	if len(lg.lines) != 1 {
		t.Errorf("mode-c must emit exactly ONE log line per request, got %d: %v", len(lg.lines), lg.lines)
	}
}
