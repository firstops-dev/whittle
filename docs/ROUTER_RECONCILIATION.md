# Whittle Router — capability reconciliation (R2 spec, DRAFT)

Status: **proposal, no code.** Closes the execution-audit's #2 gap ("the real
work of v1, unspecified"). Companion to `ROUTER_DESIGN.md` §2.6.

## Why this exists

Claude Code tailors each request to the **requested** model's capabilities. When
the router sends that request to a **different** model, the target rejects
features it doesn't support with **hard 400s**. Observed in the GATE-1
experiment (down-routing Opus→Haiku):

| observed 400 | where the feature lives |
|---|---|
| "long context beta not available…" | header `anthropic-beta: context-1m-…` |
| "does not support the effort parameter" | body `output_config.effort` + header `effort-…` beta |
| "adaptive thinking is not supported…" | body `thinking` config (+ `thinking` blocks in history) |
| "role 'system' is not supported…" | a `messages[]` entry with `role:"system"` (+ its beta) |

Reconciliation is the layer that makes a rewritten request acceptable to its new
target. **It only runs when the model actually changes.** Decide detects the
no-op (resolved model == canonicalized requested model) and **skips Reconcile
entirely** — happy-path requests are byte-passthrough (R11); Reconcile is called
only on a genuine route, and when called it always changes at least the model
(review M5).

**Scope (review M3): reconciliation handles hard-400 mismatches only.** It makes
a rewritten request *accepted* (no 400) — it does **not** make it *equivalent*.
A feature the target accepts but silently ignores or treats differently (a
valid-but-no-op param, a quietly-different default) causes **silent behavioral
drift** that no 400 and no Mode B can detect. Down-routing is not
behavior-preserving; that is out of scope and undetectable by this mechanism —
named here so no reader assumes otherwise.

## Governing principle — forward everything, strip selectively

Per the codebase's own rule ("forward all, block the few you must; never
allowlist what to keep — you'll miss something"), reconciliation is a
**blocklist by capability, not an allowlist**:

- Forward the request **as-is** by default. Only strip a feature when the target
  model is **known** to reject it.
- **Strip-only, down-direction.** A more-capable target is a superset — up-routing
  forwards unchanged (we are never obligated to *add* features). Only
  down-routing (or cross-routing to a narrower model) strips.
- **Mode B is the safety net** (Design §3): if reconciliation is incomplete and a
  400 still occurs, retry-original-once recovers the user. So an *unknown* new
  beta is forwarded (works if supported; if not, Mode B catches it) — vastly
  better than an allowlist that would strip every new Anthropic feature the day
  it ships.

Allowlist was rejected precisely because it breaks on every Anthropic release;
blocklist degrades gracefully and self-heals via Mode B.

## Data model — capabilities, not pairwise model×feature

```go
// Compiled-in baseline (NOT user config — the transform is code, tied to
// knowing how to edit each feature). Overridable via an escape-hatch config
// key for emergencies (a new model ships before we cut a release).
type ModelCaps struct {
    Supports map[Capability]bool   // on a KNOWN model, an absent capability ⇒ unsupported
    MaxContextTokens int
}

// capsFor MUST NOT return the zero value on a lookup miss (review B1). An
// unknown model id (Anthropic ships claude-opus-5; a user edits Tiers: to an id
// we have no entry for) is treated as FULLY CAPABLE + UNBOUNDED context —
// strip nothing, forward as-is, let Mode B catch any real 400. This is the
// governing principle ("forward everything") applied where it matters most; the
// zero struct would do the opposite (over-strip / MaxContextTokens==0 makes the
// tier unroutable — silent). Two distinct defaults, do not conflate:
//   unknown CAPABILITY on a KNOWN model  ⇒ unsupported (safe: strip)
//   entirely UNKNOWN model               ⇒ fully capable (safe: forward)
func capsFor(model string) ModelCaps  // miss ⇒ {all-true, MaxContextTokens: ∞}
type Feature struct {
    Needs    Capability
    Detect   func(req *Request) bool          // is the feature present?
    Strip    func(req *Request)               // remove it from BOTH body and headers, atomically
}
```

Reconcile algorithm (per rewritten request, target model `m`):
```
for f in features:
    if f.Detect(req) and not caps[m].Supports[f.Needs]:
        f.Strip(req)          # atomic across body+headers
```
Adding a **model** = declare its capability set. Adding a **feature** = declare
its capability + detector + strip transform. No N×M table.

## The feature table (v1 baseline)

| feature | capability | detect | strip transform (atomic) |
|---|---|---|---|
| 1M context | `long_context` | `anthropic-beta` contains `context-1m-*` | remove that token from the beta header. **Coupled to routing** — see below. |
| effort | `effort_param` | body `output_config.effort` present | delete the body field **and** remove the `effort-*` beta token — both or the 400 persists. |
| adaptive thinking | `thinking` | body `thinking` config present | delete `thinking` config; **also strip `thinking` blocks from prior assistant `messages[]`** (a non-thinking target rejects thinking content in history — the subtle second half). |
| mid-conversation system role | `midconv_system` | any `messages[i].role == "system"` | **see the hard case below** + remove its beta token. |

## The hard case — mid-conversation `role:"system"` (needs validation)

Dropping vs converting vs merging each changes prompt semantics differently:
- **Drop the message** — loses the instruction entirely; changes behavior.
- **Convert to `role:"user"`** — keeps the text, in place, in a role every model
  accepts. Least-lossy; the instruction stays at its intended conversational
  position.
- **Merge into top-level `system`** — reorders the instruction to "global,"
  losing its positional intent (it was meant to apply from that turn on).

**Recommended: convert to `role:"user"`** (least semantically destructive,
avoids the role-not-supported 400), logged as a lossy transform. **But this can
itself 400 (review B3):** converting a mid-array `system` message to `user`
often produces **two adjacent `user` messages** (`[…, system, assistant]` →
`[…, user, assistant]` is fine, but `[user, system, …]` → `[user, user, …]`),
and the Messages API may reject non-alternating roles. So the transform is:
convert to `user`, and if that yields adjacent same-role messages, **merge the
text into the neighboring same-role message** rather than inserting a new turn.
**Validation must test the adjacent-role 400 specifically** (not just behavioral
quality) — if convert-or-merge still 400s, Mode B recovers the user but pays a
guaranteed 400 + double-latency *every* such turn, so this must be gotten right,
not left to the safety net.

## Context-length coupling (a routing constraint, not just a strip)

Stripping the `context-1m` beta is not enough if the live conversation actually
exceeds the target's normal window — the request then 400s on context length.
So: **do not down-route a request whose `ContextTokens` exceeds
`caps[target].MaxContextTokens × 0.9`.** This is a routing guard, enforced in
Decide (resolved tier rejected → next-capable tier chosen), not a body edit.

- **Safety margin (review M1):** `ContextTokens` is `bytes/4` (§2.1), which
  **under**-counts dense JSON/code — the dangerous direction (we think it fits,
  it doesn't). The `× 0.9` margin absorbs the estimate bias; without it,
  band-edge requests 400 on length → Mode B.
- **Terminal case — no tier fits (review H3):** if NO tier's window holds the
  context, the fallback is **keep-original (no-op, R11), NEVER `Default`** —
  routing a 240k context to a 200k Default just 400s every turn. The guard uses
  the **pre-reconciliation** token count (conservative — reconciliation can only
  *shrink* context by stripping thinking history, O2).
- **Table values lean conservative (review M4):** a wrong `MaxContextTokens` is
  asymmetric — under-stating starves a tier (never chosen, safe-ish),
  over-stating routes oversized contexts that 400 (per-turn Mode B). Pending the
  systematic per-model probe (open #3), **under-state windows.**

## Atomicity — the Adapter interface must own body + headers together

The audit flagged that `Rewrite(body)` and `Upstream()→headers` being separate
invites "stripped the body field, forgot the paired header → still a 400." Fix:
one method reconciles the whole request.

```go
type Adapter interface {
    Name() string
    // Reconcile mutates BODY and HEADERS together for the chosen target model,
    // and returns whether anything changed (false ⇒ no-op short-circuit, R11).
    Reconcile(req *Request, target string) (changed bool, err error)
    Upstream(hdr http.Header) (baseURL string, out http.Header)
}
```
`Request` carries both parsed body and headers so a `Feature.Strip` edits both
in one place.

## Strip mechanics — the degenerate cases that must stay valid JSON (review M2)

Each transform must leave a **valid Anthropic request**; the failure cases:
- **Empty `anthropic-beta` after removing the only token** → remove the header
  entirely, don't send an empty value. Comma-list edits must not leave
  leading/trailing/double commas.
- **Assistant message emptied by stripping `thinking` blocks** → if content
  becomes `[]`, drop the whole message (a degenerate empty turn is itself
  invalid).
- **Content-length is recomputed after ALL mutations**, not just the model
  rewrite (§2.7's "after model rewrite" is too narrow — reconciliation edits
  body fields too).

**Body handling: parse → strip → re-serialize** (correctness over cache
preservation). This means the reserialized body loses ALL `cache_control` prefix
alignment on a **routed** request → a prompt-cache miss + cost regression on that
request. **Accepted for v1** because only routed requests pay it (the happy path
is byte-passthrough, untouched, R11) — and surgical byte-editing to preserve
cache positions risks emitting invalid JSON, a worse failure. This supersedes
the earlier "prefer position-preserving transforms" idea: under re-serialization
that is moot. Revisit only if measured cost regression on routed traffic is
material.

## Maintenance & self-healing

- **Static baseline** (the table above), compiled in, updated per release.
- **Mode B feedback loop:** an unrecognized feature is forwarded; if the target
  400s, Mode B retry-original recovers the user AND the log line records
  `model + rejected-feature + error` — the signal to add a blocklist entry.
- **Honesty:** the static-baseline loop is self-*diagnosing*, human-*healing* —
  it recovers one request and logs; a human must read the log and ship a new
  baseline (days). Without the runtime learning below, the same unknown feature
  400s + retries **every turn** until then (≈2× TTFT per turn).
- **v1 — generic beta-token runtime-strip (review H4, pulled into v1).** Most new
  Anthropic capabilities ship as `anthropic-beta` header tokens. When a
  rewrite-caused 4xx names a beta token, remember "target `m` rejects beta `X`"
  (account-scoped, alongside the entitlement blocklist) and strip token `X`
  proactively thereafter. Low-complexity (betas are comma-list header tokens;
  the generic strip already exists) and it kills the per-turn double-request for
  the common case. **In v1.**
- **v1.1 — body-structural learning:** an unknown *body* param is genuinely hard
  (we don't know how to strip a field we've never seen safely) → deferred.

## Interaction with Mode B / entitlement (folds R1)

- **Capture the pristine original BEFORE Reconcile runs (review B2).** Reconcile
  mutates body+headers in place, so the retry source must be a snapshot taken
  before reconciliation — the original body, model id, and headers — held until
  the upstream **status code** is known. Peak memory ≈ 2×body per in-flight
  routed request (≤32MB each, R18); fine for a single-user local proxy (O3).
  Equivalently, Reconcile operates on a copy and the proxy keeps the original.
- **The commit-point invariant (review H1).** The proxy must NOT write the
  status line or any body byte to the client until it has read the upstream
  **HTTP** status and decided retry-vs-relay (hold the response head; do not
  begin `io.Copy` early). Mode B fires **only on an upstream HTTP 4xx received
  before that commit point.** A 200 stream that later carries an `event: error`
  mid-flight (e.g. `overloaded_error`) is **relayed verbatim, never retried** —
  bytes are already on the wire. "Retry on any error" would be unsound; retry is
  keyed on pre-commit HTTP status only.
- On a pre-commit 4xx after a rewrite → **retry the snapshotted original once**,
  then relay if it still fails.
- Distinguishing our-fault from client-fault: **only retry if we actually
  rewrote**. A 4xx on a no-op (R11) request is the client's own → relay, never
  retry (same request would loop). Accepted double-latency (O1): if we rewrote
  but the original was itself malformed, we retry → it 400s → relay (correct, 2×
  latency).
- **Entitlement failures are ACCOUNT-global, classified by `error.type`, not
  status code (review H2).** Classify by the Anthropic error body's
  `error.type` (`permission_error` = entitlement; `invalid_request_error` =
  capability), not by 403-vs-400 (Anthropic is not 1:1). On an entitlement
  error: retry original once, AND **record the tier as blocked at the
  daemon/account level (persisted), not per-session** — a plan's tier access is
  invariant across sessions, and a session-less request (R14) has nowhere to
  store a per-session block, which would reopen the per-turn 403 loop (C2).
  Decide consults this account-level blocklist and skips blocked tiers.

## Open / validate before build
1. Mid-conversation `role:"system"` transform (convert-to-user) — validate on real traffic.
2. Whether stripping `thinking` blocks from history is sufficient, or the target also rejects other residue (tool-use signatures, etc.) — needs a down-route-with-history capture.
3. The full capability set of each current Anthropic model — the table above is seeded from 4 observed 400s; a systematic probe (send each feature to each tier, record 400s) would complete it before GA.
