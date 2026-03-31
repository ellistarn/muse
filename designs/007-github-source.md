# GitHub Source

Captures the owner's engagement on GitHub PRs and issues — code review, design discussion, bug
triage — as conversations for the observation pipeline. The signal is the owner's words, reactions,
and corrections. Code, bot chatter, and auto-generated descriptions are context or noise.

GitHub is split into two independent sources: `github-issues` and `github-prs`. Each is opt-in
(`muse add github-issues`, `muse add github-prs`). They require network access and an initial sync
of thousands of API calls, so they do not run on bare `muse compose`.

The split exists because PRs and issues carry fundamentally different signal. Issues contain design
thinking, problem framing, and bug triage. PRs contain implementation detail that can overfit the
muse toward code-level patterns rather than judgment. Independent add/remove lifecycle lets the owner
control which class of content shapes their muse.

## Conversation contract

Each GitHub thread produces one `Conversation`. Issues have source `"github-issues"`, PRs have source
`"github-prs"`. The conversation ID is `{owner}/{repo}/{pull|issues}/{number}`. Project is
`{owner}/{repo}`.

A thread must contain 2+ authentic owner messages to produce a conversation. This aligns with
`extractTurns`, which requires 2+ user turns to produce a turn pair. A thread where the owner was
merely mentioned or left a single response has no back-and-forth to observe.

### Messages

Issue comments, PR review comments, and PR review bodies are included chronologically. PR review
comments carry their file path and a truncated diff hunk — enough location context to interpret the
owner's reaction without bloating with code the observation pipeline would strip.

### Role mapping

Owner messages have role `user`. All other messages have role `assistant`, prefixed with
`[GitHub comment by @username]`. The attribution prefix prevents the refine step from discarding
observations about the owner's response to peer feedback — the refine prompt rejects observations
framed as being about "the assistant."

### PR descriptions

PR descriptions are typically LLM-generated and are not treated as owner-authored content. They are
included for context (comments reference them) but carry the prefix
`[Auto-generated PR description — not authored by the user]` and are excluded from the 2+ owner
message threshold. An owner who authored the PR description and left one real comment has 1 authentic
message, not 2.

Issue descriptions are human-authored and included as the first message with no annotation.

### Filtering

Bot messages and CI automation commands are excluded at assembly time. Bots are identified by `[bot]`
suffix or membership in a known list. Prow commands are single-line messages matching a known command
set — not a generic `/` prefix heuristic, because real comments can start with a slash.

Filtering happens at assembly, not cache. The cache stores everything the API returns. Filtering
decisions change — new bot accounts appear, prow commands evolve. Re-assembling is instantaneous;
re-fetching is bounded by rate limits.

## Sync

Coverage spans all repos accessible to the token, historical and incremental. Each source syncs only
its kind (`type:pr` or `type:issue` in GitHub search queries). Both sources share authentication, the
HTTP ETag cache, and the rate-limited transport (a single set of AIMD limiters) because they hit the
same GitHub API with the same token. Sync state is per-kind so each source tracks its own incremental
timestamp independently.

Recent threads are synced first so partial runs prioritize current content. An interrupted sync after
processing 2026–2023 has the most valuable threads cached; the next run skips them and continues
with older history.

Sync respects GitHub's published rate limits and recovers from throttling without data loss. Throttled
requests are retried with backoff. The sync timestamp advances only on full success — nothing is
permanently lost.

Raw API data is cached locally, upstream of conversation assembly. The cache stores thread metadata
plus full comment payloads from each API endpoint, including untruncated diff hunks and review states.
Assembly changes (formatting, filtering, role mapping) rebuild from cache without re-fetching.

## Decisions

### Why two sources instead of one with a filter?

The user's removal unit is the source type — "I want issues but not PRs." The add/remove lifecycle
*is* the filtering mechanism. A filter within a single source would be premature configurability:
there's no evidence anyone needs "issues with label X but not label Y." There is evidence someone
needs "issues yes, PRs no." Two sources with independent lifecycle matches the demonstrated need.

The two sources are peers, not a decomposition of a parent. If someone adds both, that's intentional.
The old `github` source is gone from the registry — not kept as sugar for "both."

### Why share the transport between sources?

Both sources hit the same GitHub API with the same token, sharing a single rate limit. Independent
AIMD controllers would each independently probe toward the ceiling and collectively overshoot. A
shared transport with shared rate limiters is simpler and correct — one budget, one controller.

### Why annotate PR descriptions instead of dropping them?

PR descriptions provide context — comments reference things in the description, and without it the
conversation loses coherence. The annotation is a content-level label, not a structural change
(different role, separate field), because the downstream consumer is an LLM that reads the content.
The observation pipeline's prompts can act on the label directly.

The threshold exclusion is independent. The threshold ensures the owner actually *engaged* — an
auto-generated description is presence without engagement. If the description was genuinely
human-written, the threshold creates a false negative — accepted as the cost of a cleaner signal.
Revisit if empirical data shows valuable conversations being dropped.

### Why filter at assembly, not cache?

The cache is raw API data. Filtering at assembly means updating a list, not re-fetching. The
observation pipeline might benefit from messages we currently discard. Re-assembling from cache is
free.

### Why 2+ owner messages, not 1?

A thread where the owner never engaged in discussion has no back-and-forth — no corrections, no
pushback, no preferences expressed in response to others. This is the engagement level where the
owner's thinking becomes legible.

## Deferred

**Richer role model**: Mapping all non-owner voices to "assistant" is a category error — a senior
engineer's pushback is different from an AI's response. The attribution prefix is the pragmatic fix.
A proper third role (`peer`) would require pipeline changes to `extractTurns` and
`compressConversation`. **Revisit when:** observation quality from GitHub conversations shows
empirical signal loss.

**GitHub Discussions**: Different API surface, different interaction pattern. Not in scope.
