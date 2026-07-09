package router

import (
	"fmt"
	"strings"
	"testing"
)

// Testing-engineer GAP pass for policy validation (T1.1). The product of the
// policy layer IS validation, so the dangerous failures are FALSE ACCEPTS
// (an invalid policy that loads) and MISSING boundary rejects. Each test pins a
// schema clause; a FAIL documents a validate.go bug.

// ---------------------------------------------------------------------------
// §5.8 — every invalid case must be rejected. Existing tests cover most; these
// close the two gaps: `not:[list]` and the two redundant-bound NumBand cases.
// ---------------------------------------------------------------------------

// WHY (§4.2, L1): `not` is unary; a list must be rejected. AC2 wants a specific,
// node-locating message. This documents whether the error actually helps.
func TestValidate_NotUnaryRejected(t *testing.T) {
	_, _, err := Load([]byte(wrapRoute(`{"not":[{"tool_loop":true},{"has_tools":true}]}`)))
	if err == nil {
		t.Fatal("not:[list] must be rejected (not is unary)")
	}
	// AC2 requires the error to locate the offending node. A bare stdlib
	// "cannot unmarshal array" that never says "not" fails that contract.
	if !strings.Contains(err.Error(), "not") {
		t.Errorf("error does not locate the `not` node: %q", err.Error())
	}
}

// WHY (§4.4): only one lower bound and one upper bound may be set. gt+gte and
// lt+lte are contradictory authoring mistakes and must be rejected.
func TestNumBand_RejectsRedundantBounds(t *testing.T) {
	routeErr(t, `{"context_tokens":{"gt":10,"gte":20}}`, "lower bound")
	routeErr(t, `{"context_tokens":{"lt":10,"lte":20}}`, "upper bound")
}

// WHY (§4.4): a bounds OBJECT with an unknown key must be rejected by the
// NumBand custom unmarshaler (it runs its own strict decoder). `grt` is the
// canonical typo from §5.8.
func TestNumBand_RejectsUnknownBoundKey(t *testing.T) {
	_, _, err := Load([]byte(wrapRoute(`{"context_tokens":{"grt":1}}`)))
	if err == nil {
		t.Fatal("unknown NumBand bound key {grt:1} must be rejected")
	}
}

// WHY (§4.4): a float scalar shorthand has no integer meaning for a count/token
// band and must error rather than silently truncate.
func TestNumBand_FloatScalarRejected(t *testing.T) {
	_, _, err := Load([]byte(wrapRoute(`{"message_count":1.9}`)))
	if err == nil {
		t.Fatal("float scalar {message_count:1.9} must be rejected, not truncated")
	}
}

// WHY (§4.1): strict-key rejection must apply at NESTED nodes, not only the top
// level — a typo'd leaf key deep in an `any` is the exact silent-misroute B1
// guards against.
func TestValidate_UnknownKeyAtNestedNode(t *testing.T) {
	_, _, err := Load([]byte(wrapRoute(`{"any":[{"tool_loop":true},{"bogus_leaf":["x"]}]}`)))
	if err == nil {
		t.Fatal("unknown key nested inside an `any` must be rejected")
	}
	if !strings.Contains(err.Error(), "bogus_leaf") {
		t.Errorf("error should name the unknown key, got %q", err.Error())
	}
}

// ---------------------------------------------------------------------------
// §5.1–5.7 — every valid scenario must load.
// ---------------------------------------------------------------------------

// WHY (AC1): the schema's canonical scenarios are the contract; each must parse.
func TestValidate_AllValidScenariosLoad(t *testing.T) {
	scenarios := map[string]string{
		"5.1 single leaf":     `{"context_tokens":{"gt":60000}}`,
		"5.2 explicit AND":    `{"all":[{"intent":["coding"]},{"context_tokens":{"gt":30000}}]}`,
		"5.3 OR":              `{"any":[{"keywords":["race condition","deadlock","root cause"]},{"context_tokens":{"gt":80000}}]}`,
		"5.4 nested":          `{"any":[{"requested_model":["claude-opus-4-8"]},{"all":[{"tool_loop":false},{"any":[{"keywords":["architecture","migrate"]},{"context_tokens":{"gt":100000}}]}]}]}`,
		"5.5 not+literal":     `{"all":[{"context_tokens":{"lt":4000}},{"not":{"keywords":["` + "```" + `","def ","func ","class "]}}]}`,
		"5.7 scalar equality": `{"message_count":1}`,
	}
	for name, when := range scenarios {
		t.Run(name, func(t *testing.T) {
			if _, _, err := Load([]byte(wrapRoute(when))); err != nil {
				t.Errorf("scenario %s should load, got: %v", name, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Depth cap — schema §4.7 says the hard cap is 6.
// ---------------------------------------------------------------------------

// nestedNots wraps a leaf fragment in n `not` layers (unary → no single-child
// group warning noise).
func nestedNots(n int, leaf string) string {
	s := leaf
	for i := 0; i < n; i++ {
		s = `{"not":` + s + `}`
	}
	return s
}

// WHY (§4.7): the depth cap must reject exactly one level past the limit and
// accept exactly at it. This pins the boundary so a refactor can't silently
// open unbounded recursion (the ReDoS/blowup guard) or reject legal policies.
func TestValidate_DepthCapBoundary(t *testing.T) {
	// leaf depth = 1 (when) + n nots. Cap is maxRuleDepth=6.
	for n := 0; n <= 8; n++ {
		when := nestedNots(n, `{"tool_loop":true}`)
		_, _, err := Load([]byte(wrapRoute(when)))
		leafDepth := 1 + n
		wantReject := leafDepth > 6
		if wantReject && err == nil {
			t.Errorf("n=%d (leaf depth %d) should be rejected by depth cap, loaded ok", n, leafDepth)
		}
		if !wantReject && err != nil {
			t.Errorf("n=%d (leaf depth %d) should load, got: %v", n, leafDepth, err)
		}
		if wantReject && err != nil && !strings.Contains(err.Error(), "nested") {
			t.Errorf("n=%d depth error should mention nesting, got %q", n, err.Error())
		}
	}
}

// ---------------------------------------------------------------------------
// classify example caps — §4.10: warn > 32, reject > 256.
// ---------------------------------------------------------------------------

func examplesJSON(tier string, count int) string {
	parts := make([]string, count)
	for i := range parts {
		parts[i] = fmt.Sprintf(`"ex-%d"`, i) // distinct → no dup warnings
	}
	return fmt.Sprintf(`{%q:[%s]}`, tier, strings.Join(parts, ","))
}

// WHY (§4.10): the caps are exact boundaries. 256 loads (with soft warn), 257
// rejects; 32 has no warn, 33 warns. An off-by-one lets a taxonomy blow past
// the cold-start/latency budget or nags on a legal set.
func TestClassify_ExampleCapBoundaries(t *testing.T) {
	mk := func(count int) string {
		return `{"version":1,"tiers":[{"name":"fast","model":"m"}],"default":"fast",
			"inspect":{"scope":"full"},"routes":[],
			"classify":{"strategy":"few_shot","min_confidence":0.5,"examples":` +
			examplesJSON("fast", count) + `}}`
	}
	cases := []struct {
		count      int
		wantReject bool
		wantWarn   bool
	}{
		{32, false, false},
		{33, false, true},
		{256, false, true},
		{257, true, false},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("count=%d", tc.count), func(t *testing.T) {
			_, warns, err := Load([]byte(mk(tc.count)))
			if tc.wantReject != (err != nil) {
				t.Errorf("count=%d: reject=%v, err=%v", tc.count, tc.wantReject, err)
			}
			if err == nil {
				gotWarn := containsSubstr(warns, "taxonomy smell")
				if gotWarn != tc.wantWarn {
					t.Errorf("count=%d: soft-cap warn=%v, want %v (warns=%v)", tc.count, gotWarn, tc.wantWarn, warns)
				}
			}
		})
	}
}

// WHY (§4.10): a duplicated example within a tier is a taxonomy smell → warn
// (not an error).
func TestClassify_DuplicateExampleWarns(t *testing.T) {
	js := `{"version":1,"tiers":[{"name":"fast","model":"m"}],"default":"fast",
		"inspect":{"scope":"full"},"routes":[],
		"classify":{"strategy":"few_shot","min_confidence":0.5,
		"examples":{"fast":["dup","dup"]}}}`
	_, warns, err := Load([]byte(js))
	if err != nil {
		t.Fatalf("duplicate example should load with a warning, got error: %v", err)
	}
	if !containsSubstr(warns, "duplicate example") {
		t.Errorf("expected duplicate-example warning, got %v", warns)
	}
}

// WHY (§4.8): an `intent` route anywhere in the waterfall triggers the ML cost
// lint — a routing policy that forgets this pays the model on every request.
func TestValidate_IntentCostLint(t *testing.T) {
	js := `{"version":1,"tiers":[{"name":"fast","model":"m"},{"name":"smart","model":"m2"}],
		"default":"fast","inspect":{"scope":"full"},
		"routes":[{"name":"top-intent","when":{"intent":["debugging"]},"to":"smart"}]}`
	_, warns, err := Load([]byte(js))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !containsSubstr(warns, "intent classifier") {
		t.Errorf("expected intent cost-lint warning, got %v", warns)
	}
}

// RESOLVED (was a deferred skip): schema §4.9 / M6 "helpful hint for a scalar
// where a LIST is expected" is now implemented via enrichDecodeError, so the
// error both rejects loudly AND tells the author to wrap the value in a list.
func TestValidate_ScalarWhereListExpected_Hint(t *testing.T) {
	for _, when := range []string{
		`{"keywords":"architecture"}`,
		`{"requested_model":"claude-opus-4-8"}`,
		`{"intent":"coding"}`,
	} {
		_, _, err := Load([]byte(wrapRoute(when)))
		if err == nil {
			t.Fatalf("%s must be rejected", when)
		}
		if !strings.Contains(err.Error(), "wrap the value in [ ]") {
			t.Errorf("%s: expected a 'wrap in [ ]' hint, got: %v", when, err)
		}
	}
	// `not` given a list also gets a targeted hint.
	_, _, err := Load([]byte(wrapRoute(`{"not":[{"tool_loop":true}]}`)))
	if err == nil || !strings.Contains(err.Error(), "`not` takes a single condition") {
		t.Errorf("not-with-list should hint it is unary, got: %v", err)
	}
}

// RESOLVED (was: per-route `sticky` is inert config). The Route.Sticky field
// was removed in the M1-hardening pass: in v1 explicit routes always win and are
// never damped (schema §0), so a per-route sticky override would be a
// load-accepted field that does nothing. With the field gone and strict
// decoding on, a stray `sticky:` on a route is now a LOUD rejection rather than
// a silent no-op — the honest behavior until per-route sticky returns with real
// semantics in the stickiness milestone. This test pins that rejection.
func TestValidate_PerRouteStickyIsRejected(t *testing.T) {
	js := `{"version":1,
	  "tiers":[{"name":"fast","model":"m0"},{"name":"smart","model":"m1"}],
	  "default":"fast","inspect":{"scope":"full"},
	  "session":{"sticky":true,"min_band_jump":2},
	  "routes":[{"name":"r","when":{"keywords":["go"]},"to":"fast","sticky":true}]}`
	if _, _, err := Load([]byte(js)); err == nil {
		t.Fatal("a route with `sticky:` must be rejected (field removed in v1), got clean load")
	} else if !strings.Contains(err.Error(), "sticky") {
		t.Fatalf("rejection should name the unknown `sticky` field, got: %v", err)
	}

	// Sanity: the same policy WITHOUT the stray field loads, and an explicit
	// route still wins over a huge min_band_jump (routes are never damped).
	ok := `{"version":1,
	  "tiers":[{"name":"fast","model":"m0"},{"name":"smart","model":"m1"}],
	  "default":"fast","inspect":{"scope":"full"},
	  "session":{"sticky":true,"min_band_jump":9},
	  "routes":[{"name":"r","when":{"keywords":["go"]},"to":"fast"}]}`
	sess := NewMemSessionStore()
	sess.Set("s", "smart")
	d := decideJSON(t, ok, Signals{RecentText: "go", SessionID: "s"}, NoopClassifier(), sess, "")
	if d.Tier != "fast" {
		t.Errorf("explicit route must win (never damped), got %s", d.Tier)
	}
}

// WHY: classify at EXACTLY min_confidence is accepted (>= is inclusive); an
// empty tier from the classifier falls to default, not a crash.
func TestDecide_ClassifyConfidenceBoundary(t *testing.T) {
	// min_confidence in policy4 is 0; use fullPolicy (0.55).
	atThreshold := decideJSON(t, fullPolicy,
		Signals{RecentText: "novel", SessionID: "s"},
		stubClassify{tier: "smart", conf: 0.55}, NewMemSessionStore(), "")
	if atThreshold.Tier != "smart" {
		t.Errorf("conf == min_confidence should be accepted (>=), got %s (%s)", atThreshold.Tier, atThreshold.Reason)
	}
	belowThreshold := decideJSON(t, fullPolicy,
		Signals{RecentText: "novel", SessionID: "s2"},
		stubClassify{tier: "smart", conf: 0.54}, NewMemSessionStore(), "")
	if belowThreshold.Tier != "main" || !strings.Contains(belowThreshold.Reason, "low-conf") {
		t.Errorf("conf < min_confidence should fall to default: %s (%s)", belowThreshold.Tier, belowThreshold.Reason)
	}
	emptyTier := decideJSON(t, fullPolicy,
		Signals{RecentText: "novel", SessionID: "s3"},
		stubClassify{tier: "", conf: 0.99}, NewMemSessionStore(), "")
	if emptyTier.Tier != "main" {
		t.Errorf("empty classifier tier should fall to default, got %s", emptyTier.Tier)
	}
}

// WHY (§4.6): `keep` is reserved and may not name a tier, but IS a legal route
// destination. Both halves must hold.
func TestValidate_KeepReservedButValidDestination(t *testing.T) {
	// keep as a route destination is valid.
	ok := `{"version":1,"tiers":[{"name":"fast","model":"m"}],"default":"fast",
		"inspect":{"scope":"full"},
		"routes":[{"name":"r","when":{"tool_loop":true},"to":"keep"}]}`
	if _, _, err := Load([]byte(ok)); err != nil {
		t.Errorf("keep should be a valid route destination: %v", err)
	}
	// keep as a tier name is rejected.
	bad := `{"version":1,"tiers":[{"name":"keep","model":"m"}],"default":"keep",
		"inspect":{"scope":"full"},"routes":[]}`
	if _, _, err := Load([]byte(bad)); err == nil {
		t.Error("keep as a tier name must be rejected")
	}
}
