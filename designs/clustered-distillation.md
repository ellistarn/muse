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

The surviving observations are classified into short thematic labels and grouped into clusters.
Grouping is by exact label match — the classifier is given existing labels and converges on a shared
vocabulary, so observations on the same theme get the same label. Labels with 3+ observations form
clusters; the rest flow through as noise. Each cluster is synthesized independently, then merged with
noise observations into the final muse.md.

```
conversations ─► OBSERVE ─► observations ─► CLUSTER ─► samples ─► COMPOSE ─► muse.md

OBSERVE    compress → extract (Observation: prefix) → refine → parse
CLUSTER    classify (label convergence) → group (label match) → sample
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
```

Change a conversation and its observations invalidate, which invalidates classifications. Change the
classify prompt and all classifications invalidate. Correctness is structural, not procedural.

Fingerprints per layer:

- **Observation**: `hash(conversation.UpdatedAt, observePromptHash)`
- **Classification**: `hash(observationContent, classifyPromptHash)`

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
│   └── clusters/{id}.json                                # ephemeral, not synced, overwritten each run
├── muse/versions/{timestamp}/muse.md                     # output, syncable
├── muse/versions/{timestamp}/diff.md                     # output, syncable
```

Observations are a JSON array of discrete strings per conversation — each observation gets its own
classification. Classifications are stored one file per conversation containing all per-observation
entries:

```json
// distill/observations/{source}/{session_id}.json
{"fingerprint": "abc123", "items": ["obs1", "obs2", "obs3"]}

// distill/classifications/{source}/{session_id}.json
{"fingerprint": "def456", "items": [
  {"observation": "obs1", "classification": "root cause over symptom fixing"},
  {"observation": "obs2", "classification": "abstraction must earn its cost"}
]}
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

### Why label convergence in classification?

The classifier receives observations in batches (up to 10 per call) and assigns each a short
thematic label (3-8 words) naming the thinking pattern it's an instance of. Critically, the
classifier also receives the list of labels already assigned to other observations and is instructed
to reuse an existing label when one fits. Sessions are processed sequentially so each batch sees the
full label vocabulary from prior sessions, preserving convergence.

Without label convergence, each observation gets a unique paraphrase ("abstraction must earn its
cost" vs. "abstraction must earn its keep") and grouping becomes impossible — everything is
equidistant. With convergence, the classifier maps both to the same label, and grouping becomes
trivial: exact string match.

### Why label-match only?

Grouping is exact label match — observations with the same classification label form a cluster. This
works because label convergence produces a shared vocabulary with ~60% reuse.

We initially designed a two-phase approach (label-match followed by HDBSCAN over embeddings for the
ungrouped residual) but found that classification convergence eliminates the sub-cluster variation
HDBSCAN was meant to capture. With 168 observations, median cosine distance was 0.92 — the embedding
space was flat because labels were paraphrasing, not categorizing. Fixing classification upstream
made the downstream algorithm irrelevant.

Observations whose labels appear fewer than 3 times flow through as noise rather than forming
micro-clusters. This threshold prevents synthesis from operating on groups too small to have a
meaningful pattern.

### Why preserve noise?

Noise means "doesn't fit a group," not "worthless." Observations that don't cluster may be the most
distinctive — patterns expressed once or twice that make the muse sound like you rather than like
generic advice. Noise flows through to COMPOSE alongside cluster syntheses. COMPOSE is already the
judgment step — it decides what to organize, preserve, or let go.

### Why two-pass compose (synthesize then merge)?

Synthesis compresses each cluster independently (parallel), then merge organizes across cluster
summaries. Single-pass would be simpler but forces one LLM call to both synthesize and organize. Two
passes keep each call focused and produce debuggable intermediate artifacts.

## Deferred

### Why not stabilize clusters across runs?

Adding one conversation can reorganize clusters entirely. Whether that's acceptable depends on how
the muse is consumed. Stable cluster identity would add complexity (tracking cluster lineage,
merging incrementally) for a problem that isn't yet real. **Revisit when:** cluster instability
causes user-visible problems.
