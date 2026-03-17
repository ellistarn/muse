# Clustered Distillation

## Problem

Distilling a large corpus of observations into a muse document. Single-pass distillation breaks down
on three fronts: the observation set outgrows context window limits, model attention dilutes
distinctive signal as input volume grows, and redundant observations bias output toward
frequently-observed patterns at the expense of rare but defining ones.

## Solution

### Pipeline

Each conversation is sent raw to an extraction LLM that identifies what the human's messages reveal
about how they think — corrections, course changes, reasoning, deliberate choices. When a
conversation exceeds the context window, assistant messages are mechanically compressed (strip code
blocks, collapse tool output to markers, truncate long messages). A refine step filters candidates
to only those that would change how the muse behaves, and a deterministic relevance filter drops
non-observations (empty output, placeholder tokens, LLM meta-commentary).

The surviving observations are classified, embedded, and grouped into thematic clusters. Each cluster
is synthesized independently, then merged with unclustered noise observations into the final muse.md.
When the observation count is too small for clustering to add value, the pipeline falls back to
single-pass compose.

```
conversations ─► OBSERVE ─► observations ─► CLUSTER ─► samples ─► COMPOSE ─► muse.md

OBSERVE    raw conversation → [compress if needed] → extract → refine → relevance filter
CLUSTER    classify → embed → group (HDBSCAN) → sample
COMPOSE    per-cluster synthesis → merge with noise
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

`--reobserve` and `--reclassify` force recomputation unconditionally, skipping fingerprint comparison.
These are debugging tools for prompt iteration — correctness never depends on them.

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

Observations are a JSON array of discrete strings per conversation — each observation gets its own
classification and embedding. Classifications and embeddings are stored one file per conversation
containing all per-observation entries:

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

### Why send raw conversation to extract?

Summarizing assistant messages before extraction would reduce token count but costs an LLM call per
turn and is lossy — the extract model needs enough context to understand what the human was reacting
to, and a compressed summary may omit the detail that provoked a correction. The extraction prompt
focuses on the human's voice regardless of how much assistant content surrounds it.

Most conversations fit within the context window without any processing. For the ones that don't,
mechanical compression strips code blocks, collapses tool output to markers like `[tool: file_edit]`,
and truncates long assistant messages. This targets the main token bloat — assistant code and tool
output carry zero signal about how the owner thinks. If compression alone isn't sufficient, chunking
at turn boundaries is the natural next step, but the expectation is that compression handles
effectively all oversize conversations.

### Why a deterministic relevance filter?

LLMs can't reliably produce empty output. The extract and refine prompts instruct the model to
return nothing when a conversation has no signal, but the model sometimes produces meta-commentary
instead ("I don't see any candidate observations"). This is a property of the technology, not a
prompt problem — generating tokens that mean "I have no tokens to generate" is adversarial to how
token prediction works.

The relevance filter is a mechanical backstop: pattern matching on known non-observation output
(empty strings, placeholder tokens, meta-commentary prefixes). It catches pipeline defects, not
borderline observations. Any string that passes pattern matching is a genuine attempt at an
observation.

### Why classify before embedding?

We could embed raw observations and let clustering discover structure unsupervised. Instead,
classification situates each observation — describing _what pattern of thinking or working it's an
instance of_ — so similar observations land near each other in embedding space even when they use
different language. This is distinct from OBSERVE, which asks "what's here." CLASSIFY asks "what is
this an instance of."

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

Instead, noise flows through clustering and is passed as raw observations to COMPOSE alongside the
cluster syntheses. COMPOSE is already the judgment step — it decides what to organize, preserve, or
let go. Framing tells COMPOSE to preserve what's distinctive and ignore what's redundant with the
clusters. Don't make a mechanical decision where the right answer requires contextual judgment.

### Why sample rather than summarize per-cluster?

We could summarize each cluster's full content before synthesis. Instead, we select representative
examples and pass raw observations. This preserves voice and specificity that summaries flatten.

### Why two-pass compose (synthesize then merge)?

Synthesis compresses each cluster independently (parallel), then merge organizes across cluster
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

## Evaluation

Clustering is validated when the muse output captures themes that map-reduce flattens and preserves
distinctive observations that map-reduce averages out. The test is qualitative: read both outputs
side by side. The clustering version should surface rare-but-defining patterns (security instincts,
naming precision, scope reduction) that get drowned out by high-frequency patterns (general
architecture, code style) in map-reduce.
