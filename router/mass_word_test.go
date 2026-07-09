package router

import "testing"

// ---- domain mass thresholding (the entropy-subsuming scalar) ----------------

func massPolicy(minMass float64) string {
	return `{
	  "version":1,
	  "tiers":[{"name":"main","model":"claude-sonnet-4-5"},{"name":"smart","model":"claude-opus-4-8"}],
	  "default":"main","inspect":{"scope":"full"},
	  "signals":{"domains":[{"name":"quant","categories":["math","physics","chemistry"],"min_mass":` +
		trimFloat(minMass) + `}]},
	  "routes":[{"name":"up","when":{"domain":"quant"},"to":"smart"}]
	}`
}

func trimFloat(f float64) string {
	if f == 0.7 {
		return "0.7"
	}
	return "0"
}

func decideWithProbs(t *testing.T, minMass float64, label string, probs map[string]float64) Decision {
	t.Helper()
	p, _ := mustLoad(t, massPolicy(minMass))
	cl := &spyClassifier{domainLabel: label, domainProbs: probs}
	return Decide(Signals{RecentText: "q", SessionID: "s"}, p, cl, nil, "")
}

// Confident in-set classification clears the threshold.
func TestDomainMass_ConfidentInSetFires(t *testing.T) {
	d := decideWithProbs(t, 0.7, "math", map[string]float64{"math": 0.95, "other": 0.05})
	if d.Tier != "smart" {
		t.Fatalf("mass 0.95 on quant must escalate, got %s (%s)", d.Tier, d.Reason)
	}
}

// Mass is invariant to WHICH in-set category won — split across two hard
// categories still clears (no top-2 special case needed).
func TestDomainMass_SplitAcrossInSetFires(t *testing.T) {
	d := decideWithProbs(t, 0.7, "math", map[string]float64{"math": 0.4, "physics": 0.4, "other": 0.2})
	if d.Tier != "smart" {
		t.Fatalf("math0.4+physics0.4=0.8 must clear 0.7, got %s (%s)", d.Tier, d.Reason)
	}
}

// An ambiguous distribution fails the threshold even when the ARGMAX is in-set:
// uncertainty falls to the default (cost-first), never up.
func TestDomainMass_AmbiguousArgmaxDoesNotFire(t *testing.T) {
	d := decideWithProbs(t, 0.7, "math", map[string]float64{"math": 0.4, "other": 0.35, "history": 0.25})
	if d.Tier != "main" {
		t.Fatalf("mass 0.4 < 0.7 must fall to default even though argmax is math, got %s (%s)", d.Tier, d.Reason)
	}
}

// min_mass unset → legacy argmax membership.
func TestDomainMass_UnsetFallsBackToArgmax(t *testing.T) {
	d := decideWithProbs(t, 0, "math", map[string]float64{"math": 0.4, "other": 0.35, "history": 0.25})
	if d.Tier != "smart" {
		t.Fatalf("min_mass unset should use argmax membership, got %s (%s)", d.Tier, d.Reason)
	}
}

// Sidecar without a distribution (probs nil) degrades to argmax membership even
// when min_mass is set — never silently disables the signal.
func TestDomainMass_NilProbsDegradesToArgmax(t *testing.T) {
	d := decideWithProbs(t, 0.7, "math", nil)
	if d.Tier != "smart" {
		t.Fatalf("nil probs should degrade to argmax membership, got %s (%s)", d.Tier, d.Reason)
	}
}

// ---- word-boundary keyword matching ------------------------------------------

func TestContainsWord(t *testing.T) {
	cases := []struct {
		hay, needle string
		want        bool
	}{
		{"we need a db migration now", "migration", true},
		{"my immigration paperwork", "migration", false}, // inside a word
		{"refactored the module", "refactor", false},     // suffix growth
		{"please refactor the module", "refactor", true},
		{"use c++ for this", "c++", true},                     // trailing non-alnum ok
		{"c++x is not a language", "c++", false},              // embedded
		{"a race condition appeared", "race condition", true}, // phrase
		{"racecondition", "race condition", false},
		{"migration", "migration", true},                         // exact / edges
		{"reformat this financial model", "reformat this", true}, // phrase-in-context still matches (inherent to keywords)
	}
	for _, c := range cases {
		if got := containsWord(c.hay, c.needle); got != c.want {
			t.Errorf("containsWord(%q, %q) = %v, want %v", c.hay, c.needle, got, c.want)
		}
	}
}
