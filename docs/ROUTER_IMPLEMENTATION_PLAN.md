# Whittle Router — implementation plan

Execution breakdown for the design in `ROUTER_DESIGN.md`, `ROUTER_POLICY_SCHEMA.md`,
`ROUTER_RECONCILIATION.md`. Principles, in priority order: **simplicity,
correctness, extensibility, performance.** Each milestone ends with a
code-reviewer pass (opus) and a testing-engineer gap pass (opus).

## Ground rules (founder, 2026-07-09)
- **§0 RESOLVED:** whittle is a PostToolUse hook **AND** an opt-in router. README
  will be updated to drop the "no proxy" non-goal. The proxy milestone is no
  longer gated.
- **All router code lives in ONE folder: `router/`. No leakage outside it.** A
  single Go package `router` (files below), mirroring how `compress/` is one
  folder. Only heavy/optional deps (ONNX) get quarantined in a `router/ml/`
  subpackage (the `compress/compressors` pattern). Nothing is added at repo root.
- **Zero-dependency, still:** the policy loader uses stdlib `encoding/json` with
  strict unknown-key rejection (isolated to one file); YAML front-end is a
  drop-in later if/when we accept the dep.

## Sequencing rationale
Build inside-out: the **pure decision core** first (no I/O, no ML, no network —
fully testable), then adapter/reconciliation, then proxy/daemon, then opt-in ML.
Matches the reviewer's "routes + static default is the true MVP" and lets each
layer be verified before the next depends on it. All files are `router/<name>.go`.

---

## Milestone 1 — Decision core (`route/`, pure, no deps beyond yaml loader)

### T1.1 — Policy types + load + validation (`route/policy.go`, `route/validate.go`)
Types: `Policy, Route, Rule (recursive), NumBand, ClassifyCfg, SessionCfg,
Tier (ordered), InspectCfg`. Load from YAML; **validation is the product** here.
- **AC1** Parses every scenario in POLICY_SCHEMA §5.1–5.7 into the expected tree.
- **AC2** Rejects every §5.8 invalid case with a *specific* error naming the node.
- **AC3** Unknown keys rejected at every node (strict); typo'd leaf → error, not silent drop.
- **AC4** `NumBand` accepts scalar (`message_count: 1`⇒Eq) and mapping; rejects empty band / `gt≥lt` / `eq`+other.
- **AC5** One-shape-per-node enforced recursively (leaf XOR all XOR any XOR not); empty group rejected; single-child group warns.
- **AC6** Tiers are ordered (band rank defined); `keep` reserved; referential integrity (routes/default/classify keys → tiers).
- **AC7** `classify.strategy` discriminator present; only `few_shot` valid in v1; per-tier example cap (warn 32 / reject 256).
- **AC8** Loader isolates the YAML dep to one file (swap-to-JSON is one function).

### T1.2 — Signal extraction (`route/signals.go`)
Parse the Anthropic request body once → `Signals`.
- **AC1** `RequestedModel` canonicalized (strip date suffix, resolve `-latest`).
- **AC2** `ContextTokens` = whole-body bytes/4 (documented scale).
- **AC3** `ToolLoop` = last message role:user AND has ≥1 tool_result block — exact predicate, not "any tool_use ever".
- **AC4** `LastUserText`/`RecentText`: only user-role `text` blocks in the `inspect` window; excludes tool_result/tool_use/thinking/image; walks back past a trailing tool_result turn.
- **AC5** `SessionID` from `X-Claude-Code-Session-Id`; absent → empty (caller skips stickiness).
- **AC6** Bounded parse, no full-body allocation on the hot path; never panics on malformed JSON (returns err → Mode A upstream).
- **AC7** Verified against the real captured request shapes in `exp_sem_routing/experiment/captures_*`.

### T1.3 — Condition eval + decision engine (`route/engine.go`, `route/decide.go`)
The precedence ladder + boolean tree evaluator + stickiness.
- **AC1** Boolean eval: all/any/not correct; short-circuit; cheap-first child ordering (ML leaves last).
- **AC2** Precedence: pin → routes(first-match) → classify(smart default) → static default.
- **AC3** Classifier behind an interface; `noop` impl → ML leaves eval false, classify → default (smart-off path), surfaced in reason.
- **AC4** Stickiness: downgrade-only damping by band rank; `min_band_jump`; `keep`; account-level entitlement blocklist consulted.
- **AC5** Decision carries `{tier, model, reason, stripped?}`; reason distinguishes every path (`pin`, `route:<name>`, `classify:<tier>@<conf>`, `sticky:kept`, `default`, `skipped:no-ml`).
- **AC6** No-op detection (resolved == canonicalized requested) → signals byte-passthrough (no reconcile).
- **AC7** Fail-open discipline: any internal error → caller forwards original (Mode A); engine never panics.

**M1 exit:** `go build ./route/...` clean; core self-tests pass; code-reviewer(opus) + testing-engineer(opus) run.

---

## Milestone 2 — Adapter + capability reconciliation (`adapter/`)
Per `ROUTER_RECONCILIATION.md`. Capability table, strip transforms, atomic
body+headers, context-length guard sharing the caps table, unknown-model
fully-capable sentinel.
- AC: down-route request → target-incompatible features stripped, result is valid Anthropic JSON; up-route/no-op untouched; unknown model → forward-all; each strip transform unit-tested incl. degenerate cases (empty beta header, emptied assistant msg, adjacent-role merge).

## Milestone 3 — Proxy/daemon (§0-GATED)
HTTP proxy, streaming passthrough (gzip→identity, flush), three fail-open modes,
Mode B retry-original-once + commit-point invariant, session store, hot-reload,
observability. **Needs the §0 packaging decision before it ships.**

## Milestone 4 — Smart mode (opt-in ONNX)
Intent classifier + embedding, precompute/cache with model-version key,
few-shot nearest-example, install-time provisioning.

---

## Review cadence
End of each milestone: **code-reviewer (opus)** for correctness/integration,
**testing-engineer (opus)** to find coverage gaps. Fold findings before the next
milestone depends on the code.
