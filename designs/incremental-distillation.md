# Incremental Distillation

## Problem

Batch distillation reprocesses all observations every run and overflows context at ~130
conversations.

## Approach

```
muse(n+1) = update(muse(n), new_observations)
```

Each update folds new observations into the existing muse. The context window holds only the
current muse plus one batch of observations, never the full history.

## Pipeline

```
conversations ─► OBSERVE ─► observations ─► UPDATE ─► (muse', forgotten)

                ┌───────────────────────────────────────────────────────┐
 OBSERVE        │  Per-conversation LLM call (parallel, Sonnet)        │
                │  Same as today — extract + refine per conversation    │
                │  → zero or more observations per conversation         │
                └───────────────────────────────────────────────────────┘

                ┌───────────────────────────────────────────────────────┐
 UPDATE         │  Single LLM call (Opus, extended thinking)           │
                │  Input: current muse + new observations              │
                │  Output: updated muse + what was forgotten           │
                └───────────────────────────────────────────────────────┘
```

Two stages, two model tiers.

### Update granularity

The natural batch is a sync: a few new conversations producing ~10-15 observations. One observation
at a time works but costs an Opus call per observation. Batching amortizes cost.

### How the update works

The muse is ground truth. The update only changes what new evidence gives reason to change:

- **Add** new patterns not yet in the muse.
- **Strengthen** patterns the new evidence reinforces (higher confidence, more specificity).
- **Weaken or remove** patterns the new evidence contradicts.
- **Leave everything else alone.** Absence of evidence is not evidence of absence. A pattern
  observed once and never contradicted persists indefinitely.

Forgetting comes from contradiction or subsumption, not from time passing. "I used to prefer tabs,
now I use spaces" is a reason to forget. Six months of silence about tabs is not.

The forgotten log is defined in [grammar.md](grammar.md). Each update produces both an updated
muse and a forgotten log recording what was removed and why.

### The update prompt

- The muse is ground truth. Only change what new evidence gives reason to change.
- New patterns are added tentatively. Confidence grows with repeated observation.
- Contradicted patterns weaken or are removed. The forgotten log records what and why.
- Reinforced patterns strengthen.
- Everything else stays.

### Order independence

There is no canonical ordering of observations. They arrive from multiple machines and sources.
Framing the update as "reinforce, add, or weaken relative to the current muse" is naturally
order-insensitive. The muse is the state, observations are perturbations.

### Storage

Uses the existing versioning structure with one new file:

```
~/.muse/
├── conversations/{source}/{session_id}.json
├── observations/{source}/{session_id}.md
├── muse/versions/{timestamp}/
│   ├── muse.md
│   ├── diff.md
│   └── forgotten.md                                  # new
```

## Commands

```bash
muse distill                     # observe new conversations, update muse
muse distill --learn             # re-update from recent observations without re-observing
muse distill --reobserve         # re-observe from scratch, then update
muse distill kiro                # only observe kiro conversations, then update
```

`muse show --diff` and `muse show --forgotten` display the respective logs.

## Bootstrap

First run (no existing muse): select the ~200 most recent observations and run a single learn
call to produce the initial muse. Older observations are not lost; they can be folded in through
subsequent incremental updates if they keep surfacing.

`muse distill --rebuild` re-bootstraps from recent observations. This is disaster recovery.

## Cost

Post-observe cost per sync (3 new conversations, ~15 new observations, Opus with 16k thinking
budget): ~$1.60. Thinking tokens dominate. The update input is always ~7k tokens (the muse plus
the new batch), so cost does not grow with total observation count.

## Retention: sticky vs. decaying

The update prompt drops one-off observations that don't reflect a clear pattern. This is a filter
on weak evidence and is uncontroversial.

The harder question is what happens to established patterns over time. Two options:

**Sticky**: patterns persist unless contradicted. Forgetting requires evidence.
**Decaying**: patterns fade unless reinforced. Forgetting is the default.

Both have failure modes. Sticky can accumulate stale patterns that no longer apply but were never
explicitly contradicted. Decaying loses important infrequent patterns: annual review feedback,
career decisions, core values. These appear rarely but matter persistently.

Decay is also hard to reason about. What rate? Uniform across all patterns? Some important things
only happen once a year. A decay model either needs per-pattern tuning (complex, probably wrong)
or applies a uniform rate that is too aggressive for rare patterns and too lenient for stale ones.

We start with sticky. The failure mode (stale accumulation) is recoverable: `--rebuild`
re-bootstraps from recent observations, and the forgotten log provides audit. The decaying failure
mode (re-learning the same things every cycle) is more disorienting and harder to detect.

Long-term memory (separating stable traits from evolving patterns) may be part of the best answer.
It remains to be seen.

Note: `self-awareness.md` currently says "Natural decay... the observation gets pruned in future
synthesis cycles." This contradicts the sticky model. If there is agreement on sticky, we should
update self-awareness.md in a follow-up change.

## Deferred

### Configurable bootstrap window

Bootstrap uses the ~200 most recent observations. Users may want to control this. **Revisit
when:** users ask for it.

### Conflict-aware sync

Concurrent updates from different machines: last write wins. The forgotten log and `--rebuild`
provide recovery. **Revisit when:** concurrent updates cause real problems.
