# Compression Benchmark — Dataset 1: Customer-Support Agents

> **Provenance & applicability to whittle.** This evaluation ran the FirstOps
> content-aware compression engine (`content-aware-router` v0.1.0) - the same
> engine, and the same `json_crusher` and `llmlingua` strategies, that whittle is
> built on - against the public Salesforce APIGen-MT-5k dataset. These numbers
> were not re-run on the whittle 0.2.x CLI; they characterize the shared
> compression core. Where the report references a "Dataset 2" `json_crusher`
> array-truncation defect, note that **whittle's `json_crusher` is lossless-only**
> (rows are never dropped) - that defect is fixed in whittle and never applied to
> these customer-support results, which were already verified 100% lossless.
>
> Raw per-item results (66 MB) and the source corpus (127 MB) are not vendored;
> the dataset is public (link below) and `compression_summary.json` holds the
> aggregates. `run.log`, judge logs, and raw annotations are omitted for size.



**Compressor under test:** `content-aware-router` v0.1.0 (`POST /v1/compress`), default gate.
**Question:** how much agent context does the compressor save on external-facing customer-support sessions, and at what information-loss cost?
**One-line answer:** it removes **22% of tool-output tokens with zero measurable information loss** (verified mechanically on 15,846/15,846 items), because customer-support tool outputs are structured JSON that the router compresses losslessly. User-input compression is negligible in size (2%) and, on a 120-item blinded-judge sample, showed **0/120 material loss**.

---

## 1. Dataset
Source: [`Salesforce/APIGen-MT-5k`](https://huggingface.co/datasets/Salesforce/APIGen-MT-5k) — 5,000 verified multi-turn tool-calling agent trajectories over the τ-bench **retail (3,411)** and **airline (1,589)** customer-support environments. Synthetic but execution- and review-verified; the tool *outputs* are genuine structured API responses. CC-licensed research dataset.

We benchmark the **full corpus (all 5,000 sessions)** — no sampling for the compression measurement. Turn roles: `human` = user input (secondary target), `observation` = tool output (primary target), `function_call`/`gpt` = context.

| Compressible surface | Turns | Tokens (o200k) |
|---|---|---|
| Tool outputs (primary) | 21,106 | 6,486,590 |
| User inputs (secondary) | 24,229 | 694,615 |

Full profile and reproduction in `DATASET_CARD.md`; sampling manifest (SHA-256-pinned source) in `raw_data/sampling_manifest.json`.

## 2. Method
1. **Compression (Step 3).** Every compressible turn is POSTed to the router at its default gate; we record the router's response plus **our own independent tiktoken counts** (`o200k_base` of record; also `cl100k_base`, `p50k_base`, `gpt2`). We never rely on vendor-reported savings. Skips (`too_short`, etc.) count as legitimate 0%-reduction outcomes — a real integration passes them through unchanged. Scripts: `run_compression.py` → `analyze_compression.py`.
2. **Quality (Step 4), two-track — matched to how the router actually compresses this data:**
   - *Structural path (`json_crusher`, 100% of compressed tool outputs):* losslessness is **verified mechanically on every item** (`verify_findings.py`) — no sampling, no LLM needed. Two lossless forms are accepted: parse-identical minification, and lossless list→columnar `{schema, rows}` re-encoding (all data values recoverable).
   - *Lossy path (`llmlingua` prose, the only path that can lose information):* a **stratified sample is annotated by a blinded LLM-judge panel** against a 4-dimension rubric (omission / distortion / task-sufficiency / entity integrity). Methodology: `../methodology/ANNOTATION_METHODOLOGY.md`.

## 3. Compression results

| Content type | Items | Compressed / Skipped | Reduction (o200k, full surface) | When compressed |
|---|---|---|---|---|
| **Tool output** | 21,106 | 15,846 / 5,260 | **22.0%** | 22.1% |
| User input | 24,229 | 562 / 23,667 | 2.1% | 35.5% |

- **Tool outputs** route **100% to `json_crusher`** (all are JSON). Skips are `too_short` (5,260). Tokens saved: **1,426,728**.
- **User inputs** are short (median ~28 tok): 23,666 skip as `too_short`, 1 hits `fidelity_guard`; only 562 compress (LLMLingua). User-input compression is **negligible in aggregate** on this corpus.

**Reduction is tokenizer-dependent** (report a range, not one number):

| | o200k | cl100k | p50k | gpt2 | vendor self-reported |
|---|---|---|---|---|---|
| Tool output | **22.0%** | 23.4% | 21.2% | 21.2% | 8.9% |

Independent measurement clusters at ~21–23%; the vendor's own count (8.9%) uses a tokenizer that spends fewer tokens on JSON punctuation. We publish o200k (GPT-4o/o-series) as primary with the range shown, and never quote the vendor number as the headline. (One caveat for reviewers: the ~22% assumes the agent's context holds *spaced* JSON; a framework that already minifies upstream would see less.)

## 4. Quality results

### 4a. Structural path — mechanically lossless (100% of tool-output compression)
Every one of the **15,846** compressed tool outputs preserves all data values:

| | count |
|---|---|
| Parse-identical minification | 15,469 |
| Lossless list→columnar re-encoding (data fully recoverable) | 377 |
| **Provably lossless (total)** | **15,846 / 15,846 (100%)** |
| Data-dropping (lossy) | **0** |

So the entire 22% tool-output reduction on Dataset 1 carries **zero information loss** — no LLM judgment required, verified on the full corpus. (Note: the same check flags the `json_crusher` array-truncation defect on Dataset 2 as genuinely lossy — it simply does not occur here, because customer-support payloads are objects and small lists, not large arrays.)

### 4b. Lossy path — LLMLingua on user inputs (annotated sample)
Panel: 4 blinded judges (Opus-4.8, Sonnet-5, GPT-5-chat, GPT-4.1; **Haiku dropped** per the pre-registered honeypot gate). Sample: **120 of 562** llmlingua items + 11 blinded honeypots.

- **Panel validity:** all four judges caught **100% of honeypots** — every injected corruption (altered numbers, dropped negations, deleted paths) flagged, and zero false positives on the identity controls.
- **Agreement:** **Gwet AC2 = 0.96–1.00** across all four dimensions (near-unanimous). Krippendorff α is degenerate here (−0.11 to 0.0) because the real items carry essentially **no score variance** — the textbook case where α is undefined and AC2 is the correct statistic (as pre-registered for skewed dimensions).
- **Result:** material-loss = **0 / 120**, severe-loss = **0 / 120** (95% CI [0, 3.0%] by rule-of-three). The real LLMLingua compressions are stopword/filler deletions that preserve all facts, entities, IDs, and numbers; only the injected honeypots scored nonzero.

On this corpus the lossy path is **lossy in name only** — LLMLingua removes filler from short customer utterances without dropping load-bearing content.

## 5. Headline & interpretation
On external-facing customer-support agents, the compressor delivers **~22% tool-output context reduction at zero information-loss cost** — verified two ways: a full-corpus mechanical proof on the structural path (15,846/15,846 lossless), and a blinded 4-judge panel finding 0/120 material loss on the only lossy path (user-input LLMLingua). This is a **conservative, defensible lower bound**: it is the near-free regime, not the aggressive-lossy regime seen on developer tool outputs (Dataset 2). User-input compression is not a meaningful size lever on this corpus (2%), but where it fires it is effectively lossless.

## 5b. Cost impact (Step 6 — caching-aware)
Token reduction does **not** translate 1:1 into dollar savings. Modelling each session's API cost with prompt caching (methodology + assumptions in `../methodology/COST_MODEL.md`; script `compute_cost_model.py`):

| | Baseline | Compressed | Cost reduction |
|---|---|---|---|
| **Opus 4.8, caching on** (5,000 sessions) | $359.51 | $348.28 | **3.1%** |
| Sonnet 5 | $215.70 | $208.97 | 3.1% |
| No-cache sensitivity | $1,170.26 | $1,140.88 | 2.5% |

The 22% tool-output *token* reduction yields only ~**3% session cost reduction**, for two reasons: (1) tool outputs are a modest slice of the total cost base (system prompt + tool definitions + assistant **output** tokens are not compressed), and (2) under prompt caching, re-read context is billed at the cheap cache-read rate (0.1× input), so compressing it saves little — the expensive parts (output generation at 5× input, and the one-time cache-write at 1.25×) are barely affected.

The saving does **compound** with turn position exactly as modelled: a reduced token no later turn re-reads saves $0.0063/1k; one re-read by 24 later turns saves $0.0183/1k (~3×). This ~3% is **free** (lossless). For contrast, the developer-agent dataset (Dataset 2) shows ~32% cost reduction from its 49% token cut — but that saving comes with 62% material information loss. Cost benefit and quality cost are both strongly content-dependent.

## 6. Limitations
- **Synthetic** (verified) trajectories, **two domains** (retail, airline) — label results as customer-support API responses, not "tool outputs in general."
- Reduction is tokenizer-relative; upstream minification would reduce realized savings.
- User-input compression is marginal here by construction.
- Quality on the lossy path is a sampled LLM-judge estimate (§4b); the lossless claim on the structural path is a full-corpus mechanical proof.

## 7. Reproduce
`prepare_dataset.py --all` → `run_compression.py` → `analyze_compression.py` + `verify_findings.py` → `build_packets.py` → judge panel → `analyze_annotation.py`. All model IDs, seeds, tokenizer, and source SHA-256 are pinned in the manifests and scripts.

## Appendix
- Cross-check of this compressor on public third-party benchmarks (the datasets Headroom discloses its savings on — SQuAD v2, BFCL, GSM8K, TruthfulQA): [`../methodology/ANNOTATION_METHODOLOGY.md`](../methodology/ANNOTATION_METHODOLOGY.md).
- **Head-to-head vs Headroom (compression *and* quality, side by side)** on those same datasets plus real agent sessions: [`../../SIDEBYSIDE.md`](../../SIDEBYSIDE.md). Key result: on structured JSON ours cuts more losslessly (18.7% vs 11.8%); on prose ours cuts 37% but drops ~29% of gold answer spans where Headroom protects (0% / 100% retention) — compression % is meaningless without the fidelity axis.
