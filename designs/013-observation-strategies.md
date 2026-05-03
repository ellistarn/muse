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

## Per-window truncation

Per-window observe calls use a token budget of 4096. Thinking models can spend
the whole budget reasoning before emitting content; the call returns truncated.
Aborting the whole conversation in that case is too aggressive. observeWindow
retries once at 16384 tokens. Windows that truncate again are skipped. Adjacent
overlapping windows usually cover the same material, so the skip rarely costs
information.

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

## Deferred

**Window size.** The 8-turn window with stride 4 is untested against alternatives. Larger
windows risk reintroducing the same problem. Smaller windows may fragment multi-turn
reasoning arcs.

**Refine step evaluation.** The refine step deduplicates across overlapping windows but may
collapse useful distinctions.

## References

[1] Orwellian Observation: orwellian-observation.md
