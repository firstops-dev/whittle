package router

import (
	"strings"
	"testing"
)

// spyClassifier records how often each signal was computed, so we can assert
// cheap-first laziness (a model must NOT be called when a heuristic sibling
// already decided the node) and per-request memoization.
type spyClassifier struct {
	domainLabel   string
	domainProbs   map[string]float64
	embedScore    float64
	complexMargin float64
	domainCalls   int
	embedCalls    int
	complexCalls  int
}

func (c *spyClassifier) Domain(string) (string, float64, map[string]float64, error) {
	c.domainCalls++
	return c.domainLabel, 1, c.domainProbs, nil
}
func (c *spyClassifier) EmbeddingScore(string, []string) (float64, error) {
	c.embedCalls++
	return c.embedScore, nil
}
func (c *spyClassifier) ComplexityMargin(string, []string, []string) (float64, error) {
	c.complexCalls++
	return c.complexMargin, nil
}

func decideJSON(t *testing.T, policyJSON string, s Signals, cl Classifier, sess SessionStore, pin string) Decision {
	t.Helper()
	p, _, err := Load([]byte(policyJSON))
	if err != nil {
		t.Fatalf("policy load: %v", err)
	}
	return Decide(s, p, cl, sess, pin)
}

// AC2: precedence — pin beats routes beats classify beats default.
func TestDecide_Precedence(t *testing.T) {
	// A request that also matches the coding route; pin must still win.
	d := decideJSON(t, fullPolicy,
		Signals{RequestedModel: "claude-sonnet-5", RecentText: "migrate the db", ContextTokens: 100},
		NoopClassifier(), NewMemSessionStore(), "fast")
	if d.Tier != "fast" || !strings.HasPrefix(d.Reason, "pin") {
		t.Fatalf("pin should win: tier=%s reason=%s", d.Tier, d.Reason)
	}

	// No pin: the hard-work route (keyword "migrate") fires → smart.
	d2 := decideJSON(t, fullPolicy,
		Signals{RecentText: "migrate the db", ContextTokens: 100},
		NoopClassifier(), NewMemSessionStore(), "")
	if d2.Tier != "smart" || !strings.Contains(d2.Reason, "route:hard-work") {
		t.Fatalf("hard-work route should fire: tier=%s reason=%s", d2.Tier, d2.Reason)
	}
}

// AC3: with the noop classifier, a route's domain leaf never matches and no
// route fires → the static default, observably (reason "default").
func TestDecide_SmartOffFallsThrough(t *testing.T) {
	d := decideJSON(t, fullPolicy,
		Signals{RecentText: "hi there", ContextTokens: 100},
		NoopClassifier(), NewMemSessionStore(), "")
	if d.Tier != "main" || d.Reason != "default" {
		t.Fatalf("smart-off should fall to default: tier=%s reason=%s", d.Tier, d.Reason)
	}
}

// AC1: cheap-first laziness — an `any` whose cheap branch is TRUE must not call
// the domain classifier at all.
func TestDecide_CheapFirstShortCircuit(t *testing.T) {
	spy := &spyClassifier{domainLabel: "computer science"}
	// hard-work is: any[ keyword migrate/race, context>60k, domain deep-work ].
	// A keyword match should short-circuit BEFORE the domain leaf runs.
	d := decideJSON(t, fullPolicy,
		Signals{RecentText: "please migrate the schema", ContextTokens: 100},
		spy, NewMemSessionStore(), "")
	if d.Tier != "smart" {
		t.Fatalf("keyword should route to smart: %s", d.Tier)
	}
	if spy.domainCalls != 0 {
		t.Fatalf("domain classifier called %d times; cheap keyword should have short-circuited", spy.domainCalls)
	}
}

// AC1: when no cheap branch matches, the domain leaf IS consulted (and matches).
func TestDecide_DomainLeafReachedWhenNoCheapMatch(t *testing.T) {
	spy := &spyClassifier{domainLabel: "computer science"}
	d := decideJSON(t, fullPolicy,
		Signals{RecentText: "why is this flaky", ContextTokens: 100}, // no keyword, small ctx
		spy, NewMemSessionStore(), "")
	if spy.domainCalls != 1 {
		t.Fatalf("domain should be consulted exactly once, got %d", spy.domainCalls)
	}
	if d.Tier != "smart" {
		t.Fatalf("domain=computer science ∈ deep-work should route hard-work→smart, got %s", d.Tier)
	}
}

// A domain leaf fires only when the classifier's label is in the signal's set.
func TestDecide_DomainSignalMembership(t *testing.T) {
	const pol = `{"version":1,"tiers":[{"name":"fast","model":"m1"},{"name":"smart","model":"m2"}],
	  "default":"fast","inspect":{"scope":"full"},
	  "signals":{"domains":[{"name":"code","categories":["computer science","engineering"]}]},
	  "routes":[{"name":"code","when":{"domain":"code"},"to":"smart"}]}`
	if d := decideJSON(t, pol, Signals{RecentText: "x"}, &spyClassifier{domainLabel: "engineering"}, nil, ""); d.Tier != "smart" {
		t.Fatalf("label in category set should fire, got %s", d.Tier)
	}
	if d := decideJSON(t, pol, Signals{RecentText: "x"}, &spyClassifier{domainLabel: "history"}, nil, ""); d.Tier != "fast" {
		t.Fatalf("label outside the set must not fire, got %s", d.Tier)
	}
}

// An embedding leaf fires only when the bank score clears the threshold.
func TestDecide_EmbeddingSignalThreshold(t *testing.T) {
	const pol = `{"version":1,"tiers":[{"name":"fast","model":"m1"},{"name":"smart","model":"m2"}],
	  "default":"fast","inspect":{"scope":"full"},
	  "signals":{"embeddings":[{"name":"arch","threshold":0.66,"candidates":["design a system"]}]},
	  "routes":[{"name":"arch","when":{"embedding":"arch"},"to":"smart"}]}`
	if d := decideJSON(t, pol, Signals{RecentText: "x"}, &spyClassifier{embedScore: 0.70}, nil, ""); d.Tier != "smart" {
		t.Fatalf("score above threshold should route smart, got %s", d.Tier)
	}
	if d := decideJSON(t, pol, Signals{RecentText: "x"}, &spyClassifier{embedScore: 0.50}, nil, ""); d.Tier != "fast" {
		t.Fatalf("score below threshold must not fire, got %s", d.Tier)
	}
	// Exactly at threshold fires (>= is inclusive).
	if d := decideJSON(t, pol, Signals{RecentText: "x"}, &spyClassifier{embedScore: 0.66}, nil, ""); d.Tier != "smart" {
		t.Fatalf("score exactly at threshold should fire (>=), got %s", d.Tier)
	}
}

// A complexity leaf fires only for the requested level; the margin→level mapping
// is symmetric around ±threshold with a medium dead-band.
func TestDecide_ComplexitySignalLevel(t *testing.T) {
	const pol = `{"version":1,"tiers":[{"name":"fast","model":"m1"},{"name":"smart","model":"m2"}],
	  "default":"fast","inspect":{"scope":"full"},
	  "signals":{"complexity":[{"name":"reason","threshold":0.15,"hard":["h"],"easy":["e"]}]},
	  "routes":[{"name":"hard","when":{"complexity":"reason:hard"},"to":"smart"}]}`
	// margin > 0.15 → hard → fires.
	if d := decideJSON(t, pol, Signals{RecentText: "x"}, &spyClassifier{complexMargin: 0.30}, nil, ""); d.Tier != "smart" {
		t.Fatalf("margin>threshold is hard → should route smart, got %s", d.Tier)
	}
	// margin in the [-0.15, 0.15] dead-band → medium → :hard leaf does not fire.
	if d := decideJSON(t, pol, Signals{RecentText: "x"}, &spyClassifier{complexMargin: 0.05}, nil, ""); d.Tier != "fast" {
		t.Fatalf("medium margin must not match :hard, got %s", d.Tier)
	}
	// margin < -0.15 → easy → :hard leaf does not fire.
	if d := decideJSON(t, pol, Signals{RecentText: "x"}, &spyClassifier{complexMargin: -0.30}, nil, ""); d.Tier != "fast" {
		t.Fatalf("easy margin must not match :hard, got %s", d.Tier)
	}
}

// AC4: stickiness damps a fuzzy (default) downgrade within min_band_jump;
// upgrades are free. With classify gone, the static default is the fuzzy source:
// a session on smart whose request matches no route falls to default(main), a
// 1-band downgrade that min_band_jump=2 damps back to smart.
func TestDecide_StickinessDowngradeDamped(t *testing.T) {
	sess := NewMemSessionStore()
	sess.Set("s1", "smart") // session currently on smart (rank 2)
	d := decideJSON(t, fullPolicy,
		Signals{RecentText: "ok", ContextTokens: 100, SessionID: "s1"},
		NoopClassifier(), sess, "")
	if d.Tier != "smart" || !strings.Contains(d.Reason, "sticky:kept") {
		t.Fatalf("1-band default downgrade should be damped to smart: tier=%s reason=%s", d.Tier, d.Reason)
	}

	// Upgrade (fast→main default) is always free even under stickiness.
	sess.Set("s2", "fast")
	d2 := decideJSON(t, fullPolicy,
		Signals{RecentText: "ok", ContextTokens: 100, SessionID: "s2"},
		NoopClassifier(), sess, "")
	if d2.Tier != "main" {
		t.Fatalf("upgrade should not be damped: tier=%s", d2.Tier)
	}
}

// AC4: `to: keep` holds the session's current tier; first request → default.
func TestDecide_KeepTier(t *testing.T) {
	sess := NewMemSessionStore()
	sess.Set("s1", "smart")
	// tool_loop route → keep. Session current is smart → hold smart.
	d := decideJSON(t, fullPolicy,
		Signals{ToolLoop: true, SessionID: "s1", ContextTokens: 100},
		NoopClassifier(), sess, "")
	if d.Tier != "smart" || !strings.Contains(d.Reason, "keep") {
		t.Fatalf("keep should hold current tier smart: tier=%s reason=%s", d.Tier, d.Reason)
	}
	// No session → keep falls to default.
	d2 := decideJSON(t, fullPolicy,
		Signals{ToolLoop: true, SessionID: "fresh", ContextTokens: 100},
		NoopClassifier(), sess, "")
	if d2.Tier != "main" || !strings.Contains(d2.Reason, "no-session") {
		t.Fatalf("keep with no session should default: tier=%s reason=%s", d2.Tier, d2.Reason)
	}
}

// AC1: boolean operators — all/any/not evaluate correctly.
func TestEval_BooleanOperators(t *testing.T) {
	pol := `{"version":1,"tiers":[{"name":"a","model":"m1"},{"name":"b","model":"m2"}],
	  "default":"a","inspect":{"scope":"full"},"routes":[
	    {"name":"andnot","when":{"all":[
	        {"context_tokens":{"gt":1000}},
	        {"not":{"tool_loop":true}}
	    ]},"to":"b"}
	  ]}`
	// ctx>1000 AND not tool_loop → matches → b.
	d := decideJSON(t, pol, Signals{ContextTokens: 5000, ToolLoop: false}, NoopClassifier(), nil, "")
	if d.Tier != "b" {
		t.Fatalf("all[ctx>1000, not toolloop] should match: %s (%s)", d.Tier, d.Reason)
	}
	// tool_loop true → not() fails → all fails → default a.
	d2 := decideJSON(t, pol, Signals{ContextTokens: 5000, ToolLoop: true}, NoopClassifier(), nil, "")
	if d2.Tier != "a" {
		t.Fatalf("tool_loop should break the AND: %s", d2.Tier)
	}
}

// AC6: IsNoOp detects resolved==requested (canonicalized) for the passthrough short-circuit.
func TestIsNoOp(t *testing.T) {
	d := Decision{Model: "claude-sonnet-5-20260101"}
	if !IsNoOp(d, Signals{RequestedModel: "claude-sonnet-5"}) {
		t.Error("dated model id should be a no-op vs canonical requested")
	}
	if IsNoOp(d, Signals{RequestedModel: "claude-opus-4-8"}) {
		t.Error("different model must not be a no-op")
	}
	if IsNoOp(d, Signals{RequestedModel: ""}) {
		t.Error("empty requested model is never a no-op")
	}
}
