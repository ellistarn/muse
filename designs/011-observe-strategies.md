# Observe Strategy Comparison

Two separate findings led to multi-zoom observation. They're related but distinct.

## Finding 1: context rot (009)

Long conversations produce 0 observations under the baseline pipeline. The same
conversations produce 7-9 observations after triage + owner-only filtering. This is
catastrophic signal loss with a clear fix.

The evidence is on the preserve-context branch. Two test conversations:

| Conversation | Turns | Baseline | Triage + owner-only |
|---|---|---|---|
| RFC design session (104 msgs) | 27 | 0 observations | 7 observations |
| Orwell pass editing (211 msgs) | 26 | 0 observations | 9 observations |

The mechanism: early turns carry design decisions and corrections. Later turns
accumulate mechanical work (git operations, CI, formatting). The observe prompt reads
sequentially and later noise washes out earlier reasoning signal. This is context rot.

Windowed observation treats context rot at the source: observe in sliding windows so
no turn is ever evaluated in the context of distant mechanical turns. Mechanical
windows return NONE, reasoning windows produce observations. This works.

## Finding 2: attention allocation (this doc)

Different observation strategies find different signal, even when context rot isn't the
problem. How you present the input changes what the LLM attends to. This is not about
losing signal -- it's about which signal gets attention.

### Corpus experiment: 2026-04-07

Ran full compose on 417-430 conversations using four strategies. Same downstream
pipeline (label through compose). Only the observe stage differs.

| Strategy | Observations | Conversations w/ obs | Clusters | Total cost |
|---|---|---|---|---|
| Multi-zoom (default) | 658 | 82 | 25 | $18.25 |
| Windowed (size 8, stride 4) | 462 | 69 | 22 | $13.46 |
| Triage + owner-only | 243 | 42 | 21 | $6.52 |
| Full conversation (baseline) | 151 | 42 | 19 | $6.40 |

All four produce ~100-line muse.md files. The difference is which 100 lines.

**Windowed** (local zoom) finds craft-level specifics: named editing passes with exit
conditions, punctuation rules, voice authenticity tests ("I would never type those"),
artifact staleness, document structure as epistemic signaling. Small-scale reasoning
moments that live in specific turns.

**Triage + owner-only** (global zoom) finds decision-making patterns: where decisions
should land (operator vs app-dev), named reusable primitives (Ralph Wiggum loop,
Orwell pass), agent loop architecture, scope cutting as discipline. Stripping assistant
text concentrates the owner's strategic reasoning.

**Baseline** finds analytical/philosophical depth: separating the lens from the
specimen, substrate vs method failure, metacognitive self-correction. The 0-observation
problem only affects long conversations; short conversations work fine under all
strategies.

**Multi-zoom** (windowed + triage, merged) combines both zoom levels. It produced
sections that neither strategy found alone: "Who decides, not what's possible"
(decision allocation, knowledge topology) and "The distribution, not the number"
(metrics as hypotheses, signal stratification).

The three single-strategy muses are not three approximations of the same portrait.
They emphasize genuinely different facets. The compose step is bottlenecked on
observation diversity, not volume.

### Orwell experiment

Separate experiment applying three strategies to Orwell's "Politics and the English
Language" (~5,000 words). Full results in orwellian-observation.md.

| Strategy | Observations | Strongest section |
|---|---|---|
| Baseline (full essay) | ~54 + 5 cross-cutting | Even spread, generic on catalog/prescription |
| Windowed (5 windows) | ~34 raw, 23 deduped | Catalog (structural insights from close reading) |
| Triage (reasoning only) | ~42 + 4 cross-cutting | Diagnosis (19 obs vs baseline's 13) |

The essay is ~5,000 words -- below the threshold where context rot appears. No
strategy returned NONE. Baseline produced 13 diagnosis observations; it didn't fail.

What the Orwell experiment shows is attention allocation, not context rot:

- **Windowed** found structural patterns in the catalog that baseline treated as a
  list: metaphor lifecycle model, escalating severity across categories, substitution
  as proof technique. Close reading of a contained section yields insights that
  sequential reading glosses over.

- **Triage** concentrated on the diagnosis section and found the sharpest observations:
  self-deception as a service, "designed" deployed as the strongest verb only after
  evidence accumulates, clarity framed as loss of capability rather than gain of wisdom.

- **All three converged** on: bidirectional causation, steelmanning before dismantling,
  epistemic calibration, self-incrimination, concrete vs abstract, the pacification
  juxtaposition, Rule 6 as override.

### Why these are different findings

Context rot is catastrophic: 0 observations from a conversation that should produce
7-9. The fix is structural (windowed observation, triage + owner-only).

Attention allocation is quantitative: 13 vs 19 observations in a section, or different
observations from the same material. The fix is running multiple zoom levels.

Multi-zoom addresses both: windowed fixes context rot in long conversations, and the
combination of local + global zoom captures signal that either alone would miss.

## Decision: multi-zoom as default

Multi-zoom is the default observe mode. For conversations over 10 turns:

1. **Local pass:** sliding windows (size 8, stride 4) with assistant context, observe
   each, deduplicate across overlapping windows, refine
2. **Global pass:** mechanical filter, LLM triage, owner-only observe
3. **Merge:** combine observations from both passes, deduplicate by text containment
4. **Downstream:** label, theme, cluster, summarize, thesis, compose (unchanged)

Small conversations (10 turns or fewer) get a single pass.

### Cost

Multi-zoom observe costs ~$17 for a 430-conversation corpus (2.7x baseline).
Downstream stages add ~$1.70 regardless of strategy. Total: ~$18.25.

The main cost lever is window size. Larger windows mean fewer observe calls but risk
reintroducing context rot. The current 8/4 split is untested against alternatives.

### Implementation

The `--observe-mode` flag on `muse compose` selects the strategy. It threads through
BaseOptions to observeAndParse, which dispatches to observeMultiZoom for the default
mode. Multi-zoom calls observeWindowed and observeTriageOwnerOnly independently, then
merges with deduplicateObservations. Global pass failure is non-fatal (falls back to
local only).

### What multi-zoom doesn't do

The two passes run independently. There is no cascade. The Orwell experiment suggested
triaging first, then windowing reasoning passages. This is a potential optimization
but the independent merge already captures both zoom levels' signal.

### Deferred: observe from documents

The Orwell experiment was done outside the pipeline because the observe prompt expects
conversations, not essays. A "generate observations from document" capability would
let the pipeline learn from authored text (design docs, essays, blog posts). Out of
scope for now.
