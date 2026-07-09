# Router calibration research brief — grounding routing in measured data

Status: **research task, not scheduled.** Everything below is the work required to
replace hand-authored routing parameters with measured ones. Written 2026-07-09
after the vSR source study + live signal probing.

## The problem, stated honestly

The router's *mechanisms* are sound (trained mmBERT classifier + embedder, mass
thresholding, contrastive margins), but its *parameters* are hand-authored:

| Parameter | Today's source | Risk |
|---|---|---|
| complexity hard/easy prototype banks (8+8) | author intuition | low recall on hard turns phrased unlike the prototypes (measured: "prove this theorem" → medium) |
| complexity threshold 0.15 | eyeballed from ~10 probes | uncalibrated precision/recall |
| domain `min_mass` 0.7 | eyeballed from ~8 probes | uncalibrated |
| which categories escalate ({math, physics, chemistry}) | vSR's benchmark **for QA**, transplanted | unvalidated for Claude Code traffic |
| embedding signal thresholds | eyeballed; the deep-work signal was REMOVED after probing showed baseline cosine ~0.6 defeats separation | any future embedding signal needs the same scrutiny |

Known residuals from live probing (accepted, undocumented failure otherwise):
- **Confident in-domain trivia escalates** ("what is the boiling point of water" →
  chemistry 0.94 → opus). Category ≠ difficulty; no prompt-side signal fixes this.
- **Phrasing ≠ task difficulty** ("improve this" on a 10-line file reads hard).
  The prompt alone cannot see the artifact.

## What vSR does that we should replicate (adapted)

vSR's real grounding is a **benchmark → train → deploy** loop
(`website/docs/training/ml-model-selection.md`): run candidate models over
**queries with ground truth**, record which model got each right, train a
selector (KNN/SVM/MLP over query embeddings), generate config. Their per-category
`use_reasoning` flags come from an accuracy-vs-cost ablation over ~15 reasoning
datasets (GSM8K, MATH, GPQA, MMLU, ARC, DROP, StrategyQA…): reasoning measurably
lifts math/science, barely moves commonsense — hence math→reason, chat→don't.

**Critical gap even in vSR: zero coding datasets.** No HumanEval/MBPP/SWE-bench
anywhere in their bench. Nobody in this stack has measured coding difficulty —
and Claude Code traffic is coding-heavy.

## The research task

**Goal:** a labeled dataset where each example is `(request text/context) →
minimal sufficient tier` (haiku-sufficient / sonnet-sufficient / needs-opus), and
parameters tuned against it.

The label is deliberately **tier-sufficiency, not "hard/easy"** — it is the
decision the router actually makes, and it is measurable: run the tiers on the
same input, judge whether the cheaper output is acceptable.

### Data sources, in order of value
1. **Own traffic (highest value, coding-realistic).** The router log already
   captures `requested`, `model`, `in/out tokens`, session. Add an opt-in capture
   of (hashed) request text for sampled turns; replay a sample through 2–3 tiers;
   judge sufficiency pairwise with a strong model (blinded, honeypot-validated —
   reuse the judging methodology from the compression evals). ~300–1000 labeled
   turns is enough to calibrate thresholds.
2. **Public QA/reasoning sets** (GSM8K/MATH/GPQA/MMLU subsets): cheap ground
   truth for the *non-coding* categories; validates the category map directly
   (per-category accuracy by tier).
3. **Public coding sets** (HumanEval/MBPP for function-level; SWE-bench-lite for
   repo-level): pass@1 per tier gives a coding-difficulty signal, imperfectly
   matched to interactive Claude Code turns but the only public option.

### What gets calibrated, concretely
1. **Category → tier map**: per-category tier-sufficiency rates replace the
   transplanted {math, physics, chemistry} set.
2. **`min_mass` and complexity threshold**: pick operating points from measured
   precision/recall curves (cost-first: optimize $ saved subject to a quality
   floor, e.g. ≤2% under-served hard turns).
3. **Prototype banks**: replace hand-typed phrases with medoid-clustered
   representatives of *measured-hard* vs *measured-easy* real turns (vSR's
   prototype-bank clustering, which we skipped, becomes useful exactly here).
4. **Optionally a trained selector** (KNN over query embeddings labeled by
   sufficient tier, per vSR) if the calibrated heuristics plateau.

### Beyond prompt-only signals (the residuals need this)
The two known residuals are unfixable from prompt text alone. Candidate
artifact-aware signals worth evaluating in the same study: actual context/tool
-result size, diff size, file count touched — cheap, already visible to the
proxy, and exactly what "improve this" needs to distinguish a 10-line file from
a subsystem.

### Deliverables
- `bench/router/` harness: replay labeled turns → per-tier outputs → judged
  sufficiency → precision/recall + $-saved report (CI-runnable on the frozen set).
- Calibrated `default.json` with provenance comments (which dataset, which
  operating point).
- A short report: measured over/under-routing rates before vs after.

### Non-goals
- Training a difficulty model from scratch (calibrate the existing signals first;
  a KNN selector is the fallback, per vSR).
- Online learning / feedback loops (a later, separate concern).
