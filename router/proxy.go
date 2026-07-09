package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Logger is the minimal logging surface the proxy needs (satisfied by the stdlib
// *log.Logger), kept tiny so callers aren't forced into a logging dependency —
// mirrors compress.Logger.
type Logger interface {
	Printf(format string, args ...any)
}

// maxBodyBytes bounds the request body we buffer to route. A larger body is
// stream-passthrough'd unrouted (R18) — rejecting a request the client would
// have succeeded on directly is the worst outcome, and large-context is exactly
// when routing matters least (it can only go up-tier).
const maxBodyBytes = 32 << 20

// upstreamHost is the Anthropic API host. The proxy forwards here with the
// client's own credentials untouched (GATE-0).
const upstreamHost = "api.anthropic.com"

// entitlementBlockTTL bounds how long a 403-blocked tier stays blocked before we
// re-probe it — long enough to stop a per-turn 403 loop, short enough that a
// transient entitlement failure (billing lapse, propagation delay) recovers.
const entitlementBlockTTL = 15 * time.Minute

// Proxy is the whittle router daemon's HTTP handler. It sits on ANTHROPIC_BASE_URL,
// routes each /v1/messages request to a model tier per the policy, reconciles the
// request for that model, and streams the response back. It holds the policy
// behind an atomic pointer for hot-reload; a nil policy means "no valid config"
// and every request is transparently passed through (cold-start safety, R3).
type Proxy struct {
	policy  atomic.Pointer[Policy]
	cl      Classifier
	sess    SessionStore
	adapter Adapter
	client  *http.Client
	log     Logger
	baseURL string // upstream base, overridable in tests

	// blocked is the account-global entitlement blocklist: tier → block-until.
	// A 403 permission_error blocks the tier so we stop routing there; the block
	// is TTL-bounded (review C1/C2) so a transient/mis-attributed failure
	// self-heals and we re-probe, rather than disabling a tier until restart.
	mu      sync.RWMutex
	blocked map[string]time.Time
}

// NewProxy builds a Proxy. A nil classifier uses the noop (heuristics-only); a
// nil session store disables stickiness; a nil logger discards logs.
func NewProxy(pol *Policy, cl Classifier, sess SessionStore, log Logger) *Proxy {
	if cl == nil {
		cl = noopClassifier{}
	}
	if log == nil {
		log = discardLogger{}
	}
	p := &Proxy{
		cl:      cl,
		sess:    sess,
		adapter: AnthropicAdapter{},
		log:     log,
		baseURL: "https://" + upstreamHost,
		blocked: map[string]time.Time{},
		client: &http.Client{
			// No TOTAL timeout — streamed generations run for minutes (R9). The
			// transport bounds connect + idle instead.
			Transport: &http.Transport{
				ResponseHeaderTimeout: 120 * time.Second,
				IdleConnTimeout:       90 * time.Second,
				MaxIdleConnsPerHost:   8,
			},
		},
	}
	p.policy.Store(pol)
	return p
}

// SetPolicy atomically swaps the active policy (hot-reload). A nil policy puts
// the proxy into transparent passthrough.
func (p *Proxy) SetPolicy(pol *Policy) { p.policy.Store(pol) }

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Local health endpoint for `whittle status` / launchd liveness — answered
	// here, NEVER forwarded upstream (a GET /health would otherwise passthrough to
	// Anthropic). Not logged as a routing verdict.
	if r.URL.Path == "/health" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"status":"ok"}`)
		return
	}

	rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	start := time.Now()
	p.serve(rec, r)
	// One structured line per request — routing verdict + outcome, NEVER prompt
	// text (review C1: the router must not persist request content to disk).
	p.log.Printf(`{"tier":%q,"requested":%q,"reason":%q,"status":%d,"latency_ms":%d,"ctx_tokens":%d,"session":%q}`,
		rec.Header().Get("X-Whittle-Route"), rec.requested, rec.Header().Get("X-Whittle-Reason"),
		rec.status, time.Since(start).Milliseconds(), rec.ctxTokens,
		shortSession(r.Header.Get("X-Claude-Code-Session-Id")))
}

// serve is the request handler proper; ServeHTTP wraps it for status capture and
// the log line.
func (p *Proxy) serve(rec *statusRecorder, r *http.Request) {
	var w http.ResponseWriter = rec
	// Only POST /v1/messages EXACTLY is routable. Every sibling (count_tokens,
	// /v1/messages/batches, model listing, GET, …) passes through untouched — an
	// over-broad prefix match would inject a model field into a batch body
	// (review M1). The proxy owns the global base-URL slot, so non-message
	// traffic must be invisible to routing.
	if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
		p.passthrough(w, r, nil, "passthrough:unroutable-path")
		return
	}

	body, tooLarge, err := readBounded(r.Body)
	rec.ctxTokens = len(body) / 4
	if err != nil || tooLarge {
		// Can't buffer-and-route → forward the FULL original body UNBUFFERED,
		// unrouted (R18). Reconstruct the stream from the already-read prefix
		// plus the rest of r.Body (readBounded left it open). Forwarding the
		// truncated buffer would corrupt the request — the B1 bug.
		full := io.MultiReader(bytes.NewReader(body), r.Body)
		p.passthroughStream(w, r, full, "passthrough:unbuffered")
		return
	}

	pol := p.policy.Load()
	if pol == nil {
		// No valid policy (cold start / bad config) → transparent passthrough.
		p.passthrough(w, r, body, "passthrough:no-policy")
		return
	}

	sig, err := Extract(body, r.Header.Get("X-Claude-Code-Session-Id"), pol.Inspect)
	rec.requested = sig.RequestedModel // for the log line (model id is not prompt text)
	if err != nil {
		// Our parse error → Mode A: forward the ORIGINAL untouched.
		p.passthrough(w, r, body, "fail-open:parse")
		return
	}

	dec := Decide(sig, pol, p.cl, p.sess, pinFromHeader(pol, r.Header))

	// No-op: the resolved model is what the client already asked for → byte
	// passthrough, no rewrite, no reconciliation (R11).
	if IsNoOp(dec, sig) {
		p.passthrough(w, r, body, "no-op:"+dec.Reason)
		return
	}

	// Guard: never route to an entitlement-blocked tier (a prior 403), and never
	// down-route a context that won't fit the target's window (CanServe). Either
	// → keep the ORIGINAL request untouched (the safe terminal fallback; a
	// next-capable-tier selection is a future optimization).
	if p.tierBlocked(dec.Tier) {
		p.passthrough(w, r, body, "guard:entitlement-blocked:"+dec.Tier)
		return
	}
	if !CanServe(dec.Model, sig.ContextTokens) {
		p.passthrough(w, r, body, "guard:context-too-large:"+dec.Tier)
		return
	}

	// Reconcile the request for the target model (rewrite model + strip
	// incompatible features across body+headers).
	outBody, outHdr, stripped, err := p.reconcile(body, r.Header, dec.Model)
	if err != nil {
		// Reconcile-time parse error → Mode A.
		p.passthrough(w, r, body, "fail-open:reconcile-parse")
		return
	}
	reason := dec.Reason
	if len(stripped) > 0 {
		reason += " stripped:" + strings.Join(stripped, "+")
	}
	p.forward(w, r, body, outBody, outHdr, dec, reason)
}

func (p *Proxy) tierBlocked(tier string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	until, ok := p.blocked[tier]
	return ok && time.Now().Before(until)
}

func (p *Proxy) blockTier(tier string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.blocked[tier] = time.Now().Add(entitlementBlockTTL)
}

// reconcile parses the body, rewrites it for target, and returns the new body,
// the reconciled headers, and the stripped feature names.
func (p *Proxy) reconcile(body []byte, hdr http.Header, target string) ([]byte, http.Header, []string, error) {
	req, err := ParseRequest(body, cloneHeader(hdr))
	if err != nil {
		return nil, nil, nil, err
	}
	stripped := p.adapter.Reconcile(req, target)
	out, err := req.Serialize()
	if err != nil {
		return nil, nil, nil, err
	}
	return out, req.Headers, stripped, nil
}

// forward sends the routed (reconciled) request upstream and, honoring the
// commit-point invariant, classifies the status BEFORE writing anything to the
// client:
//   - 2xx / 5xx / 429 / other 4xx → relay verbatim (genuine; retry can't help).
//   - 400 / 403 caused by our rewrite → Mode B: retry the ORIGINAL once (a 403
//     permission_error also blocks the tier account-globally), then relay.
//   - transport failure → Mode C: synthetic 502 (forwarding the original can't
//     reach a dead upstream).
func (p *Proxy) forward(w http.ResponseWriter, r *http.Request, originalBody, reconciledBody []byte, reconciledHdr http.Header, dec Decision, reason string) {
	resp, err := p.sendUpstream(r, reconciledBody, reconciledHdr)
	if err != nil {
		p.modeC(w, err)
		return
	}
	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusForbidden {
		p.modeBRetry(w, r, originalBody, dec, reason, resp)
		return
	}
	defer resp.Body.Close()
	p.relay(w, resp, dec.Tier, reason)
}

// modeBRetry handles a rewrite-caused 400/403. It buffers the small error body
// (nothing has been written to the client yet — the commit point is held),
// classifies by error.type, blocks the tier on a permission_error, retries the
// ORIGINAL request once, and relays the retry (or, if the retry can't be sent,
// the buffered original 4xx). Only reached when we actually rewrote, so a 4xx on
// a no-op/passthrough request is never retried (it would loop on the same input).
func (p *Proxy) modeBRetry(w http.ResponseWriter, r *http.Request, originalBody []byte, dec Decision, reason string, resp *http.Response) {
	errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	status := resp.StatusCode
	hdr := resp.Header.Clone()
	resp.Body.Close()

	// Block the tier only on a genuine 403 permission_error (entitlement). A 400
	// carrying a permission_error is far more likely a fluke than a real
	// account-level denial, and blocking on it would disable a tier the plan can
	// actually use (review C2). The block is TTL-bounded so even a mis-attributed
	// 403 self-heals (C1).
	if status == http.StatusForbidden && parseErrorType(errBody) == "permission_error" {
		p.blockTier(dec.Tier)
		reason += " entitlement-blocked"
	}

	// Surface WHICH rewrite the upstream rejected — otherwise a bad tier model id
	// (e.g. an invalid/undated model) fails on every request and is silently bailed
	// out by the retry, with no clue in the log. The model id is not prompt text, so
	// it is safe to record.
	detail := fmt.Sprintf("(rewrote→%s got %d:%s)", dec.Model, status, orDash(parseErrorType(errBody)))

	retry, err := p.sendUpstream(r, originalBody, r.Header)
	if err != nil {
		// Retry can't even be sent → relay the buffered original 4xx verbatim.
		p.relayBytes(w, status, hdr, errBody, dec.Tier, reason+" mode-b:relay-original"+detail)
		return
	}
	defer retry.Body.Close()
	p.relay(w, retry, dec.Tier, reason+" mode-b:retried-original"+detail)
}

func (p *Proxy) modeC(w http.ResponseWriter, err error) {
	// The single structured verdict line (emitted by ServeHTTP) carries this via
	// reason=mode-c:transport-error + status 502 — no second ad-hoc log line, so
	// the one-line-per-request contract holds on the transport-error path too
	// (tester Finding 2).
	setVerdict(w.Header(), "-", "mode-c:transport-error")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadGateway)
	io.WriteString(w, `{"type":"error","error":{"type":"api_error","message":"upstream unreachable"}}`)
}

// parseErrorType extracts error.type from an Anthropic error body ("" if absent
// or unparseable). Classification keys on this, not the raw status code, since
// Anthropic is not 1:1 (review H2).
func parseErrorType(b []byte) string {
	var e struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	_ = json.Unmarshal(b, &e)
	return e.Error.Type
}

// sendUpstream builds and executes the upstream request: client headers minus
// hop-by-hop, Host + Content-Length set, Accept-Encoding forced to identity so
// the SSE stream is plaintext (the gzip-SSE framing hazard, GATE-1).
func (p *Proxy) sendUpstream(r *http.Request, body []byte, hdr http.Header) (*http.Response, error) {
	up, err := http.NewRequestWithContext(r.Context(), http.MethodPost, p.baseURL+r.URL.RequestURI(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	copyUpstreamHeaders(up.Header, hdr)
	up.Host = upstreamHost
	up.Header.Set("Accept-Encoding", "identity")
	up.ContentLength = int64(len(body))
	up.Header.Set("Content-Length", strconv.Itoa(len(body)))
	return p.client.Do(up)
}

// relay copies the upstream status + headers to the client and streams the body
// with per-chunk flush (SSE). It is the commit point: the status is known here
// (T3.2 inserts the Mode-B relay-vs-retry decision BEFORE this writes anything).
func (p *Proxy) relay(w http.ResponseWriter, resp *http.Response, tier, reason string) {
	copyDownstreamHeaders(w.Header(), resp.Header)
	setVerdict(w.Header(), tier, reason)
	w.WriteHeader(resp.StatusCode)
	streamFlush(w, resp.Body)
}

// relayBytes relays a fully-buffered response (status + headers + body). Used
// for the Mode-B fallback where the original 4xx body was buffered to classify.
func (p *Proxy) relayBytes(w http.ResponseWriter, status int, hdr http.Header, body []byte, tier, reason string) {
	copyDownstreamHeaders(w.Header(), hdr)
	setVerdict(w.Header(), tier, reason)
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// passthrough forwards the request as-is (no rewrite) and streams the response.
// Used for unroutable paths, no-op decisions, no policy, and Mode-A errors. When
// body is nil the original request body is used (it was never read).
func (p *Proxy) passthrough(w http.ResponseWriter, r *http.Request, body []byte, reason string) {
	var resp *http.Response
	var err error
	if body == nil {
		resp, err = p.sendUpstreamRaw(r, r.Body, r.ContentLength)
	} else {
		resp, err = p.sendUpstream(r, body, r.Header)
	}
	if err != nil {
		p.modeC(w, err)
		return
	}
	defer resp.Body.Close()
	p.relay(w, resp, "-", reason)
}

// passthroughStream forwards an arbitrary body reader upstream UNBUFFERED and
// relays the response. Used when we can't buffer-and-route (body over the cap, or
// a read error): the request must reach upstream byte-for-byte, never truncated
// (R18/B1). The original declared Content-Length (r.ContentLength) is preserved
// so upstream sees the same framing the client sent.
func (p *Proxy) passthroughStream(w http.ResponseWriter, r *http.Request, body io.Reader, reason string) {
	resp, err := p.sendUpstreamRaw(r, body, r.ContentLength)
	if err != nil {
		p.modeC(w, err)
		return
	}
	defer resp.Body.Close()
	p.relay(w, resp, "-", reason)
}

// sendUpstreamRaw forwards a body reader unbuffered with the given declared
// length (-1 = unknown → chunked). Used for the streaming passthrough paths where
// we deliberately do not buffer the body.
func (p *Proxy) sendUpstreamRaw(r *http.Request, body io.Reader, contentLength int64) (*http.Response, error) {
	up, err := http.NewRequestWithContext(r.Context(), r.Method, p.baseURL+r.URL.RequestURI(), body)
	if err != nil {
		return nil, err
	}
	copyUpstreamHeaders(up.Header, r.Header)
	up.Host = upstreamHost
	up.Header.Set("Accept-Encoding", "identity")
	up.ContentLength = contentLength
	return p.client.Do(up)
}

// ---- helpers ----

// readBounded reads up to maxBodyBytes+1; returns (body, tooLarge, err). It does
// NOT close rc: on the too-large path the caller reconstructs the full stream
// from this prefix + the still-open rc (the net/http server closes the request
// body when the handler returns).
func readBounded(rc io.ReadCloser) ([]byte, bool, error) {
	b, err := io.ReadAll(io.LimitReader(rc, maxBodyBytes+1))
	if err != nil {
		return b, false, err
	}
	if len(b) > maxBodyBytes {
		return b, true, nil
	}
	return b, false, nil
}

// hopByHop headers are per-connection and must never be forwarded.
var hopByHop = map[string]bool{
	"Connection": true, "Keep-Alive": true, "Proxy-Authenticate": true,
	"Proxy-Authorization": true, "Te": true, "Trailer": true,
	"Transfer-Encoding": true, "Upgrade": true,
}

// copyUpstreamHeaders forwards all client headers except hop-by-hop and the few
// the transport sets itself (Host, Content-Length, Accept-Encoding). Forward
// everything, strip the few we must — never allowlist (codebase principle).
func copyUpstreamHeaders(dst, src http.Header) {
	for k, vs := range src {
		if hopByHop[k] || k == "Host" || k == "Content-Length" || k == "Accept-Encoding" {
			continue
		}
		dst[k] = append([]string(nil), vs...)
	}
}

// copyDownstreamHeaders relays upstream response headers except hop-by-hop and
// Content-Length (the body may be re-framed as we stream) and Content-Encoding
// (we forced identity upstream, so the body is already plaintext).
func copyDownstreamHeaders(dst, src http.Header) {
	for k, vs := range src {
		if hopByHop[k] || k == "Content-Length" || k == "Content-Encoding" {
			continue
		}
		dst[k] = append([]string(nil), vs...)
	}
}

func cloneHeader(h http.Header) http.Header {
	out := make(http.Header, len(h))
	for k, vs := range h {
		out[k] = append([]string(nil), vs...)
	}
	return out
}

// setVerdict stamps the routing decision on the response for observability.
func setVerdict(h http.Header, tier, reason string) {
	h.Set("X-Whittle-Route", tier)
	h.Set("X-Whittle-Reason", reason)
}

// pinFromHeader reads the configured pin-override header value, if any.
func pinFromHeader(pol *Policy, h http.Header) string {
	if pol.Overrides.PinHeader == "" {
		return ""
	}
	return h.Get(pol.Overrides.PinHeader)
}

// streamFlush copies src→dst flushing after every chunk so SSE events reach the
// client immediately (the gzip/flush framing the experiment proved).
func streamFlush(w http.ResponseWriter, src io.Reader) {
	rc := http.NewResponseController(w)
	buf := make([]byte, 8<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			_ = rc.Flush()
		}
		if err != nil {
			return
		}
	}
}

type discardLogger struct{}

func (discardLogger) Printf(string, ...any) {}

// statusRecorder wraps the ResponseWriter to capture the status code (for the
// log line) and carries the request's estimated context size. Unwrap lets
// http.NewResponseController reach the underlying writer's Flush for SSE.
type statusRecorder struct {
	http.ResponseWriter
	status    int
	requested string // the client's requested model (canonicalized), for the log
	ctxTokens int
	wrote     bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wrote {
		s.status = code
		s.wrote = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	s.wrote = true // status stays the default 200 if WriteHeader was never called
	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// orDash returns "-" for an empty string, so log fields are never blank.
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// shortSession returns a non-sensitive prefix of the session UUID for the log
// (enough to correlate a session's requests; not the full id, no prompt text).
func shortSession(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
