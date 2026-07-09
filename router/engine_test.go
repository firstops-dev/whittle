package router

import (
	"strings"
	"testing"
)

// spyClassifier records whether Intent/Classify were called, so we can assert
// cheap-first laziness (the classifier must NOT be called when a heuristic
// sibling already decided the node).
type spyClassifier struct {
	intentLabel  string
	intentConf   float64
	classifyTier string
	classifyConf float64
	intentCalls  int
	classCalls   int
}

func (c *spyClassifier) Intent(string) (string, float64, error) {
	c.intentCalls++
	return c.intentLabel, c.intentConf, nil
}
func (c *spyClassifier) Classify(string, map[string][]string) (string, float64, error) {
	c.classCalls++
	return c.classifyTier, c.classifyConf, nil
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

// AC3: with the noop classifier, an intent-referencing route never matches and
// classify falls through to default, observably (skipped:no-ml).
func TestDecide_SmartOffFallsThrough(t *testing.T) {
	// "trivial chat" matches no route (no keyword, small context, not opus) →
	// classify → but noop → default(main), reason skipped:no-ml.
	d := decideJSON(t, fullPolicy,
		Signals{RecentText: "hi there", ContextTokens: 100},
		NoopClassifier(), NewMemSessionStore(), "")
	if d.Tier != "main" || d.Reason != "skipped:no-ml" {
		t.Fatalf("smart-off should fall to default: tier=%s reason=%s", d.Tier, d.Reason)
	}
}

// AC1: cheap-first laziness — an `any` whose cheap branch is TRUE must not call
// the intent classifier at all.
func TestDecide_CheapFirstShortCircuit(t *testing.T) {
	spy := &spyClassifier{intentLabel: "debugging", classifyTier: "fast", classifyConf: 0.9}
	// hard-work is: any[ keyword migrate/race, context>60k, intent debugging/arch ].
	// A keyword match should short-circuit BEFORE the intent leaf runs.
	d := decideJSON(t, fullPolicy,
		Signals{RecentText: "please migrate the schema", ContextTokens: 100},
		spy, NewMemSessionStore(), "")
	if d.Tier != "smart" {
		t.Fatalf("keyword should route to smart: %s", d.Tier)
	}
	if spy.intentCalls != 0 {
		t.Fatalf("intent classifier called %d times; cheap keyword should have short-circuited", spy.intentCalls)
	}
}

// AC1: when no cheap branch matches, the intent leaf IS consulted (and matches).
func TestDecide_IntentLeafReachedWhenNoCheapMatch(t *testing.T) {
	spy := &spyClassifier{intentLabel: "debugging"}
	d := decideJSON(t, fullPolicy,
		Signals{RecentText: "why is this flaky", ContextTokens: 100}, // no keyword, small ctx
		spy, NewMemSessionStore(), "")
	if spy.intentCalls != 1 {
		t.Fatalf("intent should be consulted exactly once, got %d", spy.intentCalls)
	}
	if d.Tier != "smart" {
		t.Fatalf("intent=debugging should route hard-work→smart, got %s", d.Tier)
	}
}

// AC4: classify result is honored above threshold; stickiness is not triggered
// on a first request.
func TestDecide_ClassifyAboveThreshold(t *testing.T) {
	spy := &spyClassifier{classifyTier: "smart", classifyConf: 0.80}
	d := decideJSON(t, fullPolicy,
		Signals{RecentText: "design a distributed rate limiter", ContextTokens: 100, SessionID: "s1"},
		spy, NewMemSessionStore(), "")
	if d.Tier != "smart" || !strings.Contains(d.Reason, "classify:smart@0.80") {
		t.Fatalf("classify should pick smart: tier=%s reason=%s", d.Tier, d.Reason)
	}
}

// AC4: stickiness damps a fuzzy downgrade within min_band_jump; upgrades free.
func TestDecide_StickinessDowngradeDamped(t *testing.T) {
	sess := NewMemSessionStore()
	sess.Set("s1", "smart") // session currently on smart (rank 2)
	// classify wants "main" (rank 1): a 1-band downgrade, min_band_jump=2 → damped.
	spy := &spyClassifier{classifyTier: "main", classifyConf: 0.9}
	d := decideJSON(t, fullPolicy,
		Signals{RecentText: "ok", ContextTokens: 100, SessionID: "s1"},
		spy, sess, "")
	if d.Tier != "smart" || !strings.Contains(d.Reason, "sticky:kept") {
		t.Fatalf("1-band downgrade should be damped to smart: tier=%s reason=%s", d.Tier, d.Reason)
	}

	// Upgrade (main→smart) is always free even under stickiness.
	sess.Set("s2", "fast")
	spyUp := &spyClassifier{classifyTier: "smart", classifyConf: 0.9}
	d2 := decideJSON(t, fullPolicy,
		Signals{RecentText: "ok", ContextTokens: 100, SessionID: "s2"},
		spyUp, sess, "")
	if d2.Tier != "smart" {
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
