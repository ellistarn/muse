# Generative Evals

## Problem

We build muses but don't measure whether they're good. The owner reads their own muse and decides
it "feels right," which is subjective and doesn't scale to peer muses where the owner's model of
the peer is exactly what's insufficient.

`muse eval` exists but measures something different: it compares responses with and without the
muse on fixed questions, using an LLM judge. That tests whether the muse changes responses in a
positive direction. It doesn't test whether the muse captures the right patterns from the source
data.

Generative evals test the pipeline end-to-end: does the muse, built from a person's past
conversations, predict what that person would say in a new conversation?

## Solution

### Held-out evaluation

Split the person's conversations into train and test sets. Build the muse from the train set. For
each test conversation, show the muse the conversation context (the PR diff, or the discussion up
to a point) and ask it to respond as the person would. Compare the muse's generated response to
what the person actually said. Score the alignment.

```bash
muse eval --generative --peer ellistarn --project karpenter --cases 10
```

The command:

1. Lists all conversations for the peer.
2. Holds out recent conversations as the test set (20%, min 5, max 30).
3. Uses the existing muse (not rebuilt — the eval measures the muse as-is).
4. For each test conversation, extracts context and ground truth. Context is everything before
   the target's first substantive comment. Ground truth is that comment.
5. Asks the muse and a baseline (same model, no personality) to review the context.
6. The LLM judge pipeline (extract, align, score) measures alignment for both.

Many conversations are unusable: the target speaks first (no context), or their comment is too
short (under 10 words). The eval scans up to 5x the test set size to find enough usable cases.
In practice, about 1 in 3 conversations is usable for evaluation.

### Scoring

The judge is a three-step pipeline, not a single call:

1. **Extract concerns** from the ground truth review. Each concern is one actionable item — a
   distinct thing the reviewer wants changed or flagged. A broad claim ("error handling is
   fragile") supported by specific instances is one concern, not N. The extraction prompt is
   versioned — a change to it is a breaking change to the metric.
2. **Extract concerns** from the muse's review, using the same extraction prompt.
3. **Align and score**. One-to-one matching via the LLM judge. Each pair gets a score from 0
   to 1. Then:

- **Recall**: what fraction of the reviewer's stated concerns did the muse identify?
- **Precision**: what fraction of the muse's concerns match something the reviewer raised?

### What the first results show

Initial results on ellistarn's karpenter reviews (8 usable cases out of 10 scanned):

```
                    Muse    Base    Delta
  ────────────────────────────────────────
  Recall            0.67    0.61    +0.06
  Precision         0.18    0.12    +0.06

  Muse better: 1/8  Base better: 0/8  Tied: 7/8
```

Three observations from running the eval:

**Precision is structurally low for both muse and baseline.** LLMs are thorough reviewers —
they raise 8-12 concerns per PR. Human reviewers are selective — they state 2-4 concerns and
skip the rest. The precision metric penalizes thoroughness. This isn't a muse problem; it's a
measurement artifact. A reviewer who thinks "the error handling is fragile, the naming is
inconsistent, and the test coverage is thin" might only write about the error handling. The muse
raises all three. Precision says 0.33; the muse's actual coverage is higher.

This means precision is useful for comparing muse vs baseline (is the muse raising *more
relevant* concerns?) but the absolute number is not interpretable as muse quality. Recall is the
better single metric for muse quality, with the caveat that it has its own structural ceiling.

**Recall has a structural ceiling below 1.0.** Reviewers don't state everything they notice.
A recall of 0.6 may be excellent if reviewers typically state 60% of what they see.

**Most cases are tied between muse and baseline.** 7 of 8 cases show identical recall. The
base model is a strong reviewer on its own — it catches most of the same concerns. The muse's
value shows up on the cases where the reviewer's concern is specific to the codebase or reflects
a personal judgment call (issue #501: recall 1.0 vs 0.5). Issue #501 is an API design question
about whether a field should be an implementation detail — the muse caught the architectural
concern because Ellis's patterns include "what belongs at which layer." The base model missed it.

**The muse's value is on architecture and judgment, not code correctness.** The base model
catches bugs, naming issues, and missing error handling. The muse adds the reviewer's specific
architectural preferences and design instincts. This means the eval is most diagnostic on
conversations where the reviewer made a judgment call rather than pointed out an obvious defect.
The parameter tuning loop should weight these cases more heavily.

### Baseline

Each test case runs without the muse: the same base model reviews with no personality. The
baseline is pinned (same model and config across runs) so the delta is a stable measure of muse
value. The muse's contribution is the delta, not the absolute score.

### Applying to the owner's muse

The held-out approach generalizes to the owner's muse, but context extraction is harder. The
owner's conversations include AI interactions where the "ground truth" is a correction or
steering signal, not a structured review. Start with peer muse evals where the ground truth is
structured code review.

### Parameter tuning

Generative evals make tuning measurable. The immediate parameters to tune:

- **MinContentWords**: 10, 20, 50 — which produces the best eval score?
- **Observation count / limit**: 100, 200, 500 — diminishing returns?
- **Owner compose prompt vs peer-specific prompt**: measure the gap

Grid search over a small space. Build muse with each setting, run eval, pick the best.

### Prompt optimization (DSPy-style)

The eval score is an objective function over the full pipeline. The tunable surface includes
observe prompts, compose prompts, and refine prompts. Before attempting prompt optimization,
verify that eval scores are stable across runs. If the judge gives ±15% variance on the same
muse, it can't detect a 5% improvement from a prompt change.

## Decisions

### Why held-out evaluation instead of cross-validation?

Cross-validation (k-fold) gives more robust estimates but requires building k muses per eval
run. Each muse build costs $1-3 in LLM calls. Held-out evaluation is cheaper and sufficient
for directional signal.

### Why an LLM judge instead of embedding similarity?

Embedding similarity measures surface-level text overlap. Two reviews can flag the same concern
using completely different words. An LLM judge understands semantic alignment.

### Why most-recent for the test set?

Recent conversations test whether the muse generalizes forward. A muse that predicts old reviews
but not new ones has overfit to historical patterns.

### Why not rebuild the muse for each eval?

The eval measures the muse as-is. Rebuilding with a train-only split would give cleaner
held-out semantics but costs $1-3 per build and changes the muse being tested. The current
approach — use the existing muse, test against recent conversations the muse hasn't seen — is
cheaper and tests the artifact the operator actually uses.

## Deferred

### Automated prompt optimization

Use eval scores to search the prompt space. **Revisit when:** manual prompt tuning plateaus
and eval scores are stable enough to distinguish between variants.

### Regression testing

Run generative evals on every pipeline change. **Revisit when:** the eval is fast enough for CI.

### Multi-turn context

Predict the reviewer's follow-up after the author responds. **Revisit when:** single-turn eval
is validated.

### Cross-muse evaluation

Run each reviewer's muse against every other reviewer's test data to measure transferability.
Initial results (5 cases per cell, high variance):

- Some muses transfer well (Ellis→Jason +0.20, Todd→Jason +0.25)
- Some don't (Todd→Ellis -0.25 in one run, +0.00 in another)
- Most cross-evals are +0.00 — the personality signal is individual-specific

At 5 cases per cell the matrix is too noisy for strong conclusions. 20-30 cases per cell would
give stable results but costs ~1440 LLM calls across 12 cells. **Revisit when:** the eval is
cheap enough to run large matrices, or when team-muse composition needs to know which reviewers
overlap.

### Precision-adjusted scoring

The low precision numbers suggest the eval should distinguish "muse raised a valid concern the
reviewer didn't mention" from "muse raised an irrelevant concern." A second judge call could
score unmatched muse concerns as "valid but unstated" vs "wrong." **Revisit when:** precision
is the metric being used to make tuning decisions.
