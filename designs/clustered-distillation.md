# Clustered Distillation

## Problem

Distilling a large corpus of observations into a muse document. Single-pass distillation breaks down
on three fronts: the observation set outgrows context window limits, model attention dilutes
distinctive signal as input volume grows, and redundant observations bias output toward
frequently-observed patterns at the expense of rare but defining ones.

## Solution

### Pipeline

```
conversations ─► OBSERVE ─► observations ─► CLUSTER ─► samples ─► SYNTHESIZE ─► MERGE ─► muse.md

                ┌───────────────────────────────────────────────────────┐
 OBSERVE        │  Per-conversation LLM call (parallel)                │
                │  "What does this reveal about how this person thinks?"│
                │  → zero or more observations per conversation         │
                └───────────────────────────────────────────────────────┘

                ┌───────────────────────────────────────────────────────┐
 CLUSTER        │                                                       │
                │  CLASSIFY   Per-observation LLM call (parallel)       │
                │             "What pattern of thinking or working is   │
                │              this an instance of?"                    │
                │             → classification                          │
                │                                                       │
                │  EMBED      Bedrock embeddings on classifications     │
                │             → vectors                                 │
                │                                                       │
                │  GROUP      HDBSCAN (min_cluster_size=3)              │
                │             → N clusters + noise                      │
                │                                                       │
                │  SAMPLE     Per-cluster random token-bounded           │
                │             selection (~10k tokens)                    │
                │                                                       │
                └───────────────────────────────────────────────────────┘

                ┌───────────────────────────────────────────────────────┐
 SYNTHESIZE     │  Per-cluster LLM call (parallel)                     │
                │  sampled observations + cluster name → cluster summary│
                └───────────────────────────────────────────────────────┘

                ┌───────────────────────────────────────────────────────┐
 MERGE          │  Single LLM call over all cluster summaries          │
                │  + raw noise observations                            │
                │  Organize, don't filter → muse.md                    │
                │                                                       │
                │  Noise framing: "These observations didn't fit any    │
                │  theme. Preserve what's distinctive, ignore what's    │
                │  redundant with the cluster summaries."               │
                └───────────────────────────────────────────────────────┘
```

### Strategies

Two distillation methods are available permanently. Clustering produces thematically coherent output
at higher complexity. Map-reduce is simpler and sufficient for smaller observation sets.

```bash
muse distill                      # default: clustering
muse distill --method=clustering
muse distill --method=map-reduce
```

### Caching

Each cached artifact stores a fingerprint — a hash of its inputs. On read, if the fingerprint
doesn't match current inputs, the cache misses and the artifact is recomputed. No flags needed for
correctness; the dependency chain self-invalidates:

```
conversation → (observe prompt) → observations
observation → (classify prompt) → classification
classification → (embedding model) → embedding
```

Change a conversation and its observations invalidate, which invalidates classifications, which
invalidates embeddings. Change the classify prompt and all classifications invalidate, cascading to
embeddings. Correctness is structural, not procedural.

Fingerprints per layer:

- **Observation**: `hash(conversation.UpdatedAt, observePromptHash)`
- **Classification**: `hash(observationContent, classifyPromptHash)`
- **Embedding**: `hash(classificationContent, embeddingModel)`

Grouping, sampling, synthesis, and merge are recomputed each run — they're cheap relative to the
cached stages.

`--reobserve` and `--reclassify` exist as explicit force-refresh flags but correctness never depends
on them. Synced artifacts are validated by fingerprint on read, so stale data from another machine is
automatically recomputed.

### Storage

Conversations are input. The muse is output. Everything in between is pipeline internals owned by the
distillation system, nested under `distill/`.

```
~/.muse/
├── conversations/{source}/{session_id}.json              # input, syncable
├── distill/
│   ├── observations/{source}/{session_id}.json           # syncable
│   ├── classifications/{source}/{session_id}.json        # syncable
│   ├── embeddings/{source}/{session_id}.json             # syncable
│   └── clusters/{id}.json                                # ephemeral, not synced, overwritten each run
├── muse/versions/{timestamp}/muse.md                     # output, syncable
├── muse/versions/{timestamp}/diff.md                     # output, syncable
```

Observations are a JSON array of discrete strings per conversation (not a markdown blob). Each
observation gets its own classification and embedding. Classifications and embeddings are stored
one file per conversation containing all per-observation entries:

```json
// distill/observations/{source}/{session_id}.json
{"fingerprint": "abc123", "items": ["obs1", "obs2", "obs3"]}

// distill/classifications/{source}/{session_id}.json
{"fingerprint": "def456", "items": [
  {"observation": "obs1", "classification": "..."},
  {"observation": "obs2", "classification": "..."}
]}

// distill/embeddings/{source}/{session_id}.json
{"fingerprint": "ghi789", "items": [
  {"classification": "...", "vector": [0.1, 0.2, ...]},
  {"classification": "...", "vector": [0.3, 0.4, ...]}
]}
```

## Decisions

### Why cluster instead of map-reduce?

Map-reduce treats observations as an undifferentiated bag — it compresses but doesn't organize.
Clustering groups by theme first, so synthesis operates on coherent slices rather than random
partitions. This also normalizes for frequency: a pattern that dominates by volume gets grouped into
one cluster with the same token budget as a smaller cluster, preventing it from drowning out rarer
themes.

### Why classify before embedding?

We could embed raw observations and let clustering discover structure unsupervised. Instead,
classification situates each observation — describing _what pattern of thinking or working it's an
instance of_ — so similar observations land near each other in embedding space even when they use
different language. This is distinct from OBSERVE, which asks "what's here." CLASSIFY asks "what is
this an instance of."

Classification also serves a dual purpose: it improves embedding quality for clustering, and
provides a reusable classification that future knowledge-level features will consume independently.
The two uses need to be distinct — classification is not just a clustering preprocessing step.

Classification should not project onto predefined axes (e.g. "wisdom vs knowledge"). That constrains
what clusters can emerge. Let the clusters discover the natural axes.

### Why HDBSCAN over k-means?

HDBSCAN discovers cluster count automatically and explicitly labels noise. k-means forces every
observation into a cluster and requires choosing k upfront. The noise-handling property is
load-bearing — outliers that don't cluster yet may emerge as themes with more data.

### Why preserve noise?

HDBSCAN noise means "doesn't fit a group," not "worthless." Observations that don't cluster may be
the most distinctive — patterns expressed once or twice that make the muse sound like you rather
than like generic advice. Filtering noise early discards it based on no contextual information.

Instead, noise flows through clustering and is passed as raw observations to MERGE alongside the
cluster syntheses. MERGE is already the judgment step — it decides what to organize, preserve, or
let go. Framing tells MERGE to preserve what's distinctive and ignore what's redundant with the
clusters. Don't make a mechanical decision where the right answer requires contextual judgment.

### Why sample rather than summarize per-cluster?

We could summarize each cluster's full content before synthesis. Instead, we select representative
examples and pass raw observations. This preserves voice and specificity that summaries flatten.

### Why two-pass output (synthesize then merge)?

SYNTHESIZE compresses each cluster independently (parallel), then MERGE organizes across cluster
summaries. Single-pass would be simpler but forces one LLM call to both synthesize and organize. Two
passes keep each call focused and produce debuggable intermediate artifacts.

## Deferred

Intentional simplifications for the first implementation. Each names what's deferred, why it's
acceptable now, and what would trigger revisiting.

### Why random sampling over centroid-nearest + edges?

Centroid-nearest + edge sampling is cheap to compute once you have embeddings and cluster
assignments, and it's meaningfully better than random for thematic representation — centroid-nearest
captures the cluster's core, edges capture its boundaries with neighboring clusters. But it's a
sampling refinement layered on top of clustering. The goal of the first implementation is to validate
whether clustering itself improves muse quality over map-reduce. If clustering doesn't help, better
sampling wouldn't have saved it — the problem would be upstream. If clustering does help, sampling
sophistication is the obvious next lever. **Revisit when:** clustering is validated and output
quality plateaus.

### Why token budgets over concept weighting?

Fixed token budgets per cluster are predictable and debuggable. Concept weighting (having the model
assess which observations carry more weight and sampling proportionally) adds an LLM call per
observation and introduces a subjective scoring dimension that's hard to evaluate. Building two
novel things at once with no way to attribute quality differences to either one is a bad experiment.
**Revisit when:** clustering is validated and sampling is the bottleneck for output quality.

### Why not stabilize clusters across runs?

Adding one conversation can reorganize clusters entirely. Whether that's acceptable depends on how
the muse is consumed. Stable cluster identity would add complexity (tracking cluster lineage,
merging incrementally) for a problem that isn't yet real. **Revisit when:** cluster instability
causes user-visible problems.
