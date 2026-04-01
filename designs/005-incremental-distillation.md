# Incremental Distillation

## Problem

Reprocessing all observations every update is wasteful and degrades quality. As context grows,
LLMs produce lower-quality responses (context rot), and eventually the input overflows entirely.

[Exponentially weighted moving averages](https://en.wikipedia.org/wiki/EWMA) solve a similar
problem in statistics: maintain a running estimate from streaming data without revisiting history.
The current estimate plus the new data point is sufficient. The same structure applies here: the
current muse plus new observations is sufficient to produce the next muse.

The analogy is not perfect. EWMA operates in one dimension with a numeric weighting factor.
A muse update operates in a complex multi-dimensional space — the space of all possible muse
documents — where "weighting" is achieved through the prompt, not a parameter.

## Solution

```
muse(0)   = ""
muse(n+1) = update(muse(n), new_observations)
```

Observations are persisted and reusable across distillation methods. Only the reduce step
changes: instead of reprocessing all observations, each update folds a small batch into the
existing muse. The full history has influence through the muse itself — prior observations
shaped it, and some were reinforced while others were not.

```
conversations ─► OBSERVE ─► observations ─► UPDATE ─► muse'
```

**Observe** extracts and refines observations per conversation. Parallel, Sonnet-tier. Shared
across all distillation methods.

**Update** is a single Opus call with extended thinking. Input: current muse + new batch.
Output: updated muse. The update prompt includes a target length for the muse. When the muse
approaches the budget, the model must compress or prioritize rather than append. This is a
simple form of forgetting: a fixed length budget forces the model to choose what stays based
on recency and importance. There is a real tradeoff between recency and importance.

### Update granularity

Each run of `muse compose` produces a batch of new observations. Updating one observation at a
time works but costs an Opus call per observation. Batching amortizes that overhead, but too
large a batch overwhelms the model's reasoning about a dense muse. The batch size shrinks as
the muse fills (see Decisions below).

### How the update works

The update is conservative. It starts from the muse as-is and only modifies what the new
observations give specific reason to modify. There is no numeric confidence score. The weight
of evidence is conveyed through natural language:

- **Add** new patterns not yet in the muse using hedged language ("may prefer short functions").
  As more observations confirm the pattern, the language becomes more direct ("consistently
  prefers short functions").
- **Strengthen** existing patterns by making language more direct or adding specificity. "Tends
  to prefer X" becomes "prefers X, especially in context Y."
- **Weaken or remove** patterns the new evidence contradicts. "Prefers tabs" becomes "has used
  both tabs and spaces" or is removed entirely.
- **Leave everything else alone.** Absence of evidence is not evidence of absence. A pattern
  observed once and never contradicted persists indefinitely.

### Storage

```
~/.muse/
├── conversations/{source}/{conversation_id}.json          # input, syncable
├── observations/{source}/{conversation_id}.json           # shared, syncable
├── compose/
│   ├── labels/{source}/{conversation_id}.json             # clustering-specific
│   └── normalization.json                                 # clustering-specific
├── sessions/                                              # ask sessions, not compose input
├── versions/{timestamp}/
│   ├── muse.md                                            # output, syncable
│   └── diff.md                                            # output, syncable
```

### Usage

```bash
muse compose --method=incremental
```

## Bootstrap and Updates

First run: the muse is empty, so the batch can be large (~200 most recent observations). As
the muse fills through successive updates, the batch shrinks (see Decisions below).

## Decisions

### Why does batch size shrink?

The batch size is a function of how full the muse is. An empty muse has no accumulated
information to reason against, so bootstrap can process ~200 observations at once. A half-full
muse encodes enough compressed knowledge that the update step needs more care — batch drops to
~100. A full muse has high information density from many prior updates, so the batch shrinks to
~10 to let the model reason carefully about a dense input plus the new observations.

Observation count is a proxy for tokens, which is a proxy for information content. The real
batching unit is probably tokens or something closer to compressibility of the input, not a
fixed observation count. These numbers are tuned on vibes for now.

### Why bias toward the existing muse?

The update treats the existing muse as the starting point and requires new observations to justify
changes. One observation can't rewrite the muse. This is analogous to a low learning rate in EWMA:
new data adjusts the estimate gradually. The alternative (high trust in new data) risks instability
where a single unusual conversation reshapes the muse. The tradeoff is that genuine changes in how
the owner thinks take several observations to fully propagate.

### How does observation strength work?

The strength of an observation comes from the user's input. A course correction ("no, do it this way")
is stronger than a passing preference. The observe step captures this in how it phrases the
observation, and the update LLM infers relative strength from that language. We don't add explicit
weighting beyond what the user's own words convey.

## Deferred

### Batch size tuning

The sliding scale (~200 → ~100 → ~10) is a starting point. The right batching unit is very likely something
more like token count or information density rather than observation count. Requires experimental evaluation.

### Weighting

Each update takes a muse and some observations and returns a new muse. The words in the new
muse are entirely determined by the input muse, the observations, the prompt to the LLM, and
the LLM's choice (with some wiggle room from stochastic decoding). Weighting is implemented
as "prompt the LLM to strike a balance between recent observations and the current content of
the muse." Optimizing that prompt is future work.

### Defining tradeoff between recency and importance

Things change over time, and some things that were important last year are less important this year.
This isn't always true though, and recency bias happens when we assume that the recent things are
strictly more important. Some important things just don't happen often enough, and defining how to
make this tradeoff when constructing a muse is left for future work, for now we just make our most
capable model try to figure out something sensible.
