# Slack Source

Captures the owner's Slack conversations as a source for the observation pipeline. Unlike
coding-session sources (Claude Code, OpenCode), Slack conversations are between human peers,
not between a human and an AI assistant. This is where people argue, decide, persuade, and
coordinate — the signal is different from what shows up in tool-assisted work.

Slack is opt-in: `muse compose slack`. It requires `MUSE_SLACK_TOKEN` (a SAML cookie file path or
raw token) and `MUSE_SLACK_WORKSPACE` (for SSO, comma-separated for multiple workspaces). It does
not run on bare `muse compose`.

## Model

The channel is the conversation boundary. All messages in a channel — including thread replies
inlined chronologically — are flattened into a single timeline, then chunked by size into
conversations. This is the right model because Slack is async: a conversation can span days
across multiple threads in the same channel, and time-based windowing fragments it.

Threads are not separate conversations. A thread is a sub-discussion within a channel. Its replies
are fetched via `conversations.replies` and merged into the channel timeline at their timestamp.
The result is one flat, chronological stream per channel that preserves the full context of how
ideas developed — including across interleaved threads.

## What we fetch

All messages the owner sent, discovered via `search.messages` with `from:<@userID>`. This returns
up to 10,000 messages (100 pages × 100 results) across all channels and DMs. From the search
results, we extract:

- **Channels** the owner was active in, with the time range of their activity
- **Threads** the owner participated in (via `thread_ts` on search results)

For each channel, we then fetch:

1. `conversations.history` for the owner's activity time range (paginated, ±5min padding)
2. `conversations.replies` for every thread in that range — both threads from search and threads
   discovered in the history (messages with `reply_count > 0`)

All messages are merged, deduplicated by timestamp, and sorted chronologically. The result is one
flat timeline per channel.

## Conversation shape

Each channel's flat timeline is chunked into conversations of ~20k characters each (~5k tokens).
A channel with 500 messages might produce 5 conversations; a quiet DM with 10 messages is one.

Conversation IDs are `{teamID}:{channelID}:{chunkIndex}`. Project is `{teamName}/#{channelName}`.
Chunks beyond the first get `[part N]` in the title to indicate continuation.

## Role mapping

The owner's messages map to `user`. All peers map to `assistant`. Every message is prefixed with
`@displayname:` for attribution — the downstream extract prompt sees who said what.

This reuses the pipeline's existing `user`/`assistant` contract. The labels are structural, not
semantic — the `@displayname:` prefix in the content carries the actual attribution. The extract
prompt looks for reasoning, voice, and awareness in the `[human]` messages, which works for Slack
because the owner's messages are the ones mapped to `user`.

For Slack, `extractTurns` accepts 1 user turn (instead of the default 2 for AI conversations).
A thread where the owner makes one substantive point and three peers respond is valid signal —
the owner's single statement reveals reasoning and voice.

## User display names

User IDs are resolved to display names via `users.info` with an in-memory cache on the client.
Names are stored in the cached channel data so assembly doesn't require API calls. The preference
order is: profile display name → profile real name → real name → username handle → raw user ID.

## Authentication

`MUSE_SLACK_TOKEN` serves dual purpose:

- **File path** (starts with `/` or `~/`): loads cookies from the file and follows the workspace's
  SAML SSO redirect chain to obtain an `xoxc-` token. The full cookie jar from the SSO flow is
  passed to the API client — `xoxc-` tokens require the session cookies, not just the `d` cookie.
- **Token** (starts with `xox`): used directly. For `xoxc-` tokens, `MUSE_SLACK_COOKIE` must
  also be set.

`xoxc-` tokens must be sent as POST form fields, not Bearer headers. Enterprise Slack rejects
Bearer auth for these tokens.

`MUSE_SLACK_WORKSPACE` is required for SSO. It supports comma-separated values for multiple
workspaces (e.g. `company.enterprise.slack.com,community.slack.com`). Each workspace gets its
own SSO flow with the same cookie file. If one workspace fails SSO, it's skipped and the rest
continue. The cache namespaces by `teamID` so multiple workspaces don't collide.

The SSO implementation is IDP-agnostic — it follows HTTP redirects and submits HTML forms
regardless of whether the IDP is Okta, Azure AD, or anything else.

## Cache

Raw API data is cached at `~/.muse/cache/slack/`. One file per channel, containing the full flat
message stream with user display names. Formatting and chunking happen at assembly time, not cache
time — changing the chunk size or message format doesn't require re-fetching.

```
~/.muse/cache/slack/
├── {teamID}/
│   ├── state.json              # last sync timestamp + user ID
│   └── channels/
│       ├── {channelID}.json    # flat message timeline + user map
│       └── ...
```

The sync timestamp advances only on full success. User ID changes invalidate the entire workspace
cache. Incremental sync uses `after:YYYY-MM-DD` in the search query to fetch only new activity.

## Filtering

Bot messages, system messages (joins, leaves, topic changes), and URL-only messages are filtered
at assembly time. The cache stores everything the API returns — filtering decisions can change
without re-fetching.

## Rate limiting

- `search.messages`: Tier 2 (~20 req/min). 2s delay between pages.
- `conversations.history`: Tier 3 (~50 req/min). 500ms delay between pages.
- `conversations.replies`: Tier 3. 500ms delay between threads.
- `users.info`: Tier 4 (~100 req/min). No explicit delay, in-memory cache deduplicates.

429 responses trigger a retry after the `Retry-After` header value (default 5s).

## Failure modes

**Missing credentials**: Hard error with actionable message. Since Slack is opt-in, a missing
`MUSE_SLACK_TOKEN` means the user asked for Slack but didn't configure it — silent nil would be
confusing.

**Sync failure**: Hard error. Unlike default providers that fail gracefully (the user didn't ask
for them), explicitly-requested sources should fail loudly. Partial results from a broken sync
are worse than no results.

**Thread fetch failure**: Individual thread failures are skipped (`continue`). The channel history
still captures the thread parent message; only the replies are lost. The next incremental sync
retries.

**Rate limit exhaustion**: Individual API failures skip the affected channel/thread. The sync
timestamp doesn't advance, so the next run retries everything that failed.

## Decisions

### Why flatten threads into the channel timeline?

A conversation often spans multiple threads in the same channel. Treating threads as separate
conversations loses the connections between them. Flattening gives the observation pipeline (and
any future knowledge extraction layer) the full context of how ideas developed across interleaved
discussions.

### Why chunk by character count, not by topic or time?

Topic segmentation of channel messages is a hard problem we don't need to solve. The compose
pipeline's extract prompt is designed to find signal in noise — that's its job, not the chunking
logic's. Character-based chunking is simple, predictable, and gives the LLM focused windows of
~5k tokens each.

### Why 20k characters per chunk?

~5k tokens per chunk. Large enough for 200-300 Slack messages with attribution prefixes — enough
context for the extract prompt to identify reasoning patterns. Small enough that the LLM can focus.
The compose pipeline's own chunking (200k chars) would produce chunks that are too large for Slack's
information density.

### Why not the default 2-turn minimum for extractTurns?

In AI conversations, 2+ user turns means the user corrected or refined something — that's where
preferences emerge. In peer conversations, even a single substantive statement reveals reasoning
and voice. 43% of Slack conversations had only 1 owner message — dropping them would lose nearly
half the signal.

### Why `user`/`assistant` roles instead of a new peer role?

The pipeline contract. `extractTurns`, `compressConversation`, and the extract prompt all operate
on `[human]`/`[assistant]` pairs. Adding a third role would require changes across the pipeline.
The `@displayname:` prefix in message content carries the actual attribution — the role field is
structural plumbing, not semantic meaning.

### Why not auto-discover workspaces from the cookie file?

The cookie file contains IDP cookies (e.g. for an SSO provider), not Slack cookies. Slack session
cookies are created during the SSO flow itself — they don't exist in the cookie file beforehand.
Scanning the cookie file for `*.slack.com` domains yields nothing useful. The workspace must be
specified explicitly via `MUSE_SLACK_WORKSPACE`.

## Deferred

**Knowledge extraction**: The flat channel model was designed to support future knowledge
extraction (what was discussed, what was decided) in addition to current observation
extraction (how the person thinks). The data model is ready; the extraction layer is not.

**DM discovery via conversations.list**: Currently we only find DMs where the owner sent a
message that appears in search results. `conversations.list` with `types=im,mpim` would discover
all DM channels for more complete coverage.

**Cluster quality**: Adding 258 Slack observations stressed the labeling/normalization pipeline.
The two largest clusters (99 and 95 observations) are catch-all buckets that smear distinct
patterns. This is a compose pipeline issue, not a Slack issue.
