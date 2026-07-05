# Information-Loss Annotation Methodology — v0.3

Adapted from an internal trial-run framework and re-reviewed for the published benchmarks here. Purpose: quantify, defensibly, whether the compressor loses information that matters to the **consuming agent** — separately from how many tokens it saves.

## 0. What changed from v0.2 (and why)
The prior run's compressor output was almost entirely lossless minification, so the interesting cases were rare. Our Step-3 measurements show the opposite for Dataset 2: reduction is **lossy-dominated** (`tabular_crusher`, `log_compressor`, `llmlingua`, and a `json_crusher` array-truncation defect). So:
- The **mechanical lossless pre-pass no longer exempts most items.** Only items we can *prove* lossless (parse-equal JSON, or verified deletion-only whose deleted spans are pure formatting) are auto-labeled; everything else is judged.
- We add explicit handling for **structural truncation** (dropped array elements / table rows) as a first-class, high-severity loss — the `json_crusher` 30→15 case must score as severe omission, not "minor formatting."

## 1. Grounding in published practice
| Source | What we take |
|---|---|
| SummEval (Fabbri 2021) | multi-dimension rubric beats single score; but our reader is a model, not a human — drop fluency/coherence |
| FRANK (Pagnoni 2021) | separate **omission** from **distortion** — deletion compressors omit; distortions (dropped negation, wrong number) are rarer and more dangerous |
| QAGS/QuestEval (2020-21) | recall-direction QA probe = "information recoverability"; used as a validation layer on a subsample |
| LLMLingua-1/2 (2023-24) | the compressor family under test; key lesson: high semantic similarity ≠ stable downstream behavior → we add **task-sufficiency** grounded in the real next agent action |
| G-Eval + LLM-judge surveys | panel of 2-3 judges, reason-then-score, blinded, honeypot-checked, IAA-gated; Krippendorff α ≥0.67 tentative / ≥0.8 strong |

## 2. The rubric — 4 dimensions (judge reasons first, then scores)
The consumer is an **AI coding/support agent** that will act on the compressed text (edit files, run commands, answer the user). Telegraphic phrasing is FINE if the facts survive. Reward nothing for compression ratio; a 90% cut with all facts intact is 0/0/0/0.

- **D1 Omission (0-3):** 0 lossless · 1 minor (redundancy/formatting) · 2 material (substantive fact the consumer needs) · 3 severe (load-bearing facts/records/identifiers/constraints lost — *includes truncated arrays/tables/log runs*).
- **D2 Distortion (0-2):** 0 faithful · 1 nuance shift (dropped hedge/qualifier) · 2 falsification (compressed asserts something false vs original — inverted negation, wrong number, merged entities, fabricated atom).
- **D3 Retrospective task-sufficiency (0-2):** given what the agent actually did next in the real session — 0 next action fully supported by compressed version · 1 degraded (guesswork / would re-fetch) · 2 insufficient (action could not be taken). If no next-action evidence, judge by likely use.
- **D4 Entity integrity (0-2):** entities = paths, identifiers, numbers, code symbols, URLs, error codes, record keys. 0 intact · 1 peripheral loss · 2 critical entity dropped/garbled. A mechanically-generated fragment/missing-atom list is provided to *verify against*, not assume complete.

**Material-loss** = D1≥2 OR D2≥1 OR D3≥1 OR D4≥2 (any one). **Severe-loss** = D1=3 OR D2=2 OR D3=2 OR D4=2.

## 3. Blinding & bias controls
Judges see ONLY: original text, compressed text, tool name, and `what_agent_did_next`. They never see strategy, detected-type, token counts, reduction, or which side is "compressed" is labeled neutrally as "version B". Packet order randomized. Rationale ≤120 words required before scores.

## 4. Panel & aggregation
Single-provider constraint (Claude only in this environment) → **3 heterogeneous Claude models** as judges: `claude-opus-4-8`, `claude-sonnet-5`, `claude-haiku-4-5`. This is a *stated limitation* (not cross-family). Per item: report each judge, the median per dimension, and flag any item where judges disagree by ≥2 on any dimension for adjudication.

## 5. IAA gating (pilot before full run)
Run a stratified **pilot** first. Gate to proceed: bootstrap 95% CI lower bound of Krippendorff α ≥ **0.67** on D1 and D3 (ordinal); Gwet's AC2 for the zero-skewed D2/D4. If below, rewrite the offending dimension and re-pilot (max 2 rounds, all published). Honeypots excluded from IAA.

## 6. Honeypots (hidden registry judges never see)
Injected into the packet stream at a known rate:
- **identity** (compressed == original) → must score 0/0/·/0. Catches judges hallucinating loss.
- **alter_number** → expect D2≥1. **drop_negation** → expect D2≥1. **delete_path** → expect D4≥1. **truncate_array** → expect D1=3/D4=2.
A judge failing >20% of honeypots in the expected direction is dropped and re-run.

## 7. Reporting rules
Composite never appears alone. Always report: row-level AND token-weighted material-loss rate, severe-loss counts, D2=2 (falsification) rate prominently, broken out **by strategy** (`tabular_crusher` vs `llmlingua` vs …) and by detected-type. Every fidelity claim labeled with measurement basis. Claim language pinned to "no material loss under our rubric," never "lossless" unless mechanically proven.

## 8. Reproducibility
Judge model IDs, decoding params, exact prompts, packet manifest sha256, and honeypot registry sha256 all pinned. Packets and per-judge raw verdicts saved as artifacts. Deterministic sampling seed recorded.

## 9. Over-flagging controls (added after independent audit — see `developer_agent/ANNOTATION_AUDIT.md`)
The v0.3 honeypots test only *under*-flagging (do judges catch injected corruption). An audit found judges also *over*-flag lossless-by-design transforms as loss, inflating rates ~2×. Fixes:

- **Zero-loss-transform allowlist (score 0/0/0/0 regardless of textual difference).** If the only differences reduce to: ANSI/VT escape stripping; `cat -n` / Read line-number prefixes; whitespace / blank-line collapse; JSON/XML reserialization with equal parse tree; minification; byte-identical repeated-line dedup; or table header/separator (`Name` / `----`) removal — it is **not** loss. This is now stated in the judge prompt and pre-computed where possible.
- **Normalized evidence artifact.** `missing_atoms` / `novel_atoms` are computed *after* ANSI + line-number normalization (`build_packets.py::_normalize`), so a lossless de-ANSI'd token is not reported as "novel/fabricated." (This was the top false-positive source — e.g. an ANSI-stripped function name scored as a fabrication.)
- **Over-flag honeypots (must score 0/0/0/0):** ANSI-colorized→stripped, pretty→parse-equal-minified JSON, whitespace collapse, `cat -n`→stripped, repeated-log dedup, header removed — including large packets. Report a per-judge **false-positive rate** alongside the catch rate; add a symmetric rule: **>20% over-flag on lossless controls → drop the judge.**
- **D3 gating.** If `what_agent_did_next` was fully supported by the compressed text, cap D1/D4 at "minor" — a flag must cite a specific consumer-relevant missing/wrong token, not "text differs."
