package router

import "strings"

// feature is one reconcilable capability: how to detect its use in a request and
// how to strip it (across body AND headers, atomically). Reconcile applies a
// strip only when the target model lacks the capability.
type feature struct {
	needs  Capability
	name   string
	detect func(*Request) bool
	strip  func(*Request)
}

// reconcileFeatures is the v1 baseline set, seeded from the GATE-1 experiment.
// Order is irrelevant to the result — the four are independent, and alternation
// repair is a shared post-pass (repairAlternation), not owned by any one strip.
var reconcileFeatures = []feature{
	{
		needs: CapLongContext, name: "context-1m",
		detect: func(r *Request) bool { return r.hasBetaPrefix("context-1m") },
		strip:  func(r *Request) { r.removeBetaPrefix("context-1m") },
	},
	{
		needs: CapEffortParam, name: "effort",
		detect: func(r *Request) bool {
			if oc, ok := r.Body["output_config"].(map[string]any); ok {
				if _, has := oc["effort"]; has {
					return true
				}
			}
			return r.hasBetaPrefix("effort-")
		},
		strip: func(r *Request) {
			// Atomic: the body field AND the paired beta token, or the 400 persists.
			if oc, ok := r.Body["output_config"].(map[string]any); ok {
				delete(oc, "effort")
				if len(oc) == 0 {
					delete(r.Body, "output_config")
				}
			}
			r.removeBetaPrefix("effort-")
		},
	},
	{
		needs: CapThinking, name: "thinking",
		detect: func(r *Request) bool {
			if _, has := r.Body["thinking"]; has {
				return true
			}
			return r.hasBetaContaining("thinking") || hasThinkingContextEdit(r)
		},
		strip: func(r *Request) {
			// Strip the config, the thinking blocks already in history (or a
			// non-thinking target rejects the history), AND every thinking beta.
			// Not just known prefixes: dependent betas are an OPEN-ENDED family
			// (interleaved-thinking, thinking-token-count, clear_thinking_… — the
			// last one confirmed live), and a lone thinking beta whose feature we
			// removed 400s ("requires thinking to be enabled"). Any beta naming
			// "thinking" must go when thinking is disabled.
			delete(r.Body, "thinking")
			stripThinkingFromHistory(r)
			r.removeBetaContaining("thinking")
			stripThinkingContextEdits(r)
		},
	},
	{
		needs: CapMidConvSystem, name: "midconv-system",
		// Detect on EITHER a mid-conversation system message OR the session-scoped
		// beta token (Claude Code sets the beta for the whole session while only
		// some turns carry a system message — review C1). Strip removes both.
		detect: func(r *Request) bool {
			for _, m := range r.messages() {
				if msgRole(m) == "system" {
					return true
				}
			}
			return r.hasBetaPrefix("mid-conversation-system")
		},
		strip: func(r *Request) {
			convertSystemToUser(r) // no-op if no system message this turn
			r.removeBetaPrefix("mid-conversation-system")
		},
	},
}

// Reconcile sets req to the target model and strips every feature the target is
// known to reject (body + headers, atomic per feature), then repairs message
// alternation if anything was stripped. It returns the names of stripped
// features for the log line. It cannot fail — the caller has already parsed the
// body (ParseRequest) and re-serializes after.
//
// Called ONLY on a genuine route (Decide detects the no-op where resolved ==
// requested and byte-passthroughs without invoking this). An unknown target is
// fully capable (capsFor), so nothing is stripped and only the model is set.
func Reconcile(req *Request, target string) []string {
	caps := capsFor(target)
	req.setModel(target)
	var stripped []string
	for _, f := range reconcileFeatures {
		if !caps.supports(f.needs) && f.detect(req) {
			f.strip(req)
			stripped = append(stripped, f.name)
		}
	}
	// A message-mutating strip (thinking-drop, system→user) can leave adjacent
	// same-role turns, which the Messages API rejects. Repair once, centrally,
	// whenever we changed the request — never owned by a single feature (the
	// bug that let thinking-drop adjacency slip through when no system message
	// was present).
	if len(stripped) > 0 {
		repairAlternation(req)
	}
	return stripped
}

// hasThinkingContextEdit reports whether context_management carries an edit whose
// type references thinking (e.g. clear_thinking_20251015) — such an edit REQUIRES
// thinking to be enabled, so it must be removed when thinking is disabled.
func hasThinkingContextEdit(r *Request) bool {
	for _, e := range contextEdits(r) {
		if m, ok := e.(map[string]any); ok {
			if t, _ := m["type"].(string); strings.Contains(strings.ToLower(t), "thinking") {
				return true
			}
		}
	}
	return false
}

// stripThinkingContextEdits drops context_management.edits that require thinking
// (see hasThinkingContextEdit). A leftover such edit 400s ("requires thinking to
// be enabled") once thinking is stripped. If edits becomes empty, context_management
// is removed entirely so an empty edits array can't itself be rejected.
func stripThinkingContextEdits(r *Request) {
	edits := contextEdits(r)
	if edits == nil {
		return
	}
	kept := edits[:0:0]
	for _, e := range edits {
		if m, ok := e.(map[string]any); ok {
			if t, _ := m["type"].(string); strings.Contains(strings.ToLower(t), "thinking") {
				continue
			}
		}
		kept = append(kept, e)
	}
	if len(kept) == len(edits) {
		return
	}
	cm, _ := r.Body["context_management"].(map[string]any)
	if len(kept) == 0 {
		delete(r.Body, "context_management")
	} else {
		cm["edits"] = kept
	}
}

// contextEdits returns body.context_management.edits as a slice, or nil.
func contextEdits(r *Request) []any {
	cm, ok := r.Body["context_management"].(map[string]any)
	if !ok {
		return nil
	}
	edits, _ := cm["edits"].([]any)
	return edits
}

// stripThinkingFromHistory removes thinking / redacted_thinking blocks from every
// message's array content. A message emptied by this (content becomes []) is
// dropped entirely — a degenerate empty turn is itself invalid. Any adjacency
// this drop creates is fixed by repairAlternation.
func stripThinkingFromHistory(r *Request) {
	msgs := r.messages()
	if msgs == nil {
		return
	}
	kept := make([]any, 0, len(msgs))
	for _, m := range msgs {
		mm, ok := m.(map[string]any)
		if !ok {
			kept = append(kept, m)
			continue
		}
		blocks, isArray := mm["content"].([]any)
		if !isArray {
			kept = append(kept, m) // string content has no thinking blocks
			continue
		}
		filtered := make([]any, 0, len(blocks))
		for _, b := range blocks {
			if t := blockType(b); t == "thinking" || t == "redacted_thinking" {
				continue
			}
			filtered = append(filtered, b)
		}
		if len(filtered) == 0 {
			continue // drop the emptied message
		}
		mm["content"] = filtered
		kept = append(kept, mm)
	}
	r.Body["messages"] = kept
}

// convertSystemToUser rewrites every mid-conversation role:"system" message to
// role:"user", in place. Alternation is fixed separately by repairAlternation.
func convertSystemToUser(r *Request) {
	for _, m := range r.messages() {
		if mm, ok := m.(map[string]any); ok && mm["role"] == "system" {
			mm["role"] = "user"
		}
	}
}

// repairAlternation coalesces consecutive same-role messages so the request is
// valid (no adjacent same-role turns — review B1/B3/H3). It is a SHARED post-pass
// run after any mutating strip, not owned by one feature.
//
// It is non-map-safe (review B1): a non-map messages[] entry is passed through
// untouched and never merged — two adjacent non-map entries can no longer panic
// on a type assertion. Merging concatenates both messages' content blocks in
// order (a bare string is wrapped as a text block), preserving every block.
func repairAlternation(r *Request) {
	msgs := r.messages()
	if len(msgs) < 2 {
		return
	}
	out := make([]any, 0, len(msgs))
	for _, m := range msgs {
		mm, ok := m.(map[string]any)
		if !ok {
			out = append(out, m) // pass non-map entries through, never merge
			continue
		}
		role, _ := mm["role"].(string)
		if n := len(out); n > 0 && role != "" {
			if prev, ok := out[n-1].(map[string]any); ok {
				if prole, _ := prev["role"].(string); prole == role {
					prev["content"] = append(contentBlocksOf(prev), contentBlocksOf(mm)...)
					continue
				}
			}
		}
		out = append(out, mm)
	}
	r.Body["messages"] = out
}
