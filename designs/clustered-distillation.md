# Clustered Distillation

## Problem

Distilling a large corpus of observations into a muse document. Single-pass distillation breaks down
on three fronts: the observation set outgrows context window limits, model attention dilutes
distinctive signal as input volume grows, and redundant observations bias output toward
frequently-observed patterns at the expense of rare but defining ones.

## Solution

### Pipeline

Conversations are mechanically compressed (code blocks stripped, tool output collapsed to markers,
long assistant messages truncated) and sent to an extraction LLM that identifies what the human's
messages reveal about how they think. The extract prompt requires a structured `Observation:` prefix
on each output line — lines without the prefix are discarded at parse time. A refine step filters
candidates to only those that would change how the muse behaves.

The surviving observations are labeled with short thematic labels, normalized to merge synonyms, and
grouped into clusters by exact label match. Labels with 3+ observations form clusters; the rest flow
through as noise. Each cluster is summarized independently, then composed with noise observations into
the final muse.md.

```
conversations ─► OBSERVE ─► observations ─► CLUSTER ─► samples ─► COMPOSE ─► muse.md

OBSERVE    compress → extract (Observation: prefix) → refine → parse
CLUSTER    label (parallel) → normalize (merge synonyms) → group (exact match)
COMPOSE    per-cluster summarize → compose with noise
```

### Strategies

Two distillation methods are available permanently. Clustering produces thematically coherent output
at higher complexity. Map-reduce is simpler and sufficient for smaller observation sets.

```bash
muse compose                      # default: clustering
muse compose --method=clustering
muse compose --method=map-reduce
```

### Caching

Each cached artifact stores a fingerprint — a hash of its inputs. On read, if the fingerprint
doesn't match current inputs, the cache misses and the artifact is recomputed. No flags needed for
correctness; the dependency chain self-invalidates:

```
conversation → (observe prompt) → observations
observation → (label prompt) → labels
labels → (sorted unique labels) → normalization mapping
```

Change a conversation and its observations invalidate, which invalidates labels. Change the label
prompt and all labels invalidate. Change the label vocabulary and the normalization mapping
invalidates.

Fingerprints per layer:

- **Observation**: `hash(conversation.LastModified, observePromptHash)`
- **Label**: `hash(observationContent, labelPromptHash)`
- **Normalization**: `hash(sorted unique labels, normalizePromptHash)`

Grouping, sampling, summarization, and composition are recomputed each run — they're cheap relative
to the cached stages.

`--reobserve` and `--relabel` force recomputation unconditionally, skipping fingerprint comparison.
These are debugging tools for prompt iteration — correctness never depends on them.

### Storage

Conversations are input. The muse is output. Everything in between is pipeline internals owned by the
distillation system, nested under `compose/`.

```
~/.muse/
├── conversations/{source}/{conversation_id}.json              # input, syncable
├── compose/
│   ├── observations/{source}/{conversation_id}.json           # syncable
│   ├── labels/{source}/{conversation_id}.json                 # syncable
│   ├── normalization.json                                # label mapping, ephemeral
│   └── clusters/{id}.json                                # ephemeral, not synced, overwritten each run
├── muse/versions/{timestamp}/muse.md                     # output, syncable
├── muse/versions/{timestamp}/diff.md                     # output, syncable
```

Observations are a JSON array of discrete strings per conversation — each observation gets its own
label. Labels are stored one file per conversation containing all per-observation entries:

```json
// compose/observations/{source}/{conversation_id}.json
{"fingerprint": "abc123", "items": ["obs1", "obs2", "obs3"]}

// compose/labels/{source}/{conversation_id}.json
{"fingerprint": "def456", "items": [
  {"observation": "obs1", "label": "root cause over symptom fixing"},
  {"observation": "obs2", "label": "abstraction must earn its cost"}
]}

// compose/normalization.json
{"fingerprint": "789abc", "mapping": {
  "abstraction must earn its keep": "abstraction must earn its cost",
  "infra-tasks": "infrastructure work"
}}
```

## Decisions

### Why cluster instead of map-reduce?

Map-reduce treats observations as an undifferentiated bag — it compresses but doesn't organize.
Clustering groups by theme first, so synthesis operates on coherent slices rather than random
partitions. This also normalizes for frequency: a pattern that dominates by volume gets grouped into
one cluster with the same token budget as a smaller cluster, preventing it from drowning out rarer
themes.

### Why mechanical compression over raw or LLM-summarized input?

The extract model needs enough context to understand what the human was reacting to, but assistant
messages are mostly code blocks, tool output, and verbose explanations — none of which carry signal
about how the owner thinks. Mechanical compression (strip code fences, collapse tool calls to
`[tool: name]`, truncate long assistant messages to 500 chars) removes the bloat while preserving
human messages in full. This is cheaper and faster than LLM summarization and doesn't risk losing
the detail that provoked a correction.

### Why a structured prefix over empty-output instructions?

LLMs can't reliably produce empty output. Instructing the model to "return nothing" when a
conversation has no signal is adversarial to how token prediction works — the model wants to emit
tokens. Instead of hoping for absence, we require structure: each observation must start with
`Observation:`. Lines without the prefix are discarded at parse time. This converts a semantic
judgment ("is this nothing?") into a structural parse rule ("does this line start with the prefix?").

The `Observation:` prefix also anchors the model's generation — it's harder to drift into
conversational meta-commentary when the required output format is explicit. A secondary relevance
filter catches any well-formed-but-vacuous observations that slip through (e.g. parenthesized
meta-commentary).

### Why normalize instead of sequential convergence?

The previous design processed label assignments sequentially so each batch saw the full label
vocabulary from prior batches. This used ordering as an implicit normalization mechanism — label
convergence was an emergent property of execution order. The consequence was that labeling could not
be parallelized, making it the pipeline bottleneck.

Normalization separates two concerns that are genuinely separate: *labeling* (what thematic pattern
does this observation reflect?) and *normalization* (are these two labels the same concept?).
Labeling runs in parallel with a shared but unordered label set — each call sees the current
vocabulary and naturally reuses labels where they fit, providing convergence pressure without
requiring strict ordering. A single normalization call then takes the full label set and produces a
canonical mapping that merges synonyms, applying it to all stored labels before grouping.

This is both faster (parallel labeling) and architecturally cleaner (normalization is a named step,
not an emergent property of execution order). The normalization mapping is cached by label set hash
and only recomputed when the vocabulary actually changes.

### Why label-match only?

Grouping is exact label match — observations with the same (normalized) label form a cluster. This
works because labeling with shared vocabulary plus normalization produces consistent terminology.

We initially designed a two-phase approach (label-match followed by HDBSCAN over embeddings for the
ungrouped residual) but found that consistent labeling eliminates the sub-cluster variation HDBSCAN
was meant to capture. With 168 observations, median cosine distance was 0.92 — the embedding space
was flat because labels were paraphrasing, not categorizing. Fixing labeling upstream made the
downstream algorithm irrelevant.

Observations whose labels appear fewer than 3 times flow through as noise rather than forming
micro-clusters. This threshold prevents summarization from operating on groups too small to have a
meaningful pattern.

### Why preserve noise?

Noise means "doesn't fit a group," not "worthless." Observations that don't cluster may be the most
distinctive — patterns expressed once or twice that make the muse sound like you rather than like
generic advice. Noise flows through to compose alongside cluster summaries. Compose is already the
judgment step — it decides what to organize, preserve, or let go.

### Why two-pass compose (summarize then compose)?

Summarization compresses each cluster independently (parallel), then composition organizes across
cluster summaries. Single-pass would be simpler but forces one LLM call to both summarize and
organize. Two passes keep each call focused and produce debuggable intermediate artifacts.

## Deferred

### Why not stabilize clusters across runs?

Adding one conversation can reorganize clusters entirely. Whether that's acceptable depends on how
the muse is consumed. Stable cluster identity would add complexity (tracking cluster lineage,
merging incrementally) for a problem that isn't yet real. **Revisit when:** cluster instability
causes user-visible problems.
