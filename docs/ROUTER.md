# The whittle model router

*One document: architecture, policy reference, and design rationale. If you're
looking for how to turn it on, see the [README](../README.md#model-routing-opt-in);
this is how and why it works.*

---

## 1. What this is

`whittle route` is a local proxy that sits on `ANTHROPIC_BASE_URL` and rewrites
each Claude Code request to the **cheapest model tier that can still handle it**,
per a policy you author in one JSON file. Hard reasoning stays on your strongest
model; trivial edits drop to the cheapest; the broad middle rides a capable
default. It is opt-in, fails open on every path, and never touches your
credentials or conversation history — only the `model` field and the features the
target model can't serve.

**Non-goals.** It is not an API gateway (no auth, no quotas, no multi-tenant
anything), not a prompt rewriter (history is never mutated, so prompt-cache
prefixes survive), and not a hosted service (everything runs on your machine; the
one log line per request never contains prompt text).

## 2. Design principles

- **Fail open, always.** A router that breaks your agent is worse than no router.
  Every failure — bad policy, unparseable request, rejected rewrite, dead
  upstream, dead classifier — degrades to "your original request, untouched" or
  the safe middle tier. The worst case is *not routed*, never *broken*.
- **One policy file.** All routing behavior lives in `~/.whittle/router.json` —
  no DSL, no database, no learned state at runtime. You can read the file and
  predict every decision; the per-request log tells you which rule fired and why.
- **Compute where the data lives.** ML models stay in the Python sidecar
  (whittle's Go binary has zero external dependencies); the sidecar returns raw
  scores and distributions; the **policy owns every threshold**. Swapping a model
  never changes the decision logic, and the decision logic never hides in a model.
- **Uncertainty falls to the middle, not the top.** This router optimizes cost
  subject to a quality floor. When a signal can't tell what a request is, the
  request rides the default tier — it is never escalated *because* we're unsure
  (see §5.1 for how that property falls out of the math).

## 3. Request lifecycle

```
Claude Code ──ANTHROPIC_BASE_URL──▶ whittle route (127.0.0.1:45873)
                                        │
                     ┌──────────────────┼──────────────────────────────┐
                     │ 1 EXTRACT   body → signals (tokens, tool-loop,  │
                     │              requested model, recent user text) │
                     │ 2 DECIDE    pin header → route waterfall →      │
                     │              default   (ML leaves lazy, §5)     │
                     │ 3 RECONCILE rewrite model + strip features the  │
                     │              target can't serve (§6)            │
                     │ 4 FORWARD   stream response back, per-chunk     │
                     │              flush; classify failures (§7)      │
                     └──────────────────┬──────────────────────────────┘
                                        ▼
                              api.anthropic.com   (your credentials,
                                                   forwarded untouched)
   ML signals, on demand:  sidecar :45872  /v1/route/{domain,embedding,complexity}
```

Walkthrough of one request:

1. **Extract.** The body is parsed once into `Signals`: estimated context tokens
   (whole body ÷ 4), message count, tool-loop shape, the requested model
   (canonicalized — dated snapshots match bare ids), and the recent user text
   (`inspect` window; tool results and system prompt excluded from keyword/ML
   text).
2. **Decide.** A pin header (if configured) wins outright. Otherwise the routes
   run top-down, **first match wins**; if nothing matches, the static `default`
   tier applies. Inside a route's condition tree, cheap heuristic leaves evaluate
   before ML leaves and short-circuit — a keyword match never pays for a
   classifier call. Each ML signal is computed at most once per request.
3. **No-op check.** If the chosen tier's model *is* the requested model, the
   request is forwarded **byte-for-byte** — no rewrite, no reconciliation, no
   risk. Guards also keep the original when the target is entitlement-blocked or
   its context window can't fit the request.
4. **Reconcile** (§6) and **forward**, streaming the response with per-chunk
   flush (SSE reaches the client immediately; `Accept-Encoding: identity`
   upstream avoids the gzip-SSE framing hang).
5. **One structured log line** (§8) — verdict, models, token usage. Never prompt
   text.

## 4. The policy file

Strict JSON (unknown keys are load errors, never silently ignored), validated
with path-precise messages. A missing or invalid policy puts the router in
transparent passthrough — it never bricks Claude Code. `SIGHUP` hot-reloads; a
bad edit keeps the running policy.

```json
{
  "version": 1,
  "tiers": [
    {"name": "fast",  "model": "claude-haiku-4-5-20251001"},
    {"name": "main",  "model": "claude-sonnet-4-5-20250929"},
    {"name": "smart", "model": "claude-opus-4-8"}
  ],
  "default": "main",
  "inspect": {"scope": "recent_turns", "turns": 3},
  "signals": {
    "domains": [
      {"name": "quantitative", "categories": ["math", "physics", "chemistry"], "min_mass": 0.7},
      {"name": "high-stakes",  "categories": ["law", "health", "business", "economics"]}
    ],
    "complexity": [
      {"name": "reasoning", "threshold": 0.15,
       "hard": ["analyze the root cause of this failure", "..."],
       "easy": ["fix this typo", "..."]}
    ]
  },
  "routes": [
    {"name": "escalate",    "when": {"any": [
        {"complexity": "reasoning:hard"},
        {"domain": "quantitative"}]},                  "to": "smart"},
    {"name": "de-escalate", "when": {"all": [
        {"complexity": "reasoning:easy"},
        {"not": {"domain": "high-stakes"}}]},          "to": "fast"}
  ],
  "session":   {"sticky": false},
  "overrides": {"pin_header": "x-whittle-route"}
}
```

- **`tiers`** — ordered cheap → capable; the order defines band rank. Use the
  full dated model ids your account accepts (`whittle policy init` auto-detects
  them from your Claude Code config; bare ids often 404).
- **`default`** — the tier for everything no route claims. The safe, capable
  middle is the recommended posture.
- **`routes`** — the ordered waterfall. Each `when` is a recursive boolean tree:
  combinators `all` / `any` / `not` (one per node), leaves one-per-node:

  | leaf | fires when |
  |---|---|
  | `context_tokens`, `message_count` | numeric band: scalar (`= n`) or `{gt,gte,lt,lte}` |
  | `tool_loop`, `has_tools` | request shape booleans |
  | `keywords` | literal, case-insensitive, **whole-word/phrase** (§5.4) |
  | `keywords_regex` | explicit RE2, opt-in |
  | `requested_model` | membership, canonicalized both sides |
  | `domain` / `embedding` / `complexity` | named ML signals (§5) |

  `to` is a tier name, or `keep` (hold the session's current tier).
- **`signals`** — named ML tests, defined once, referenced by any number of
  leaves, computed at most once per request.
- **`overrides.pin_header`** — a per-request escape hatch: send
  `x-whittle-route: smart` and routing is bypassed.

Author loop: `whittle policy init [name]` writes a built-in preset with your
detected model ids; `whittle policy validate <file>` runs the real loader and
prints errors and cost-lint warnings.

## 5. Signals

Two small models power three signals. Both run in the sidecar; the Go engine
holds only thresholds. If the sidecar is down or smart mode is off, ML leaves
evaluate false and the keyword/context heuristics still route (a route whose
match *depended* on an errored signal never fires — so a `not` over an
unavailable signal can't invert fail-open; the log gains an `ml-degraded` tag).

### 5.1 `domain` — subject classification with mass thresholding

A 14-category classifier (MMLU-Pro taxonomy: math, physics, law, health,
computer science, other, …) returns its **full softmax distribution**, and the
leaf fires iff the **total probability mass on the signal's categories** clears
`min_mass`:

```
fire  ⟺  Σ p(c) for c in categories  ≥  min_mass
```

One scalar does the work that otherwise takes an uncertainty ladder:

- *Confident in-set* (`p(math)=0.95`) → passes.
- *Split across in-set* (`p(math)=0.4, p(physics)=0.4`) → 0.8 passes, with no
  top-2 special case — mass is invariant to which in-set category won.
- *Ambiguous* (flat distribution) → mass ≈ |set|/14 → fails → the request rides
  the default tier. **Uncertainty lands in the middle, never on the expensive
  tier** — the cost-first safety default is a property of the arithmetic, not a
  rule.

Without `min_mass`, the leaf falls back to argmax membership (also the graceful
path when the sidecar predates distributions).

### 5.2 `complexity` — contrastive difficulty

The policy provides two banks of exemplar phrasings. The signal embeds the
request text and both banks, scores each bank as
`0.75·best-cosine + 0.25·mean(top-2)`, and takes the margin:

```
margin = score(hard bank) − score(easy bank)
margin >  t  → "hard"      margin < −t  → "easy"      else "medium"
```

A leaf names a level: `"reasoning:hard"`. Medium fires neither direction — the
broad middle rides the default. Empirically, `domain` and `complexity` have
complementary failure modes (a theorem proof reads "medium" to the exemplars but
classifies math at 0.98; a misclassified equation still scores complexity-hard),
which is why the shipped default ORs them for escalation.

### 5.3 `embedding` — similarity to your own examples

Bank score of the request text against a candidate phrase list, thresholded.
Useful for one specific, high-value shape (e.g. architecture/design asks in the
`coding` preset). Note the embedding space has a high similarity floor
(unrelated sentences score ~0.35–0.4), so thresholds live in a narrow band —
probe before trusting a new candidate list.

### 5.4 `keywords` — whole-word literals

Case-insensitive whole-word/phrase matching: an occurrence embedded in a larger
alphanumeric run does not match (`migration` never fires on *immigration*,
`refactor` never on *refactored* — list the variants you want). Boundaries are
non-alphanumeric runes or string edges, so `c++` works. Keywords are the
smart-off fallback and the zero-latency fast path; the ML signals carry the
nuance.

## 6. Capability reconciliation

Down-routing is only safe if the cheaper model can accept the request. Requests
from a stronger model routinely carry features a cheaper one rejects with a 400:
long-context betas, extended-thinking config **and** its dependent beta tokens
**and** `context_management` edits that require thinking, effort parameters,
mid-conversation `system` messages, thinking blocks in history.

The reconciler is a **blocklist**: forward everything, strip only what the
target is known to reject. Unknown model *families* are assumed fully capable
(forward untouched, let the retry safety net catch a real 400); unknown
*versions* of a known family get the family's conservative floor — over-stripping
on a down-route is harmless, under-stripping is a guaranteed 400. Stripping
whole messages can break user/assistant alternation, so a repair pass coalesces
adjacent same-role messages afterward. Every strip is named in the log
(`stripped:context-1m+thinking+…`).

## 7. Failure model

Three failure modes, deliberately distinct:

| mode | trigger | behavior |
|---|---|---|
| **A** | *our* error (unparseable body, reconcile bug) | forward the **original** request untouched |
| **B** | upstream rejects our **rewrite** (400/403) | retry the **original** once, relay that; a genuine 403 `permission_error` blocks the tier (TTL-bounded); the log names the rejected model and upstream error |
| **C** | transport failure | synthetic 502 — never a hang |

Plus the cheap safety paths: **no-op** (target = requested → byte passthrough),
oversized bodies stream through unbuffered and untouched, non-`/v1/messages`
traffic passes through blind, and `GET /health` answers locally. The commit
point is held until the upstream status is known, so a Mode-B retry never
double-writes the client.

The design bias: Mode B means a reconciliation gap costs one extra round-trip,
not a user-visible failure — and the log line makes the gap loud instead of
silent.

## 8. Observability

Exactly one JSON line per request, never prompt text:

```json
{"tier":"main", "requested":"claude-opus-4-8", "model":"claude-sonnet-4-5-20250929",
 "reason":"default stripped:context-1m+thinking", "status":200, "latency_ms":2060,
 "ctx_tokens":25640, "in_tokens":1974, "out_tokens":16, "session":"c2f9659c"}
```

`requested` vs `model` is what routing changed; `in_tokens`/`out_tokens` are the
response's real usage — enough to compute per-request savings offline:
`cost(requested, in, out) − cost(model, in, out)`. `reason` names the exact rule
that fired, every stripped feature, and any retry (`mode-b:…(rewrote→X got 400
invalid_request_error: …)`), so misroutes and capability gaps are one-line
diagnoses. The same verdict rides response headers (`x-whittle-route`,
`x-whittle-reason`).

## 9. Codemap

All router code lives in `router/` (Go, stdlib only). The ML lives in `model/`
(Python sidecar, shared with the compression hook).

| file | role |
|---|---|
| `router/policy.go` | policy types, signal catalog, canonicalization, load |
| `router/rule.go` | the recursive condition-tree grammar |
| `router/validate.go` | structural + referential validation, cost lints |
| `router/signals.go` | request body → `Signals` extraction |
| `router/engine.go` | `Decide`: waterfall, cheap-first eval, memoized ML leaves, keyword matching |
| `router/caps.go` | model capability table + family floors |
| `router/reconcile.go` | feature strips + alternation repair |
| `router/proxy.go` | HTTP handler: lifecycle, modes A/B/C, streaming, log line |
| `router/server.go` | daemon entrypoint, hot-reload, smart-mode wiring |
| `router/ml/client.go` | HTTP client to the sidecar (fail-open) |
| `router/policies/*.json` | built-in presets (`default`, `coding`, `heuristic`) |
| `model/route.py` | the two models + exact scoring math (pure functions, stub-tested) |
| `cmd/whittle/route.go`, `policy.go` | CLI: `route`, `policy init/validate/…` |

Invariants worth knowing before changing anything: the router package has **no
external Go dependencies**; handlers never see credentials as data (headers are
forwarded, not read); prompt text never reaches a log; every ML call is
fail-open; validation rejects what it doesn't understand rather than ignoring it.

## 10. Prior art & credits

The ML layer stands on [vLLM Semantic Router](https://github.com/vllm-project/semantic-router)
(Apache-2.0), and we want to be precise about the debt:

- **Models**: we use their two trained models directly —
  `llm-semantic-router/mmbert32k-intent-classifier-merged` (the MMLU-Pro domain
  classifier) and `llm-semantic-router/mmbert-embed-32k-2d-matryoshka` (the 32k
  text embedder).
- **Math**: the bank-score blend (`0.75·best + 0.25·mean(top-2)`) and the
  contrastive hard/easy margin are their mechanisms, replicated exactly.
- **Ideas**: the signal taxonomy (domain / embedding-similarity / complexity as
  composable routing inputs) and the insight that per-category routing should be
  grounded in measured accuracy-vs-cost (their reasoning benchmark) shaped this
  design.

The architecture around those pieces is our own and intentionally much smaller:
a single-file boolean policy instead of a category→model-score projection
system, probability-mass thresholding instead of an entropy decision ladder
(§5.1 — including inverting their uncertain→escalate default to
uncertain→middle, which is the right direction for a cost-first router),
capability reconciliation and the A/B/C fail-open model for rewriting real
Anthropic traffic, and per-request cost observability. Where we diverge, it's
deliberate; where we borrowed, it's named here.

## 11. Honest limitations & roadmap

- **Phrasing is not the task.** Every prompt-side signal judges the *wording*.
  "Improve this" over a 10-line file and over a distributed system read
  identically. Artifact-aware signals (actual context/diff size — already
  visible to the proxy) are the designed next step.
- **Category is not difficulty.** A confidently-classified trivial question in
  an escalating domain ("what is the boiling point of water" → chemistry 0.94)
  over-escalates. Accepted for now: bounded by `min_mass`, rare in real traffic,
  and the same trade vSR's measured config makes for math.
- **The parameters are hand-authored.** The mechanisms are trained models; the
  thresholds, prototype banks, and category sets were chosen by probing, not
  calibrated against labeled data. The roadmap is a **tier-sufficiency dataset**:
  label real traffic with the *minimal sufficient tier* (replay through tiers,
  judge blindly), then tune the category map, `min_mass`, complexity threshold,
  and prototype banks against measured precision/recall — optimizing dollars
  saved subject to a quality floor. Notably, no public benchmark measures
  *coding* difficulty (vSR's reasoning bench is QA/math/science only), so the
  coding half of that dataset has to come from real sessions.
- **Session stickiness is minimal.** With three tiers, band-jump damping is
  nearly inert; the shipped presets disable it. A "hold the strong tier for N
  turns after a hard trigger" latch is the likely replacement.
