# whittle

**Carves your agent's tool outputs down to what matters. Never cuts what doesn't come back.**

Whittle is a content-aware compressor for the text AI agents read: tool outputs,
file reads, logs, JSON, terminal streams. Long agent sessions drown in tokens —
but most compressors buy their ratio by silently destroying things agents need:
array rows vanish, file reads get gutted, identifiers come back mangled.

Whittle holds one hard line: **structural compression is lossless or clearly
marked, code never reaches a lossy model, and every anomaly fails open to the
original bytes.** The reduction number it reports is calibrated to real
tokenizer counts — not byte counts that overstate savings by up to 4×.

## Install

```
go install github.com/firstops-dev/whittle/cmd/whittle@latest
whittle setup
```

That's the whole thing. `setup`:

- installs the **Claude Code PostToolUse hook** — every tool output your agent
  reads is whittled from now on (Claude Code is the supported agent today;
  Cursor, Codex and OpenCode adapters are on the roadmap);
- materializes the ML prose sidecar (embedded in the binary) into `~/.whittle`,
  builds its venv, and uses your GPU automatically (CUDA > Apple MPS > CPU) —
  if `python3` is missing, whittle simply runs deterministic-only;
- registers a **launchd agent** (macOS) so the service starts at login and is
  kept alive.

Manage it with `whittle status`, `whittle stop`, and `whittle cleanup` (stops
the service and removes the hook). Everything fails open: if whittle is down,
your agent sees original outputs, never an error.

## Use

```
whittle compress output.json                 # compressed to stdout
cat build.log | whittle compress -stats      # stats to stderr
whittle serve -addr :45871                    # HTTP: POST /v1/compress
```

As a library:

```go
eng := whittle.New(whittle.Options{})
res := eng.Compress(ctx, toolOutput)
// res.Output, res.Action ("compressed"|"skipped"), res.SkipReason,
// res.Strategy, res.Detected
```

## What it does per content type

| detected | strategy | guarantee |
|---|---|---|
| JSON | minify + columnar reshape (union schema, typed CSV, nested flattening, constant factoring) | **lossless** — reconstructs byte-exact; rows are never dropped |
| logs / build output | keep errors, warnings, stack traces, summaries | lossy but **marked** — `... [N lines omitted]`, exact accounting |
| terminal | ANSI strip + CR-overwrite collapse (progress bars → final frame) | what the terminal actually displayed; rune-safe |
| markdown file reads | structure-aware: prose compressed by the model, **code fences / tables / lists / headings passed through byte-exact** | code never reaches the model |
| source code | untouched | routed away from every lossy path |
| prose | extractive model (optional) with fidelity guards: entity protection, whole-token deletion, negation preservation | fails open on any guard trip |

Every path is wrapped in fail-open guardrails: empty-output, expansion (both
byte- and token-honest), panic recovery. The worst case is always "not
compressed", never "corrupted".

## Optional: the ML prose path

Deterministic strategies need nothing. To also compress natural-language prose,
run the model sidecar (LLMLingua-2 + whittle's fidelity guards — see
[model/](model/)):

```
cd model && python -m venv .venv && .venv/bin/pip install -r requirements.txt
.venv/bin/uvicorn app:app --port 45872
export WHITTLE_MODEL_URL=http://127.0.0.1:45872
```

## Configuration

| env | default | meaning |
|---|---|---|
| `WHITTLE_MODEL_URL` | *(unset — prose off)* | model sidecar URL |
| `WHITTLE_MAX_CHARS` | 262144 | global size ceiling (skip before classify) |
| `WHITTLE_PROSE_MAX_CHARS` | 4500 | prose-path latency ceiling |


## Why whittle — compress at write-time, not read-time

Context compressors typically integrate with coding agents as **request-path
proxies**: your agent's base URL is redirected through a local server that
rewrites conversation history at *read time*, on every LLM call. That position
forces hard problems — prompt-cache stabilization (mutating history invalidates
cached prefixes), per-call re-compression, terminating your API traffic (keys,
system prompts and all), and a resident runtime that must stay up or your agent
goes down with it. It also makes lossy compression the default, backed by a
retrieval loop: the runtime is guaranteed present, so dropped content can be
served back on demand.

Whittle takes the other position: it is a **PostToolUse hook**. Each tool output
is compressed **once, at the moment it is born**, before it ever enters
conversation history. Everything else follows from that choice:

- **Savings compound.** A tool output lives in context for every subsequent
  turn. Tokens removed at write-time are removed from *every* later call —
  no per-call rework, no cache surgery, because history is never mutated.
- **No trust expansion.** A hook sees one tool output at a time, locally, with
  zero credentials. Nothing terminates your API traffic.
- **Failure is free.** The hook fails open; if whittle is down or declines, the
  agent proceeds with the original output. A gateway outage is an agent outage.
- **The loss budget is honest.** A read-time proxy can afford recoverable lossy
  compression — its resident runtime serves dropped content back when the model
  asks. A hook has no runtime standing by: the compressed output is the agent's
  *only* copy. That is why whittle is lossless-or-marked by construction — not a
  preference, but what the position demands.

The hook is whittle's default surface, not its only one: **library**
(`whittle.New`) → **HTTP service** (`whittle serve`) → **hook adapters**
(Claude Code PostToolUse today; Cursor, Codex, OpenCode adapters on the
roadmap) — and the same library embeds in gateways or pipelines if that is
where you need it. The position is the point: compression happens where output
is born, whatever surface delivers it there.

## Performance

Deterministic strategies are pure CPU, single static binary, zero allocatable
model state (Apple M-series, `go test -bench`):

| input | size | latency |
|---|---|---|
| JSON array, 200 rows, pretty-printed | ~21 KB | ~1.0 ms |
| terminal progress stream | ~12 KB | ~3.9 ms |
| build log, 800 lines | ~66 KB | ~21 ms |

The hook runs after the tool call completes, so this cost is **off the LLM
request path entirely** — model-call latency is unchanged. The optional ML
prose path is capped by a fail-open budget (default 1.5 s) and never blocks
beyond it.

## Design principles

1. **Fail open.** A compressor that breaks your agent is worse than no compressor.
2. **Never silent loss.** Lossy paths mark what they removed and account for it exactly.
3. **Code is sacred.** File reads, fences, identifiers: byte-exact or untouched.
4. **Token-honest.** Accept gates and reported savings use calibrated token
   estimates (MAE ~8% vs `o200k_base`), not bytes.
5. **Adversarially tested.** The invariants above are pinned by reconstruction
   fuzzing, per-language routing suites, and fail-open contract tests.


## Acknowledgments

Whittle's log-selection strategy, several content-detection heuristics, and the
tabular parser were adapted from [Headroom](https://github.com/headroomlabs-ai/headroom)
(Apache-2.0) — adapted portions are marked in source comments, and we think
their compaction work is excellent. Whittle exists because we wanted the other
position: a write-time PostToolUse hook instead of a read-time request-path
proxy, with the stricter fidelity contract that position requires. See NOTICE.

## License

Apache-2.0.
