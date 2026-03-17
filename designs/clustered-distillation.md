# Clustered Distillation

## Problem

Single-pass distillation doesn't scale. As the number of conversation samples grows attention and
quality degrades.

## Solution

### Pipeline

```
conversations ─► OBSERVE ─► observations ─► CLUSTER ─► clustered samples ─► COMPOSE ─► muse.md

OBSERVE           Per-conversation LLM call (parallel)
                  "What does this reveal about how this person thinks?"

CLUSTER

  CLASSIFY        Per-observation LLM call (parallel)
                  "What dimension of wisdom does this touch?" → classification string

  EMBED           Bedrock embeddings on classifications → vectors

  GROUP           HDBSCAN (min_cluster_size=3) → N clusters + noise

  SAMPLE          Per-cluster token-bounded selection (~10k tokens)
                  Centroid-nearest first, then edges
                  Preserve count: "12 of 47 observations shown"

COMPOSE

  SYNTHESIZE      Per-cluster LLM call (parallel)
                  Input: sampled observations + cluster name + count
                  Output: cluster summary

  MERGE           Single LLM call over all cluster summaries
                  Organize, don't filter → muse.md
```

### Strategies

```bash
muse distill                      # default: clustering
muse distill --method=clustering
muse distill --method=map-reduce  # old implementation
```

### Caching

Observations, classifications, and embeddings cached per-conversation. Grouping, sampling,
and compose recomputed each run. `--reclassify` forces re-classification. `--reobserve` forces
re-observation.

### Storage

```
~/.muse/
├── distill/
│   ├── observations/{key}.json
│   ├── classifications/{key}.json
│   ├── embeddings/{key}.json
│   └── runs/{timestamp}/
│       ├── clusters.json
│       ├── samples/
│       │   ├── cluster-0.json
│       │   └── noise.json
│       ├── syntheses/
│       │   ├── cluster-0.md
│       │   └── cluster-1.md
│       └── compose-input.md
```

## Decisions

D1. Observe — per-conversation, parallel. Each conversation produces observations independently.
Which conversations to process, staleness, and deduplication are concerns of this step.

D2. Classification — dimension of wisdom, not topic. The classification names _how_ someone thinks,
not _what_ they were talking about. Embedding operates on the classification string, so its quality
drives cluster coherence directly.

D3. Embeddings — Bedrock API. Extends existing Bedrock client. No new dependencies, no local model
management. Need to expand providers eventually.

D4. Grouping — HDBSCAN, min_cluster_size=3. No pre-specified k. Explicit noise handling — outliers
labeled, not forced into clusters. Permissive threshold so rare dimensions survive. No mature Go
HDBSCAN exists; likely shell out to Python for this step.

D5. Sampling — token-bounded, ~10k per cluster. Scales to any cluster size. Centroid-nearest for
representative coverage, edges for tension/boundary cases. Frequency signal preserved via counts
passed to synthesize.

D6. Noise — persist, don't synthesize. Saved to disk for debugging. If something doesn't cluster, it's
either noise or a dimension that will emerge as more observations arrive.

D7. Compose — two passes. SYNTHESIZE compresses each cluster into a summary (parallel), then MERGE
combines all summaries into the final muse.md. The intermediate summaries provide debuggable
artifacts and keep the final merge call focused on organization rather than synthesis.

D8. Strategies — --method flag, default clustering. clustering and map-reduce. Both produce
identical output format. Strategy interface in Go.

D9. Deferred. Evaluation framework (build both first, compare with data). Observation weighting
(pipeline accommodates it). Incremental cluster assignment (measure speed first). Alternative
embedding providers (start with Bedrock).
