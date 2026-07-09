# whittle

**Cut your AI agent's token bill twice: compress every tool output the moment it's created, and route each request to the cheapest model that can handle it. Local, fail-open, and every claim in this README is verifiable from a clone.**

[![CI](https://github.com/firstops-dev/whittle/actions/workflows/ci.yml/badge.svg)](https://github.com/firstops-dev/whittle/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/firstops-dev/whittle)](https://github.com/firstops-dev/whittle/releases)
[![Go Reference](https://pkg.go.dev/badge/github.com/firstops-dev/whittle.svg)](https://pkg.go.dev/github.com/firstops-dev/whittle)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)

![whittle stats: tokens carved across real agent sessions](demo/stats.gif)

Long agent sessions drown in tokens — and most compressors buy their ratio by silently destroying what agents need: array rows vanish, file reads get gutted, identifiers come back mangled. Whittle holds one hard line: **lossless or clearly marked, code never touched, every anomaly fails open to the original bytes.**

## Highlights

- 🪓 **Write-time compression** — a Claude Code PostToolUse hook whittles each tool output *before* it enters context, so the savings repeat on every later turn. History is never mutated, so prompt caches stay intact.
- 🔒 **Lossless or marked** — JSON reshapes byte-exact (rows are never dropped), logs keep every error plus an honest `[N lines omitted]`, source code passes through untouched.
- 🎯 **Token-honest numbers** — savings measured against calibrated tokenizer counts, not byte counts that overstate by up to 4×.
- 🧭 **Opt-in model router** — hard reasoning stays on your strongest model, trivia drops to the cheapest, per one auditable policy file driven by trained-classifier signals ([how it works](docs/ROUTER.md)).
- 🛟 **Fail-open everywhere** — whittle down means your agent runs on originals; a rejected rewrite means your original request, retried. Never blocked, never corrupted.
- 🏠 **Runs entirely on your machine** — zero credentials leave your box; the Go binary has zero external dependencies.
- 🧾 **Executable claims** — `make test` and `go run ./bench` regenerate every number below from a clone.

## Install

```sh
go install github.com/firstops-dev/whittle/cmd/whittle@latest
whittle setup      # hook + local daemon + optional ML sidecar — one command
```

Tool outputs are whittled from now on; `whittle stats` shows what you're saving. (Homebrew: `brew install firstops-dev/tap/whittle`. If `go install`'s binary isn't found, add `~/go/bin` to your PATH. Linux runs the daemon under systemd — [notes](docs/compression.md).)

**Optional — turn on model routing:**

```sh
whittle policy init                              # calibrated policy, your model ids auto-detected
whittle route -install                           # background service (or `whittle route` in a terminal)
export ANTHROPIC_BASE_URL=http://127.0.0.1:45873
```

## See it

![whittle compressing a noisy build log](demo/compress.gif)

```
$ tail -n 6 build.log | whittle compress -stats
ERROR migrate failed: relation "users" does not exist
... [118 lines omitted]
2026-07-04 INFO  worker drained cleanly
ERROR shutdown: connection reset by peer

whittle: action=compressed detected=log strategy=log_compressor tokens=1904->47
```

Errors and the summary survive; 118 lines of INFO noise become one honest marker. Across **5,000 real agent sessions**: **22% tool-output reduction at zero measured information loss** — mechanically lossless on 15,846/15,846 items, and a blinded 4-judge panel found 0/120 material loss on the lossy prose path. Full receipts, including an honest side-by-side against headroom: [bench/](bench/README.md).

## Compression: what happens to each content type

JSON is reshaped **losslessly** (byte-exact reconstruction). Logs and terminal streams are cut lossily but **marked and exactly accounted**. Markdown file reads keep code fences, tables, and headings byte-exact while prose is compressed by an optional local model with fidelity guards. Source code is **never touched**. Every path is wrapped in fail-open guardrails — the worst case is always *not compressed*, never *corrupted*.

The full per-type contract, ML prose path, architecture, and performance tables: [docs/compression.md](docs/compression.md) · what each guarantee is pinned by: [GUARANTEES.md](GUARANTEES.md).

## Model routing (opt-in)

Whittle's second surface: a local proxy on `ANTHROPIC_BASE_URL` that sends each request to the cheapest model tier that can still handle it — per a policy you can read in one screen.

- **Calibrated out of the box** — `whittle policy init` writes a conservative default (hard reasoning → strongest tier, confident chit-chat → cheapest, *everything else untouched*) with your account's real model ids auto-detected. [What it does & how to customize](router/policies/default.md).
- **Multi-signal, not keyword-matching** — a trained 14-subject classifier (probability-mass thresholded, so an *uncertain* classification never escalates), a contrastive difficulty score, and your own keywords. Every log line shows each signal's value against its gate.
- **Rewrites the model, never your history** — prompt-cache prefixes survive; capabilities the cheaper model rejects are stripped automatically; credentials pass through untouched.
- **Fail-open by construction** — bad policy, dead classifier, or a rejected rewrite all fall back to your original request. Unset the env var and you're direct again.
- **Savings you can measure** — every request logs requested model, served model, and real token usage.

Architecture, signal math, and precise credits (the classifier models come from [vLLM Semantic Router](https://github.com/vllm-project/semantic-router)): [docs/ROUTER.md](docs/ROUTER.md).

## Why write-time?

Most context compressors are read-time proxies: they rewrite your conversation history on every LLM call — which breaks prompt caches, terminates your API traffic, and makes lossy compression the default. Whittle compresses each output **once, at the moment it's born**, before it enters history: savings compound across every later turn, nothing sits in your request path, and failure costs nothing. The full argument: [docs/why-write-time.md](docs/why-write-time.md).

## FAQ

**Will it break my agent?** No — that's the core design constraint. Every path fails open: if whittle is down, declines, or errors, your agent sees original bytes. The router likewise: worst case is your request untouched.

**Does it need Python?** No. The deterministic compressors (JSON, logs, terminal, markdown structure) are pure Go. Python powers the *optional* prose model and router classifiers; `whittle setup` installs it if `python3` exists, and everything else works without it.

**Are token savings dollar savings?** Not 1:1 — under prompt caching, cheap cache-reads dominate the bill, so a 22% token cut is roughly 3–5% of session cost. We publish both numbers rather than pretending otherwise: [bench/](bench/README.md).

**How does it compare to headroom?** On identical bytes, headroom's defaults compress ~5 points more — by dropping rows whittle refuses to drop. On conversation-shaped content whittle leads *while staying lossless*. Which trade you want is the whole point: [bench/SIDEBYSIDE.md](bench/SIDEBYSIDE.md).

**Where does my data go?** Nowhere. Hook, daemon, models, and router all run on your machine. The router forwards your own credentials to Anthropic and logs token *counts*, never prompt text.

**Which agents?** Claude Code today (hook + router). Cursor, Codex, and OpenCode adapters are on the roadmap — the compression engine is also a plain Go library and HTTP service.

## Verify it yourself

Every claim here is checkable from a clone — that is the point.

```sh
make test                          # guarantees as executable tests (GUARANTEES.md)
go run ./bench                     # corpus reductions + fidelity, SHA-pinned, CI-gated
python bench/calibrate_tokens.py   # reproduces the token-estimator MAE
```

## Contributing

The bar: guarantees are executable — see [CONTRIBUTING.md](CONTRIBUTING.md). Fidelity bugs (whittle changing an output's meaning) are treated as urgent. Good first issues: agent adapters, Linux packaging, detection corpus cases.

## Acknowledgments

Whittle's log-selection strategy, several detection heuristics, and the tabular parser were adapted from [Headroom](https://github.com/headroomlabs-ai/headroom) (Apache-2.0) — their compaction work is excellent; we wanted the write-time position and the stricter fidelity contract it demands. The router's classifier and embedding models come from [vLLM Semantic Router](https://github.com/vllm-project/semantic-router) ([whitepaper](https://vllm-semantic-router.com/white-paper)). See [NOTICE](NOTICE).

## License

Apache-2.0.
