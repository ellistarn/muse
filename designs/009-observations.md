# Observations

An observation is a set of discrete items extracted from a single conversation, each capturing
something the owner said that reveals how they think. Observations are stored as JSON per
conversation, keyed by source and conversation ID, and fingerprinted so unchanged conversations
are not re-observed.

## Pipeline

```
sources --> upload channel --> upload --> observe channel --> observe --> label
```

Three stages run concurrently, connected by two channels:

- **Sources** discover conversations and push them to the upload channel.
- **Upload** determines which conversations are new or changed and which need observation.
- **Observe** runs the LLM call and stores the resulting observations.

A conversation can be observed as soon as it is uploaded, without waiting for all sources to
finish discovering. Label waits for all observations to complete.

## Fingerprinting

Each observation is fingerprinted by the conversation's last-modified timestamp and the current
prompt chain. A conversation whose fingerprint matches its stored observation is skipped. Changed
conversations and new conversations flow through to observe.

`--reobserve` clears all stored observations, forcing every conversation through observe.

## Limit

`--limit N` selects the N most recent conversations for observation. Conversations are ranked
by last-modified timestamp across all sources. When a source is slow, the pipeline can finalize
the top N and cancel remaining sources without waiting for them to finish.

Without `--limit`, all conversations that need observation are processed.

## Timestamps

The priority queue orders by last-modified timestamp: file modification time for local sources,
`updated_at` for GitHub and Slack. Wrong timestamps cause the wrong N to be selected. The
failure is soft: the next run picks up what was missed.

Sources that produce conversations newest-first enable early termination under `--limit`.
Local sources sort by modification time. API sources with incremental sync fetch newest-first.

## Errors

A conversation-level error (bad JSON, missing field) is logged and skipped. A source-level
error (missing directory, auth failure) closes that source. Other sources continue. An observe
error stops the pipeline.

## Decisions

### Why stream across sources but not within a source?

`Conversations()` returns a batch. Streaming within a source (observe page 1 while page 2
fetches) would require changing every provider. Cross-source streaming captures most of the
benefit because the bottleneck is the slowest source.

### Why a priority queue for limit?

First-come-first-served is unfair to slow API sources (local sources consume the budget).
Even distribution introduces recency bias. The priority queue guarantees the N newest regardless
of discovery order.

### Why track completion at the upload level?

A source pushes conversations to the upload channel and returns, but those conversations sit in
the buffer until processed. Marking the source done at push time can finalize the priority queue
before its conversations are inserted. Completion is tracked per conversation processed.

## Deferred

### Intra-source streaming

Stream within a single source (observe while still fetching). Requires a channel-based provider
interface. **Revisit when:** a single source's fetch time dominates the pipeline.

### Dispatch order

The pipeline processes conversations in discovery order. The previous sequential path sorted
largest-first. **Revisit when:** observe wall-clock time increases measurably.
