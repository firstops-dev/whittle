package router

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// GAP pass for the decision engine. Each test pins a real invariant with a WHY
// comment. Where a test FAILS it documents a bug in engine.go, not a wrong
// expectation — do not relax the want, fix the source.

// gapSignals defines a domain signal per label used by the cheap-first grid, so
// a `domain` leaf is a boolean over the fixed classifier label. (Non-MMLU
// category names only trigger a warning, which mustLoad tolerates.)
const gapSignals = `"signals":{"domains":[
  {"name":"dbg","categories":["debugging"]},
  {"name":"cod","categories":["coding"]},
  {"name":"cht","categories":["chat"]},
  {"name":"sx","categories":["x"]},
  {"name":"sy","categories":["y"]},
  {"name":"blk","categories":["blocked"]}
]},`

// loadWhenSignals wraps a `when` fragment in a policy that defines gapSignals, so
// its `domain` leaves resolve.
func loadWhenSignals(t *testing.T, when string) *Policy {
	t.Helper()
	js := `{"version":1,"tiers":[{"name":"a","model":"m"}],"default":"a","inspect":{"scope":"full"},` +
		gapSignals + `"routes":[{"name":"r","when":` + when + `,"to":"a"}]}`
	p, _ := mustLoad(t, js)
	return p
}

// ---------------------------------------------------------------------------
// 1. Cheap-first reordering must not change RESULTS, only cost.
//
// WHY: evalGroup evaluates non-ML children first, then ML children, and
// short-circuits. Boolean AND/OR are commutative, so the final truth value MUST
// equal a naive full-tree evaluation in author order. We cross-check the real
// evaluator against an INDEPENDENT reimplementation over many trees × signal
// states × classifier labels.
// ---------------------------------------------------------------------------

// fixedDomain is a deterministic classifier: Domain always returns `label`.
type fixedDomain struct{ label string }

func (f fixedDomain) Domain(string) (string, float64, map[string]float64, error) {
	return f.label, 1, nil, nil
}
func (f fixedDomain) EmbeddingScore(string, []string) (float64, error) {
	return 0, nil
}
func (f fixedDomain) ComplexityMargin(string, []string, []string) (float64, error) {
	return 0, nil
}

// naiveEval evaluates a rule tree fully, in author order, no cheap-first, no ML
// deferral. Independent of engine.go's evaluator on purpose.
func naiveEval(r *Rule, s *Signals, cl Classifier, pol *Policy) bool {
	switch {
	case r.All != nil:
		for i := range r.All {
			if !naiveEval(&r.All[i], s, cl, pol) {
				return false
			}
		}
		return true
	case r.Any != nil:
		for i := range r.Any {
			if naiveEval(&r.Any[i], s, cl, pol) {
				return true
			}
		}
		return false
	case r.Not != nil:
		return !naiveEval(r.Not, s, cl, pol)
	default:
		return naiveLeaf(r, s, cl, pol)
	}
}

func naiveLeaf(r *Rule, s *Signals, cl Classifier, pol *Policy) bool {
	switch {
	case r.ContextTokens != nil:
		return r.ContextTokens.match(s.ContextTokens)
	case r.MessageCount != nil:
		return r.MessageCount.match(s.MessageCount)
	case r.ToolLoop != nil:
		return *r.ToolLoop == s.ToolLoop
	case r.HasTools != nil:
		return *r.HasTools == s.HasTools
	case r.Keywords != nil:
		hay := strings.ToLower(s.RecentText)
		for _, kw := range r.Keywords {
			if kw != "" && containsWord(hay, strings.ToLower(kw)) {
				return true
			}
		}
		return false
	case r.KeywordsRegex != nil:
		for _, pat := range r.KeywordsRegex {
			if re, err := regexp.Compile(pat); err == nil && re.MatchString(s.RecentText) {
				return true
			}
		}
		return false
	case r.RequestedModel != nil:
		for _, w := range r.RequestedModel {
			if s.RequestedModel == canonicalModel(w) {
				return true
			}
		}
		return false
	case r.Domain != "":
		label, _, _, err := cl.Domain(s.RecentText)
		if err != nil {
			return false
		}
		sig := pol.domainSignal(r.Domain)
		if sig == nil {
			return false
		}
		for _, c := range sig.Categories {
			if c == label {
				return true
			}
		}
		return false
	}
	return false
}

func TestEval_CheapFirstMatchesNaive(t *testing.T) {
	trees := []string{
		`{"any":[{"keywords":["migrate"]},{"domain":"dbg"},{"context_tokens":{"gt":60000}}]}`,
		`{"all":[{"has_tools":true},{"not":{"domain":"cht"}}]}`,
		`{"any":[{"all":[{"tool_loop":true},{"domain":"cod"}]},{"keywords":["urgent"]}]}`,
		`{"not":{"any":[{"domain":"dbg"},{"message_count":{"gte":10}}]}}`,
		`{"all":[{"any":[{"keywords":["a"]},{"domain":"sx"}]},{"any":[{"context_tokens":{"lt":5}},{"domain":"sy"}]}]}`,
		`{"all":[{"not":{"domain":"blk"}},{"any":[{"keywords":["ship"]},{"context_tokens":{"gte":100}}]}]}`,
		`{"any":[{"not":{"all":[{"has_tools":true},{"domain":"cod"}]}},{"tool_loop":true}]}`,
	}
	// Load each tree into a policy that defines the domain signals.
	pols := make([]*Policy, len(trees))
	whens := make([]Rule, len(trees))
	for i, tj := range trees {
		pols[i] = loadWhenSignals(t, tj)
		whens[i] = pols[i].Routes[0].When
	}

	sigs := []Signals{
		{RecentText: "please migrate the schema", ContextTokens: 100, MessageCount: 3, HasTools: true, ToolLoop: false},
		{RecentText: "hi there", ContextTokens: 200000, MessageCount: 20, HasTools: false, ToolLoop: true},
		{RecentText: "urgent ship it now", ContextTokens: 3, MessageCount: 1, HasTools: true, ToolLoop: true},
		{RecentText: "a quiet message", ContextTokens: 100, MessageCount: 10, HasTools: false, ToolLoop: false},
		{RecentText: "", ContextTokens: 0, MessageCount: 0, HasTools: false, ToolLoop: false},
	}
	labels := []string{"", "debugging", "coding", "chat", "x", "y", "blocked"}

	for ti := range whens {
		for si := range sigs {
			for _, lbl := range labels {
				sig := sigs[si]
				cl := fixedDomain{label: lbl}
				st := &evalState{s: &sig, cl: cl, pol: pols[ti]}
				got := st.eval(&whens[ti])
				sigNaive := sigs[si]
				want := naiveEval(&whens[ti], &sigNaive, cl, pols[ti])
				if got != want {
					t.Errorf("tree[%d]=%s sig[%d] label=%q: cheap-first=%v naive=%v (reordering changed the result)",
						ti, trees[ti], si, lbl, got, want)
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// 2. Cheap-first is a COST optimization: an ML child must not run when a cheap
// sibling already settled the group.
// ---------------------------------------------------------------------------
func TestEval_LazyMLCostContract(t *testing.T) {
	cases := []struct {
		name       string
		when       string
		sig        Signals
		wantCalls  int
		wantResult bool
	}{
		{"any cheap-true skips ML", `{"any":[{"has_tools":true},{"domain":"sx"}]}`,
			Signals{HasTools: true}, 0, true},
		{"all cheap-false skips ML", `{"all":[{"has_tools":true},{"domain":"sx"}]}`,
			Signals{HasTools: false}, 0, false},
		{"any cheap-false must reach ML", `{"any":[{"has_tools":true},{"domain":"sx"}]}`,
			Signals{HasTools: false}, 1, true},
		{"all cheap-true must reach ML", `{"all":[{"has_tools":true},{"domain":"sx"}]}`,
			Signals{HasTools: true}, 1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := loadWhenSignals(t, tc.when)
			spy := &spyClassifier{domainLabel: "x"}
			sig := tc.sig
			st := &evalState{s: &sig, cl: spy, pol: p}
			got := st.eval(&p.Routes[0].When)
			if got != tc.wantResult {
				t.Errorf("result = %v, want %v", got, tc.wantResult)
			}
			if spy.domainCalls != tc.wantCalls {
				t.Errorf("domain calls = %d, want %d", spy.domainCalls, tc.wantCalls)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 3. Stickiness state machine — min_band_jump boundary.
//
// WHY (DESIGN R15): min_band_jump damps DOWNGRADES only. With classify gone, the
// static default is the sole fuzzy source, so we drive an arbitrary target tier
// by setting the policy's default and hold a different session current.
// ---------------------------------------------------------------------------

// policyDefault builds a 4-tier policy with no routes and the given default, so a
// session-current downgrade to `def` exercises the damping boundary.
func policyDefault(def string) string {
	return `{"version":1,"tiers":[
	    {"name":"t0","model":"m0"},{"name":"t1","model":"m1"},
	    {"name":"t2","model":"m2"},{"name":"t3","model":"m3"}
	  ],"default":"` + def + `","inspect":{"scope":"full"},"routes":[],
	  "session":{"sticky":true,"min_band_jump":2}}`
}

func TestDecide_MinBandJumpBoundary(t *testing.T) {
	cases := []struct {
		name     string
		cur      string
		target   string
		wantTier string
		wantKept bool
	}{
		{"jump==min switches (t3->t1)", "t3", "t1", "t1", false},
		{"jump<min holds (t3->t2)", "t3", "t2", "t3", true},
		{"jump<min holds (t2->t1)", "t2", "t1", "t2", true},
		{"jump>min switches (t3->t0)", "t3", "t0", "t0", false},
		{"upgrade free (t1->t3)", "t1", "t3", "t3", false},
		{"upgrade free small (t0->t1)", "t0", "t1", "t1", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sess := NewMemSessionStore()
			sess.Set("s", tc.cur)
			d := decideJSON(t, policyDefault(tc.target),
				Signals{RecentText: "x", SessionID: "s"},
				NoopClassifier(), sess, "")
			if d.Tier != tc.wantTier {
				t.Errorf("tier = %s, want %s (reason %q)", d.Tier, tc.wantTier, d.Reason)
			}
			if kept := strings.Contains(d.Reason, "sticky:kept"); kept != tc.wantKept {
				t.Errorf("sticky:kept = %v, want %v (reason %q)", kept, tc.wantKept, d.Reason)
			}
		})
	}
}

// WHY: a damped decision must store the tier ACTUALLY USED (the kept current),
// not the rejected target — otherwise the session "current" drifts a band per
// turn and damping erodes to nothing (DESIGN R15 "stored current = tier used").
func TestDecide_DampedDecisionDoesNotDrift(t *testing.T) {
	sess := NewMemSessionStore()
	sess.Set("s", "t3")
	for turn := 0; turn < 5; turn++ {
		d := decideJSON(t, policyDefault("t2"), // default t2: a 1-band downgrade from t3, always damped
			Signals{RecentText: "x", SessionID: "s"},
			NoopClassifier(), sess, "")
		if d.Tier != "t3" {
			t.Fatalf("turn %d: tier drifted to %s, want t3", turn, d.Tier)
		}
		if cur, _ := sess.Current("s"); cur != "t3" {
			t.Fatalf("turn %d: stored current drifted to %s, want t3", turn, cur)
		}
	}
}

// WHY (DESIGN R14): a session-less request ("" id) must skip stickiness entirely
// and never be bucketed under "". Two different session-less requests must not
// contaminate each other's damping via a shared "" key.
func TestDecide_SessionlessSkipsStickiness(t *testing.T) {
	sess := NewMemSessionStore()
	d := decideJSON(t, policyDefault("t0"), Signals{RecentText: "x", SessionID: ""},
		NoopClassifier(), sess, "")
	if d.Tier != "t0" {
		t.Fatalf("session-less should take the default t0, got %s", d.Tier)
	}
	if _, ok := sess.Current(""); ok {
		t.Errorf("session store bucketed a request under the empty session id")
	}
	// A subsequent session-less request to a lower default must NOT be damped
	// (there is no shared current to damp against).
	d2 := decideJSON(t, policyDefault("t3"), Signals{RecentText: "x", SessionID: ""},
		NoopClassifier(), sess, "")
	if strings.Contains(d2.Reason, "sticky:kept") {
		t.Errorf("session-less request was damped against a shared '' current: %q", d2.Reason)
	}
}

// WHY (DESIGN R16): pin is override-only — a pin decision must NOT write the
// session current, so it can't poison later stickiness/keep.
func TestDecide_PinDoesNotWriteSession(t *testing.T) {
	sess := NewMemSessionStore()
	d := decideJSON(t, fullPolicy,
		Signals{RequestedModel: "claude-sonnet-5", RecentText: "hello", SessionID: "s"},
		NoopClassifier(), sess, "fast")
	if !strings.HasPrefix(d.Reason, "pin") {
		t.Fatalf("expected pin decision, got %q", d.Reason)
	}
	if _, ok := sess.Current("s"); ok {
		t.Errorf("pin wrote session current; pin must be override-only (R16)")
	}
}

// WHY (DESIGN R16): a pin naming an unknown tier is ignored and routing falls
// through — never a hard failure on a bad header.
func TestDecide_UnknownPinFallsThrough(t *testing.T) {
	d := decideJSON(t, fullPolicy,
		Signals{RecentText: "please migrate the schema", ContextTokens: 100},
		NoopClassifier(), NewMemSessionStore(), "ghost-tier")
	if strings.HasPrefix(d.Reason, "pin") {
		t.Fatalf("unknown pin should be ignored, got %q", d.Reason)
	}
	if d.Tier != "smart" { // hard-work keyword route still fires
		t.Errorf("expected fall-through to hard-work route (smart), got %s", d.Tier)
	}
}

// WHY: `to: keep` holds the session current across turns and must NEVER write the
// store (DESIGN R-keep). A keep must not itself become the stored current.
func TestDecide_KeepNeverWritesSession(t *testing.T) {
	sess := NewMemSessionStore()
	sess.Set("s", "smart")
	for i := 0; i < 3; i++ {
		d := decideJSON(t, fullPolicy,
			Signals{ToolLoop: true, SessionID: "s", ContextTokens: 100},
			NoopClassifier(), sess, "")
		if d.Tier != "smart" {
			t.Fatalf("keep should hold smart, got %s", d.Tier)
		}
	}
	if cur, _ := sess.Current("s"); cur != "smart" {
		t.Errorf("keep altered stored current to %s, want smart", cur)
	}
}

// WHY (AC5): the reason string must distinguish every decision path so the
// x-whittle-reason header/log is diagnosable.
func TestDecide_ReasonDistinguishesPaths(t *testing.T) {
	sess := NewMemSessionStore()
	cases := []struct {
		name       string
		sig        Signals
		cl         Classifier
		pin        string
		wantPrefix string
	}{
		{"pin", Signals{RequestedModel: "claude-sonnet-5"}, NoopClassifier(), "fast", "pin:"},
		{"route", Signals{RecentText: "migrate now", ContextTokens: 100}, NoopClassifier(), "", "route:"},
		{"default", Signals{RecentText: "novel task"}, NoopClassifier(), "", "default"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := decideJSON(t, fullPolicy, tc.sig, tc.cl, sess, tc.pin)
			if !strings.Contains(d.Reason, tc.wantPrefix) {
				t.Errorf("reason %q does not contain %q", d.Reason, tc.wantPrefix)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 4. Concurrency: parallel Decide over a shared SessionStore + the global
// regexCache must be race-free (run with -race).
// ---------------------------------------------------------------------------

const policyRegex = `{
  "version":1,
  "tiers":[{"name":"fast","model":"m0"},{"name":"smart","model":"m1"}],
  "default":"fast",
  "inspect":{"scope":"full"},
  "routes":[
    {"name":"rx","when":{"keywords_regex":["(?i)race\\s+condition","deadlock"]},"to":"smart"}
  ],
  "session":{"sticky":true,"min_band_jump":1}
}`

func TestDecide_ConcurrentSharedState(t *testing.T) {
	p, _, err := Load([]byte(policyRegex))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	sess := NewMemSessionStore()
	var wg sync.WaitGroup
	for g := 0; g < 32; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			sid := fmt.Sprintf("sess-%d", g%4) // deliberate contention on 4 keys
			for i := 0; i < 200; i++ {
				text := "no match here"
				if i%2 == 0 {
					text = "there is a RACE condition and a deadlock"
				}
				d := Decide(Signals{RecentText: text, SessionID: sid}, p, NoopClassifier(), sess, "")
				if d.Tier != "fast" && d.Tier != "smart" {
					t.Errorf("unexpected tier %q", d.Tier)
				}
			}
		}(g)
	}
	wg.Wait()
}

// WHY: regexCache is a process-global sync.Map. Confirm a validated regex route
// actually matches through the cache.
func TestDecide_RegexRouteMatches(t *testing.T) {
	d := decideJSON(t, policyRegex,
		Signals{RecentText: "hit a RACE   condition today"},
		NoopClassifier(), nil, "")
	if d.Tier != "smart" {
		t.Errorf("regex route should match (case-insensitive, \\s+): got %s (%s)", d.Tier, d.Reason)
	}
}
