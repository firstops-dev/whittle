package router

import (
	"strings"
	"testing"
)

const requestedPolicy = `{
  "version":1,
  "tiers":[{"name":"fast","model":"claude-haiku-4-5"},{"name":"smart","model":"claude-opus-4-8"}],
  "default":"requested","inspect":{"scope":"full"},
  "routes":[{"name":"down","when":{"keywords":["trivial"]},"to":"fast"}]
}`

// default:"requested" — no route matched → keep the client's model, a guaranteed
// no-op. Every rewrite must come from an explicit rule (fail-open applied to
// routing itself).
func TestRequestedDefault_NoMatchIsNoOp(t *testing.T) {
	p, _ := mustLoad(t, requestedPolicy)
	d := Decide(Signals{RequestedModel: "claude-opus-4-8", RecentText: "hard novel work", SessionID: "s"},
		p, nil, NewMemSessionStore(), "")
	if !IsNoOp(d, Signals{RequestedModel: "claude-opus-4-8"}) {
		t.Fatalf("unmatched traffic must be a no-op, got tier=%q model=%q", d.Tier, d.Model)
	}
	if !strings.Contains(d.Reason, "default:requested") {
		t.Errorf("reason should say default:requested, got %q", d.Reason)
	}
}

// The critical mixed-model property: a cheap-model request (Claude Code
// background/title tasks) is NOT up-routed by the default — it stays cheap.
func TestRequestedDefault_NeverUpRoutesBackgroundTraffic(t *testing.T) {
	p, _ := mustLoad(t, requestedPolicy)
	sig := Signals{RequestedModel: "claude-haiku-4-5", RecentText: "novel work", SessionID: "s"}
	d := Decide(sig, p, nil, NewMemSessionStore(), "")
	if !IsNoOp(d, sig) {
		t.Fatalf("a haiku request with no matching rule must stay haiku, got model=%q", d.Model)
	}
}

// Explicit rules still fire in both directions.
func TestRequestedDefault_RulesStillRoute(t *testing.T) {
	p, _ := mustLoad(t, requestedPolicy)
	d := Decide(Signals{RequestedModel: "claude-opus-4-8", RecentText: "a trivial ask", SessionID: "s"},
		p, nil, NewMemSessionStore(), "")
	if d.Tier != "fast" {
		t.Fatalf("explicit rule must still down-route, got %q (%s)", d.Tier, d.Reason)
	}
}

// Validation: "requested" is reserved — legal as default, illegal as a tier name.
func TestRequestedDefault_Validation(t *testing.T) {
	if _, _, err := Load([]byte(`{"version":1,
	  "tiers":[{"name":"requested","model":"m"}],"default":"requested",
	  "inspect":{"scope":"full"},"routes":[]}`)); err == nil {
		t.Fatal("a tier named 'requested' must be rejected")
	}
}
