# Whittle Router — policy schema (v4, vSR-aligned)

Status: **implemented.** The routing policy: `signals` (named ML signals) +
`routes` (explicit boolean rules referencing them) + condition grammar. v4
(2026-07-09) replaces the invented `classify` "smart default" tier-picker with
vLLM Semantic Router's signal model — the ML lives *inside* route conditions as
boolean leaves, not a separate fallback stage (founder call: "use what vSR uses,
don't invent"). v3 renamed `guards`→`routes`; v2 folded the code-review. The
"Revision log" at the end maps findings to resolutions. Extends `ROUTER_DESIGN.md`
§2.2/§2.3.

---

## 0. Decision precedence — how `routes` and `signals` relate

There is **one ordered resolution**; each rung is the fallback for the one
above. This is the answer to "which wins":

```
1. pin header (overrides.pin_header)  explicit user override   → always wins
2. routes (ordered list)              your explicit rules      → FIRST MATCH wins
3. default    (static tier)           the STATIC fallback      → when NO route matched
   (session stickiness then damps a fuzzy default downgrade; see §8)
```

**There is no separate "classify" stage.** `signals` are not a routing rung —
they are *inputs* the routes evaluate. A `domain`/`embedding`/`complexity` leaf
inside a route's `when` is a boolean test computed from an ML model; the route
fires (or not) like any other condition. So the ML intelligence lives *in the
waterfall*, composable with heuristic leaves (`context_tokens`, `tool_loop`,
`keywords`), not bolted on as a competing fallback. This mirrors vSR exactly: its
`decisions` (our routes) reference typed signals; the "default" is just the last
rule. (Earlier drafts had a `classify` block that embedded per-tier examples and
argmax-picked a tier — that conflated topic with difficulty and could not compose
with other conditions; it was removed. See the Revision log.)

**Weighted scoring, if ever added, is a signal too** — a scored leaf that
thresholds inside a route (`when score(X) ≥ t → to Y`). A route stays boolean; a
boolean can only *threshold* a score, never route to the highest-scoring tier.
Nothing is reserved on `Route`.

---

## 1. The shape decision

A condition is a **recursive boolean tree**. Each node is EITHER a **leaf**
(one predicate on one signal) or a **combinator** (`all` / `any` / `not`).
Operator-as-key (the combinator is a YAML map key), not positional:

```yaml
any:
  - all:
      - context_tokens: { gt: 60000 }
      - tool_loop: true
  - keywords: [architecture, migrate]
```

**v2 decision — no implicit AND.** A leaf node holds **exactly one predicate**;
all conjunction is written explicitly with `all`. v1 allowed several leaf keys
in one node to mean AND, which created two ways to write AND and — combined with
"a one-element `any` is legal" — let a single mis-indentation silently flip an
OR into an AND with no error (review B2/H5). Explicit-only makes a two-key leaf
node an immediate validation error, so that trap becomes loud instead of silent.
More verbose, uniform, teachable.

**Naming (v3):** the rule list is `routes:` (was `guards:` — "guard" reads as
blocking, wrong for a router), and each route's target field is `to:` (was
`route:`, which collided with the list name). Reads as plain English: *"when
context_tokens > 60k, route `to: smart`."* The **route waterfall** is ordered,
first-match-wins (like nginx location blocks); each route's `when` is a condition
tree.

```go
type Route struct {
    Name string `yaml:"name"`
    When Rule   `yaml:"when"`   // the condition tree (§2)
    To   string `yaml:"to"`     // tier name | "keep" (reserved keyword)
}
```
(A per-route `sticky` override is **deferred to the stickiness milestone** — in
v1 explicit routes always win and are never damped, so it would be a
load-accepted field that does nothing. A stray `sticky:` on a route is rejected,
not silently ignored.)

---

## 2. Go representation

```go
// Rule is one node. EXACTLY ONE of: a single leaf predicate, All, Any, or Not.
type Rule struct {
    All []Rule `yaml:"all,omitempty"`
    Any []Rule `yaml:"any,omitempty"`
    Not *Rule  `yaml:"not,omitempty"`

    // ---- leaf predicates: a valid leaf sets EXACTLY ONE of these ----
    ContextTokens  *NumBand `yaml:"context_tokens,omitempty"`
    MessageCount   *NumBand `yaml:"message_count,omitempty"`
    ToolLoop       *bool    `yaml:"tool_loop,omitempty"`
    HasTools       *bool    `yaml:"has_tools,omitempty"`
    Keywords       []string `yaml:"keywords,omitempty"`        // LITERAL whole-word/phrase, case-insensitive
    KeywordsRegex  []string `yaml:"keywords_regex,omitempty"`  // explicit regex, when you mean it
    RequestedModel []string `yaml:"requested_model,omitempty"` // membership (canonicalized both sides)

    // ---- ML signal leaves: each NAMES a signal defined in Policy.Signals (§7),
    //      evaluated lazily via the classifier, memoized once per request ----
    Domain     string `yaml:"domain,omitempty"`     // domain signal fires: classifier label ∈ its categories
    Embedding  string `yaml:"embedding,omitempty"`  // embedding signal fires: bank score ≥ its threshold
    Complexity string `yaml:"complexity,omitempty"` // complexity level "name:hard|easy|medium"
}

// NumBand: a numeric predicate. Custom UnmarshalYAML accepts EITHER a bare
// scalar (message_count: 1  ⇒  Eq=1) OR a mapping (context_tokens: {gte: 60000}).
// This is the ONLY custom unmarshaler; Rule itself stays plain-struct.
type NumBand struct{ Eq, Gt, Gte, Lt, Lte *int }
```

- **Three ML signal leaves, two models (vSR-aligned).** `domain` uses the intent
  classifier (one MMLU-Pro label); `embedding` and `complexity` use the text
  embedding model. A leaf holds the signal's *name* (defined in §7), not inline
  data, so one signal is authored once and referenced by many routes and computed
  at most once per request. `complexity` additionally carries the level after a
  colon (`needs_reasoning:hard`). Replaces the old single `intent:[labels]` leaf.
- **`keywords` is literal, case-insensitive, WHOLE-WORD/PHRASE** (v5): an
  occurrence embedded in a larger alphanumeric run does not match — "migration"
  never fires on "immigration", "refactor" never fires on "refactored" (list the
  variants you want). Boundaries are non-alphanumeric runes or string edges, so
  "c++" still matches. Chosen over bm25/ngram/fuzzy (vSR's methods) from first
  principles: within-word collision was the observed failure class and boundary
  matching fixes it with zero config; the residual keyword failure
  (phrase-in-larger-context) is inherent to keywords and is mitigated by the ML
  signals + route ordering, not by fuzzier matching. Previously (review H6): a coder
  typing `["c++"]` or `["a.b"]` must not hit a regex-metachar explosion or a
  silent over-match. Regex is opt-in via `keywords_regex`.
- **No `Score` field.** v1 reserved a `score:` leaf; removed (review H4).
  Weighted scoring arrives as a `WeightedScorer` *classify strategy* (a decision
  mode that argmax-routes by score), per `ROUTER_DESIGN.md` §2.3 — NOT as a
  boolean route leaf, because a boolean tree can only threshold a score, never
  route by highest. Reserving nothing beats reserving the wrong seam (§7).

---

## 3. Evaluation semantics

- **Boolean only** — a route's condition tree returns match / no-match; no confidence
  propagates up the tree (confidence lives in the classify/score layer). Avoids
  vllm-sr's confidence-averaging complexity.
- **`not` is unary.** Empty `all`/`any` rejected.
- **Cost is data-dependent — NOT a guarantee (corrected, review H1/H2).**
  Predicates are pure, so the evaluator reorders each node's children
  cheap-heuristics-first and short-circuits (`any` on first true, `all` on first
  false). This *reduces* classifier calls but does **not** guarantee zero: an
  `all` whose cheap child is usually-true, or an `any` whose cheap child is
  usually-false, still descends into an ML child. And reordering is **intra-tree
  only** — the route *list* is author-ordered and first-match-wins, so a bare
  `intent` route at the top of the waterfall runs the classifier on every
  request. The honest rule: cheap-first is a best-effort cost optimization; the
  cost lint (§4) is what actually protects you.

---

## 4. Validation (pure structural checks, applied RECURSIVELY at every node)

1. **Strict keys.** Loader uses `yaml.Decoder.KnownFields(true)` /
   `additionalProperties:false`. **Any unknown key is rejected** (review B1) —
   this is what catches a typo'd leaf (`keywrods:`) or signal that would
   otherwise be silently dropped and misroute.
2. **One shape per node.** Exactly one of: one leaf predicate, `all`, `any`,
   `not`. Zero keys → error. Two leaf keys → error (this is what makes the
   mis-indent B2 loud). Combinator + leaf → error. Two combinators → error.
   Checked at **every** node, including `all`/`any` elements and `not`'s child.
3. `all: []` / `any: []` → error. Single-element `all`/`any` → **warn** (usually
   a mis-indent, §5).
4. **NumBand sanity** (review M1): ≥1 bound set; `eq` exclusive of other bounds;
   `gt < lt`, `gte ≤ lte`; reject impossible/empty bands.
5. **Regex safety** (review C5): `keywords_regex` entries compile and are
   length/complexity-bounded (ReDoS guard) — they run per-request on user text.
   `keywords` (literal) are auto-quoted, never a ReDoS vector.
6. **Referential integrity**: every `route` tier and `default` exists in
   `tiers`; every `domain`/`embedding`/`complexity` leaf names a signal defined
   in `signals`; a `complexity` leaf's level is `hard|easy|medium`. `keep` is a
   **reserved keyword** — rejected as a tier name (review M5). Signal names are
   unique per kind.
7. **Depth cap = 6, hard** (review H3/L2): also serves as the recursion bound
   the parent's C5 asked for — this recursive tree **supersedes** C5's
   "single-level `Any`."
8. **Cost lint** (restores parent C3): warn on ANY route whose `when` references
   an ML signal leaf (`domain`/`embedding`/`complexity`) — it runs a model on
   every request that reaches it; place cheap routes above it.
9. Helpful errors for the two highest-frequency author mistakes: scalar where a
   list is expected (`keywords: architecture` → "wrap in `[ ]`"), and scalar
   where a NumBand mapping is expected.
10. **Signal candidate-list cap**: each `candidates`/`hard`/`easy` list rejects
    > **256** (hard) and warns > **32** (soft). Candidates are embedded once and
    cached (sidecar, by content hash); the cap bounds cold-start cost. Domain
    `categories` warn if not in the MMLU-Pro set; complexity `threshold` must be
    ≥ 0 (a symmetric margin band). Duplicate candidates within a list → warn.

---

## 5. Scenarios

### 5.1 Trivial — single leaf
```yaml
routes:
  - name: huge-context
    when: { context_tokens: { gt: 60000 } }
    to: smart
```

### 5.2 AND (explicit — the only way in v2)
```yaml
  - name: big-coding-task
    when:
      all:
        - intent: [coding]
        - context_tokens: { gt: 30000 }
    to: smart
```

### 5.3 OR
```yaml
  - name: obviously-hard
    when:
      any:
        - keywords: ["race condition", deadlock, "root cause"]
        - context_tokens: { gt: 80000 }
    to: smart
```

### 5.4 Nested (the OR[ leaf, AND[ leaf, OR[ leaf, leaf ]]] case)
```yaml
  - name: nested
    when:
      any:
        - requested_model: [claude-opus-4-8]
        - all:
            - tool_loop: false
            - any:
                - keywords: [architecture, migrate]
                - context_tokens: { gt: 100000 }
    to: smart
```

### 5.5 NOT + literal keywords (note `keywords` is literal here — safe for `def `, backticks)
```yaml
  - name: cheap-unless-code
    when:
      all:
        - context_tokens: { lt: 4000 }
        - not: { keywords: ["```", "def ", "func ", "class "] }
    to: fast
```

### 5.6 Realistic full policy
```yaml
version: 1
tiers:                                        # ORDERED cheap→capable; the order
  - { name: fast,  model: claude-haiku-4-5 }  # IS the band rank, so min_band_jump
  - { name: main,  model: claude-sonnet-5 }   # has defined meaning (was an unordered
  - { name: smart, model: claude-opus-4-8 }   # map → min_band_jump was undefined)
default: main
inspect: { scope: recent_turns, turns: 3 }   # keyword/signal text scans THIS window

signals:                                       # named ML signals, referenced by routes
  domains:                                     # intent classifier → MMLU-Pro label ∈ set
    - { name: coding, categories: [computer science, engineering] }
    - { name: math,   categories: [math] }
  embeddings:                                  # bank score ≥ threshold → fires
    - name: architecture_design
      threshold: 0.66
      candidates:
        - design a scalable architecture for this system
        - plan a migration strategy for a distributed system
        - compare tradeoffs between system designs and recommend one
  complexity:                                  # contrastive margin → hard|easy|medium
    - name: needs_reasoning
      threshold: 0.15
      hard: ["debug this race condition", "analyze the root cause of this failure", "solve this step by step"]
      easy: ["rename this variable", "fix this typo", "answer briefly"]

routes:
  - name: pinned-opus
    when: { requested_model: [claude-opus-4-8] }
    to: smart
  - name: background
    when: { requested_model: [claude-haiku-4-5] }
    to: fast
  - name: mid-tool-loop
    when: { tool_loop: true }
    to: keep
  - name: hard-work                            # ML leaves reached last, cost-linted
    when:
      any:
        - keywords: [migrate, "race condition", "root cause", architecture, refactor]
        - context_tokens: { gt: 60000 }
        - complexity: needs_reasoning:hard     # contrastive difficulty signal
        - embedding: architecture_design       # semantic similarity signal
    to: smart
  - name: coding                               # domain (subject) signal
    when: { any: [ { domain: coding }, { domain: math } ] }
    to: main

session:  { sticky: true, min_band_jump: 2 }
overrides: { pin_header: x-whittle-route }
```

### 5.7 First-turn equality via scalar shorthand
```yaml
  - name: first-turn
    when: { message_count: 1 }        # scalar ⇒ Eq=1
    to: main
```

### 5.8 Invalid — validation must reject (expanded)
```yaml
when: { any: [ {tool_loop: true} ], context_tokens: {gt: 10} }  # combinator + leaf
when: { all: [...], any: [...] }                                # two combinators
when: { any: [] }                                               # empty group
when: {}                                                        # empty node
when: { not: [ {tool_loop: true}, {has_tools: true} ] }         # not is unary
when: { keywords: [a], context_tokens: {gt: 5} }                # two leaf keys (no implicit AND)
when: { keywrods: [a] }                                         # unknown key (typo) — B1 catch
when: { context_tokens: {} }                                    # empty NumBand
when: { context_tokens: {gt: 100, lt: 50} }                     # impossible range
when: { any: [ {}, {tool_loop: true} ] }                        # empty child (recursive check)
```

---

## 6. Route-list OR vs in-route `any` — choose deliberately (review M7)

They are NOT interchangeable style:
- **Separate routes** produce distinct `matched-rule` names in the log (§3 obs),
  can each carry a different `Sticky`, and are **priority-ordered**
  (first-match-wins).
- **`any` branches** collapse to one route name, share one stickiness, and are
  unordered (reorderable for cost).

Use separate routes when you want different destinations, labels, or stickiness
per condition; use `any` when one destination is chosen by a compound OR.

---

## 7. The `signals` catalog (vSR-aligned)

Signals are named ML tests defined once and referenced by route leaves (§2).
Two models back them (hosted in the Python sidecar; the Go engine owns the
thresholds — *compute where the data lives*):

```yaml
signals:
  domains:      [{ name, categories: [<MMLU-Pro labels>] }]        # intent classifier
  embeddings:   [{ name, threshold, candidates: [<phrases>] }]     # embedding model
  complexity:   [{ name, threshold, hard: [<phrases>], easy: [<phrases>] }]  # embedding model
```

- **`domain`** — supports two forms. With `min_mass` (0 < m ≤ 1): fires iff the
  classifier's total softmax MASS on `categories` ≥ `min_mass` — one scalar that
  subsumes entropy handling (vSR routes reasoning via an entropy ladder over the
  same distribution; thresholding in-set mass is formally equivalent at every
  branch we need, minus vSR's accuracy-first "very-uncertain → escalate" override,
  which is wrong for a cost-first router: here an ambiguous distribution simply
  fails the threshold and falls to the default middle tier). It is also invariant
  to which in-set category won (math 0.4 + physics 0.4 clears 0.7 with no top-2
  special case). Without `min_mass`: argmax label ∈ categories (legacy; also the
  graceful fallback when the sidecar returns no distribution). The classifier
  (ModernBERT, 14 MMLU-Pro categories:
  biology, business, chemistry, computer science, economics, engineering, health,
  history, law, math, other, philosophy, physics, psychology) predicts one label;
  the leaf fires when that label ∈ `categories`. Unknown categories warn (a
  swapped model could differ), never error.
- **`embedding`** — bank score of the query against `candidates`, fires when
  `score ≥ threshold`. The bank score is vSR's exact blend:
  `0.75·best + 0.25·mean(top-2)` over per-candidate cosine similarities. Cosine
  lands ~0.3–0.5 for related phrasing, so a `threshold` near 0.4–0.66 is typical.
- **`complexity`** — contrastive difficulty. `margin = bank_score(hard) −
  bank_score(easy)`; the leaf `name:hard` fires when `margin > threshold`,
  `name:easy` when `margin < −threshold`, `name:medium` otherwise. This isolates
  *difficulty* from *topic* (unlike a plain nearest-example scheme), which is the
  whole point of tier routing.

Each signal is computed **at most once per request**, lazily (only if a route
actually reaches its leaf — cheap heuristic siblings evaluate first), and the
sidecar caches candidate embeddings across requests by content hash + model
version, so re-embedding the static `candidates`/`hard`/`easy` is amortized.

**Future — weighted scoring** stays a signal, not a route mode: a scored leaf
that thresholds inside a route (`when score(X) ≥ t`). Routes remain boolean; a
boolean can only threshold a score, never route by *highest*. No field is
reserved on `Route`.

---

## 8. Revision log (v1 review → v2 resolution)

| finding | v2 |
|---|---|
| B1 unknown-key silent drop; `domain` not in struct | §4.1 `KnownFields(true)`; removed `domain`, `intent` is the one classifier signal |
| B2/H5 mis-indent OR→AND via implicit-AND | §1 dropped implicit AND; §4.2 two-leaf node = error; §4.3 warn single-child group |
| H1/H2 cheap-first overclaimed | §3 rewritten: data-dependent, intra-tree; §4.8 restores C3 top-of-waterfall warn |
| H3 recursive tree vs C5 single-level `Any` | §4.7 explicit supersession; depth cap = recursion bound |
| H4 Score seam bakes wrong assumptions | §2 removed `Score`; §7 scoring is a classify strategy |
| H6 keywords = regex, `c++` breaks | §2 `keywords` literal+case-insensitive; `keywords_regex` opt-in |
| H7 no equality/scalar shorthand | §2 NumBand `Eq` + scalar-⇒-Eq unmarshal; §5.7 |
| M1 NumBand nonsense | §4.4 sanity checks |
| M2 validation recursion unstated | §4 "applied recursively at every node" |
| M3 keyword window/case unstated | §2 case-insensitive; §5.6 scans `inspect` window |
| M4 requested_model canonicalization | §2 canonicalized both sides (gated on parent O5) |
| M5 `keep` magic string | §4.6 reserved keyword; no-session → `default` (still gated on GATE-2) |
| M6 scalar-not-list hard error | §4.9 helpful hint |
| M7 route-list vs `any` "cosmetic" | §6 documents the real differences |
| L1 `not:[list]` cryptic error | §4.9 helpful hint |

**Open for founder/next turn:**
- (a) **Flatten the tree?** (holistic review, judgment call) Real hand-authored
  policies are shallow; IAM/Envoy/k8s/claude-code-router all use flat
  implicit-AND + OR-via-multiple-rules, no deep nesting — and §6 already argues
  separate routes often beat a nested `any`. Option: a route's `when` is a flat
  implicit-AND list + a single non-nestable `any` for OR, dropping the recursive
  tree (or keeping it only as a capped escape hatch). Simpler for the 95% case;
  cost = reopens the v2 "no implicit AND" decision and can't express a deeply
  nested route as one node. **Founder's call.**
- (b) **Does `classify` ship in v1 at all,** or do `routes` + static `default`
  ship first (the true MVP), with `classify` sequenced behind GATE-1/2 passing?
- (c) `keep`'s dependence on the session store — ship only after the
  subagent/`--resume`/`/compact` session-id behavior is verified.
