// Package router is whittle's opt-in model router: it sits between Claude Code
// and the Anthropic API, inspects each request, and routes it to the cheapest
// capable model tier per a user-authored policy. It is a sibling to the
// compress package (compress intercepts tool OUTPUTS; router intercepts LLM
// REQUESTS) and is fully self-contained under this folder.
//
// Layering, inside-out (see docs/ROUTER_IMPLEMENTATION_PLAN.md):
//   - policy/rule/validate — the policy schema, loading, and validation
//   - signals              — heuristic extraction from a request body
//   - engine/decide        — the precedence ladder that turns signals into a Decision
//   - adapter/reconcile     — rewriting a request for its target model (later)
//   - proxy                — the HTTP daemon (later)
//   - ml (subpackage)      — opt-in ONNX classifier + embedding (later)
//
// The decision core (policy + signals + engine) has no network, no ML, and no
// filesystem dependencies, so it is exhaustively unit-testable in isolation.
package router
