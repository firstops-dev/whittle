# Dataset Card — Customer-Support Compression Benchmark (Dataset 1)

**Use case:** external-facing agents (CRM / customer support).
**Compression targets:** tool-call outputs (**primary**) and user inputs (secondary).

## Source

| | |
|---|---|
| Dataset | [`Salesforce/APIGen-MT-5k`](https://huggingface.co/datasets/Salesforce/APIGen-MT-5k) |
| What it is | 5,000 verified multi-turn tool-calling agent trajectories |
| Domains | Retail (3,411 sessions) + Airline (1,589) — the τ-bench customer-support environments |
| Provenance | **Synthetic**, LLM-generated then execution- and review-verified (the training data behind Salesforce's xLAM models). Not real customer traffic. |
| Parquet | single file, ~9.6 MB, one `train` split; SHA-256 pinned in `raw_data/sampling_manifest.json` |
| License | ⚠️ to confirm before publishing derived metrics |

Each row = one session with three fields: `system` (agent policy prompt),
`tools` (JSON of available tool defs), `conversations` (ordered turns).

**Turn roles** (`from`):

| role | meaning | benchmark use |
|---|---|---|
| `human` | user input | compression target — **secondary** |
| `observation` | tool-call output | compression target — **PRIMARY** |
| `function_call` | the tool call the agent issued | context only |
| `gpt` | assistant natural-language turn | context only |

## Full-corpus profile (all 5,000 sessions, tokenizer `o200k_base`)

- Session length: min 3, **median 18 turns**, max 56.
- **Tool outputs:** 21,955 turns, ~6.5M tokens. Median ~180 tok, p99 ~790 tok,
  max ~6,300 tok. Right-skewed: only ~0.3% exceed ~1,000 tokens. 849 are empty
  (the `think` tool) and are excluded from compression.
- **User inputs:** 24,229 turns, ~0.75M tokens. **Median ~28 tokens** — short.

### Why this scoping
Tool outputs carry essentially all the compressible substance; user turns are
too short to compress meaningfully (high loss-risk per token saved). Results are
therefore reported as a **conservative lower bound** for *customer-support API
responses* — this is not the long-output regime (large file reads, logs, HTML)
where compression pays off most. User-input compression is reported as
measured-but-marginal on this dataset.

## Sampled working set (`raw_data/`)

Reproduce with `python scripts/prepare_dataset.py --n 50` (seed 42).

| | |
|---|---|
| Sessions | **50** (domain-stratified, proportional: 34 retail / 16 airline) |
| Tool outputs to compress | **211 turns, ~60.4k tokens** (median 262 tok/output) |
| User inputs to compress | 234 turns, ~7.0k tokens |
| Sampling | seeded random within domain strata, largest-remainder rounding |

### `raw_data/sessions.jsonl` — one session per line
```
session_id, source, source_row, domain, system, tools, num_turns,
turns: [ { pos, role, char_len, token_len, content,
           compressible?, content_type? } ]
```
`source_row` traces each session back to its 1-based row in the source parquet.
`turn_position` for the Step-4 cost model = `turns[].pos`.

## Known limitations (stated up front for external review)
1. **Synthetic**, not real customer transcripts (verified/reputable, but label it so).
2. Only **2 domains** (retail, airline).
3. Tool outputs are compact structured JSON → conservative compression ratios,
   not representative of long/noisy real-world outputs.
4. User-input compression is marginal on this corpus by construction.

These are why Dataset 1 **complements** rather than replaces a future
large-/long-output corpus (Dataset 2, developer-agent sessions).
