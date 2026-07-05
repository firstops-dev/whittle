# Cost Model — Methodology (Step 6)

Estimates the API dollar cost of each session **with and without compression**, accounting for how prompt caching makes a token's cost *compound* across turns. Script: `scripts/compute_cost_model.py`.

## Why token-reduction % ≠ cost-reduction %
Two effects break the naive "22% fewer tokens → 22% cheaper":
1. **Compressible content is only a slice of the cost base.** A session's cost also includes the system prompt + tool definitions, the assistant's *output* tokens, and un-compressed turns. Compression touches only user inputs and tool outputs.
2. **Prompt caching makes re-read context cheap.** A token already in the cached prefix is billed at the **cache-read** rate (0.1× input). Compressing it saves only that 0.1×, while the expensive parts — **output** generation (5× input) and the one-time **cache-write** (1.25×) — are barely affected.

## The caching-aware model
A session is a growing transcript. Each assistant-side turn (`assistant` / `gpt` / `function_call`) is one **model call** that sees the entire prior context. A piece of content entering the context at model call *i* of a *K*-call session is:
- **cache-creation once** at call *i* (rate *W* = 1.25× input), then
- **cache-read** on every later call *i+1 … K* (rate *R* = 0.1× input).

So *T* tokens entering at call *i* cost `T·(W + R·(K−i))`. Compression removing Δ tokens saves **`Δ·(W + R·(K−i))`** — the reduction is multiplied by how many later turns re-read it. Early-turn items compound; late-turn items barely (this is why Step 3 recorded `turn_position`). **Output tokens are unchanged** — we hold agent behaviour fixed and replay the recorded transcript. Baseline uses original token counts; the compressed world subtracts each item's measured `token_reduced`.

## Prices (per MTok, from the claude-api skill, 2026-06)
| Model | Input | Output | Cache write (5m, 1.25×) | Cache read (0.1×) |
|---|---|---|---|---|
| **Opus 4.8** (default) | $5.00 | $25.00 | $6.25 | $0.50 |
| Sonnet 5 | $3.00 | $15.00 | $3.75 | $0.30 |
| Haiku 4.5 | $1.00 | $5.00 | $1.25 | $0.10 |

Model and price table are parameters (`--model`); percentages are near-invariant to the tier (rates scale together).

## Stated assumptions (for scrutiny)
- Prompt caching active; the full prior prefix is cache-read each turn within TTL (idealized). We also report a **no-cache bound** as sensitivity.
- Cache write at the **5-minute-TTL** rate (1.25×); each session caches independently (no cross-session amortization of the shared system+tools prefix — conservative).
- **Behaviour held fixed**: compression is assumed not to change what the agent does or outputs; we replay the transcripts. (This is the standard idealization; real compression could change trajectories.)
- No compaction / context-editing (sessions here are short — D1 median ~18 turns).
- Token counts via tiktoken `o200k_base`; the model's own tokenizer differs — this scales absolute $ but not the % much.
- The compressor's own inference cost/latency is out of scope (this models the *consumer* model's token cost only).
- Each `gpt`/`function_call` (assistant) turn is treated as a **separate model call**, which inflates K (~2× in D1). Defensible if the traces were separate round-trips; a real chat API can return text + a tool call in one response. It raises absolute cost but applies to baseline and compressed alike, so reduction% is insulated.
- Cache persistence is treated as **unlimited within a session** (one write, then reads). Real 5-min-TTL eviction would re-charge writes on long sessions (D2 median K=150), so D2's absolute caching benefit is optimistic; the *reduction %* is robust to this.

## Results

| Dataset | Model | Sessions | Baseline | Compressed | **Cost reduction (cached)** | No-cache | Info-loss |
|---|---|---|---|---|---|---|---|
| Customer-support (D1) | Opus 4.8 | 5,000 | $359.51 | $348.28 | **3.12%** | 2.51% | zero (lossless) |
| Developer-agent (D2) | Opus 4.8 | 28 | $145.99 | $99.74 | **31.68%** | 32.56% | 62% material |

**The headline:** cost reduction is **strongly content-dependent and much smaller than token reduction under caching**. On customer-support, a 22% tool-output token cut yields only **~3% session cost reduction** — because tool outputs are a modest share of the cost base and cached context is cheap. On developer-agent sessions (long file/log outputs, longer sessions → more compounding, tool outputs dominate the context), a 49% token cut yields **~32% cost reduction** — but that saving comes *with* the 62% material-information-loss cost measured in Step 4. D1's 3% is free; D2's 32% is not.

**Compounding, verified** (D1, saving per 1,000 reduced tokens): $0.0063 for an item no later turn re-reads (cache-write only) rising to $0.0183 for one re-read by 24 later turns (~3×) — exactly the `W + R·(K−i)` structure, confirming the turn-position effect is real and material.

## Independent audit & the turn-count nuance
An independent agent re-implemented this model from the spec above (without reading the code) and **reproduced every figure exactly** — D1 $359.51 / $348.28 / 3.12%, D2 $145.99 / $99.74 / 31.68%, no-cache 2.51% / 32.56% — with no arithmetic bug, double-count, or off-by-one, and correct edge-case handling (trailing turns with i>K cost 0; no K=0 sessions). It refined one interpretive claim:

**`cost-reduction% ≈ (compressible share of cost) × (compression ratio on that content)` is an exact identity** (D1: 15.7% × 0.199 = 3.12%; D2: 69.3% × 0.457 = 31.7%). But this is **not** turn-count-invariant — that holds only *asymptotically* (large K). Savings% rises with session length and then plateaus, because two cost components never scale with the cache-read multiplier: the **fixed system+tools prefix** (always i=1) and **output tokens** (per-call). At small K they dilute the compressible share:

| D1 by K (model calls) | savings% | | D2 by K | savings% |
|---|---|---|---|---|
| ≤6 | 1.56% | | <80 | 21.9% |
| 7–9 | 3.00% | | 80–150 | 30.6% |
| 10–13 | 3.52% | | 151–250 | 32.9% |
| 14+ | 3.60% | | 250+ | 32.1% |

**So turn count *does* matter at low K** — D1's short sessions (median K=9) sit on the rising part of the curve, which holds its saving below the ~3.6% large-K asymptote. But it is a **secondary** driver: the 3% vs 32% gap between D1 and D2 is caused primarily by **content composition** (compressible share 16% vs 69%, and compression ratio 20% vs 46%), not turn count — two datasets with equal K but different content would still diverge. The earlier shorthand "turn count cancels out in the percentage" was an over-statement of the large-K limit.

Artifacts: `benchmark/*/cost_report_<model>.json`.
