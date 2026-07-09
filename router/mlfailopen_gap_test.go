package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/firstops-dev/whittle/router/ml"
)

// GAP pass for smart mode's LOAD-BEARING safety property: with
// WHITTLE_ROUTER_MODEL_URL configured but the sidecar unreachable or erroring, the
// daemon must still ROUTE — the failing signal leaf simply evaluates FALSE, so its
// route does not fire and evaluation falls through to the static default. It
// exercises the REAL ml.Client (not a spy) end-to-end into Decide, so the
// client's fail-open error and the engine's leaf-false handling are one path.
//
// (With ML now inside route conditions rather than a separate classify step, a
// broken sidecar is fail-open by construction: a signal that can't be computed
// can't fire its route.)

// downClient points the real client at a port with nothing listening — a
// connection-refused transport error on every call (returns fast, not a stall).
func downClient() *ml.Client { return ml.New("http://127.0.0.1:1") }

// reachSignalSig matches no cheap leaf in fullPolicy, so evaluation reaches the
// hard-work route's `domain` signal leaf — where the down/erroring sidecar is hit.
var reachSignalSig = Signals{RecentText: "just a normal ask", ContextTokens: 100, SessionID: "s1"}

func TestSmartMode_SignalFailsOpenWhenSidecarDown(t *testing.T) {
	p, _ := mustLoad(t, fullPolicy)

	var d Decision
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("a down sidecar must never panic the routing path: %v", r)
			}
		}()
		d = Decide(reachSignalSig, p, downClient(), NewMemSessionStore(), "")
	}()

	// The signal leaf's failed model call evaluates FALSE, so hard-work does not
	// fire and routing falls open to the static default — never an error. Because
	// a REAL sidecar failure (not smart-off) drove it, the reason is tagged
	// ml-degraded so oncall can tell "sidecar down" from "signal didn't fire".
	if d.Tier != "main" || !strings.Contains(d.Reason, "default") || !strings.Contains(d.Reason, "ml-degraded") {
		t.Fatalf("down sidecar must fail open to the static default (main) tagged ml-degraded, got tier=%q reason=%q", d.Tier, d.Reason)
	}
}

// C1 regression: a `not` over an ML leaf must NOT invert fail-open. With the
// sidecar down the signal is unavailable; "not hard" cannot be confirmed, so the
// route must NOT fire (else not(false)=true routes a genuinely hard prompt to the
// cheap tier during an outage). Routing falls open to the default instead.
func TestSmartMode_NegatedSignalDoesNotFireWhenSidecarDown(t *testing.T) {
	const negPolicy = `{
      "version": 1,
      "tiers": [{"name":"fast","model":"claude-haiku-4-5"},{"name":"smart","model":"claude-opus-4-8"}],
      "default": "smart", "inspect": {"scope": "full"},
      "signals": {"complexity": [{"name":"reasoning","threshold":0.15,
        "hard":["debug this race condition"],"easy":["fix a typo"]}]},
      "routes": [{"name":"downgrade-nonhard","when":{"not":{"complexity":"reasoning:hard"}},"to":"fast"}]
    }`
	p, _ := mustLoad(t, negPolicy)
	d := Decide(Signals{RecentText: "debug this deadlock", ContextTokens: 100, SessionID: "n1"},
		p, downClient(), NewMemSessionStore(), "")
	if d.Tier != "smart" {
		t.Fatalf("negated ML leaf over an unavailable signal must not fire (fail-open to default smart), got tier=%q reason=%q", d.Tier, d.Reason)
	}
	if !strings.Contains(d.Reason, "ml-degraded") {
		t.Errorf("a real sidecar failure should tag the reason ml-degraded, got %q", d.Reason)
	}
}

func TestSmartMode_SignalFailsOpenOnSidecar500(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError) // sidecar raised (e.g. model missing)
	}))
	t.Cleanup(srv.Close)
	p, _ := mustLoad(t, fullPolicy)

	d := Decide(reachSignalSig, p, ml.New(srv.URL), NewMemSessionStore(), "")
	if d.Tier != "main" {
		t.Fatalf("a 500 from the sidecar must fail open to the static default (main), got tier=%q reason=%q", d.Tier, d.Reason)
	}
}

// A route whose ONLY match is a signal leaf must NOT fire when the classifier is
// down — the leaf evaluates false and routing falls to the default, never fires
// the route on a broken model, never panics.
func TestSmartMode_DomainRouteDoesNotFireWhenSidecarDown(t *testing.T) {
	const domainOnly = `{
	  "version":1,
	  "tiers":[{"name":"fast","model":"m0"},{"name":"smart","model":"m1"}],
	  "default":"fast",
	  "inspect":{"scope":"full"},
	  "signals":{"domains":[{"name":"deep","categories":["computer science","engineering"]}]},
	  "routes":[
	    {"name":"deep","when":{"domain":"deep"},"to":"smart"}
	  ]
	}`
	p, _ := mustLoad(t, domainOnly)

	d := Decide(Signals{RecentText: "help me understand this", ContextTokens: 100}, p, downClient(), NewMemSessionStore(), "")
	if d.Tier != "fast" {
		t.Fatalf("a domain route must NOT fire when the classifier is down; must fall to default(fast), got tier=%q reason=%q", d.Tier, d.Reason)
	}
}
