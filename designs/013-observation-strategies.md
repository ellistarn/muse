# Observation Strategies

## Problem

The default observation pipeline feeds the full compressed conversation to the observe prompt
in one pass. On long conversations (27+ turns), it returns zero observations. The owner's
reasoning from early turns gets washed out by later mechanical turns (git operations, CI,
formatting). Research on this problem [1] found that the observe prompt loses early signal
under later noise, and that the failure is catastrophic: conversations that should produce
7-9 observations produce 0.

## Windowed owner-only observation (woo)

Woo slides an 8-turn window across the conversation with a stride of 4 turns, strips
assistant text from each window, and observes each window independently. Empty windows
(mechanical content) return NONE at negligible cost. Reasoning windows produce observations.

Stripping assistant text helps because the assistant's output competes for the observe
prompt's attention without contributing to the owner's reasoning signal. At matched context
sizes, assistant text increases the misleading observation rate from 7% to 16% [1].

Observations from overlapping windows are deduplicated by text containment, then refined.

### Evidence

On four conversations of 115-183 turns, the default pipeline produced 0, 1, 4, and 11
observations. Woo produced 76, 62, 44, and 26. An LLM-as-judge found woo captured 64 of 75
distinct insights on one conversation; the default captured 15. At corpus scale (453
conversations), woo produces observations at 91% grounding rate, where grounded means
well-supported by the source conversation as rated by an Opus quality judge [1].

## Adaptive observation

Adaptive tries woo first on each window. If woo returns NONE, it tries the default method
(with assistant text) on the same window. At most two calls per window.

Adaptive finds more grounded observations than woo alone because some windows contain terse
owner messages that only make sense with assistant context. The fallback catches those. Exact
counts vary across runs as the conversation corpus grows; representative numbers are in [1].

## Per-window failures

Two failure modes are handled per-window rather than aborting the whole
conversation. Adjacent overlapping windows usually cover the same material, so
skipping one rarely costs information.

**Output truncation.** Per-window observe calls use a token budget of 4096.
Thinking models can spend the whole budget reasoning before emitting content;
the call returns truncated. observeWindow retries once at 16384 tokens.
Windows that truncate again are skipped.

**Input exceeds context size.** Owner messages occasionally contain large
pastes (logs, diffs, code dumps) that push a single window past the model's
context window. observeWindow does not retry (a higher output budget doesn't
help when the input itself doesn't fit). The window is skipped on the first
attempt. The openai client classifies these errors via a string-match against
known patterns (llama.cpp's `exceed_context_size_error`, OpenAI's
`context_length_exceeded`, "maximum context length") and emits a typed
`inference.ContextSizeError` so callers can distinguish from other failures.

## Per-mode observation storage

Each observation strategy stores results in a separate directory so switching strategies does
not invalidate cached observations from other strategies.

```
observations/{source}/{id}.json              (default mode)
observations/{mode}/{source}/{id}.json       (named modes: woo, adaptive, etc.)
```

Source is the conversation provider (e.g., `claude-code`, `github`, `kiro-cli`). ID is the
conversation identifier.

The observation fingerprint includes the mode, so changing strategies forces re-observation
for that mode without affecting others.

## Interface

`--observe-mode` on `muse compose` selects the strategy:

- `""` (default): full compressed conversation
- `"woo"`: windowed owner-only
- `"adaptive"`: woo-first with default fallback

## Concurrency

Observation has two phases with different parallelism characteristics:

1. **Window observation** — independent API calls. Each window produces raw text; no
   dependencies between windows, even within the same conversation.
2. **Dedup + refine** — per-conversation. Needs all window results for that conversation
   before it can run. One API call per conversation.

The implementation flattens phase 1 into a shared work queue. On entry to `runObserve`:

1. Load all pending conversations and build their window lists (cheap, no API calls).
2. Emit one work unit per window: `(conversationID, windowIndex, input, method)`.
   For adaptive mode, each window emits up to two units (woo attempt, then default
   fallback if woo returns NONE). The fallback is conditional — it becomes a work unit
   only after the woo attempt completes empty.
3. Process the queue through an errgroup capped at the rate limiter's max concurrency.
   The AIMD token bucket already gates actual Bedrock calls; the errgroup cap prevents
   unbounded goroutine accumulation.
4. When all windows for a conversation complete, fire the dedup + refine step for that
   conversation. These are also independent across conversations and enter the same queue.

This eliminates head-of-line blocking: a 30-window conversation no longer serializes 30
API calls behind one goroutine slot while short conversations finish and free theirs.

Sort order for the queue: largest conversations first (most windows), so their windows
begin processing immediately rather than arriving as a long tail.

## Edge cases

**Zero clusters.** When all observations land as outliers (0 clusters, 0 summaries), the
thesis step has no input. The pipeline returns early with an error: "no clusters formed —
need more observations to compose a muse." This happens with very small `--limit` values
where the observation count is below the clustering threshold.

## Deferred

**Window size.** The 8-turn window with stride 4 is untested against alternatives. Larger
windows risk reintroducing the same problem. Smaller windows may fragment multi-turn
reasoning arcs.

**Refine step evaluation.** The refine step deduplicates across overlapping windows but may
collapse useful distinctions.

## References

[1] Orwellian Observation: orwellian-observation.md
