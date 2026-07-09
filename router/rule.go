package router

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Rule is one node of a route's condition tree. A node is EITHER a combinator
// (exactly one of All / Any / Not) OR a single leaf predicate. Mixing a
// combinator with a leaf, setting two combinators, or setting two leaf
// predicates in one node is invalid — enforced recursively by validate(), not
// by the type, because "operator-as-key unmarshals 1:1 into an all-optional
// struct" is the entire reason this grammar needs no custom parser (see
// docs/ROUTER.md §4).
//
// Strict-key decoding (json.Decoder.DisallowUnknownFields) turns a typo'd leaf
// key ("keywrods") into a load error rather than a silently-dropped predicate
// that would misroute (review B1 of the schema).
type Rule struct {
	// Combinators (choose at most one; mutually exclusive with any leaf).
	All []Rule `json:"all,omitempty"`
	Any []Rule `json:"any,omitempty"`
	Not *Rule  `json:"not,omitempty"`

	// Leaf predicates: a valid leaf node sets EXACTLY ONE of these.
	ContextTokens  *NumBand `json:"context_tokens,omitempty"`
	MessageCount   *NumBand `json:"message_count,omitempty"`
	ToolLoop       *bool    `json:"tool_loop,omitempty"`
	HasTools       *bool    `json:"has_tools,omitempty"`
	Keywords       []string `json:"keywords,omitempty"`        // LITERAL substring, case-insensitive
	KeywordsRegex  []string `json:"keywords_regex,omitempty"`  // explicit regex, opt-in
	RequestedModel []string `json:"requested_model,omitempty"` // membership (canonicalized both sides)

	// ML signal leaves: each names a signal defined in Policy.Signals and fires
	// per that signal's rule (lazy — computed at most once per request).
	Domain     string `json:"domain,omitempty"`     // named domain signal (classifier label ∈ its set)
	Embedding  string `json:"embedding,omitempty"`  // named embedding signal (bank score ≥ its threshold)
	Complexity string `json:"complexity,omitempty"` // named complexity level, "signal:hard|easy|medium"
}

// leafKind is a stable identifier for which single leaf predicate a node holds.
// It doubles as the "is this an ML leaf" signal for cheap-first ordering: only
// intentLeaf currently requires a model call.
type leafKind int

const (
	noLeaf leafKind = iota
	contextTokensLeaf
	messageCountLeaf
	toolLoopLeaf
	hasToolsLeaf
	keywordsLeaf
	keywordsRegexLeaf
	requestedModelLeaf
	domainLeaf
	embeddingLeaf
	complexityLeaf
)

// leaves reports which leaf predicates are set on this node (order stable for
// deterministic error messages). Combinators are not leaves.
func (r *Rule) leaves() []leafKind {
	var out []leafKind
	if r.ContextTokens != nil {
		out = append(out, contextTokensLeaf)
	}
	if r.MessageCount != nil {
		out = append(out, messageCountLeaf)
	}
	if r.ToolLoop != nil {
		out = append(out, toolLoopLeaf)
	}
	if r.HasTools != nil {
		out = append(out, hasToolsLeaf)
	}
	if r.Keywords != nil {
		out = append(out, keywordsLeaf)
	}
	if r.KeywordsRegex != nil {
		out = append(out, keywordsRegexLeaf)
	}
	if r.RequestedModel != nil {
		out = append(out, requestedModelLeaf)
	}
	if r.Domain != "" {
		out = append(out, domainLeaf)
	}
	if r.Embedding != "" {
		out = append(out, embeddingLeaf)
	}
	if r.Complexity != "" {
		out = append(out, complexityLeaf)
	}
	return out
}

// combinators reports which combinator keys are set (0, 1, or — invalid — more).
func (r *Rule) combinatorCount() int {
	n := 0
	if r.All != nil {
		n++
	}
	if r.Any != nil {
		n++
	}
	if r.Not != nil {
		n++
	}
	return n
}

// isMLLeaf reports whether this node's single leaf needs a model call. Used by
// the evaluator to order cheap heuristic children before ML children so an
// already-decided node never pays for a classifier (docs/ROUTER.md §5).
func (r *Rule) isMLLeaf() bool {
	ls := r.leaves()
	if len(ls) != 1 {
		return false
	}
	switch ls[0] {
	case domainLeaf, embeddingLeaf, complexityLeaf:
		return true
	}
	return false
}

// NumBand is a numeric predicate over a signal (token count, message count).
// It accepts EITHER a bare scalar (message_count: 1 ⇒ Eq=1) OR a bounds object
// (context_tokens: {gte: 60000}); see UnmarshalJSON. Multiple bounds form a
// range (gt+lt). Eq is exclusive of the others.
type NumBand struct {
	Eq  *int `json:"eq,omitempty"`
	Gt  *int `json:"gt,omitempty"`
	Gte *int `json:"gte,omitempty"`
	Lt  *int `json:"lt,omitempty"`
	Lte *int `json:"lte,omitempty"`
}

// UnmarshalJSON is the ONE custom unmarshaler in the policy grammar. It keeps
// the ergonomic scalar shorthand (`message_count: 1`) without a DSL: a JSON
// number becomes Eq; a JSON object is decoded strictly (unknown bound keys are
// rejected, matching the rest of the schema). A string (the common
// `context_tokens: "60000"` mistake) yields a targeted error, not a type panic.
func (n *NumBand) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return fmt.Errorf("numeric predicate: empty value")
	}
	switch trimmed[0] {
	case '{':
		// Bounds object — decode strictly so `{grt: 1}` etc. is a loud error.
		type rawBand NumBand // avoid recursing into this method
		var rb rawBand
		dec := json.NewDecoder(bytes.NewReader(trimmed))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&rb); err != nil {
			return fmt.Errorf("numeric predicate object: %w", err)
		}
		*n = NumBand(rb)
		return nil
	case '"':
		return fmt.Errorf("numeric predicate must be a number or a {gt/gte/lt/lte/eq} object, not a quoted string")
	default:
		// Bare scalar shorthand ⇒ equality.
		var v int
		if err := json.Unmarshal(trimmed, &v); err != nil {
			return fmt.Errorf("numeric predicate scalar: %w", err)
		}
		n.Eq = &v
		return nil
	}
}

// match evaluates the band against a value. An all-nil band never matches
// (validation rejects it, so this is defense in depth).
func (n *NumBand) match(v int) bool {
	if n.Eq != nil {
		return v == *n.Eq
	}
	ok := false
	if n.Gt != nil {
		if v <= *n.Gt {
			return false
		}
		ok = true
	}
	if n.Gte != nil {
		if v < *n.Gte {
			return false
		}
		ok = true
	}
	if n.Lt != nil {
		if v >= *n.Lt {
			return false
		}
		ok = true
	}
	if n.Lte != nil {
		if v > *n.Lte {
			return false
		}
		ok = true
	}
	return ok
}
