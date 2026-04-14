# Composition Fixes

## Problem

Better observation strategies [1] find three to five times more grounded observations than
the default pipeline. But the composition pipeline compresses additional observations into
more abstract summaries. A cluster containing "I would never say ablate," "colons paper over
conceptual gaps," and "negative definitions are a pathology" becomes "tracks linguistic
precision across multiple dimensions." The details that make a muse distinctive are lost.

This matters when clusters are large (20+ observations). The default pipeline produces small
clusters (~5 per cluster) where the summarize step already has manageable input. The adaptive
observation strategy produces large clusters (~25 per cluster) where the summarize step
over-compresses [2].

The composition pipeline has four stages between observations and the muse. **Label** assigns
a topic label to each observation. **Cluster** groups observations by label. **Sample** selects
observations up to a token budget per cluster. **Summarize** writes one paragraph per cluster.
The fixes below target the sample and summarize stages.

## Three fixes

### Quote-prioritized sampling

The sample step selects observations up to a token budget per cluster. It currently shuffles
randomly. Observations with verbatim owner quotes compete equally with quoteless observations,
and the budget often fills before quoted observations get a chance.

The fix partitions observations into two groups: those with quotes and those without. Quoted
observations fill the budget first. Within each group, observations are shuffled randomly.
This is a stable partition and shuffle.

### Exemplar prompt

Two sentences added to the summarize prompt:

> Summarize the pattern first, then include one or two verbatim quotes that illustrate the
> pattern in action. The summary tells the reader what the person does. The quotes show them
> doing it.

Without this, the summarize step produces accurate abstractions. With it, the abstractions
are followed by the owner's actual words demonstrating the pattern.

### Quality gate

A judge command (`muse judge --filter`) rates each observation against its source conversation
as grounded, generic, or misleading, using a more capable model (Opus) than the one used for
observation. Filtering to grounded-only before composition removes observations that compete
for attention without contributing signal.

On a representative run of adaptive observations, roughly 88% rate grounded, 6% generic, 6%
misleading. Exact counts vary across runs as the corpus grows. The quality gate costs roughly
$0.02 per conversation, cacheable across runs.

Filtered observations are stored separately so the unfiltered set is preserved. The current
implementation encodes filtering as a storage-level convention (`observations/{mode}-filtered/`
accessed via `--observe-mode=adaptive-filtered --skip-observe`). This is a workaround.
Filtering is a pipeline stage between observe and compose, and the deferred work below
addresses making it one.

## Provenance metadata

The composed muse includes an HTML comment header with composition metadata:

```html
<!--
composed: {date}
observe: {mode}
observations: {count}
clusters: {count}
-->
```

This tells a future reader or pipeline version what produced the muse.

## Evidence

Research [2] tested these three fixes independently and in combination on a corpus of 453
conversations. The qualitative evidence is strongest: the same topic (language precision)
composed without the exemplar prompt produces "I treat abbreviations as contracts. Naming the
steps is part of the design." With the exemplar prompt, it produces "I don't like
'consolidateAfter determines candidacy,' because consolidateAfter determines which nodes
CANNOT be candidates. Related, but different in goal."

The fixes help proportionally to cluster size. On adaptive observations (25 per cluster),
all three fixes improve concreteness. On default observations (5 per cluster), the fixes
barely register because the summarize step was already working with manageable input.

## Deferred

**Automating the quality gate.** The judge step is currently a separate command. It should
run as a pipeline stage between observe and label.

**Single-command adaptive-filtered compose.** Currently requires three commands: compose,
judge --filter, compose --skip-observe. Should be one command.

## References

[1] designs/013-observation-strategies.md

[2] Improving Muse Composition: improving-muse-composition.md
