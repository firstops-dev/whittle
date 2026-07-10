# Writing a whittle routing policy

This is the task guide for authoring `~/.whittle/router.json`: the mental model,
every condition you can write with its exact JSON, the signals and how to tune
them, copy-paste recipes, and how to debug a policy from the router log. For the
architecture and the signal math behind all of this, see
[docs/ROUTER.md](ROUTER.md); this page is how to write the file, that page is how
and why it works.

Every JSON example below is validated against the real loader
(`whittle policy validate`). If an example errors, that is a bug in this doc.

## Mental model in ten lines

1. You define **tiers**: named models, ordered cheap to capable.
2. Every request is matched against an ordered list of **routes**.
3. Each route has a `when` condition and a `to` tier. **First match wins.**
4. If no route matches, the **`default`** applies.
5. `default: "requested"` means "keep the model the client asked for" (a no-op).
   A tier name as the default down-routes everything unmatched to that tier.
6. A route condition is a boolean tree of **leaves** (keywords, token counts,
   ML signals) combined with `all` / `any` / `not`.
7. Cheap heuristic leaves (keywords, counts) cost nothing. **ML leaves**
   (`domain`, `complexity`, `embedding`) run a local model.
8. An ML signal that cannot answer (sidecar down, smart mode off) makes its leaf
   **false**, and its route will not fire. Routing always falls open.
9. You reload a changed policy with `SIGHUP`; a bad edit keeps the live policy.
10. Every request logs which route fired and every signal value, so you can see
    exactly why it routed.

## The file at a glance

```json
{
  "version": 1,
  "tiers": [
    {"name": "fast",  "model": "claude-haiku-4-5-20251001"},
    {"name": "main",  "model": "claude-sonnet-4-5-20250929"},
    {"name": "smart", "model": "claude-opus-4-8"}
  ],
  "default": "requested",
  "inspect": {"scope": "recent_turns", "turns": 3},
  "signals": { "domains": [], "embeddings": [], "complexity": [] },
  "routes": [
    {"name": "example", "when": {"keywords": ["refactor"]}, "to": "smart"}
  ],
  "session":   {"sticky": false},
  "overrides": {"pin_header": "x-whittle-route"}
}
```

| field | required | what it is |
|---|---|---|
| `version` | yes | must be `1`. |
| `tiers` | yes | at least one `{name, model}`, ordered cheap to capable. |
| `default` | yes | a tier name, or the reserved `"requested"`. |
| `inspect` | yes | `{scope, turns}`; bounds what text signals see (below). |
| `routes` | no | the ordered waterfall; an empty list means "always default". |
| `signals` | no | named ML signals your routes reference by name. |
| `session` | no | stickiness; off by default. |
| `overrides` | no | the pin header name. |

Strict decoding: an unknown or misspelled key is a load error, never silently
ignored. The smallest valid policy is:

```json
{
  "version": 1,
  "tiers": [{"name": "main", "model": "claude-sonnet-4-5-20250929"}],
  "default": "requested",
  "inspect": {"scope": "last_user_turn"}
}
```

That policy routes nothing (it always keeps the requested model). It is the safe
starting skeleton you add routes to.

## Tiers and the default

`tiers` order is meaningful: index 0 is the cheapest band, the last is the most
capable. Session stickiness and the context-window guard use this rank, so keep
the list ordered cheap to capable even if you never use those features.

Use the **full dated model id** your account accepts (`claude-sonnet-4-5-20250929`,
not `claude-sonnet-4-5`). Bare aliases are often rejected upstream with a 4xx.
`whittle policy init` fills these in by reading the ids your Claude Code config
actually sends; verify them before trusting a hand-edited id.

`default` is where a request goes when no route claims it:

- **`"requested"`** (the shipped posture): keep the model the client asked for,
  untouched. With zero evidence about a request, whittle does not rewrite it, so
  every model change (up or down) traces to a route you wrote. This also protects
  mixed-model clients: Claude Code's cheap background requests are never
  up-routed by a fixed default.
- **A tier name** (for example `"main"`): down-route everything unmatched to that
  tier. This is the single biggest cost lever, and a quality trade: traffic you
  did not explicitly route now rides that tier. Pair it with guard routes that
  push hard or sensitive work back up (see the aggressive-savings recipe).

## Routes: the waterfall and the condition grammar

`routes` is an ordered list. For each request the engine tries routes top to
bottom and the **first** whose `when` matches decides the tier. Order your routes
cheap-first and specific-first: put fast keyword routes above ML routes so a
keyword match never pays for a classifier, and put narrow routes above broad ones.

Each route is `{name, when, to}`:

- **`name`**: appears in the log as `route:<name>`. Name every route; an unnamed
  route is logged by index and validation warns.
- **`to`**: a tier name, or the reserved **`"keep"`** (below).
- **`when`**: a condition tree.

### The condition tree

A `when` node is **either** one combinator **or** one leaf, never both, never two
of either. The combinators:

| combinator | JSON | matches when |
|---|---|---|
| `all` | `{"all": [A, B]}` | every child matches (AND) |
| `any` | `{"any": [A, B]}` | at least one child matches (OR) |
| `not` | `{"not": A}` | the single child does **not** match |

`not` takes a **single** node, not a list. `all` and `any` take a list.

Nesting is allowed up to 6 levels deep. There is **no implicit AND**: two leaves
in one node is an error, wrap them in `all`.

```json
{"all": [
  {"domain": "casual"},
  {"not": {"has_tools": true}},
  {"any": [
    {"complexity": "reasoning:easy"},
    {"keywords": ["typo", "rephrase"]}
  ]}
]}
```

A gotcha with `not` and ML signals: if the child leaf's signal is unavailable
(sidecar down, smart mode off), the child is false, but the route still will not
fire on the inverted result. An unavailable signal never causes a route to fire,
directly or through `not`. This is deliberate fail-open behavior (see ROUTER.md
§5), so you cannot accidentally up-route every request when the sidecar is down.

### `to: "keep"`

`keep` holds the session's current tier instead of naming a fixed one. It is only
meaningful with session tracking: on the first request of a session there is
nothing to keep, so `keep` falls to the `default`. `keep` never changes the stored
tier. It is a niche tool for "do not move this session"; most policies do not need
it. Stickiness in v1 is minimal (see ROUTER.md §11), so treat `keep` as advanced.

## Leaf reference

Every leaf, its exact JSON, its matching rule, and when to reach for it. A leaf is
one key on a `when` node.

### `keywords` (literal, whole-word)

```json
{"keywords": ["refactor", "race condition", "c++"]}
```

- **Matches**: case-insensitive, whole-word or whole-phrase, over the `inspect`
  text window. An occurrence inside a larger alphanumeric run does not match:
  `migration` does not fire on `immigration`, `refactor` does not fire on
  `refactored`. List the variants you want. Word boundaries are any
  non-alphanumeric character or the string edge, so `c++` matches in `use c++`.
- **Use when**: you want a zero-latency, deterministic route for terms specific to
  your work. Keywords are also the fallback when smart mode is off, so keyword
  routes keep working with no sidecar.

### `keywords_regex` (RE2, opt-in)

```json
{"keywords_regex": ["\\bTODO\\b", "fix ?me"]}
```

- **Matches**: Go's RE2 (linear-time, no catastrophic backtracking) against the
  `inspect` window text. Patterns are capped at 512 characters; an invalid
  pattern is a load error.
- **Use when**: whole-word `keywords` is not expressive enough. Prefer `keywords`
  for the common case; regex is the escape hatch.

### `context_tokens` (numeric band)

```json
{"context_tokens": {"gte": 120000}}
```

- **Matches**: the estimated context size of the whole request (raw body bytes
  divided by 4). This is the cost and prompt-cache scale; a floor around 20k is
  normal even for a short prompt because the system prompt and tools count.
- **Shape**: a bounds object with any of `gt`, `gte`, `lt`, `lte` (combine a
  lower and an upper for a range), or a bare number as a shorthand for equality
  (`{"context_tokens": 60000}` means exactly 60000, rarely what you want here).
  `eq` cannot combine with the bounds. A quoted string is an error.
- **Use when**: you want large requests on a stronger tier without any ML. This is
  a heuristic proxy for difficulty (bigger context is often harder), cheap and
  deterministic.

### `message_count` (numeric band)

```json
{"message_count": {"lte": 2}}
```

- **Matches**: the number of messages in the request. Same band shape as
  `context_tokens`; the scalar shorthand is handy here (`{"message_count": 1}`
  means the very first turn).
- **Use when**: you want to treat the opening turn differently from a deep
  session, or gate on conversation depth.

### `tool_loop` (boolean shape)

```json
{"tool_loop": true}
```

- **Matches**: the last message is `role:user` and carries a `tool_result` block,
  meaning this request is an agent continuing a tool loop (not a fresh human
  turn). `{"tool_loop": false}` matches when it is not. This is a precise
  predicate, not "any tool was ever used".
- **Use when**: you want tool-loop iterations to ride a different tier than
  human-authored turns.

### `has_tools` (boolean shape)

```json
{"has_tools": false}
```

- **Matches**: the request declares tools. `{"has_tools": false}` matches a
  request with no tools (often plain chat). The boolean is compared for equality,
  so `false` is a real predicate, not "ignore this leaf".
- **Use when**: tool-enabled requests and plain-chat requests should route
  differently.

### `requested_model` (membership)

```json
{"requested_model": ["claude-opus-4-8"]}
```

- **Matches**: the model the client asked for is in the list. Both sides are
  canonicalized (a trailing date snapshot and `-latest` are stripped), so
  `claude-opus-4-8` matches an incoming `claude-opus-4-8-20260101`.
- **Use when**: you want behavior that depends on what the client requested, for
  example "only rewrite requests that came in on opus".

### `domain` (ML: subject classification)

```json
{"domain": "quantitative"}
```

- **Matches**: the named domain signal (defined under `signals.domains`) fires.
  See the signals section for the definition and its `min_mass` semantics.
- **Use when**: you want to route by subject (math, law, health, casual chit-chat)
  rather than by wording.

### `complexity` (ML: difficulty level)

```json
{"complexity": "reasoning:hard"}
```

- **Matches**: the named complexity signal resolves to the given level. The ref is
  `signal_name:level` where level is `hard`, `easy`, or `medium`. `medium` is a
  real, matchable level (the band between hard and easy), not a fallback.
- **Use when**: you want to route by how hard the request reads, independent of
  its subject.

### `embedding` (ML: similarity to your examples)

```json
{"embedding": "design-ask"}
```

- **Matches**: the named embedding signal's similarity score against your
  candidate phrases clears its threshold.
- **Use when**: you have one specific, high-value request shape you can describe
  with a handful of example sentences.

## Signals: define once, reference by name

ML leaves (`domain`, `complexity`, `embedding`) do not carry their own parameters.
You define a named signal under `signals`, then reference it by name from any
number of leaves. Each signal is computed at most once per request, and only if a
route actually reaches it.

Two small models power all three signals, both in the sidecar `whittle setup`
installs at `127.0.0.1:45872`. The engine holds only your thresholds. If the
sidecar is absent, smart mode is simply off and ML leaves evaluate false; if it is
present but broken, the request is tagged `ml-degraded` in the log.

### `domains`: subject classification with mass thresholding

```json
"signals": {
  "domains": [
    {"name": "quantitative", "categories": ["math", "physics", "chemistry"], "min_mass": 0.7},
    {"name": "casual",       "categories": ["other"], "min_mass": 0.85}
  ]
}
```

- **`categories`**: one or more MMLU-Pro categories. The classifier only ever
  emits these 14 labels: `biology`, `business`, `chemistry`, `computer science`,
  `economics`, `engineering`, `health`, `history`, `law`, `math`, `other`,
  `philosophy`, `physics`, `psychology`. A category outside this set is inert
  (validation warns: the classifier will never emit it).
- **`min_mass`** (0 to 1): the leaf fires when the **total** softmax probability
  the classifier assigns to your categories is at least `min_mass`. Mass is
  invariant to which in-set category won (math 0.4 + physics 0.4 clears a 0.7 gate
  with no top-2 special case). An ambiguous, flat distribution never concentrates
  enough mass, so it fails the gate and the request falls to the default. This is
  why uncertainty lands on the middle tier and never escalates.
- **Without `min_mass`**: the leaf falls back to argmax membership (fires if the
  single top label is in your set). Prefer `min_mass`; it is the confident form.

**Tuning `min_mass`.** Higher is stricter (more confidence required to fire).
Probe the real number for a phrase before guessing:

```sh
curl -s localhost:45872/v1/route/domain \
  -d '{"text": "prove that the sum of two odd numbers is even"}'
# -> {"label":"math","confidence":0.91,"probs":{"math":0.91,"physics":0.03,...}}
```

Sum the `probs` over your categories: that number is what `min_mass` is compared
against. Set the gate a little below the mass your target phrases produce and a
little above what your false positives produce. The router log shows the evaluated
mass on every request (`dom:quantitative=0.94/0.7`), so you can watch real traffic
and adjust.

### `complexity`: contrastive difficulty

```json
"signals": {
  "complexity": [
    {"name": "reasoning", "threshold": 0.15,
     "hard": [
       "analyze the root cause of this failure",
       "weigh the tradeoffs and recommend an approach",
       "design the architecture for this system"
     ],
     "easy": [
       "fix this typo",
       "rephrase this sentence",
       "summarize this in one line"
     ]}
  ]
}
```

- **`hard` / `easy`**: banks of example phrasings. The signal embeds the request
  and both banks, scores similarity to each, and takes the margin
  `score(hard) - score(easy)`.
- **`threshold`** (a symmetric margin band, at least 0): margin above `+threshold`
  is `hard`, below `-threshold` is `easy`, in between is `medium`. A larger
  threshold widens the medium band, so fewer requests are classified either way.
- Populate the banks with phrasings **shaped like your real requests**. The signal
  scores similarity to these exemplars, so generic examples score generically.
  Bank size is soft-capped at 32 phrases (a warning above that), hard-capped at
  256.

**Tuning `threshold`.** Probe the margin for a phrase:

```sh
curl -s localhost:45872/v1/route/complexity \
  -d '{"text": "debug this intermittent race condition",
       "hard": ["analyze the root cause of this failure"],
       "easy": ["fix this typo"]}'
# -> {"margin": 0.184}
```

A margin of 0.184 is `hard` at threshold 0.15, `medium` at threshold 0.2. Probe
a handful of your hard and easy phrasings, then set the threshold between the two
clusters. The log shows the margin per request (`cplx:reasoning=+0.184`).

### `embeddings`: similarity to your examples

```json
"signals": {
  "embeddings": [
    {"name": "design-ask", "threshold": 0.55,
     "candidates": [
       "design the architecture for this system",
       "propose a system design for this feature",
       "what are the tradeoffs between these approaches"
     ]}
  ]
}
```

- **`candidates`**: example phrases. The signal scores the request's similarity to
  this bank (same 0.75 best + 0.25 mean-top-2 blend as complexity), and the leaf
  fires when the score is at least `threshold`. Same 32 soft / 256 hard caps.
- **`threshold`**: the embedding space has a high similarity floor (unrelated
  sentences score around 0.35 to 0.4), so useful thresholds live in a narrow band
  above that floor. Do not trust a new candidate list without probing.

**Tuning.** Probe the score:

```sh
curl -s localhost:45872/v1/route/embedding \
  -d '{"text": "how should I structure the service layer",
       "candidates": ["design the architecture for this system"]}'
# -> {"score": 0.58}
```

Set the threshold above what unrelated requests score and below what your target
phrasings score. The log shows the score per request (`emb:design-ask=+0.58`).

## `inspect`: what text the signals see

```json
"inspect": {"scope": "recent_turns", "turns": 3}
```

`inspect` bounds which user text is considered, keeping the classifier off the
giant system prompt and off tool output. Only user-authored text blocks count;
tool results, model thinking, and framework-injected `<system-reminder>` blocks
are excluded from both keyword and ML input.

| `scope` | text considered |
|---|---|
| `last_user_turn` | only the most recent user message. |
| `recent_turns` | the last `turns` user messages, joined. Requires `turns` > 0. |
| `full` | all user messages, joined. |

One important asymmetry: `inspect` sets the **keyword** window (a keyword two
turns back still protects, which is the point). The **ML signals always classify
only the latest user turn** regardless of scope, because averaging turns dilutes a
classifier (a turn scoring hard alone falls to medium once joined with two trivial
turns, silently suppressing escalation). So `recent_turns: 3` gives keywords a
three-turn memory while `domain` and `complexity` still judge just the latest turn.

## Session stickiness and the pin header

```json
"session":   {"sticky": false},
"overrides": {"pin_header": "x-whittle-route"}
```

**Stickiness** damps a downgrade on the fuzzy path (the static default) so a
session does not flap between tiers. It only damps **default** decisions, never an
explicit route or a pin, and only **downgrades** (upgrades are always free). A
downgrade is held unless it jumps at least `min_band_jump` bands. With only three
tiers a one-band downgrade is the common case, so `min_band_jump` must be at least
2 to damp anything; `sticky: true` with a smaller jump does nothing and validation
warns. The shipped presets keep it off.

**Pin header** is the per-request escape hatch. With `pin_header` set, a request
carrying that header pins the tier and bypasses all routing:

```sh
curl ... -H 'x-whittle-route: smart'
```

An unknown tier in the header is ignored (the request falls through to normal
routing), never a 400. A pin never writes the session's tracked tier.

## The authoring loop

1. **Start from a preset.** `whittle policy init` writes the calibrated `default`
   policy to `~/.whittle/router.json` with your model ids detected. Run
   `whittle policy list` to see available presets.

2. **Edit** `~/.whittle/router.json`.

3. **Validate before reloading.** Validation is the authoring contract: it runs
   the real loader and prints errors and cost-lint warnings.

   ```sh
   whittle policy validate ~/.whittle/router.json
   # VALID ✓  tiers=3 routes=3 default=requested
   #   warn: routes[0] (escalate): references an ML signal ... place cheap routes above it
   ```

   Fix every error. Read the warnings; they flag inert config (a route that never
   fires, an ML route above a cheap one) that loads fine but is probably not what
   you meant.

4. **Reload with SIGHUP.** The running router hot-reloads on `SIGHUP`. A reload
   that fails validation is rejected and the live policy stays, so a bad edit never
   bricks routing.

   ```sh
   # launchd (installed with `whittle route -install`):
   launchctl kill HUP gui/$(id -u)/dev.firstops.whittle.router

   # foreground (`whittle route` in a terminal):
   kill -HUP <router-pid>
   ```

   The startup and reload log lines confirm the policy loaded and whether smart
   mode is on.

5. **Watch the log.** The router writes one JSON line per request. Under launchd
   it goes to `~/.whittle/logs/router.log`; in the foreground it goes to stderr.

   ```sh
   tail -f ~/.whittle/logs/router.log
   ```

## Reading the log's `signals` field

Every request logs one line. The `reason` field names the rule that fired; the
`signals` field shows every ML value computed, against its gate:

```json
{"tier":"fast","requested":"claude-opus-4-8","model":"claude-haiku-4-5-20251001",
 "reason":"route:casual-easy","signals":"dom=other@1.000 dom:casual=0.899/0.85 cplx:reasoning=-0.471",
 "status":200,"latency_ms":41,"ctx_tokens":2140,"in_tokens":1974,"out_tokens":16,"session":"c2f9659c"}
```

- `requested` vs `model` is what routing changed. Equal means no rewrite happened.
- `reason` names the exact path: `route:<name>`, `default`, `default:requested`,
  `pin:<tier>`, plus suffixes like ` stripped:<features>`, ` sticky:kept`,
  ` ml-degraded`, and ` mode-b:...` on a retry.
- `signals` is the ML trace, space-separated. It is empty when a heuristic leaf
  decided first (no ML was reached).

The `signals` vocabulary:

| token | meaning |
|---|---|
| `dom=<label>@<conf>` | the classifier's top label and confidence for this request. |
| `dom:<name>=<mass>/<gate>` | the mass your domain signal summed, against its `min_mass`. Fires when mass is at or above the gate. |
| `cplx:<name>=<margin>` | the complexity margin (signed). `hard` above `+threshold`, `easy` below `-threshold`. |
| `emb:<name>=<score>` | the embedding bank score, against the signal's threshold. |
| `<signal>=off` | smart mode is off (sidecar absent or `WHITTLE_ROUTER_MODEL_URL=off`). The leaf was false. |
| `<signal>=err` | the sidecar was present but errored. The leaf was false and the request is tagged `ml-degraded`. |

Read a routing decision by matching `reason` against `signals`: if a route did not
fire, the `signals` field shows the value that missed its gate.

## Recipes

Each recipe is a complete, validated policy. Fill your own tier model ids.

### Send trivia to the cheapest tier (no ML)

Whole-word keyword routes, zero latency, work with smart mode off.

```json
{
  "version": 1,
  "tiers": [
    {"name": "fast", "model": "claude-haiku-4-5-20251001"},
    {"name": "main", "model": "claude-sonnet-4-5-20250929"}
  ],
  "default": "requested",
  "inspect": {"scope": "recent_turns", "turns": 3},
  "routes": [
    {"name": "trivia",
     "when": {"keywords": ["capital of", "how do you spell", "what year", "who wrote", "what time"]},
     "to": "fast"}
  ]
}
```

### Keywords for your own domain

Push work your team cares about up to the strong tier without touching anything
else.

```json
{
  "version": 1,
  "tiers": [
    {"name": "main",  "model": "claude-sonnet-4-5-20250929"},
    {"name": "smart", "model": "claude-opus-4-8"}
  ],
  "default": "requested",
  "inspect": {"scope": "recent_turns", "turns": 3},
  "routes": [
    {"name": "infra-hard",
     "when": {"keywords": ["terraform", "kubernetes", "helm", "race condition", "deadlock"]},
     "to": "smart"}
  ]
}
```

### Send large requests to the strong tier

A pure heuristic route, no sidecar required.

```json
{
  "version": 1,
  "tiers": [
    {"name": "main",  "model": "claude-sonnet-4-5-20250929"},
    {"name": "smart", "model": "claude-opus-4-8"}
  ],
  "default": "requested",
  "inspect": {"scope": "last_user_turn"},
  "routes": [
    {"name": "big-context", "when": {"context_tokens": {"gte": 150000}}, "to": "smart"}
  ]
}
```

### Protect legal and medical from down-routing

With `default: "requested"` sensitive work is already safe (only affirmative
routes move it). The remaining risk is a casual down-route misfiring on a serious
question, so add a guard route **above** the down-routes: first-match-wins sends a
legal or health question to `smart` before any cheaper route can claim it.

```json
{
  "version": 1,
  "tiers": [
    {"name": "fast",  "model": "claude-haiku-4-5-20251001"},
    {"name": "main",  "model": "claude-sonnet-4-5-20250929"},
    {"name": "smart", "model": "claude-opus-4-8"}
  ],
  "default": "requested",
  "inspect": {"scope": "recent_turns", "turns": 3},
  "signals": {
    "domains": [
      {"name": "sensitive", "categories": ["law", "health"], "min_mass": 0.6},
      {"name": "casual",    "categories": ["other"], "min_mass": 0.85}
    ],
    "complexity": [
      {"name": "reasoning", "threshold": 0.15,
       "hard": ["analyze the root cause of this failure"],
       "easy": ["fix this typo", "summarize this in one line"]}
    ]
  },
  "routes": [
    {"name": "protect-sensitive", "when": {"domain": "sensitive"}, "to": "smart"},
    {"name": "casual-easy", "when": {"all": [{"domain": "casual"}, {"complexity": "reasoning:easy"}]}, "to": "fast"}
  ]
}
```

### Aggressive savings mode

Down-route everything to sonnet by default, and pay for opus only on hard or
sensitive work. This is the biggest cost lever and a real quality trade; the guard
route keeps the expensive cases safe.

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
      {"name": "sensitive", "categories": ["law", "health"], "min_mass": 0.7}
    ],
    "complexity": [
      {"name": "reasoning", "threshold": 0.15,
       "hard": ["analyze the root cause of this failure", "weigh the tradeoffs and recommend an approach", "design the architecture for this system"],
       "easy": ["fix this typo", "rephrase this sentence", "summarize this in one line"]}
    ]
  },
  "routes": [
    {"name": "keep-hard", "when": {"any": [{"complexity": "reasoning:hard"}, {"domain": "sensitive"}]}, "to": "smart"},
    {"name": "trivial",   "when": {"complexity": "reasoning:easy"}, "to": "fast"}
  ]
}
```

### Route a specific request shape with your own embedding signal

When the shape is not a keyword and not a subject, describe it with a few example
sentences and route on similarity.

```json
{
  "version": 1,
  "tiers": [
    {"name": "main",  "model": "claude-sonnet-4-5-20250929"},
    {"name": "smart", "model": "claude-opus-4-8"}
  ],
  "default": "requested",
  "inspect": {"scope": "last_user_turn"},
  "signals": {
    "embeddings": [
      {"name": "design-ask", "threshold": 0.55,
       "candidates": [
         "design the architecture for this system",
         "propose a system design for this feature",
         "what are the tradeoffs between these approaches"
       ]}
    ]
  },
  "routes": [
    {"name": "designs", "when": {"embedding": "design-ask"}, "to": "smart"}
  ]
}
```

### Pin a session to opus

This one is not a policy edit; it is a per-request override. With
`overrides.pin_header` set (the default is `x-whittle-route`), send the header:

```sh
curl "$ANTHROPIC_BASE_URL/v1/messages" -H 'x-whittle-route: smart' ...
```

Every request carrying that header bypasses routing and pins the named tier.

## Debugging: symptom, log, fix

| symptom | what the log shows | fix |
|---|---|---|
| A route never fires. | `signals` shows the ML value missed its gate, e.g. `dom:casual=0.72/0.85`. | Lower the gate, or widen `categories` / bank phrasings toward your real requests. Probe the sidecar to find the right number. |
| An ML route never fires and `signals` shows `=off`. | `cplx:reasoning=off`, `dom=off`. | Smart mode is off. Run `whittle setup` to install the sidecar, or check `WHITTLE_ROUTER_MODEL_URL`. Heuristic (keyword, count) routes still work. |
| Requests routed wrong and tagged degraded. | `reason` ends in `ml-degraded`, `signals` shows `=err`. | The sidecar is up but erroring. Check the sidecar log; the ML leaves are failing false and routes depending on them are not firing. |
| A keyword route misses an obvious word. | `signals` is empty (heuristic decided elsewhere) or the route just did not fire. | Whole-word matching: `refactor` does not match `refactored`. Add the variants, or use `keywords_regex`. |
| A request keeps the requested model, no rewrite. | `reason":"default:requested"`, `requested` equals `model`. | No route matched and the default is `requested`. Add a route, or set a tier as the default. |
| A down-route lands on a legal or medical question. | `reason":"route:casual-easy"` on a serious prompt. | Add a guard route (domain law/health to a strong tier) **above** the down-route; first match wins. |
| The rewrite gets a 400 and retries. | `reason` contains `mode-b:retried-original`, names the rejected model and error. | The cheaper tier rejected a feature. Usually self-heals (one extra round-trip on the original); if persistent, do not down-route that shape. See ROUTER.md §6. |
| Edits do not take effect. | Startup log still shows the old policy; no reload line. | Send `SIGHUP` (above). Confirm the reload log line. A reload that fails validation is rejected and the live policy is kept, so validate first. |
| An unmatched request rides an unexpected tier. | `reason":"default"`, `model` is the default tier. | Your `default` is a tier name, not `"requested"`. Every unmatched request down-routes to it. |

## Validation errors and warnings

Validation is the authoring contract: `whittle policy validate <file>` runs the
real loader. The common messages and what they mean:

**Errors (the policy will not load):**

| message | cause |
|---|---|
| `multiple predicates in one node ... wrap them in all` | two leaves in one `when` node. There is no implicit AND. |
| `` `not` takes a single condition, not a list `` | `not` was given a list; it takes one node. |
| `unknown field "keywrods"` | a misspelled or unknown key. Strict decoding rejects it. |
| `"casual" is not a defined signals.domains entry` | a `domain` leaf names a signal you did not define under `signals.domains`. |
| `"" is not a defined signals.complexity entry (use name:hard\|easy\|medium)` | a `complexity` leaf is missing its `:level` suffix. |
| `"turbo" is not a defined tier (or "requested")` | `default` names a tier that does not exist. |
| `to="turbo" is not a defined tier (nor "keep")` | a route's `to` names a tier that does not exist. |
| `min_mass must be in [0,1]` | a domain signal's `min_mass` is out of range. |
| `numeric predicate must be a number or a {gt/gte/lt/lte/eq} object` | a numeric leaf was given a quoted string. |
| `"keep" is a reserved keyword and cannot be a tier name` | a tier is named `keep` or `requested`; both are reserved. |
| `inspect.scope: required` | `inspect` is missing or has no scope. |
| `scope=recent_turns requires turns > 0` | `recent_turns` without a positive `turns`. |

**Warnings (the policy loads, but the config is probably not what you meant):**

| message | meaning |
|---|---|
| `references an ML signal ... place cheap routes above it` | this route runs a model on every request that reaches it. Put cheap routes above it. |
| `"coding" is not a known MMLU-Pro category ... inert` | a category the classifier never emits; the signal will not fire on it. |
| `route has no name ... logged by index` | add a `name` so the log reason is readable. |
| `single-element group is usually a mis-indentation` | an `all` or `any` with one child, often a folded-together mistake. |
| `sticky=true with min_band_jump=... damps nothing` | stickiness is on but the jump threshold is too small to hold any downgrade. |

## Related

- [docs/ROUTER.md](ROUTER.md): architecture, request lifecycle, signal math,
  capability reconciliation, the failure model, and prior-art credits.
- [router/policies/default.md](../router/policies/default.md): a walkthrough of
  the shipped `default` policy.
- [router/policies/default.json](../router/policies/default.json): the shipped
  policy, a working reference you can copy from.
</content>
</invoke>
