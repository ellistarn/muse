# Clustered Compose

## Problem

Composing a large corpus of observations into a muse document. Single-pass composition breaks down
on three fronts: the observation set outgrows context window limits, model attention dilutes
distinctive signal as input volume grows, and redundant observations bias output toward
frequently-observed patterns at the expense of rare but defining ones.

## Solution

### Pipeline

Conversations are mechanically compressed (code blocks stripped, tool output collapsed to markers,
long messages truncated) and sent to an observe LLM that identifies what the owner's messages
reveal about how they think. Human-to-human conversations (Slack, GitHub) use a separate observe
prompt tuned for peer discussion — positions defended, mentorship, organizational reasoning — with
`[owner]`/`[peer]` role labels instead of `[owner]`/`[assistant]`. The observe prompt requires a
structured `Observation:` prefix on each output line — lines without the prefix are discarded at
parse time. A refine step filters candidates to only those that would change how the muse behaves.

The surviving observations are labeled with short thematic labels, themed into canonical patterns,
and grouped into clusters by exact label match. Labels with 3+ observations form clusters; the rest
flow through as noise. Each cluster is summarized independently, then composed with noise
observations into the final muse.md.

```
conversations ─► OBSERVE ─► observations ─► CLUSTER ─► samples ─► COMPOSE ─► muse.md

OBSERVE    compress → observe (Observation: prefix) → refine → parse
CLUSTER    label (parallel) → theme (consolidate vocabulary) → group (exact match)
COMPOSE    per-cluster summarize → compose with noise
```

### Source-aware observation

The observe step routes to different prompts based on source type:

- **AI conversations** (claude-code, opencode, kiro, codex): Uses the standard observe prompt.
  Extracts reasoning and awareness only — corrections, course changes, and preferences expressed
  while directing an AI. Voice is not extracted — AI interactions select for a corrective register.
  `[owner]`/`[assistant]` role labels. Assistant messages truncated to 500 chars.

- **Human conversations** (slack, github): Uses the human observe prompt. Extracts reasoning,
  awareness, and voice — the exclusive source for voice observations. Signal comes from positions
  defended against peers, architectural reasoning explained to colleagues, mentorship,
  organizational judgment. `[owner]`/`[peer]` role labels. Both sides preserved in full because
  peer messages carry context the owner is responding to.

`isHumanSource()` determines routing. Adding a new human source requires one line in that function.

### Theming

Parallel labeling produces fragmented vocabulary — the same concept gets different names across
conversations because each labeling call runs independently. When the unique label count exceeds 50,
a theme step consolidates them into 15-25 canonical themes. Each theme names a distinct thinking
pattern at the right altitude — specific enough to be meaningful, general enough to absorb variants.

The theme step is deliberately naive about what happens downstream. It doesn't know its output
becomes cluster keys, which become summaries, which become muse sections. It just consolidates
vocabulary. Structural decisions about the muse belong in the compose step.

The compose step treats each cluster as one distinct idea regardless of observation count. A cluster
with 40 observations and one with 3 each contribute one idea. This corrects for volume asymmetry —
the muse represents the *breadth* of the person's thinking, not the frequency distribution of their
conversations.

### Strategies

Two composition methods are available permanently. Clustering produces thematically coherent output
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
labels → (sorted unique labels, theme prompt) → theme mapping
```

Change a conversation and its observations invalidate, which invalidates labels. Change the label
prompt and all labels invalidate. Change the label vocabulary or theme prompt and the theme mapping
invalidates. The observe prompt fingerprint includes both AI and human prompts, so changing the
human observe prompt invalidates all observations for re-observation.

Fingerprints per layer:

- **Observation**: `hash(conversation.LastModified, observePromptHash, observeHumanPromptHash, refinePromptHash)`
- **Label**: `hash(observationContent, labelPromptHash)`
- **Theme**: `hash(sorted unique labels, themePromptHash)`

Grouping, sampling, summarization, and composition are recomputed each run — they're cheap relative
to the cached stages.

`--reobserve` and `--relabel` force recomputation unconditionally, skipping fingerprint comparison.
These are debugging tools for prompt iteration — correctness never depends on them.

### Storage

Conversations are input. The muse is output. Everything in between is pipeline internals owned by
the compose system, nested under `compose/`.

```
~/.muse/
├── conversations/{source}/{conversation_id}.json              # input, syncable
├── observations/{source}/{conversation_id}.json               # syncable
├── compose/
│   ├── labels/{source}/{conversation_id}.json                 # syncable
│   └── themes.json                                            # label mapping, ephemeral
├── sessions/                                                  # ask sessions, not compose input
├── versions/{timestamp}/muse.md                               # output, syncable
├── versions/{timestamp}/diff.md                               # output, syncable
```

Observations are a JSON array of discrete items per conversation — each observation gets its own
label. Labels are stored one file per conversation containing all per-observation entries:

```json
// observations/{source}/{conversation_id}.json
{"fingerprint": "abc123", "items": [
  {"observation": "obs1"},
  {"observation": "obs2", "quote": "exact words"}
]}

// compose/labels/{source}/{conversation_id}.json
{"fingerprint": "def456", "items": [
  {"observation": "obs1", "label": "root cause over symptom fixing"},
  {"observation": "obs2", "label": "abstraction must earn its cost"}
]}

// compose/themes.json
{"fingerprint": "789abc", "mapping": {
  "abstraction must earn its keep": "Complexity Cost and Justification",
  "root cause over symptom fixing": "Structural Diagnosis over Symptom Fixing"
}}
```

### Eval

`muse eval` runs each case twice — once with the muse, once without — then an LLM judge
characterizes the difference. Cases are single-question markdown files in `cmd/evals/`. The judge
receives the question, base response, and muse response, and describes how the muse changed the
response — specificity, reasoning patterns, blind spots — then makes a net-positive/net-negative
call. Within each case, the baseline and muse calls run in parallel.

```bash
muse eval                   # built-in cases
muse eval --dir ./my-cases  # custom cases
```

## Decisions

### Why cluster instead of map-reduce?

Map-reduce treats observations as an undifferentiated bag — it compresses but doesn't organize.
Clustering groups by theme first, so synthesis operates on coherent slices rather than random
partitions. This also normalizes for frequency: a pattern that dominates by volume gets grouped into
one cluster with the same token budget as a smaller cluster, preventing it from drowning out rarer
themes.

### Why mechanical compression over raw or LLM-summarized input?

The observe model needs enough context to understand what the owner was reacting to, but assistant
messages are mostly code blocks, tool output, and verbose explanations — none of which carry signal
about how the owner thinks. Mechanical compression (strip code fences, collapse tool calls to
`[tool: name]`, truncate long assistant messages to 500 chars) removes the bloat while preserving
owner messages in full. This is cheaper and faster than LLM summarization and doesn't risk losing
the detail that provoked a correction.

### Why a structured prefix over empty-output instructions?

LLMs can't reliably produce empty output. Instructing the model to "return nothing" when a
conversation has no signal is adversarial to how token prediction works — the model wants to emit
tokens. Instead of hoping for absence, we require structure: each observation must start with
`Observation:`. Lines without the prefix are discarded at parse time. This converts a semantic
judgment ("is this nothing?") into a structural parse rule ("does this line start with the prefix?").

The `Observation:` prefix also anchors the model's generation — it's harder to drift into
conversational meta-commentary when the required output format is explicit. A secondary relevance
filter catches any well-formed-but-vacuous observations that slip through.

### Why separate observe prompts for human conversations?

The original observe prompt was designed for human-AI interaction — it looks for corrections,
course changes, and preferences expressed while directing a model. For Slack and GitHub, this
produced 85% empty conversations even when the owner wrote thousands of characters. The signal in
peer conversations is structurally different: positions defended against pushback, architectural
reasoning explained to colleagues, mentorship, strategic reasoning. The `[assistant]` role label
caused the LLM to treat peer messages as AI output and ignore them.

The human observe prompt uses `[owner]`/`[peer]` labels and reframes what signal looks like.
This increased Slack observation yield roughly 3x. Voice extraction is exclusive to the human
observe prompt.

### Why theme instead of normalize?

The original pipeline had two steps: "distill" (coarse, 356 labels → 25 themes) then "normalize"
(fine-grained synonym merge). The normalize step was redundant — distill already consolidated the
vocabulary. The two steps were collapsed into one called "theme" because it names what the step
actually does: decide what themes the observations should be organized around.

The step is deliberately naive about its downstream effect. It produces a clean vocabulary; the
compose step decides structure.

### Why label-match only?

Grouping is exact label match — observations with the same (themed) label form a cluster. This
works because labeling with shared vocabulary plus theming produces consistent terminology.

We initially designed a two-phase approach (label-match followed by HDBSCAN over embeddings for the
ungrouped residual) but found that consistent labeling eliminates the sub-cluster variation HDBSCAN
was meant to capture. Fixing labeling upstream made the downstream algorithm irrelevant.

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

### Why treat each cluster as one idea regardless of size?

Without this correction, compose over-represents high-frequency patterns. A cluster with 42
observations about abstraction boundaries would dominate the muse while a cluster with 4
observations about organizational judgment gets squeezed out. Both represent distinct thinking
patterns the person has. Volume means they revisit a topic often — it doesn't mean the topic
deserves more space. The muse represents breadth, not frequency.

## Deferred

### Why not stabilize clusters across runs?

Adding one conversation can reorganize clusters entirely. Whether that's acceptable depends on how
the muse is consumed. Stable cluster identity would add complexity (tracking cluster lineage,
merging incrementally) for a problem that isn't yet real. **Revisit when:** cluster instability
causes user-visible problems.
