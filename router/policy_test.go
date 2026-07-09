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

// A realistic full policy (schema §5.6), as JSON (v1 loader). Reused across tests.
const fullPolicy = `{
  "version": 1,
  "tiers": [
    {"name": "fast",  "model": "claude-haiku-4-5"},
    {"name": "main",  "model": "claude-sonnet-5"},
    {"name": "smart", "model": "claude-opus-4-8"}
  ],
  "default": "main",
  "inspect": {"scope": "recent_turns", "turns": 3},
  "routes": [
    {"name": "pinned-opus", "when": {"requested_model": ["claude-opus-4-8"]}, "to": "smart"},
    {"name": "background",  "when": {"requested_model": ["claude-haiku-4-5"]}, "to": "fast"},
    {"name": "mid-tool-loop", "when": {"tool_loop": true}, "to": "keep"},
    {"name": "hard-work", "when": {"any": [
        {"keywords": ["migrate", "race condition"]},
        {"context_tokens": {"gt": 60000}},
        {"intent": ["debugging", "architecture"]}
    ]}, "to": "smart"}
  ],
  "classify": {"strategy": "few_shot", "min_confidence": 0.55, "examples": {
    "fast":  ["add a docstring", "rename this variable"],
    "smart": ["design the migration", "why does this deadlock under load"]
  }},
  "session": {"sticky": true, "min_band_jump": 2},
  "overrides": {"pin_header": "x-whittle-route"}
}`

// AC1 + AC6 + AC7: the flagship policy loads, tiers are ordered, classify parses.
func TestLoad_FullPolicy(t *testing.T) {
	p, warns := mustLoad(t, fullPolicy)
	if p.tierRank("fast") != 0 || p.tierRank("smart") != 2 {
		t.Fatalf("tier ranks wrong: fast=%d smart=%d", p.tierRank("fast"), p.tierRank("smart"))
	}
	if p.tierModel("smart") != "claude-opus-4-8" {
		t.Fatalf("tierModel(smart)=%q", p.tierModel("smart"))
	}
	// hard-work references intent → expect a cost-lint warning.
	if !containsSubstr(warns, "intent classifier") {
		t.Fatalf("expected intent cost-lint warning, got %v", warns)
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

// AC7: classify strategy discriminator + example caps.
func TestClassify_StrategyAndCaps(t *testing.T) {
	loadErr(t, classifyPolicy(`"strategy":"weighted","min_confidence":0.5,"examples":{"fast":["x"]}`),
		"not supported in v1")
	loadErr(t, classifyPolicy(`"strategy":"few_shot","min_confidence":1.5,"examples":{"fast":["x"]}`),
		"min_confidence")
	loadErr(t, classifyPolicy(`"strategy":"few_shot","min_confidence":0.5,"examples":{"ghost":["x"]}`),
		`"ghost" is not a defined tier`)
	// Over the hard cap rejects.
	big := "[" + strings.TrimSuffix(strings.Repeat(`"e",`, 300), ",") + "]"
	loadErr(t, classifyPolicy(`"strategy":"few_shot","min_confidence":0.5,"examples":{"fast":`+big+`}`),
		"hard cap")
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

func classifyPolicy(classifyInner string) string {
	return `{"version":1,"tiers":[{"name":"fast","model":"m"}],"default":"fast",
		"inspect":{"scope":"full"},"routes":[],"classify":{` + classifyInner + `}}`
}
