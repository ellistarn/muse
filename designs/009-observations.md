# Observations

An observation is a set of discrete items extracted from a single conversation, each capturing
something the owner said that reveals how they think. Observations are stored as JSON per
conversation, keyed by source and conversation ID, and fingerprinted so unchanged conversations
are not re-observed.

## Pipeline

```
sources --> upload channel --> upload --> observe channel --> compress --> observe --> label
```

Three stages run concurrently, connected by two channels:

- **Sources** discover conversations and push them to the upload channel.
- **Upload** determines which conversations are new or changed and which need observation.
- **Compress** reduces the conversation to fit the observe context window.
- **Observe** runs the LLM call and stores the resulting observations.

A conversation can be observed as soon as it is uploaded, without waiting for all sources to
finish discovering. Label waits for all observations to complete.

## Compression

The observe prompt asks for strong, specific observations anchored in concrete examples.
"Weak: 'Prefers composition over inheritance.' Strong: 'Avoids struct embedding in Go
because it hides the dependency graph and makes refactoring brittle.'" Specificity requires
context: what the owner was reacting to, not just what they said.

Compression currently strips that context. Owner messages are preserved in full. Assistant
messages have code blocks replaced with `[code block]`, tool calls collapsed to
`[tool: name]`, and the remainder truncated to 500 characters. Tool inputs and outputs are
discarded entirely.

This serves the context window constraint but works against the observe prompt's intent.
When the owner says "on line 12, be more specific," compression preserves the directive but
strips line 12. The observation captures a general preference instead of a specific
transformation.

### Approaches attempted and abandoned after experiments

**A: Adaptive compression.** Scale the assistant text limit by owner message length
(500-2000 chars). More context diluted signal in local experiments: 17k chars
produced 0 observations while 13k produced 5. The extra context made the
conversation look less interesting to muse.

**B: Prompt-driven extraction.** Ask the observe prompt for transformations, not just
principles. Made the LLM more cautious, producing fewer observations.

**C: Context-aware prompting.** Ask the LLM to reason about what context each response
requires. Same performance regression relative to mainline as B.

**D: Word-count and pattern filters.** Remove confirmations and short directives before
observe by matching known low-signal phrases and applying a minimum word count. Insufficient:
many low-signal turns survive the patterns, and the turns that are removed aren't the ones
causing observe to fail.

**E: LLM triage + compressed conversation.** Cheap LLM call classifies turns as reasoning vs
housekeeping, then pass triaged turns through the standard compression pipeline. Triage
works as a classifier — it correctly identifies which turns contain reasoning. But observe
still returns NONE on the compressed triaged conversation. The triaged output still includes
compressed assistant text (~500 chars per turn), and that's enough to trigger the same
failure. We don't have a proven mechanism for why assistant text in reasoning turns kills
observe. Possible explanations: pure volume (11 triaged turns × 500 chars of assistant text),
attention shifting to the assistant's framing instead of the owner's reasoning, or mixed-role
presentation changing how the model processes the input. We tested the endpoint (owner-only
works, mixed doesn't) but not the mechanism.

### What we found: context rot

Long conversations exhibit context rot. Early turns carry design decisions, corrections,
and reasoning. Later turns accumulate mechanical work: git operations, CI checks,
formatting fixes. The signal-to-noise ratio degrades as the conversation grows.

The observe prompt reads through the compressed conversation sequentially. When reasoning
turns from early in the conversation are followed by pages of mechanical turns, the LLM's
attention drifts to the recent, mechanical content. Reasoning that is still in the context
window gets washed out by surrounding noise. The result is NONE for the whole conversation.

This is a local signal problem, not a global ratio problem. Turn 4 is a design decision
regardless of what turns 15-27 contain. But observing the full conversation forces the
LLM to evaluate turn 4 in the context of everything that came after it.

The triage + owner-only approach works around this by stripping noise before observe sees
it. A more fundamental fix would observe in local windows so that no turn is ever evaluated
in the context of distant mechanical turns. See reboot.md for next steps.

### Solution: triage + owner-only observe

For conversations with more than 10 turns:

1. **Mechanical filter.** Remove confirmations, mechanical directives, interrupted
   requests. Pattern matching on known low-signal phrases.
2. **LLM triage.** Cheap classification pass identifies which remaining turns contain
   reasoning, decisions, corrections, or design thinking.
3. **Owner-only observe.** Feed only the owner's messages from triaged turns to the
   observe prompt. No assistant context, no compression, no refine step.

For small conversations (10 turns or fewer), the original pipeline works: compress
the full conversation and run observe + refine.

Results on two test conversations:

| Conversation | Turns | Old pipeline | New pipeline |
|---|---|---|---|
| RFC design session (104 msgs, 27 turns) | 27 → 15 → 11 | 0 observations | 7 observations |
| Orwell pass editing (211 msgs, 26 turns) | 26 → 19 → 16 | 0 observations | 9 observations |

The observations are specific: "Uses formal analysis to constrain the design space,
then derives a natural set ({1, 2, 3}) rather than making it configurable beyond what
the math supports." "Monitors the AI's vocabulary for jargon that doesn't match how
they actually speak, and flags it directly."

### Prompt update: agent-directed work

The observe prompt now recognizes that agent-directed work carries signal. When the
owner corrects the agent's draft, makes design decisions through the agent, or rewrites
the agent's output in their own voice, the prompt treats it as reasoning, not routine.

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
Local sources sort by modification time; API sources with incremental sync fetch newest-first.

## Errors

Errors are scoped to minimize blast radius. A conversation-level error (bad JSON, missing
field) is logged and skipped. A source-level error (missing directory, auth failure) closes
that source but others continue. An observe error stops the pipeline.

## Decisions

### Why stream across sources but not within a source?

`Conversations()` returns a batch. Streaming within a source (observe page 1 while page 2
fetches) would require changing every provider. Cross-source streaming captures most of the
benefit because the bottleneck is the slowest source.

### Why a priority queue for limit?

First-come-first-served lets fast local sources consume the entire budget before slow API
sources finish. Distributing the budget evenly across sources sacrifices recency. The priority
queue guarantees the N newest regardless of discovery order.

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
