# Incremental Distillation

## Problem

Batch distillation reprocesses all observations every run. A 200k context window holds ~100-130
conversations before the learn step overflows. Clustered distillation solves this but introduces six
pipeline stages, an embedding model dependency, a custom HDBSCAN implementation, and a local-only
artifact store. We need to update a document with new information without rereading everything.

## Insight

The question isn't "what does the complete set of observations say?" — it's "given who we think
this person is, what does this new evidence change?"

```
muse(n+1) = update(muse(n), new_observations)
```

Each update folds new observations into the existing muse. Reinforced patterns persist. Patterns
that stop appearing fade. This is analogous to EWMA: recent observations have direct influence,
older observations are represented only through their cumulative effect on the muse.

Forgetting is a feature. A muse that remembers everything equally is a database, not a model of
how someone thinks. The muse should reflect who someone is now, weighted toward the recent past.

## Design

### Pipeline

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

Two stages, two model tiers. The context window holds only the current muse plus one batch of
observations, never the full history.

### Update granularity

The natural batch is a sync: a few new conversations producing ~10-15 observations. One observation
at a time works but costs an Opus call per observation. Batching amortizes cost.

### Forgetting and the forgotten log

Each update produces two outputs:

1. **muse.md** — the updated muse
2. **forgotten.md** — what was removed or softened, with a reason for each

The forgotten log provides:

- **Audit.** Why did the muse stop mentioning X?
- **Recovery.** Feed a dropped observation back in to restore it.
- **Self-calibration.** If the same pattern keeps getting forgotten and re-observed, the update
  is too aggressive.

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

### The update prompt

The prompt encodes EWMA-like dynamics:

- The existing muse is the prior. New observations are the update.
- Reinforced patterns strengthen. Contradicted patterns soften — weaken first, remove on subsequent
  updates if not re-observed.
- New patterns are added tentatively. Confidence grows with repeated observation.
- The muse represents many observations; a small batch should shift it proportionally.

### Order independence

There is no canonical ordering of observations. They arrive from multiple machines and sources.
Framing the update as "reinforce, add, or soften relative to the current muse" is naturally
order-insensitive — the muse is the state, observations are perturbations.

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
call to produce the initial muse. Older observations are not lost — they can be folded in through
subsequent incremental updates if they keep surfacing. This avoids context overflow on bootstrap
and reflects the EWMA principle: recent observations matter most.

`muse distill --rebuild` re-bootstraps from recent observations. This is disaster recovery.

## Cost

The observe step is shared across all approaches. Post-observe cost per sync (3 new conversations,
~15 new observations):

| | Map-reduce | Clustered | Incremental |
|---|---|---|---|
| Classify | — | 15 Sonnet calls | — |
| Embed | — | 15 Titan calls | — |
| Synthesize | — | ~20 Sonnet calls | — |
| Learn/Merge/Update | 1 Opus, ~300k tokens | 1 Opus, ~20k tokens | 1 Opus, ~7k tokens |

Map-reduce's Opus input grows linearly with total observations and eventually overflows. Clustered
stays bounded but requires ~35 Sonnet + 15 Titan calls per sync. Incremental is one small Opus call.

At 10k total observations, map-reduce is broken (~3M input tokens). Clustered's synthesize step
grows with cluster count. Incremental's update is still ~7k tokens — the muse plus the new batch.

## Decisions

### Why incremental over clustered?

Both solve context overflow. Clustering groups and summarizes before merging — six stages, four
API calls, an embedding model, a custom clustering algorithm. Incremental updates never look at all
observations at once — two stages, one API call.

Clustering gives thematic organization within each run. Incremental updates rely on the muse
accumulating structure over time. The clustered merge prompt says "don't preserve cluster boundaries
if a better structure emerges" — the thematic organization is already expected to be reorganized.

### Why forgetting is acceptable

Strong patterns that are consistently reinforced survive. Weak patterns that appeared once fade.
If forgetting drops something it shouldn't, the forgotten log makes it visible, and feeding the
observation back in restores it.

## Deferred

### Configurable bootstrap window

Bootstrap uses the ~200 most recent observations. Users may want to control this — larger windows
for comprehensive initial muses, smaller for faster starts. **Revisit when:** users ask for it.

### Adaptive forgetting rate

The forgetting rate is implicit in the prompt. An explicit mechanism — tracking observation count
or muse age — could tune it. **Revisit when:** the muse is too sticky or too volatile.

### Conflict-aware sync

Concurrent updates from different machines: last write wins. The forgotten log and `--rebuild`
provide recovery. **Revisit when:** concurrent updates cause real problems.
