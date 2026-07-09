package router

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// ErrMLDisabled is returned by a Classifier when smart mode is off / models are
// absent. The engine treats it as "this ML signal is unavailable": the signal
// leaf evaluates false (the route simply won't fire on it), never a panic and
// never a silent misroute.
var ErrMLDisabled = errors.New("router: ML classifier disabled")

// Classifier is the opt-in ML surface — vSR's two models behind one interface.
// The core ships only noopClassifier; the router/ml subpackage implements it over
// the sidecar, so the engine never imports a model. The engine owns the policy
// thresholds/membership; the classifier only computes raw scores from text +
// candidates (compute where the data lives).
type Classifier interface {
	// Domain returns the intent classifier's argmax label + confidence AND the
	// full softmax distribution over its categories. The engine thresholds
	// probability MASS over a policy-defined category set (matchDomain); nil/empty
	// probs (older sidecar, degraded mode) falls back to argmax membership.
	Domain(text string) (label string, conf float64, probs map[string]float64, err error)
	// EmbeddingScore returns the bank score of text against candidates
	// (0.75·best + 0.25·mean(top-2) cosine).
	EmbeddingScore(text string, candidates []string) (score float64, err error)
	// ComplexityMargin returns score(hard) − score(easy): >0 leans hard, <0 easy.
	ComplexityMargin(text string, hard, easy []string) (margin float64, err error)
}

// noopClassifier is the smart-off implementation: every call reports disabled.
type noopClassifier struct{}

func (noopClassifier) Domain(string) (string, float64, map[string]float64, error) {
	return "", 0, nil, ErrMLDisabled
}
func (noopClassifier) EmbeddingScore(string, []string) (float64, error) {
	return 0, ErrMLDisabled
}
func (noopClassifier) ComplexityMargin(string, []string, []string) (float64, error) {
	return 0, ErrMLDisabled
}

// NoopClassifier returns a Classifier that is always disabled (heuristics-only).
func NoopClassifier() Classifier { return noopClassifier{} }

// Decision is the engine's output: the chosen tier, its concrete model id, and a
// reason string that names the exact path taken (for the observability log line
// and the x-whittle-reason header). Stripped is populated later by the adapter's
// reconciliation; the core leaves it nil.
type Decision struct {
	Tier     string
	Model    string
	Reason   string
	Stripped []string
}

// IsNoOp reports whether the decision routes to the model the client already
// requested (compared canonicalized). The proxy uses this to byte-passthrough
// the request unchanged — no model rewrite, no capability reconciliation (R11).
func IsNoOp(d Decision, s Signals) bool {
	return s.RequestedModel != "" && canonicalModel(d.Model) == s.RequestedModel
}

// decisionSource tracks how a tier was chosen, so stickiness damps only the
// fuzzy tail (the static default) and never overrides an explicit route or pin.
type decisionSource int

const (
	srcPin decisionSource = iota
	srcRoute
	srcDefault
)

// Decide runs the precedence ladder (docs/ROUTER.md §4):
//
//	pin → routes (first match, ML signals evaluated in-condition) → static default
//	then session stickiness damps a fuzzy (default) downgrade.
//
// It is pure over the already-extracted Signals and never panics; a classifier
// error makes the referencing signal leaf evaluate false (Mode-A safety is the
// caller's job for extraction/transport errors — this function only decides).
func Decide(s Signals, p *Policy, cl Classifier, sess SessionStore, pin string) Decision {
	if cl == nil {
		cl = noopClassifier{}
	}
	st := &evalState{s: &s, cl: cl, pol: p}

	// 1. Pin override — explicit, always wins, override-only (never writes session).
	if p.Overrides.PinHeader != "" && pin != "" {
		if p.tierRank(pin) >= 0 {
			return p.decide(pin, "pin:"+pin, srcPin, s, sess)
		}
		// Unknown tier in the pin header → ignore and fall through (never 400
		// the request on a bad header). Recorded as a reason suffix if we log.
	}

	// 2. Routes — ordered waterfall, first match wins. A route only fires on a
	// DEFINITIVE match: if evaluating its condition consulted a signal that
	// errored (mlErr), the match is not trustworthy (an unavailable signal must
	// never cause a route to fire, directly or via `not`) — skip it, fail open.
	for i := range p.Routes {
		r := &p.Routes[i]
		st.mlErr = false
		if st.eval(&r.When) && !st.mlErr {
			name := r.Name
			if name == "" {
				name = fmt.Sprintf("[%d]", i) // index fallback keeps reasons distinct
			}
			reason := "route:" + name
			if r.To == keepTier {
				return st.annotate(p.decideKeep(reason+"→keep", sess, s))
			}
			return st.annotate(p.decide(r.To, reason, srcRoute, s, sess))
		}
	}

	// 3. Static default. The ML now lives inside route conditions (signal leaves),
	// not a separate "smart default" step — matching vSR, where the default is
	// just the last rule.
	return st.annotate(p.decide(p.Default, "default", srcDefault, s, sess))
}

// decide resolves a tier name to a Decision, applies stickiness for fuzzy
// sources, and records the final tier in the session (except pin, which is
// override-only).
func (p *Policy) decide(tier, reason string, src decisionSource, s Signals, sess SessionStore) Decision {
	final := tier
	// Stickiness: damp a FUZZY downgrade only. Explicit routes and pins are the
	// user's intent and are never overridden; keep is handled separately. The
	// static default is the only fuzzy source now (ML lives in route conditions).
	if p.Session.Sticky && src == srcDefault && sess != nil {
		if cur, ok := sess.Current(s.SessionID); ok && cur != tier {
			curRank, newRank := p.tierRank(cur), p.tierRank(tier)
			// Downgrade = moving to a cheaper (lower-rank) tier. Damp it unless
			// the jump is at least min_band_jump. Upgrades are always free.
			if curRank >= 0 && newRank >= 0 && newRank < curRank &&
				(curRank-newRank) < p.Session.MinBandJump {
				final = cur
				reason += " sticky:kept"
			}
		}
	}
	if src != srcPin && sess != nil {
		sess.Set(s.SessionID, final)
	}
	return Decision{Tier: final, Model: p.tierModel(final), Reason: reason}
}

// decideKeep resolves `to: keep` — hold the session's current tier. On the first
// request of a session (no current tier) there is nothing to keep, so fall to
// the static default (design R-keep). keep never changes the stored tier.
func (p *Policy) decideKeep(reason string, sess SessionStore, s Signals) Decision {
	if sess != nil {
		if cur, ok := sess.Current(s.SessionID); ok {
			return Decision{Tier: cur, Model: p.tierModel(cur), Reason: reason}
		}
	}
	return Decision{Tier: p.Default, Model: p.tierModel(p.Default), Reason: reason + "(no-session→default)"}
}

// evalState carries per-request evaluation context: the request signals, the
// classifier, the policy (to resolve a leaf's signal name → its definition), and
// per-request memos of every ML signal. Each signal is computed at most once per
// request and only if a leaf referencing it is actually reached (lazy).
type evalState struct {
	s   *Signals
	cl  Classifier
	pol *Policy

	// domain classification: computed once (the classifier emits one label).
	domainLabel string
	domainProbs map[string]float64
	domainDone  bool
	domainErr   error
	// embedding/complexity results memoized by signal name.
	embed   map[string]mlResult
	complex map[string]mlResult

	// mlErr is set when an ML leaf actually consulted for the CURRENT route
	// errored (reset per route). A route whose match depended on an unavailable
	// signal must NOT fire — otherwise a `not` over a failed leaf inverts fail-open
	// (down sidecar → leaf false → not → true → route fires → misroute). anyMLErr
	// is the sticky per-request version, surfaced as an `ml-degraded` reason so a
	// degraded sidecar is observable, not silently indistinguishable from "no match".
	mlErr    bool
	anyMLErr bool

	loweredRecent string
	loweredOnce   bool
}

// mlFailed records an unavailable ML signal and reports the leaf as not-firing.
// The per-route skip (mlErr) applies to ANY unavailability so a `not` over an
// unresolved signal never fires a route. But smart mode simply being OFF (the
// noop classifier → ErrMLDisabled) is the expected heuristics-only mode, NOT a
// degradation — only a real sidecar failure marks the request ml-degraded.
func (st *evalState) mlFailed(err error) bool {
	st.mlErr = true
	if !errors.Is(err, ErrMLDisabled) {
		st.anyMLErr = true
	}
	return false
}

// annotate tags a decision as ml-degraded when any signal errored this request,
// so the reason/log distinguishes "sidecar down" from "signal legitimately didn't fire".
func (st *evalState) annotate(d Decision) Decision {
	if st.anyMLErr {
		d.Reason += " ml-degraded"
	}
	return d
}

// mlResult memoizes one signal computation (a score/margin and its error) so a
// signal referenced by several leaves in one request is computed only once.
type mlResult struct {
	val float64
	err error
}

// eval evaluates a condition tree to a boolean. Pure predicates + short-circuit
// + cheap-first child ordering keep the classifier off requests a cheap sibling
// already decided (docs/ROUTER.md §5).
func (st *evalState) eval(r *Rule) bool {
	switch {
	case r.All != nil:
		return st.evalGroup(r.All, false)
	case r.Any != nil:
		return st.evalGroup(r.Any, true)
	case r.Not != nil:
		return !st.eval(r.Not)
	default:
		return st.evalLeaf(r)
	}
}

// evalGroup evaluates children cheap-first (heuristic subtrees before any that
// require ML), short-circuiting. isAny=true is OR (short-circuit on first true);
// isAny=false is AND (short-circuit on first false).
func (st *evalState) evalGroup(children []Rule, isAny bool) bool {
	// Pass 1: cheap (no ML anywhere in the subtree).
	for i := range children {
		if ruleUsesML(&children[i]) {
			continue
		}
		if st.eval(&children[i]) == isAny {
			return isAny
		}
	}
	// Pass 2: ML-bearing children, only reached if pass 1 didn't decide.
	for i := range children {
		if !ruleUsesML(&children[i]) {
			continue
		}
		if st.eval(&children[i]) == isAny {
			return isAny
		}
	}
	return !isAny
}

func (st *evalState) evalLeaf(r *Rule) bool {
	switch {
	case r.ContextTokens != nil:
		return r.ContextTokens.match(st.s.ContextTokens)
	case r.MessageCount != nil:
		return r.MessageCount.match(st.s.MessageCount)
	case r.ToolLoop != nil:
		return *r.ToolLoop == st.s.ToolLoop
	case r.HasTools != nil:
		return *r.HasTools == st.s.HasTools
	case r.Keywords != nil:
		return st.matchLiteralKeywords(r.Keywords)
	case r.KeywordsRegex != nil:
		return st.matchRegexKeywords(r.KeywordsRegex)
	case r.RequestedModel != nil:
		return matchRequestedModel(st.s.RequestedModel, r.RequestedModel)
	case r.Domain != "":
		return st.matchDomain(r.Domain)
	case r.Embedding != "":
		return st.matchEmbedding(r.Embedding)
	case r.Complexity != "":
		return st.matchComplexity(r.Complexity)
	}
	return false // defensive: validated policy never reaches here
}

func (st *evalState) recentLower() string {
	if !st.loweredOnce {
		st.loweredRecent = strings.ToLower(st.s.RecentText)
		st.loweredOnce = true
	}
	return st.loweredRecent
}

// matchLiteralKeywords: case-insensitive WHOLE-WORD/PHRASE OR over the
// inspect-window text. Literal (not regex) — a coder's "c++" never explodes.
// Whole-word means the occurrence is not embedded inside a larger alphanumeric
// run: "migration" no longer matches "immigration", "refactor" no longer matches
// "refactored" (list the variants you want). A boundary is any non-alphanumeric
// rune or the string edge, so "c++" still matches in "use c++ here".
func (st *evalState) matchLiteralKeywords(kws []string) bool {
	hay := st.recentLower()
	for _, kw := range kws {
		if kw != "" && containsWord(hay, strings.ToLower(kw)) {
			return true
		}
	}
	return false
}

// containsWord reports whether needle occurs in hay with non-alphanumeric (or
// edge) boundaries on both sides. Both inputs must already be lowercased.
func containsWord(hay, needle string) bool {
	for from := 0; ; {
		i := strings.Index(hay[from:], needle)
		if i < 0 {
			return false
		}
		start := from + i
		end := start + len(needle)
		if (start == 0 || !isAlnum(hay[start-1])) && (end == len(hay) || !isAlnum(hay[end])) {
			return true
		}
		from = start + 1
	}
}

func isAlnum(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

func (st *evalState) matchRegexKeywords(pats []string) bool {
	for _, pat := range pats {
		if re := compiledRegex(pat); re != nil && re.MatchString(st.s.RecentText) {
			return true
		}
	}
	return false
}

// mlText is what the ML signals classify: the LATEST user turn, not the joined
// inspect window. Averaging turns dilutes the classifiers — measured live: a turn
// scoring complexity +0.171 (hard) alone fell to +0.025 (medium) once joined with
// two earlier trivial turns, silently suppressing mid-session escalation. Keywords
// deliberately KEEP the window (matchLiteralKeywords) — persistence is a feature
// there (a hard keyword two turns back keeps protecting); for a classifier it is
// noise. Falls back to the window text when the last turn has no user text.
func (st *evalState) mlText() string {
	if st.s.LastUserText != "" {
		return st.s.LastUserText
	}
	return st.s.RecentText
}

// matchDomain fires when the classifier's predicted label is in the named
// domain's category set. The classification is computed once per request; on
// ML-disabled/error the leaf is false (the route simply won't fire).
func (st *evalState) matchDomain(name string) bool {
	sig := st.pol.domainSignal(name)
	if sig == nil {
		return false // validation guarantees the reference resolves
	}
	if !st.domainDone {
		st.domainLabel, _, st.domainProbs, st.domainErr = st.cl.Domain(st.mlText())
		st.domainDone = true
	}
	if st.domainErr != nil {
		return st.mlFailed(st.domainErr)
	}
	// Mass thresholding (preferred): fire iff the total probability the classifier
	// assigns to the signal's categories clears MinMass. One scalar subsumes
	// entropy handling — mass concentrates only on a CONFIDENT in-set
	// classification; an ambiguous/flat distribution fails the threshold, so an
	// uncertain classification falls to the policy default (the safe middle tier),
	// never up. Invariant to which in-set category won (math 0.4 + physics 0.4
	// passes 0.7 without a special top-2 case).
	if sig.MinMass > 0 && len(st.domainProbs) > 0 {
		mass := 0.0
		for _, c := range sig.Categories {
			mass += st.domainProbs[c]
		}
		return mass >= sig.MinMass
	}
	// Argmax membership fallback: MinMass unset, or the sidecar didn't return a
	// distribution (older sidecar) — degrade gracefully rather than never firing.
	for _, c := range sig.Categories {
		if c == st.domainLabel {
			return true
		}
	}
	return false
}

// matchEmbedding fires when the named signal's bank score clears its threshold.
func (st *evalState) matchEmbedding(name string) bool {
	sig := st.pol.embeddingSignal(name)
	if sig == nil {
		return false
	}
	score, err := st.embedScore(name, sig.Candidates)
	if err != nil {
		return st.mlFailed(err)
	}
	return score >= sig.Threshold
}

// matchComplexity fires when the named signal's contrastive level equals the
// requested one (`signal:hard|easy|medium`).
func (st *evalState) matchComplexity(ref string) bool {
	name, level := splitComplexityRef(ref)
	sig := st.pol.complexitySignal(name)
	if sig == nil {
		return false
	}
	margin, err := st.complexMargin(name, sig.Hard, sig.Easy)
	if err != nil {
		return st.mlFailed(err)
	}
	return complexityLevel(margin, sig.Threshold) == level
}

// complexityLevel maps a contrastive margin to a level against the symmetric
// threshold — vSR's classifyComplexityDifficulty.
func complexityLevel(margin, threshold float64) string {
	switch {
	case margin > threshold:
		return "hard"
	case margin < -threshold:
		return "easy"
	default:
		return "medium"
	}
}

// embedScore returns the bank score for a signal, memoized once per request.
func (st *evalState) embedScore(name string, candidates []string) (float64, error) {
	if st.embed == nil {
		st.embed = map[string]mlResult{}
	}
	if r, ok := st.embed[name]; ok {
		return r.val, r.err
	}
	score, err := st.cl.EmbeddingScore(st.mlText(), candidates)
	st.embed[name] = mlResult{score, err}
	return score, err
}

// complexMargin returns the contrastive margin for a signal, memoized once per request.
func (st *evalState) complexMargin(name string, hard, easy []string) (float64, error) {
	if st.complex == nil {
		st.complex = map[string]mlResult{}
	}
	if r, ok := st.complex[name]; ok {
		return r.val, r.err
	}
	margin, err := st.cl.ComplexityMargin(st.mlText(), hard, easy)
	st.complex[name] = mlResult{margin, err}
	return margin, err
}

// matchRequestedModel compares canonicalized model ids on both sides so a dated
// snapshot id from Claude Code still matches a hand-pinned bare id.
func matchRequestedModel(got string, want []string) bool {
	for _, w := range want {
		if got == canonicalModel(w) {
			return true
		}
	}
	return false
}

// regexCache memoizes compiled keywords_regex patterns across requests. Validation
// already guaranteed each compiles, so a miss here is defensive (returns nil ⇒
// no match) rather than an error.
var regexCache sync.Map // string -> *regexp.Regexp

func compiledRegex(pat string) *regexp.Regexp {
	if v, ok := regexCache.Load(pat); ok {
		re, _ := v.(*regexp.Regexp)
		return re
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		regexCache.Store(pat, (*regexp.Regexp)(nil))
		return nil
	}
	regexCache.Store(pat, re)
	return re
}
