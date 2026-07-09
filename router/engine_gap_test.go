package router

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// This file is the testing-engineer GAP pass for the decision engine (T1.3).
// Each test pins a real invariant with a WHY comment. Where a test FAILS it is
// documenting a bug in engine.go, not a wrong expectation — do not relax the
// want, fix the source.

// ---------------------------------------------------------------------------
// 1. Cheap-first reordering must not change RESULTS, only cost.
//
// WHY: evalGroup evaluates non-ML children first, then ML children, and
// short-circuits. Boolean AND/OR are commutative, so the final truth value MUST
// equal a naive full-tree evaluation in author order. If reordering ever flips
// a result, routing silently misroutes. We cross-check the real evaluator
// against an INDEPENDENT reimplementation over many trees × signal states ×
// classifier labels.
// ---------------------------------------------------------------------------

// fixedIntent is a deterministic classifier: Intent always returns `label`.
type fixedIntent struct{ label string }

func (f fixedIntent) Intent(string) (string, float64, error) { return f.label, 1, nil }
func (f fixedIntent) Classify(string, map[string][]string) (string, float64, error) {
	return "", 0, nil
}

// naiveEval evaluates a rule tree fully, in author order, no cheap-first, no
// ML deferral. Independent of engine.go's evaluator on purpose.
func naiveEval(r *Rule, s *Signals, cl Classifier) bool {
	switch {
	case r.All != nil:
		for i := range r.All {
			if !naiveEval(&r.All[i], s, cl) {
				return false
			}
		}
		return true
	case r.Any != nil:
		for i := range r.Any {
			if naiveEval(&r.Any[i], s, cl) {
				return true
			}
		}
		return false
	case r.Not != nil:
		return !naiveEval(r.Not, s, cl)
	default:
		return naiveLeaf(r, s, cl)
	}
}

func naiveLeaf(r *Rule, s *Signals, cl Classifier) bool {
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
			if kw != "" && strings.Contains(hay, strings.ToLower(kw)) {
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
	case r.Intent != nil:
		label, _, err := cl.Intent(s.RecentText)
		if err != nil {
			return false
		}
		for _, w := range r.Intent {
			if w == label {
				return true
			}
		}
		return false
	}
	return false
}

func TestEval_CheapFirstMatchesNaive(t *testing.T) {
	trees := []string{
		`{"any":[{"keywords":["migrate"]},{"intent":["debugging"]},{"context_tokens":{"gt":60000}}]}`,
		`{"all":[{"has_tools":true},{"not":{"intent":["chat"]}}]}`,
		`{"any":[{"all":[{"tool_loop":true},{"intent":["coding"]}]},{"keywords":["urgent"]}]}`,
		`{"not":{"any":[{"intent":["debugging"]},{"message_count":{"gte":10}}]}}`,
		`{"all":[{"any":[{"keywords":["a"]},{"intent":["x"]}]},{"any":[{"context_tokens":{"lt":5}},{"intent":["y"]}]}]}`,
		`{"all":[{"not":{"intent":["blocked"]}},{"any":[{"keywords":["ship"]},{"context_tokens":{"gte":100}}]}]}`,
		`{"any":[{"not":{"all":[{"has_tools":true},{"intent":["coding"]}]}},{"tool_loop":true}]}`,
	}
	// Build the parsed *Rule for each tree once.
	var whens []Rule
	for _, tj := range trees {
		p := mustLoadRoute(t, tj)
		whens = append(whens, p.Routes[0].When)
	}

	// Signal state grid.
	sigs := []Signals{
		{RecentText: "please migrate the schema", ContextTokens: 100, MessageCount: 3, HasTools: true, ToolLoop: false},
		{RecentText: "hi there", ContextTokens: 200000, MessageCount: 20, HasTools: false, ToolLoop: true},
		{RecentText: "urgent ship it now", ContextTokens: 3, MessageCount: 1, HasTools: true, ToolLoop: true},
		{RecentText: "a quiet message", ContextTokens: 100, MessageCount: 10, HasTools: false, ToolLoop: false},
		{RecentText: "", ContextTokens: 0, MessageCount: 0, HasTools: false, ToolLoop: false},
	}
	labels := []string{"", "debugging", "coding", "chat", "x", "y", "blocked"}

	for ti, when := range whens {
		for si := range sigs {
			for _, lbl := range labels {
				sig := sigs[si]
				cl := fixedIntent{label: lbl}
				st := &evalState{s: &sig, cl: cl}
				got := st.eval(&when)
				sigNaive := sigs[si]
				want := naiveEval(&when, &sigNaive, cl)
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
//
// WHY: the design's load-bearing property (DESIGN §1, §2.3) is lazy-ML. If a
// cheap sibling short-circuits, the classifier must not be consulted, or every
// request pays for a model call and the whole point collapses.
// ---------------------------------------------------------------------------
func TestEval_LazyMLCostContract(t *testing.T) {
	cases := []struct {
		name       string
		when       string
		sig        Signals
		wantCalls  int
		wantResult bool
	}{
		{"any cheap-true skips ML", `{"any":[{"has_tools":true},{"intent":["x"]}]}`,
			Signals{HasTools: true}, 0, true},
		{"all cheap-false skips ML", `{"all":[{"has_tools":true},{"intent":["x"]}]}`,
			Signals{HasTools: false}, 0, false},
		{"any cheap-false must reach ML", `{"any":[{"has_tools":true},{"intent":["x"]}]}`,
			Signals{HasTools: false}, 1, true},
		{"all cheap-true must reach ML", `{"all":[{"has_tools":true},{"intent":["x"]}]}`,
			Signals{HasTools: true}, 1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := mustLoadRoute(t, tc.when)
			spy := &spyClassifier{intentLabel: "x"}
			sig := tc.sig
			st := &evalState{s: &sig, cl: spy}
			got := st.eval(&p.Routes[0].When)
			if got != tc.wantResult {
				t.Errorf("result = %v, want %v", got, tc.wantResult)
			}
			if spy.intentCalls != tc.wantCalls {
				t.Errorf("intent calls = %d, want %d", spy.intentCalls, tc.wantCalls)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 3. Stickiness state machine — min_band_jump boundary.
//
// WHY (DESIGN R15): min_band_jump damps DOWNGRADES only. jump < min_band_jump
// holds the current tier; jump == min_band_jump switches. An off-by-one here
// either pins sessions forever or defeats damping entirely.
// ---------------------------------------------------------------------------

// policy4 has four ordered tiers, no matching routes, and a classify block so a
// stubbed classifier can drive an arbitrary target tier through the fuzzy path.
const policy4 = `{
  "version":1,
  "tiers":[
    {"name":"t0","model":"m0"},{"name":"t1","model":"m1"},
    {"name":"t2","model":"m2"},{"name":"t3","model":"m3"}
  ],
  "default":"t0",
  "inspect":{"scope":"full"},
  "routes":[],
  "classify":{"strategy":"few_shot","min_confidence":0,"examples":{
    "t0":["a"],"t1":["b"],"t2":["c"],"t3":["d"]}},
  "session":{"sticky":true,"min_band_jump":2}
}`

// stubClassify drives Classify to a chosen tier; Intent is unused here.
type stubClassify struct {
	tier string
	conf float64
}

func (s stubClassify) Intent(string) (string, float64, error) { return "", 0, nil }
func (s stubClassify) Classify(string, map[string][]string) (string, float64, error) {
	return s.tier, s.conf, nil
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
			d := decideJSON(t, policy4,
				Signals{RecentText: "x", SessionID: "s"},
				stubClassify{tier: tc.target, conf: 1}, sess, "")
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
		d := decideJSON(t, policy4,
			Signals{RecentText: "x", SessionID: "s"},
			stubClassify{tier: "t2", conf: 1}, sess, "") // 1-band downgrade, always damped
		if d.Tier != "t3" {
			t.Fatalf("turn %d: tier drifted to %s, want t3", turn, d.Tier)
		}
		if cur, _ := sess.Current("s"); cur != "t3" {
			t.Fatalf("turn %d: stored current drifted to %s, want t3", turn, cur)
		}
	}
}

// WHY (DESIGN R14): a session-less request ("" id) must skip stickiness
// entirely and never be bucketed under "". Two different session-less requests
// must not contaminate each other's damping via a shared "" key.
func TestDecide_SessionlessSkipsStickiness(t *testing.T) {
	sess := NewMemSessionStore()
	// First: classify picks t0.
	d := decideJSON(t, policy4, Signals{RecentText: "x", SessionID: ""},
		stubClassify{tier: "t0", conf: 1}, sess, "")
	if d.Tier != "t0" {
		t.Fatalf("session-less should take classify target t0, got %s", d.Tier)
	}
	// Nothing may be stored under "".
	if _, ok := sess.Current(""); ok {
		t.Errorf("session store bucketed a request under the empty session id")
	}
	// A subsequent session-less downgrade must NOT be damped (no shared current).
	d2 := decideJSON(t, policy4, Signals{RecentText: "x", SessionID: ""},
		stubClassify{tier: "t3", conf: 1}, sess, "")
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

// WHY: `to: keep` holds the session current across turns and must NEVER write
// the store (DESIGN R-keep). A keep must not itself become the stored current.
func TestDecide_KeepNeverWritesSession(t *testing.T) {
	sess := NewMemSessionStore()
	sess.Set("s", "smart")
	// mid-tool-loop route → keep. Fire it several times.
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
		{"classify", Signals{RecentText: "novel task"}, stubClassify{tier: "smart", conf: 0.9}, "", "classify:smart@"},
		{"no-ml default", Signals{RecentText: "novel task"}, NoopClassifier(), "", "skipped:no-ml"},
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
//
// WHY: the proxy calls Decide from many goroutines. MemSessionStore and
// regexCache are shared mutable state; a data race here corrupts routing under
// load. This test is meaningful only under `go test -race`.
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
// actually matches through the cache (a nil-cache defensive miss would silently
// disable the route).
func TestDecide_RegexRouteMatches(t *testing.T) {
	d := decideJSON(t, policyRegex,
		Signals{RecentText: "hit a RACE   condition today"},
		NoopClassifier(), nil, "")
	if d.Tier != "smart" {
		t.Errorf("regex route should match (case-insensitive, \\s+): got %s (%s)", d.Tier, d.Reason)
	}
}
