package router

import (
	"strings"
	"testing"
)

// mustLoad fails the test if loading errors; returns policy + warnings.
func mustLoad(t *testing.T, js string) (*Policy, []string) {
	t.Helper()
	p, warns, err := Load([]byte(js))
	if err != nil {
		t.Fatalf("expected valid policy, got error: %v", err)
	}
	return p, warns
}

// loadErr asserts loading fails and the aggregated error mentions `want`.
func loadErr(t *testing.T, js, want string) {
	t.Helper()
	_, _, err := Load([]byte(js))
	if err == nil {
		t.Fatalf("expected error containing %q, got valid load", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not contain %q", err.Error(), want)
	}
}

// A realistic full policy (schema §5.6), as JSON (v1 loader). Reused across
// tests. Its hard-work route ends in a `domain` signal leaf so cheap-first
// laziness and the ML fail-open path are exercised through it.
const fullPolicy = `{
  "version": 1,
  "tiers": [
    {"name": "fast",  "model": "claude-haiku-4-5"},
    {"name": "main",  "model": "claude-sonnet-5"},
    {"name": "smart", "model": "claude-opus-4-8"}
  ],
  "default": "main",
  "inspect": {"scope": "recent_turns", "turns": 3},
  "signals": {
    "domains": [
      {"name": "deep-work", "categories": ["computer science", "engineering"]}
    ],
    "embeddings": [
      {"name": "architecture", "threshold": 0.66,
       "candidates": ["design a scalable architecture", "plan a migration strategy"]}
    ],
    "complexity": [
      {"name": "reasoning", "threshold": 0.15,
       "hard": ["debug this race condition", "analyze the root cause"],
       "easy": ["fix this typo", "rename this variable"]}
    ]
  },
  "routes": [
    {"name": "pinned-opus", "when": {"requested_model": ["claude-opus-4-8"]}, "to": "smart"},
    {"name": "background",  "when": {"requested_model": ["claude-haiku-4-5"]}, "to": "fast"},
    {"name": "mid-tool-loop", "when": {"tool_loop": true}, "to": "keep"},
    {"name": "hard-work", "when": {"any": [
        {"keywords": ["migrate", "race condition"]},
        {"context_tokens": {"gt": 60000}},
        {"domain": "deep-work"}
    ]}, "to": "smart"}
  ],
  "session": {"sticky": true, "min_band_jump": 2},
  "overrides": {"pin_header": "x-whittle-route"}
}`

// AC1 + AC6: the flagship policy loads, tiers are ordered, signals parse.
func TestLoad_FullPolicy(t *testing.T) {
	p, warns := mustLoad(t, fullPolicy)
	if p.tierRank("fast") != 0 || p.tierRank("smart") != 2 {
		t.Fatalf("tier ranks wrong: fast=%d smart=%d", p.tierRank("fast"), p.tierRank("smart"))
	}
	if p.tierModel("smart") != "claude-opus-4-8" {
		t.Fatalf("tierModel(smart)=%q", p.tierModel("smart"))
	}
	if p.domainSignal("deep-work") == nil || p.embeddingSignal("architecture") == nil || p.complexitySignal("reasoning") == nil {
		t.Fatal("signals did not parse into the catalog")
	}
	// hard-work references a domain signal → expect an ML cost-lint warning.
	if !containsSubstr(warns, "ML signal") {
		t.Fatalf("expected ML cost-lint warning, got %v", warns)
	}
}

// AC3: an unknown / typo'd key is a load error, never a silent drop.
func TestLoad_RejectsUnknownKey(t *testing.T) {
	loadErr(t, `{"version":1,"tiers":[{"name":"a","model":"m"}],"default":"a",
		"inspect":{"scope":"full"},"routes":[{"name":"r","when":{"keywrods":["x"]},"to":"a"}]}`,
		"keywrods")
}

// AC4: NumBand accepts a bare scalar (⇒ Eq) and a bounds object.
func TestNumBand_ScalarAndObject(t *testing.T) {
	p := mustLoadRoute(t, `{"message_count": 1}`)
	nb := p.Routes[0].When.MessageCount
	if nb == nil || nb.Eq == nil || *nb.Eq != 1 {
		t.Fatalf("scalar shorthand did not become Eq=1: %+v", nb)
	}
	p2 := mustLoadRoute(t, `{"context_tokens": {"gt": 60000}}`)
	if g := p2.Routes[0].When.ContextTokens.Gt; g == nil || *g != 60000 {
		t.Fatalf("object bound not parsed: %+v", p2.Routes[0].When.ContextTokens)
	}
}

// AC4: NumBand sanity — impossible range, empty band, eq+bound, quoted string.
func TestNumBand_Rejects(t *testing.T) {
	routeErr(t, `{"context_tokens": {"gt": 100, "lt": 50}}`, "impossible range")
	routeErr(t, `{"context_tokens": {}}`, "empty numeric predicate")
	routeErr(t, `{"context_tokens": {"eq": 5, "gt": 1}}`, "eq cannot combine")
	routeErr(t, `{"context_tokens": "60000"}`, "quoted string")
}

// AC5: one-shape-per-node, enforced recursively.
func TestRule_OneShapePerNode(t *testing.T) {
	routeErr(t, `{}`, "empty condition node")
	routeErr(t, `{"tool_loop": true, "has_tools": true}`, "no implicit AND")
	routeErr(t, `{"all": [{"tool_loop": true}], "context_tokens": {"gt": 1}}`, "group (all/any/not) OR a condition")
	routeErr(t, `{"all": [], "any": []}`, "only one of all/any/not")
	routeErr(t, `{"any": []}`, "empty group")
	// Recursive: a bad node nested inside a group is still caught.
	routeErr(t, `{"any": [{"tool_loop": true}, {}]}`, "empty condition node")
}

// AC5: single-element group warns (mis-indent smell) but is not an error.
func TestRule_SingleChildGroupWarns(t *testing.T) {
	p, warns := mustLoad(t, wrapRoute(`{"any": [{"tool_loop": true}]}`))
	_ = p
	if !containsSubstr(warns, "single-element group") {
		t.Fatalf("expected single-element-group warning, got %v", warns)
	}
}

// AC2/AC6: referential integrity — a route to an unknown tier, bad default, keep-as-tier.
func TestValidate_ReferentialIntegrity(t *testing.T) {
	routeErrTo(t, "nope", "not a defined tier")
	loadErr(t, `{"version":1,"tiers":[{"name":"a","model":"m"}],"default":"ghost","inspect":{"scope":"full"},"routes":[]}`,
		`"ghost" is not a defined tier`)
	loadErr(t, `{"version":1,"tiers":[{"name":"keep","model":"m"}],"default":"keep","inspect":{"scope":"full"},"routes":[]}`,
		"reserved keyword")
}

// Signal definitions + references: undefined-name errors, candidate caps,
// complexity level/threshold, and the non-MMLU-category warning.
func TestSignals_Validation(t *testing.T) {
	// A route referencing an undefined signal name is a load error.
	loadErr(t, signalsPolicy(
		`"embeddings":[{"name":"arch","threshold":0.5,"candidates":["x"]}]`,
		`{"embedding":"ghost"}`),
		"not a defined signals.embeddings entry")
	loadErr(t, signalsPolicy(
		`"domains":[{"name":"code","categories":["math"]}]`,
		`{"domain":"ghost"}`),
		"not a defined signals.domains entry")
	// Complexity leaf must carry a valid level.
	loadErr(t, signalsPolicy(
		`"complexity":[{"name":"r","threshold":0.1,"hard":["h"],"easy":["e"]}]`,
		`{"complexity":"r:spicy"}`),
		"level must be hard|easy|medium")
	// Negative complexity threshold is meaningless (a symmetric margin band).
	loadErr(t, signalsPolicy(
		`"complexity":[{"name":"r","threshold":-0.1,"hard":["h"],"easy":["e"]}]`,
		`{"complexity":"r:hard"}`),
		"threshold must be >= 0")
	// Candidate list over the hard cap rejects.
	big := "[" + strings.TrimSuffix(strings.Repeat(`"e",`, 300), ",") + "]"
	loadErr(t, signalsPolicy(
		`"embeddings":[{"name":"arch","threshold":0.5,"candidates":`+big+`}]`,
		`{"embedding":"arch"}`),
		"hard cap")
}

// A non-MMLU domain category WARNS (a swapped model could emit it) but loads.
func TestSignals_UnknownCategoryWarns(t *testing.T) {
	_, warns := mustLoad(t, signalsPolicy(
		`"domains":[{"name":"code","categories":["not-a-category"]}]`,
		`{"domain":"code"}`))
	if !containsSubstr(warns, "not a known MMLU-Pro category") {
		t.Fatalf("expected unknown-category warning, got %v", warns)
	}
}

// AC1 (canonicalization): dated + latest suffixes normalize equally.
func TestCanonicalModel(t *testing.T) {
	cases := map[string]string{
		"claude-opus-4-8-20260101": "claude-opus-4-8",
		"claude-opus-4-8":          "claude-opus-4-8",
		"claude-3-5-sonnet-latest": "claude-3-5-sonnet",
		"  claude-haiku-4-5  ":      "claude-haiku-4-5",
	}
	for in, want := range cases {
		if got := canonicalModel(in); got != want {
			t.Errorf("canonicalModel(%q) = %q, want %q", in, got, want)
		}
	}
}

// ---- helpers ----

func containsSubstr(ss []string, sub string) bool {
	for _, s := range ss {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// wrapRoute embeds a `when` fragment in a minimal valid policy.
func wrapRoute(when string) string {
	return `{"version":1,"tiers":[{"name":"a","model":"m"}],"default":"a",
		"inspect":{"scope":"full"},"routes":[{"name":"r","when":` + when + `,"to":"a"}]}`
}

func mustLoadRoute(t *testing.T, when string) *Policy {
	t.Helper()
	p, _ := mustLoad(t, wrapRoute(when))
	return p
}

func routeErr(t *testing.T, when, want string) {
	t.Helper()
	loadErr(t, wrapRoute(when), want)
}

func routeErrTo(t *testing.T, to, want string) {
	t.Helper()
	loadErr(t, `{"version":1,"tiers":[{"name":"a","model":"m"}],"default":"a",
		"inspect":{"scope":"full"},"routes":[{"name":"r","when":{"tool_loop":true},"to":"`+to+`"}]}`, want)
}

// signalsPolicy builds a policy with a signals block and one route whose `when`
// references a signal, so signal validation + reference integrity can be probed.
func signalsPolicy(signalsInner, when string) string {
	return `{"version":1,"tiers":[{"name":"fast","model":"m"},{"name":"smart","model":"m2"}],
		"default":"fast","inspect":{"scope":"full"},
		"signals":{` + signalsInner + `},
		"routes":[{"name":"r","when":` + when + `,"to":"smart"}]}`
}
