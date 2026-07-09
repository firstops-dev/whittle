package router

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// ErrMLDisabled is returned by a Classifier when smart mode is off / models are
// absent. The engine treats it as "this ML signal is unavailable": intent leaves
// evaluate false, and classify falls through to the static default — surfaced in
// the Decision reason so it's observable, never silent.
var ErrMLDisabled = errors.New("router: ML classifier disabled")

// Classifier is the opt-in ML surface. The core ships only noopClassifier; the
// real ONNX implementation lands in the router/ml subpackage (later milestone)
// behind this same interface, so the engine never imports the models.
type Classifier interface {
	// Intent returns the classifier's category label for text and its confidence.
	Intent(text string) (label string, conf float64, err error)
	// Classify returns the best tier for text by few-shot nearest-example over
	// the per-tier examples, plus the match confidence (cosine).
	Classify(text string, examples map[string][]string) (tier string, conf float64, err error)
}

// noopClassifier is the smart-off implementation: every call reports disabled.
type noopClassifier struct{}

func (noopClassifier) Intent(string) (string, float64, error) {
	return "", 0, ErrMLDisabled
}
func (noopClassifier) Classify(string, map[string][]string) (string, float64, error) {
	return "", 0, ErrMLDisabled
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
// fuzzy tail (classify/default) and never overrides an explicit route or pin.
type decisionSource int

const (
	srcPin decisionSource = iota
	srcRoute
	srcClassify
	srcDefault
)

// Decide runs the precedence ladder (docs/ROUTER_POLICY_SCHEMA.md §0):
//
//	pin → routes (first match) → classify (smart default) → static default
//	then session stickiness damps a fuzzy downgrade.
//
// It is pure over the already-extracted Signals and never panics; a classifier
// error degrades to the static default (Mode-A safety is the caller's job for
// extraction/transport errors — this function only decides).
func Decide(s Signals, p *Policy, cl Classifier, sess SessionStore, pin string) Decision {
	if cl == nil {
		cl = noopClassifier{}
	}
	st := &evalState{s: &s, cl: cl}

	// 1. Pin override — explicit, always wins, override-only (never writes session).
	if p.Overrides.PinHeader != "" && pin != "" {
		if p.tierRank(pin) >= 0 {
			return p.decide(pin, "pin:"+pin, srcPin, s, sess)
		}
		// Unknown tier in the pin header → ignore and fall through (never 400
		// the request on a bad header). Recorded as a reason suffix if we log.
	}

	// 2. Routes — ordered waterfall, first match wins.
	for i := range p.Routes {
		r := &p.Routes[i]
		if st.eval(&r.When) {
			name := r.Name
			if name == "" {
				name = fmt.Sprintf("[%d]", i) // index fallback keeps reasons distinct
			}
			reason := "route:" + name
			if r.To == keepTier {
				return p.decideKeep(reason+"→keep", sess, s)
			}
			return p.decide(r.To, reason, srcRoute, s, sess)
		}
	}

	// 3. Classify — the intelligent default, only if smart mode is on and there
	// are examples to match against (validation rejects an empty block; the len
	// guard is defensive).
	if p.Classify != nil && len(p.Classify.Examples) > 0 {
		tier, conf, err := cl.Classify(s.RecentText, p.Classify.Examples)
		switch {
		case errors.Is(err, ErrMLDisabled):
			// smart off → fall to static default, observably.
			return p.decide(p.Default, "skipped:no-ml", srcDefault, s, sess)
		case err != nil:
			return p.decide(p.Default, "classify:error→default", srcDefault, s, sess)
		case tier != "" && conf >= p.Classify.MinConfidence && p.tierRank(tier) >= 0:
			return p.decide(tier, fmt.Sprintf("classify:%s@%.2f", tier, conf), srcClassify, s, sess)
		default:
			return p.decide(p.Default, fmt.Sprintf("classify:low-conf(%.2f)→default", conf), srcDefault, s, sess)
		}
	}

	// 4. Static default.
	return p.decide(p.Default, "default", srcDefault, s, sess)
}

// decide resolves a tier name to a Decision, applies stickiness for fuzzy
// sources, and records the final tier in the session (except pin, which is
// override-only).
func (p *Policy) decide(tier, reason string, src decisionSource, s Signals, sess SessionStore) Decision {
	final := tier
	// Stickiness: damp a FUZZY downgrade only. Explicit routes and pins are the
	// user's intent and are never overridden; keep is handled separately.
	if p.Session.Sticky && (src == srcClassify || src == srcDefault) && sess != nil {
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

// evalState carries per-request evaluation context: the signals, the classifier,
// and a one-shot memo of the intent classification (lazy — computed only if an
// intent leaf is actually reached, at most once per request).
type evalState struct {
	s             *Signals
	cl            Classifier
	intentDone    bool
	intentErr     error
	loweredRecent string
	loweredOnce   bool
}

// eval evaluates a condition tree to a boolean. Pure predicates + short-circuit
// + cheap-first child ordering keep the classifier off requests a cheap sibling
// already decided (docs ROUTER_DESIGN §2.3).
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
		if ruleUsesIntent(&children[i]) {
			continue
		}
		if st.eval(&children[i]) == isAny {
			return isAny
		}
	}
	// Pass 2: ML-bearing children, only reached if pass 1 didn't decide.
	for i := range children {
		if !ruleUsesIntent(&children[i]) {
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
	case r.Intent != nil:
		return st.matchIntent(r.Intent)
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

// matchLiteralKeywords: case-insensitive substring OR over the inspect-window
// text. Literal (not regex) — a coder's "c++" never explodes.
func (st *evalState) matchLiteralKeywords(kws []string) bool {
	hay := st.recentLower()
	for _, kw := range kws {
		if kw != "" && strings.Contains(hay, strings.ToLower(kw)) {
			return true
		}
	}
	return false
}

func (st *evalState) matchRegexKeywords(pats []string) bool {
	for _, pat := range pats {
		if re := compiledRegex(pat); re != nil && re.MatchString(st.s.RecentText) {
			return true
		}
	}
	return false
}

// matchIntent classifies lazily (once per request) and matches membership. On
// ML-disabled/error the intent leaf is false (the route simply won't fire).
func (st *evalState) matchIntent(want []string) bool {
	if !st.intentDone {
		label, conf, err := st.cl.Intent(st.s.RecentText)
		st.s.Intent, st.s.IntentConf, st.intentErr = label, conf, err
		st.intentDone = true
	}
	if st.intentErr != nil {
		return false
	}
	for _, w := range want {
		if w == st.s.Intent {
			return true
		}
	}
	return false
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
