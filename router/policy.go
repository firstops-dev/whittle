package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Policy is the parsed, validated routing policy. It is immutable after Load;
// the proxy holds it behind an atomic pointer for hot-reload (later milestone).
//
// The user-facing file is intended to be YAML; v1 loads strict JSON (stdlib,
// zero-dependency). Because the types carry only `json` tags and Load is the
// single decode site, swapping in a YAML front-end is a one-function change.
type Policy struct {
	Version   int          `json:"version"`
	Tiers     []Tier       `json:"tiers"`   // ORDERED cheap→capable; index == band rank
	Default   string       `json:"default"` // terminal tier when nothing else resolves
	Inspect   InspectCfg   `json:"inspect"`
	Routes    []Route      `json:"routes"`
	Classify  *ClassifyCfg `json:"classify,omitempty"` // the "smart default"; nil ⇒ absent
	Session   SessionCfg   `json:"session"`
	Overrides OverrideCfg  `json:"overrides"`
}

// Tier maps a logical name to a concrete provider model id. Order in Policy.Tiers
// is the band rank used by stickiness (min_band_jump) and the context-length
// routing guard — an unordered map could not express "2 bands from fast".
type Tier struct {
	Name  string `json:"name"`
	Model string `json:"model"`
}

// Route is one rule in the ordered waterfall: when `When` matches, route `To`.
// First match wins. `To` is a tier name or the reserved keyword "keep".
//
// A per-route sticky override is deliberately NOT a field yet: in v1 explicit
// routes always win and are never damped (schema §0), so it would be a
// load-accepted field that does nothing. It returns with real semantics in the
// stickiness milestone; until then strict decoding rejects a stray `sticky:` on
// a route rather than silently ignoring it.
type Route struct {
	Name string `json:"name"`
	When Rule   `json:"when"`
	To   string `json:"to"`
}

// InspectCfg bounds what request text signals see. `recent_turns` + `turns`
// keeps the classifier off the giant system prompt.
type InspectCfg struct {
	Scope string `json:"scope"` // last_user_turn | recent_turns | full
	Turns int    `json:"turns,omitempty"`
}

// ClassifyCfg is the intelligent default: consulted only when no route matched
// and smart mode is on. Strategy is a discriminator so a future weighted/learned
// scorer is an additive change, not a breaking rename (schema §7).
type ClassifyCfg struct {
	Strategy      string              `json:"strategy"` // v1: only "few_shot"
	MinConfidence float64             `json:"min_confidence"`
	Examples      map[string][]string `json:"examples"`
}

// SessionCfg controls stickiness. Damping applies to the fuzzy tail
// (classify/default), downgrade-only, by band rank.
type SessionCfg struct {
	Sticky      bool `json:"sticky"`
	MinBandJump int  `json:"min_band_jump,omitempty"`
}

// OverrideCfg names the request header that force-pins a tier, bypassing routing.
type OverrideCfg struct {
	PinHeader string `json:"pin_header,omitempty"`
}

const (
	// keepTier is the reserved `to:` value meaning "hold the session's current
	// model." It is rejected as a tier name so the two never collide.
	keepTier = "keep"

	strategyFewShot = "few_shot"

	// Example-count caps for classify (schema §4.10): the cap is about taxonomy
	// quality + cold-start embedding cost, not runtime (nearest-example over a
	// capped set is sub-millisecond).
	examplesSoftCap = 32
	examplesHardCap = 256

	maxRuleDepth = 6 // recursion bound (schema §4.7); doubles as the ReDoS/blowup guard
)

// dateSuffix matches an 8-digit date snapshot suffix on a model id
// (claude-opus-4-8-20260101). It must NOT eat the version hyphens (…-4-8), so it
// is anchored to a trailing -YYYYMMDD only.
var dateSuffix = regexp.MustCompile(`-\d{8}$`)

// canonicalModel normalizes a model id for membership comparison and no-op
// detection: strip a trailing date snapshot and a trailing "-latest". Without
// this, Claude Code's dated ids silently never match a hand-pinned
// `requested_model` and routing quietly disables itself (schema M4 / design R7).
func canonicalModel(id string) string {
	id = strings.TrimSpace(id)
	id = strings.TrimSuffix(id, "-latest")
	id = dateSuffix.ReplaceAllString(id, "")
	return id
}

// tierRank returns the band index of a tier name, or -1 if unknown.
func (p *Policy) tierRank(name string) int {
	for i, t := range p.Tiers {
		if t.Name == name {
			return i
		}
	}
	return -1
}

// tierModel returns the provider model id for a tier name, or "".
func (p *Policy) tierModel(name string) string {
	for _, t := range p.Tiers {
		if t.Name == name {
			return t.Model
		}
	}
	return ""
}

// Load parses and validates a policy from strict JSON. It returns the policy,
// any non-fatal warnings (cost lints, single-child groups, oversized example
// sets), and an error aggregating every fatal problem. A non-nil error means
// the policy must not be used.
func Load(data []byte) (*Policy, []string, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields() // typo'd/unknown keys are loud, never silently dropped
	var p Policy
	if err := dec.Decode(&p); err != nil {
		return nil, nil, fmt.Errorf("parse policy: %w", enrichDecodeError(err))
	}
	warnings, errs := p.validate()
	if len(errs) > 0 {
		return nil, warnings, fmt.Errorf("invalid policy:%s", indentErrors(errs))
	}
	return &p, warnings, nil
}

// enrichDecodeError turns the two most common hand-authoring mistakes into
// actionable hints on top of the stdlib message (which already names the JSON
// path). A scalar where a list is expected, and a list where `not` expects a
// single node, are the frequent ones (schema M6/L1).
func enrichDecodeError(err error) error {
	m := err.Error()
	switch {
	case strings.Contains(m, "of type []string"):
		return fmt.Errorf("%w — this field takes a list; wrap the value in [ ]", err)
	case strings.Contains(m, "unmarshal array") && strings.Contains(m, ".not"):
		return fmt.Errorf("%w — `not` takes a single condition, not a list", err)
	}
	return err
}

func indentErrors(errs []error) string {
	var b strings.Builder
	for _, e := range errs {
		b.WriteString("\n  - ")
		b.WriteString(e.Error())
	}
	return b.String()
}
