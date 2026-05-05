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

The composition pipeline has four stages between observations and the muse: label, cluster,
sample, summarize. The fixes below target sample and summarize.

## Fixes

### Quote-prioritized sampling

The sample step selects observations up to a token budget per cluster. It currently shuffles
randomly. Observations with verbatim owner quotes compete equally with quoteless observations,
and the budget often fills before quoted observations get a chance.

The fix partitions observations into quoted and quoteless groups. Quoted observations fill
the budget first. Within each group, observations are shuffled randomly.

### Exemplar prompt

Two sentences added to the summarize prompt:

> Summarize the pattern first, then include one or two verbatim quotes that illustrate the
> pattern in action. The summary tells the reader what the person does. The quotes show them
> doing it.

Without this, the summarize step produces accurate abstractions. With it, the abstractions
are followed by the owner's actual words demonstrating the pattern.

## Provenance metadata

The composed muse includes an HTML comment header with composition metadata:

```html
<!--
composed: {date}
observe: {mode}
observations: {count}
clusters: {count}
corpus: {count} conversations
-->
```

## Evidence

Research [2] tested these fixes on a corpus of 453 conversations. The qualitative evidence
is strongest: the same topic (language precision) composed without the exemplar prompt
produces "I treat abbreviations as contracts. Naming the steps is part of the design." With
the exemplar prompt, it produces "I don't like 'consolidateAfter determines candidacy,'
because consolidateAfter determines which nodes CANNOT be candidates. Related, but different
in goal."

The fixes help proportionally to cluster size. On adaptive observations (25 per cluster),
both fixes improve concreteness. On default observations (5 per cluster), the fixes barely
register because the summarize step was already working with manageable input.

## Deferred

**Quality gate.** An Opus-tier judge rates each observation against its source conversation
as grounded, generic, or misleading; only grounded observations flow into composition.
Reduces misleading rate from 7-12% to under 2%. Costs ~$0.02/conversation, cacheable.
Should land as a pipeline stage between observe and label.
Depends on pipeline integration work not yet designed.

## References

[1] designs/013-observation-strategies.md

[2] Improving Muse Composition: improving-muse-composition.md
