package router

import (
	"fmt"
	"regexp"
	"strings"
)

// validate checks a decoded Policy against the schema rules
// (docs/ROUTER.md §4). It returns non-fatal warnings and fatal
// errors separately; Load turns any error into a failed load. Every problem
// names its location so a hand-author can find it.
//
// Validation is the product of the policy layer: strict decoding already
// rejected unknown keys, so here we enforce structural and referential
// invariants the type system cannot.
func (p *Policy) validate() (warnings []string, errs []error) {
	v := &validator{p: p}

	if p.Version != 1 {
		v.errf("version: must be 1 (got %d)", p.Version)
	}
	v.validateTiers()
	v.validateDefault()
	v.validateInspect()
	v.validateSignals()
	v.validateRoutes()
	v.validateSession()

	return v.warns, v.errs
}

type validator struct {
	p     *Policy
	errs  []error
	warns []string
}

func (v *validator) errf(format string, a ...any) { v.errs = append(v.errs, fmt.Errorf(format, a...)) }
func (v *validator) warnf(format string, a ...any) {
	v.warns = append(v.warns, fmt.Sprintf(format, a...))
}

func (v *validator) validateTiers() {
	if len(v.p.Tiers) == 0 {
		v.errf("tiers: at least one tier is required")
		return
	}
	seen := map[string]bool{}
	for i, t := range v.p.Tiers {
		switch {
		case t.Name == "":
			v.errf("tiers[%d]: missing name", i)
		case t.Name == keepTier:
			v.errf("tiers[%d]: %q is a reserved keyword and cannot be a tier name", i, keepTier)
		case seen[t.Name]:
			v.errf("tiers[%d]: duplicate tier name %q", i, t.Name)
		}
		if t.Model == "" {
			v.errf("tiers[%d] (%s): missing model", i, t.Name)
		}
		seen[t.Name] = true
	}
}

func (v *validator) validateDefault() {
	if v.p.Default == "" {
		v.errf("default: required (the terminal tier)")
		return
	}
	if v.p.Default == keepTier {
		v.errf("default: cannot be %q — the terminal fallback must be a real tier", keepTier)
		return
	}
	if v.p.tierRank(v.p.Default) < 0 {
		v.errf("default: %q is not a defined tier", v.p.Default)
	}
}

func (v *validator) validateInspect() {
	switch v.p.Inspect.Scope {
	case "last_user_turn", "full":
	case "recent_turns":
		if v.p.Inspect.Turns <= 0 {
			v.errf("inspect: scope=recent_turns requires turns > 0")
		}
	case "":
		v.errf("inspect.scope: required (last_user_turn | recent_turns | full)")
	default:
		v.errf("inspect.scope: %q is not valid (last_user_turn | recent_turns | full)", v.p.Inspect.Scope)
	}
}

func (v *validator) validateRoutes() {
	names := map[string]bool{}
	for i, r := range v.p.Routes {
		loc := fmt.Sprintf("routes[%d]", i)
		if r.Name == "" {
			// A name is how a fired route is identified in the log/reason
			// (schema §6 / AC5). Without one, two routes are indistinguishable;
			// the engine falls back to the index, but warn so authors name them.
			v.warnf("%s: route has no name — it will be logged by index; add a name for a readable reason", loc)
		} else {
			loc = fmt.Sprintf("routes[%d] (%s)", i, r.Name)
			if names[r.Name] {
				v.errf("%s: duplicate route name", loc)
			}
			names[r.Name] = true
		}
		// Destination must be a real tier or the reserved keep keyword.
		if r.To != keepTier && v.p.tierRank(r.To) < 0 {
			v.errf("%s: to=%q is not a defined tier (nor %q)", loc, r.To, keepTier)
		}
		v.validateRule(&r.When, loc+".when", 1)

		// Cost lint (schema §4.8 / design C3): a route with an ML signal leaf
		// (domain/embedding/complexity) pays for a model on every request reaching it.
		if ruleUsesML(&r.When) {
			v.warnf("%s: references an ML signal (domain/embedding/complexity) — it runs a model on requests reaching this route; place cheap routes above it", loc)
		}
	}
}

// validateRule enforces one-shape-per-node recursively, plus leaf-level checks.
func (v *validator) validateRule(r *Rule, loc string, depth int) {
	if depth > maxRuleDepth {
		v.errf("%s: condition nested deeper than %d levels — flatten it", loc, maxRuleDepth)
		return
	}
	combos := r.combinatorCount()
	ls := r.leaves()

	switch {
	case combos == 0 && len(ls) == 0:
		v.errf("%s: empty condition node (needs one predicate, or all/any/not)", loc)
		return
	case combos > 1:
		v.errf("%s: a node may set only one of all/any/not", loc)
		return
	case combos == 1 && len(ls) > 0:
		v.errf("%s: a node is a group (all/any/not) OR a condition, not both", loc)
		return
	case len(ls) > 1:
		v.errf("%s: multiple predicates in one node — there is no implicit AND, wrap them in `all`", loc)
		return
	}

	switch {
	case r.All != nil:
		v.validateGroup(r.All, loc, "all", depth)
	case r.Any != nil:
		v.validateGroup(r.Any, loc, "any", depth)
	case r.Not != nil:
		v.validateRule(r.Not, loc+".not", depth+1)
	default:
		v.validateLeaf(r, ls[0], loc)
	}
}

func (v *validator) validateGroup(items []Rule, loc, op string, depth int) {
	if len(items) == 0 {
		v.errf("%s.%s: empty group (an empty %s is %s)", loc, op, op,
			map[string]string{"all": "vacuously true", "any": "always false"}[op])
		return
	}
	if len(items) == 1 {
		// Almost always a mis-indent that folded siblings into one node.
		v.warnf("%s.%s: single-element group is usually a mis-indentation", loc, op)
	}
	for i := range items {
		v.validateRule(&items[i], fmt.Sprintf("%s.%s[%d]", loc, op, i), depth+1)
	}
}

func (v *validator) validateLeaf(r *Rule, kind leafKind, loc string) {
	switch kind {
	case contextTokensLeaf:
		v.validateBand(r.ContextTokens, loc+".context_tokens")
	case messageCountLeaf:
		v.validateBand(r.MessageCount, loc+".message_count")
	case keywordsLeaf:
		if len(r.Keywords) == 0 {
			v.errf("%s.keywords: empty list", loc)
		}
	case keywordsRegexLeaf:
		if len(r.KeywordsRegex) == 0 {
			v.errf("%s.keywords_regex: empty list", loc)
		}
		for _, pat := range r.KeywordsRegex {
			// Go's regexp is RE2: linear-time, no catastrophic backtracking, so
			// there is no time-complexity ReDoS here. We still bound length to
			// avoid pathological compiled-program memory.
			if len(pat) > 512 {
				v.errf("%s.keywords_regex: pattern too long (%d > 512 chars)", loc, len(pat))
				continue
			}
			if _, err := regexp.Compile(pat); err != nil {
				v.errf("%s.keywords_regex: invalid pattern %q: %v", loc, pat, err)
			}
		}
	case requestedModelLeaf:
		if len(r.RequestedModel) == 0 {
			v.errf("%s.requested_model: empty list", loc)
		}
	case domainLeaf:
		if v.p.domainSignal(r.Domain) == nil {
			v.errf("%s.domain: %q is not a defined signals.domains entry", loc, r.Domain)
		}
	case embeddingLeaf:
		if v.p.embeddingSignal(r.Embedding) == nil {
			v.errf("%s.embedding: %q is not a defined signals.embeddings entry", loc, r.Embedding)
		}
	case complexityLeaf:
		name, level := splitComplexityRef(r.Complexity)
		switch {
		case v.p.complexitySignal(name) == nil:
			v.errf("%s.complexity: %q is not a defined signals.complexity entry (use `name:hard|easy|medium`)", loc, name)
		case !complexityLevels[level]:
			v.errf("%s.complexity: level must be hard|easy|medium (got %q); use `name:level`", loc, level)
		}
	}
}

// validateBand enforces NumBand sanity (schema §4.4): at least one bound; Eq
// exclusive; no redundant lower/upper pair; non-empty range.
func (v *validator) validateBand(n *NumBand, loc string) {
	if n.Eq == nil && n.Gt == nil && n.Gte == nil && n.Lt == nil && n.Lte == nil {
		v.errf("%s: empty numeric predicate (set eq, or gt/gte/lt/lte)", loc)
		return
	}
	// The signals that use NumBand (context tokens, message count) are never
	// negative, so a negative bound is almost certainly an author error and a
	// silent no-op predicate.
	for _, b := range []*int{n.Eq, n.Gt, n.Gte, n.Lt, n.Lte} {
		if b != nil && *b < 0 {
			v.warnf("%s: negative bound %d — token/message counts are never negative, so this predicate is inert", loc, *b)
		}
	}
	if n.Eq != nil && (n.Gt != nil || n.Gte != nil || n.Lt != nil || n.Lte != nil) {
		v.errf("%s: eq cannot combine with gt/gte/lt/lte", loc)
		return
	}
	if n.Gt != nil && n.Gte != nil {
		v.errf("%s: set only one lower bound (gt or gte)", loc)
	}
	if n.Lt != nil && n.Lte != nil {
		v.errf("%s: set only one upper bound (lt or lte)", loc)
	}
	// Reject an impossible range (e.g. {gt:100, lt:50}).
	lo, hasLo := bandLowerInclusive(n)
	hi, hasHi := bandUpperInclusive(n)
	if hasLo && hasHi && lo > hi {
		v.errf("%s: impossible range (lower %d > upper %d)", loc, lo, hi)
	}
}

// bandLowerInclusive returns the smallest integer the band admits, if bounded below.
func bandLowerInclusive(n *NumBand) (int, bool) {
	switch {
	case n.Gte != nil:
		return *n.Gte, true
	case n.Gt != nil:
		return *n.Gt + 1, true
	}
	return 0, false
}

// bandUpperInclusive returns the largest integer the band admits, if bounded above.
func bandUpperInclusive(n *NumBand) (int, bool) {
	switch {
	case n.Lte != nil:
		return *n.Lte, true
	case n.Lt != nil:
		return *n.Lt - 1, true
	}
	return 0, false
}

// validateSignals checks the named ML signal definitions: names present +
// unique per kind, category/candidate lists non-empty and capped, thresholds
// sane. Route leaves referencing these by name are checked in validateLeaf.
func (v *validator) validateSignals() {
	if v.p.Signals == nil {
		return
	}
	s := v.p.Signals

	domNames := map[string]bool{}
	for i := range s.Domains {
		d := &s.Domains[i]
		loc := fmt.Sprintf("signals.domains[%d]", i)
		v.checkSignalName(d.Name, loc, domNames)
		if len(d.Categories) == 0 {
			v.errf("%s (%s): no categories", loc, d.Name)
		}
		if d.MinMass < 0 || d.MinMass > 1 {
			v.errf("%s (%s): min_mass must be in [0,1] (got %g)", loc, d.Name, d.MinMass)
		}
		for _, c := range d.Categories {
			if !mmluCategories[c] {
				v.warnf("%s (%s): %q is not a known MMLU-Pro category — the classifier will never emit it, so this is inert", loc, d.Name, c)
			}
		}
	}

	embNames := map[string]bool{}
	for i := range s.Embeddings {
		e := &s.Embeddings[i]
		loc := fmt.Sprintf("signals.embeddings[%d]", i)
		v.checkSignalName(e.Name, loc, embNames)
		v.checkCandidates(e.Candidates, loc)
		if e.Threshold < 0 || e.Threshold > 1 {
			v.warnf("%s (%s): threshold %g is outside the usual cosine range [0,1]", loc, e.Name, e.Threshold)
		}
	}

	cxNames := map[string]bool{}
	for i := range s.Complexity {
		c := &s.Complexity[i]
		loc := fmt.Sprintf("signals.complexity[%d]", i)
		v.checkSignalName(c.Name, loc, cxNames)
		// A colon in a complexity name is unreferenceable: a `name:level` leaf
		// splits on the LAST colon, so `foo:bar` would parse as name `foo`, level `bar`.
		if strings.Contains(c.Name, ":") {
			v.errf("%s: complexity signal name %q must not contain ':' (the leaf ref is `name:level`)", loc, c.Name)
		}
		v.checkCandidates(c.Hard, loc+".hard")
		v.checkCandidates(c.Easy, loc+".easy")
		if c.Threshold < 0 {
			// The threshold is a symmetric margin band; negative is meaningless.
			v.errf("%s (%s): threshold must be >= 0 (a margin band; got %g)", loc, c.Name, c.Threshold)
		}
	}
}

func (v *validator) checkSignalName(name, loc string, seen map[string]bool) {
	switch {
	case name == "":
		v.errf("%s: missing name", loc)
	case seen[name]:
		v.errf("%s: duplicate signal name %q", loc, name)
	}
	seen[name] = true
}

func (v *validator) checkCandidates(cands []string, loc string) {
	switch {
	case len(cands) == 0:
		v.errf("%s: empty candidate list", loc)
	case len(cands) > candidatesHardCap:
		v.errf("%s: %d candidates exceeds hard cap %d", loc, len(cands), candidatesHardCap)
	case len(cands) > candidatesSoftCap:
		v.warnf("%s: %d candidates (>%d) — usually a signal-design smell", loc, len(cands), candidatesSoftCap)
	}
	seen := map[string]bool{}
	nonEmpty := 0
	for _, c := range cands {
		if c == "" {
			continue // filtered out sidecar-side; counted below
		}
		nonEmpty++
		if seen[c] {
			v.warnf("%s: duplicate candidate %q", loc, c)
		}
		seen[c] = true
	}
	// A bank of only empty strings is filtered to nothing sidecar-side; for a
	// complexity bank that silently skews the margin (score 0 on that side), so
	// reject it rather than warn.
	if len(cands) > 0 && nonEmpty == 0 {
		v.errf("%s: all candidates are empty strings (the list is effectively empty)", loc)
	}
}

func (v *validator) validateSession() {
	if v.p.Session.MinBandJump < 0 {
		v.errf("session.min_band_jump: must be >= 0 (got %d)", v.p.Session.MinBandJump)
	}
	// A downgrade is always ≥1 band, and damping requires jump < min_band_jump,
	// so any value < 2 makes stickiness a silent no-op. Warn rather than error —
	// it's a real config, just an inert one.
	if v.p.Session.Sticky && v.p.Session.MinBandJump < 2 {
		v.warnf("session: sticky=true with min_band_jump=%d damps nothing (a 1-band downgrade needs min_band_jump>=2 to be held)", v.p.Session.MinBandJump)
	}
}

// ruleUsesML reports whether a condition tree references any ML signal leaf
// (domain/embedding/complexity) anywhere — used by the cost lint.
func ruleUsesML(r *Rule) bool {
	if r.Domain != "" || r.Embedding != "" || r.Complexity != "" {
		return true
	}
	for i := range r.All {
		if ruleUsesML(&r.All[i]) {
			return true
		}
	}
	for i := range r.Any {
		if ruleUsesML(&r.Any[i]) {
			return true
		}
	}
	if r.Not != nil {
		return ruleUsesML(r.Not)
	}
	return false
}
