# Whittle Router — design (DRAFT for review)

Status: **proposal, no code yet.** This is the design to shred before we build.
Scope: a local model-router for Claude Code, folded into (or shipped beside)
whittle. Companion to the compressor: compress intercepts tool **outputs**;
route intercepts LLM **requests**.

---

## 0. The conflict we must resolve first (read this before the rest)

`PLAN.md` publishes these **non-goals**: *"No proxy mode · no model hosting ·
no telemetry."* The router violates the first two:

- **Proxy mode.** Today whittle is a *hook* — Claude Code calls
`POST /hook` (PostToolUse) and whittle is never in the network path. Routing
LLM calls **requires** being on `ANTHROPIC_BASE_URL` as a real proxy. There
is no hook that can redirect a model call; the interception point is the HTTP
request itself.
- **Model hosting.** The opt-in `smart` mode hosts two ONNX models
(~200-300MB). The compressor deliberately keeps models server-side
(the Python sidecar), not in the daemon.

This is not just packaging/branding — it is a **trust escalation** and a
**resource-exclusivity** change (review C1/O2):

- **Credential + prompt exposure.** The hook only ever saw tool *outputs*
post-hoc. A proxy on `ANTHROPIC_BASE_URL` sees **every credential** (the
user's OAuth token / API key) and **every token of every prompt**. A bug or
a compromised dependency now leaks secrets. The compressor's threat model
never included this.
- `**ANTHROPIC_BASE_URL` is a single exclusive slot** and it is **global** — it
captures *all* the user's Anthropic traffic, not just Claude Code, and it
cannot coexist with any other tool that also wants that slot (other routers,
observability proxies). whittle-as-hook had no such conflict.

So this is not a feature addition — it changes whittle's deployment model from
"invisible hook" to "network proxy." Three ways forward, **founder's call**
(evaluate each against the trust + exclusivity facts above, not just branding):

1. **Separate binary / mode** (`whittle route`), clearly partitioned from the
  published hook surface; non-goals amended to "no proxy mode *for
   compression*."
2. **Sibling project** entirely (a `whittle-router` repo) that shares the
  compress library.
3. **Revise the non-goals** and make whittle a two-headed daemon (hook +
  proxy).

The rest of this doc assumes we build the router; it does **not** assume where
it ships. Everything below is written so the routing core is a library
(`route/`, `adapter/`) that any of the three packagings can wrap.

---

## 1. High-level design

```
Claude Code ──(Anthropic Messages, streaming)──▶ whittle proxy (localhost)
                                                     │
                          ┌──────────────────────────┼───────────────────────────┐
                          │ 1. EXTRACT (cheap, always)                            │
                          │    request body ─▶ Signals{tokens, tool_loop,         │
                          │    requested_model, last_user_text, session_key, …}   │
                          │    heuristic only — NO model call here                │
                          ├───────────────────────────────────────────────────────┤
                          │ 2. DECIDE (cheap → expensive, short-circuits)          │
                          │    a. pin-header override        ─▶ done               │
                          │    b. guards waterfall (heuristic) ─▶ maybe done       │
                          │    c. [smart] lazy ML only if unresolved:              │
                          │       intent classifier · text embedding    (signals)   │
                          │    d. session stickiness gate                          │
                          │    e. default tier                                     │
                          │    ⇒ Decision{tier, model, reason}                     │
                          ├───────────────────────────────────────────────────────┤
                          │ 3. ROUTE (adapter: factory + strategy)                 │
                          │    rewrite body.model ─▶ forward to upstream           │
                          │    stream response back (v1: byte passthrough)         │
                          ├───────────────────────────────────────────────────────┤
                          │ 4. OBSERVE                                             │
                          │    x-whittle-route header + one structured log line    │
                          └───────────────────────────────────────────────────────┘
                                                     │ client's own auth, untouched
                                                     ▼
                                              api.anthropic.com
```

**The load-bearing property: cheap-first, lazy-ML.** Heuristic guards run before
any model call, so "huge context → smart" or "background → fast" never pays for
an embedding. The ML models are consulted only on the residual traffic no guard
resolved. A naive "extract-all-signals → score → decide" pipeline would run the
models on every request and defeat the entire point. **This is why "score
evaluator" is not a separate top-level stage** — scoring/ML is a sub-step *inside*
Decide, reached only when cheap rules don't settle it.

**Package layout (mirrors `compress/`):**


| package                        | mirrors                 | responsibility                                                   |
| ------------------------------ | ----------------------- | ---------------------------------------------------------------- |
| `route/`                       | `compress/`             | the brain: extract → decide. Library, no HTTP.                   |
| `route/` `signals.go`          | `compress/router.go`    | heuristic signal extraction (precompiled regex, hot-path)        |
| `route/` `ml.go`               | (new)                   | opt-in ONNX classifier + embedding, behind a `Classifier` iface  |
| `route/` `policy.go`           | `compress/types.go`     | the fixed YAML struct + validation                               |
| `route/` `engine.go`           | `compress/pipeline.go`  | ties extract+decide; holds no mutable state except session store |
| `route/` `session.go`          | (new)                   | stickiness state, TTL/LRU evicted                                |
| `adapter/`                     | `compress/compressors/` | provider strategies (factory), Anthropic passthrough in v1       |
| `proxy/` (or extend `server/`) | `server/hook.go`        | the HTTP proxy; "we are a real proxy now" lives here             |


---

## 2. Low-level design, per component

### 2.1 Signal Extractor — `route/signals.go`

**Responsibility:** parse the request body **once**, emit heuristic `Signals`.
No ML, no allocation of the full body where avoidable (bodies are 20k+ tokens
of system prompt + tool defs; work on offsets/slices).

```go
type Signals struct {
    RequestedModel string  // body.model, canonicalized (see §2.6)
    ContextTokens  int     // ESTIMATE (bytes/4); banding only, not billing
    LastUserText   string  // per policy.Inspect.Scope
    RecentText     string  // recent-turns window
    ToolLoop       bool    // PRECISE: last message has role:user AND contains ≥1 tool_result block
    MessageCount   int     // count of top-level messages[] entries
    HasTools       bool
    SessionID      string  // = X-Claude-Code-Session-Id header (§2.5); no derivation

    // ML fields are NOT filled here — Decide fills them lazily.
    Intent     string
    IntentConf float64
}

func Extract(body []byte, hdr http.Header, pol *Policy) (Signals, error)
```

- Parse strategy: bounded `gjson`-style peek for `model`, `messages[]` roles,
`tools` presence — never a full `json.Unmarshal` of the whole body on the hot
path (matches compress's `extractContentFast` discipline).
- **`ContextTokens` counts the WHOLE request body** (system + tools + messages)
  ÷ 4 — because that is what actually drives cost and cache size. **Consequence
  (R5): the floor is ~20k on a trivial first turn** (CC always sends a big
  system prompt + tool defs), so author thresholds against the *whole-request*
  scale — a `{ lt: 4000 }` band can never match; use realistic numbers
  (e.g. `gt: 60000`). Every example threshold in the schema is calibrated to
  this definition.
- **`ToolLoop` is exactly:** the last `messages[]` entry has `role: "user"` and
  its content contains at least one `tool_result` block. NOT "any assistant turn
  ever emitted a tool_use" (that is true from turn ~2 of every agent session and
  would pin every session via `to: keep` — R4).
- **`LastUserText` / `RecentText` extraction (R6):** flatten only **`text`
  blocks of `role:"user"` messages** within the `inspect` window; **exclude
  `tool_result`, `tool_use`, `thinking`, and `image` blocks**. When the last
  message is a `tool_result` (an agent step, not human intent), walk back to the
  most recent genuine user-text turn for classification. These are the ONLY text
  the ML sees — feeding it raw tool output re-creates the noise problem this
  field exists to avoid.
- **`RequestedModel` canonicalization (R7) is required, not deferred:** strip
  date suffixes and resolve `-latest` on BOTH the incoming id and the `tiers`
  model strings before any membership match or no-op comparison — else CC's
  dated ids (`claude-opus-4-…-<date>`) silently never match `requested_model`
  routes and routing quietly disables itself.

### 2.2 Policy — `route/policy.go`

The fixed struct the YAML unmarshals into. **No DSL**: every condition is typed
data; the only external grammar is regex (Go's `regexp`, not ours). Validates
with JSON-Schema, not a compiler.

```go
type Policy struct {
    Version   int
    Tiers     []Tier          // ORDERED cheap→capable; index = band rank
    Default   string
    Inspect   InspectCfg
    Signals   *SignalSet      // named ML signals referenced by route leaves
    Routes    []Route         // ordered waterfall; When is a recursive bool tree
    Session   SessionCfg
    Overrides OverrideCfg
}
// A route's When is a recursive Rule tree (all/any/not + one leaf per node). Leaves
// are the heuristic ones (context_tokens, message_count, tool_loop, has_tools,
// keywords, keywords_regex, requested_model) PLUS three ML SIGNAL leaves that name
// an entry in Signals:
//   Domain     string   // domain signal: classifier label ∈ its categories
//   Embedding  string   // embedding signal: bank score ≥ its threshold
//   Complexity string   // "name:hard|easy|medium"
type SignalSet struct {          // vSR-aligned; exact JSON in ROUTER_POLICY_SCHEMA.md
    Domains    []DomainSignal     // {name, categories}              — intent classifier
    Embeddings []EmbeddingSignal  // {name, threshold, candidates}   — embedding model
    Complexity []ComplexitySignal // {name, threshold, hard, easy}   — embedding (contrastive)
}
```

Loaded behind `atomic.Pointer[Policy]` for SIGHUP hot-reload: parse → validate →
atomic swap. Invalid reload keeps the old policy and logs. Hot path reads the
pointer once per request. (Signal embeddings are NOT precomputed at load — the
sidecar caches candidate embeddings by content hash on first use; §2.4.)

### 2.3 Decider — `route/engine.go`

**Responsibility:** `Signals + Policy → Decision`. Owns the `Classifier`. Pure
except for the session store.

```go
type Decision struct {
    Tier, Model, Reason string   // Reason: "route:hard-work" | "default" | "… ml-degraded"
}
func Decide(s Signals, p *Policy, cl Classifier, sess SessionStore, pin string) Decision
```

Ordered algorithm:

1. **pin override** (`Overrides.PinHeader`) → done.
2. **routes, top-down.** First route whose `When` tree holds wins. A signal leaf
   (domain/embedding/complexity) triggers a **lazy** classifier call, memoized per
   request; cheap heuristic leaves in the same node evaluate first, so a matched
   sibling skips the model entirely. A route fires only on a **definitive** match:
   if a signal it consulted errored (sidecar down), the route is **skipped**
   (fail-open) — so a `not` over an unavailable signal never fires, and the reason
   is tagged `ml-degraded`.
   ⚠️ **Cost lint (§4.4):** a route with a signal leaf runs a model on every
   request reaching it; validation warns. Put cheap routes above it.
3. **stickiness** (§2.5): damp a fuzzy (default) downgrade from the session's
   current tier when band-jump `< MinBandJump`.
4. **default** — the static fallback. There is **no separate "classify" stage**:
   the ML lives inside route conditions as signal leaves, matching vSR (the
   default is just the last rung). If weighted scoring is ever added it is another
   signal leaf that thresholds inside a route, never a competing stage.

**Failure mode = fail-open to the ORIGINAL request (corrected, review B3).**
Any error (bad signals, ML panic, timeout, body too large to parse, malformed
JSON) → **forward the client's request untouched, honoring its `requested_model`**
— never block, and never silently substitute `Default`. This matches the
compressor's convention (`pipeline.go` / `hook.go` fail-open to the *original*,
not to a chosen alternative). `Default` is reserved for the ONE case where
routing *succeeds* but no route matched — it is a routing outcome,
not an error path. The distinction matters: "route to Default on error" would
silently downgrade every request to a weaker tier on any bug, and is literally
impossible on the paths where we can't read the body at all.

### 2.4 Classifier — `router/ml` (HTTP client to the sidecar)

```go
type Classifier interface {
    Domain(text string) (label string, conf float64, err error)              // intent classifier
    EmbeddingScore(text string, candidates []string) (score float64, err error)   // bank score
    ComplexityMargin(text string, hard, easy []string) (margin float64, err error) // contrastive
}
```

- Impls: `noop` (smart off → `ErrMLDisabled`) and the `router/ml` HTTP client to
  the Python sidecar. **The models live in the sidecar** (vSR's two mmBERT models —
  a ModernBERT intent classifier + a 32k text embedder); the Go engine carries no
  model runtime and owns the **thresholds** (score ≥ T; margin band → level).
- **vSR's exact scoring, sidecar-side.** Bank score = `0.75·best + 0.25·mean(top-2)`
  over per-candidate cosine (candidates are the policy's `candidates`/`hard`/`easy`
  lists). `EmbeddingScore` returns the bank score; `ComplexityMargin` returns
  `score(hard) − score(easy)`. Compute where the data lives.
- **Cache is sidecar-side, keyed by (candidate text hash + model version).** The
  engine passes candidates on every call; the sidecar embeds them once and reuses.
  No Go-side centroid/matrix/emb-cache — the design is simpler than the earlier
  few-shot plan, which precomputed per-tier example matrices at load. The model
  version in the key guards against silent v1-vs-v2 embedding garbage.
- **Per-list candidate cap** (schema validation): soft ~32, hard 256 — quality +
  cold-start cost, not runtime.
- **Fail-open, two levels.** A classifier error makes the signal leaf evaluate
  false AND marks the route non-firing (§2.3), so an unavailable signal never
  routes anywhere — routing falls to `default`, never blocks, never panics. A
  request-level parse/transport error is still Mode A at the proxy (forward the
  ORIGINAL untouched, §2.3); the engine itself only decides.

### 2.5 Session store — `route/session.go`  (DE-RISKED by GATE-2 experiment)

Stickiness needs "what tier is this session on." **The GATE-2 experiment
(2026-07-09) refuted the premise that we must derive a key.** Claude Code sends
a real, stable session id header:

```
X-Claude-Code-Session-Id: 911c4d7e-…   (UUID)
```

Observed byte-stable across all requests of a session (including the internal
title-generation sub-request) and distinct between sessions. **Use it directly**
— the entire "derive a key by hashing the system prefix" design is deleted. The
experiment also proved that derivation would have *failed*: two distinct sessions
in the same cwd had **byte-identical** system blocks (no wall-clock/git-status in
the block), so a content hash would collide; and the positionally-last
`cache_control` breakpoint lives in the *messages* (moves every turn), so
"hash to the last breakpoint" would churn per turn. The header sidesteps both.

```go
type SessionStore struct { /* sharded map keyed by session-id + TTL + LRU cap */ }
func (s *SessionStore) Current(id string) (tier string, ok bool)
func (s *SessionStore) Set(id, tier string)
```

- Lifecycle: TTL eviction (~30 min idle) + max-entries LRU.
- **Still to verify** (not testable headlessly): does the header stay stable
  across `--resume` and `/compact`, and do **parallel subagents inherit the
  parent's id or get their own**? The thrash question now hinges entirely on
  that last behavior — if subagents share the parent id, concurrent subagent
  requests contend on one store entry. Test before shipping `keep`/stickiness.

### 2.6 Adapter — `adapter/`

```go
type Adapter interface {
    Name() string
    // Reconcile sets the target model AND strips target-incompatible features
    // across BODY and HEADERS together (atomic — see ROUTER_RECONCILIATION.md).
    // Called ONLY on a genuine route; Decide detects the no-op (resolved ==
    // requested model) and byte-passthroughs WITHOUT calling Reconcile (R11/M5).
    // Operates on a COPY — the caller retains the pristine original for Mode B
    // retry (B2). `stripped` lists features removed, for the log line.
    Reconcile(req *Request, target string) (stripped []string, err error)
    Upstream(hdr http.Header) (baseURL string, out http.Header)
    // v2 only: TranslateRequest / TranslateResponse for cross-protocol
}
func For(dest Destination) Adapter   // factory
```

- **v1: `anthropicPassthrough`.** Auth headers pass through untouched (GATE-0
confirmed OAuth/subscription works through a transparent proxy — NOT API-key-
only); **response is a byte stream passthrough** — GATE-1 confirmed Claude Code
does **not** validate the echoed `message_start.message.model`, so no SSE
parsing/rewrite is needed. This is the entire reason v1 is small, and it is now
empirically validated, not assumed.
- **⚠️ Rewrite is capability RECONCILIATION, not just `body.model`** — the real
work of v1, now fully specified in **`ROUTER_RECONCILIATION.md`**. Summary: a
blocklist-by-capability (forward everything, strip only features the target is
known to reject — never allowlist), strip-only/down-direction, body+headers
atomic, with Mode B (§3) as the self-healing safety net for unknown new betas.
Reconciliation and Decide share the capability table so oversized contexts are
never down-routed below a target's window (routing guard, not a body edit).
- **v2: cross-provider** (Claude Code → GPT/local). Do **not** reinvent SSE
translation — vllm-sr is Apache-2.0; port its `sse_out.go` (~650 LOC, three
production bugs already paid for).

### 2.7 Proxy — `proxy/` (or `server/`)

- Hand-rolled over `net/http` (not `httputil.ReverseProxy` — we must read the
body for signals before forwarding). Request body is **buffered** (bounded
read, e.g. 32MB like the hook); response is **streamed** (`io.Copy` + flush,
or `Flusher`).
- Content-length recomputed after model rewrite.
- **⚠️ SSE framing is a real hazard (GATE-1 reproduced a client hang).**
  Anthropic returns **gzip-encoded `text/event-stream`**. Forwarding gzip
  verbatim while re-chunking hung Claude Code. Fix that worked: send
  `Accept-Encoding: identity` upstream, then stream the body with immediate
  per-write flush. Get this exact or the client times out.
- **Unknown paths pass through untouched** — Claude Code also hits
`/v1/messages/count_tokens` and others; the proxy must not assume every
request is a routable `/v1/messages`.
- **Relay upstream status verbatim (review O1).** Unlike the hook's
unconditional `200` (a hook must never fail a tool call), a proxy MUST pass
upstream 4xx/5xx (401/403/429/400/529) through unchanged — collapsing them
breaks Claude Code's error handling and retry logic. "Never 500 *from our own
bug*" is a different rule from "always 200."
- **Retries/fallback:** v1 leaves retries to Claude Code (it already retries);
the proxy surfaces upstream errors verbatim. A fallback-tier chain is a
deliberate v2 decision (§4.7) to avoid double-retry storms.
- Never 500 on a routing bug: any internal error → **forward the original
request untouched** (B3), never a substituted model.

---

## 3. Cross-cutting

- **Config hot-reload:** SIGHUP → parse → validate → atomic swap. Invalid → keep
old + log. (No precompute step — the sidecar caches candidate embeddings lazily.)
- **Observability (the 90% that matters):** `x-whittle-route: <tier>` +
`x-whittle-reason` response headers, and one structured JSON log line per
request. The log line carries **token counts, tier, reason, matched-rule
name, latency — NEVER prompt text** (review C1: the compressor's stats log
deliberately never persists content; the router must hold the same line, or
it becomes user-prompt-content-on-disk). This still answers "why did my
request go to Haiku" via the reason + matched-rule. No remote telemetry.
- **"Fail-open" is THREE distinct behaviors — never conflate them (the root
  cause of the R1 fail-loop).** Name and separate:
  - **Mode A — process-error fallthrough.** *Our* internal error (body parse
    fails, ML classifier panics/errors, invalid signals). → **Forward the
    ORIGINAL request untouched** (original model, unmodified body/headers). Safe
    because nothing has been written to the client yet. Never substitute
    `default`. (This is what B3, §2.3, and the §2.4 classifier path all mean —
    §2.4 corrected below.)
  - **Mode B — upstream 4xx/5xx on a request we forwarded.** (R1 **resolved**.)
    If we did **not** rewrite (no-op, R11) → **relay verbatim**: it's the
    client's own request, retrying would loop. If we **did** rewrite and get a
    **400/403** → **retry the buffered ORIGINAL body+model once**, then relay if
    it still fails (safe: 400/403 arrive as a full body before any SSE flush, so
    nothing is on the wire). A **403 entitlement** failure additionally marks
    that tier **blocked for the session** so Decide stops routing there.
    Genuine client/quota statuses (429, 529) → **relay verbatim** regardless.
    Full mechanism in `ROUTER_RECONCILIATION.md` ("Interaction with Mode B").
  - **Mode C — transport failure** (upstream unreachable/reset). Forwarding the
    original can't help (the upstream is down). → **return a synthetic
    Anthropic-shaped 502 + log**; do NOT call this "fail-open."
- **Fail-open never 500s from our own bug** (Mode A). The proxy is in the
  critical path of every Claude Code call.

---

## 4. Build gate — RESULTS (experiment 2026-07-09) + pre-code must-fixes

The verification experiment (transparent proxy + real headless `claude -p`,
`experiment/proxy.py`) returned **architecture GO**:

- **GATE-0 — OAuth/subscription through a proxy: PASS.** Not API-key-only; the
  client's own auth forwards untouched. (Folded into §2.6.)
- **GATE-1 — model echo: PASS.** CC does not validate the echoed model →
  byte-passthrough viable, no SSE rewrite. BUT surfaced the capability-
  reconciliation work (§2.6) and the gzip-SSE hazard (§2.7).
- **GATE-2 — session key: PASS, better than designed.** `X-Claude-Code-Session-Id`
  header replaces the risky content hash (§2.5).

Still unverified (not testable headlessly), gate before shipping stickiness:
session-id behavior across `--resume`/`/compact`/**subagents**; interactive
`/cost` display; and the net-cost model on real multi-session traffic (per-model
cold-cache loss).

**Two must-fixes before code (holistic review, 2026-07-09):**
1. **Tier ordering.** `min_band_jump` stickiness assumes tiers have rank, but
   `Tiers` is an unordered `map` → the parameter is currently undefined. Make
   tiers an ordered list / add explicit rank. (Schema §2.)
2. **~~`classify.strategy` discriminator~~ — SUPERSEDED (v5).** The `classify`
   block was removed entirely; ML now lives in route conditions as named signal
   leaves (domain/embedding/complexity), matching vSR. A future weighted scorer is
   just another signal leaf, additive by construction. (Schema §7.)

## 5. Ranked findings from review (fixes folded in above)


| #     | sev     | issue                                                                                                                       | status                                                                         |
| ----- | ------- | --------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------ |
| B1    | blocker | model echo                                                                                                                  | → GATE-1                                                                       |
| B2    | blocker | session key + thrash economics                                                                                              | → GATE-2; key now hashes stable prefix (§2.5)                                  |
| B3    | blocker | fail-open was "route to Default" — WRONG                                                                                    | **fixed**: forward original untouched (§2.3, §2.7)                             |
| C1    | high    | proxy sees creds; log leaked prompt text                                                                                    | **fixed**: §0 trust note; log excludes content (§3)                            |
| C2    | high    | routing to a model the user's plan can't access → 400/403 + CC-retry fail-loop                                              | **open** — needs entitlement handling; B3 at least breaks the loop             |
| C3    | med     | cheap-first under-specified: intra-`MatchCfg` field order, two separate ML models, warn-scope too narrow                    | **open** — spec exact eval order; cost-warn ANY intent guard                   |
| C4    | med     | centroids must live *inside* the swapped Policy snapshot; `Current→Set` not atomic per key                                  | **moot** — no Go-side centroids/matrix (sidecar caches embeddings); session store locks per op |
| C5    | med     | user regexes = ReDoS on hot path; is `Any` recursive?                                                                       | **open** — bound/timeout regexes; declare `Any` single-level                   |
| C6    | med     | `count_tokens` passes through on original model, but request runs on routed model → tokenizer mismatch in CC's context math | **open** — tied to GATE-1                                                      |
| O1    | obs     | relay upstream status, not always-200                                                                                       | **fixed** (§2.7)                                                               |
| O2    | obs     | base-URL is one exclusive global slot                                                                                       | **fixed** (§0)                                                                 |
| O3    | obs     | ML latency is on synchronous TTFT path                                                                                      | **open** — set a latency budget                                                |
| O4/O5 | obs     | request gzip/chunked encoding; model-id canonicalization                                                                    | **open**                                                                       |


**Guard vs. stickiness precedence** and **token-estimate accuracy** remain open
design choices (not blockers): proposal is guards win over stickiness, and
bytes/4 is acceptable for banding. Revisit if GATE-2 shows band-edge misroutes
matter.

## 6. Execution-readiness audit (2026-07-09) — resolved defaults + open work

Signal-extraction precision (R4/R5/R6/R7) folded into §2.1; three-mode fail-open
(R1/R8/R23) into §3/§2.4. Remaining items:

**Resolved as stated defaults (build to these):**
| id | decision |
|----|----------|
| R9  | Streaming: connect + idle timeout only; **no total-response deadline** on the streamed body (LLM gens run minutes). Upstream drop mid-stream → propagate close. |
| R10 | Smart off / models absent: a signal leaf (domain/embedding/complexity) evaluates **false** and its route is skipped → routing falls to `default`. Smart-off is the expected heuristics-only mode (NOT tagged); a real sidecar failure is surfaced per-request as an `ml-degraded` reason. |
| R11 | **No-op short-circuit:** resolved model == canonicalized requested model → byte-passthrough, NO rewrite, NO reconciliation. Protects the common (happy-path) case. |
| R12 | Each signal owns its own threshold on its own scale: `domain` is pure argmax membership (no floor), `embedding` a cosine-ish bank-score threshold, `complexity` a symmetric margin band — set per-signal in the policy, never a single global knob. |
| R13 | Nearest-example tie → **cheaper tier wins** (lowest band rank), deterministic. |
| R14 | `X-Claude-Code-Session-Id` absent → **skip stickiness** (stateless decide); never bucket session-less requests under `""`. |
| R15 | `min_band_jump` damps **downgrades only**; upgrades are free. Stored "current" = tier actually used. |
| R16 | Pin header naming unknown tier → **ignore + log**, fall through to routes; pin is **override-only** (does not write session current). |
| R17 | `Sticky:false` forced switch **does** update session current. |
| R18 | Body over 32MB cap → **stream-passthrough unbuffered** (can't route; forward untouched), not 413. |
| R19 | Config file deleted at runtime → keep last-good policy + log (same as invalid reload). |
| R20 | emb-cache version component = **model file content hash**, not a manual version string. |
| R21 | Models → download-once to `~/.whittle/route/models/`, pinned version + SHA256 verify; corrupt/failed load → smart disabled + warn-and-degrade, never crash. |
| R22 | `x-whittle-route`/`-reason` headers + one JSON log line on **all** paths with distinct reasons (`route:<name>`, `default`, `fail-open:parse`, `passthrough:unroutable-path`, `mode-b:retried-original`, `… ml-degraded`). No prompt text. |
| R24 | `count_tokens` tokenizer skew vs routed model: **accepted for v1** (documented, not fixed). |

**Resolved this pass:**
1. **[R1] Rewrite-caused 400/403 → retry-original-once, then relay** (§3 Mode B);
   403 entitlement marks the tier session-blocked. Distinguishes our-fault from
   client-fault by "did we rewrite."
2. **[R2] Capability reconciliation** — fully specified in
   **`ROUTER_RECONCILIATION.md`** (capability blocklist, strip-only, atomic
   body+headers, context-length routing guard, Mode B self-healing, maintenance).
   Three items still marked *validate-on-real-traffic* in that doc (mid-conv
   `role:"system"` transform, thinking-history residue, full per-model cap probe).
3. **[R3] Cold-start with no/invalid config → start in transparent
   passthrough + loud warning** (NOT refuse-to-start, which would brick all of
   Claude Code given the exclusive global base-URL, §0). Runtime config delete
   → keep last-good (R19).