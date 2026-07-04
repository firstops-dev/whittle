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
```

Zero dependencies. Works immediately — the optional ML prose path is opt-in (below).

## Use

```
whittle compress output.json                 # compressed to stdout
cat build.log | whittle compress -stats      # stats to stderr
whittle serve -addr :8095                    # HTTP: POST /v1/compress
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
.venv/bin/uvicorn app:app --port 8096
export WHITTLE_MODEL_URL=http://127.0.0.1:8096
```

## Configuration

| env | default | meaning |
|---|---|---|
| `WHITTLE_MODEL_URL` | *(unset — prose off)* | model sidecar URL |
| `WHITTLE_MAX_CHARS` | 262144 | global size ceiling (skip before classify) |
| `WHITTLE_PROSE_MAX_CHARS` | 4500 | prose-path latency ceiling |

## Design principles

1. **Fail open.** A compressor that breaks your agent is worse than no compressor.
2. **Never silent loss.** Lossy paths mark what they removed and account for it exactly.
3. **Code is sacred.** File reads, fences, identifiers: byte-exact or untouched.
4. **Token-honest.** Accept gates and reported savings use calibrated token
   estimates (MAE ~8% vs `o200k_base`), not bytes.
5. **Adversarially tested.** The invariants above are pinned by reconstruction
   fuzzing, per-language routing suites, and fail-open contract tests.

## License

Apache-2.0.
