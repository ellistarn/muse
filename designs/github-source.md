# GitHub Source

Captures the owner's back-and-forth on GitHub PRs and issues — code review, design discussion,
bug triage — as conversations for the observation pipeline. This is the first network source;
existing sources read local files and databases.

GitHub is opt-in: `muse compose github`. It does not run on bare `muse compose` because the
initial sync makes thousands of API calls. Default providers read local files and are free to
invoke unconditionally.

## What we fetch

PRs and issues where the owner participated, across all repos accessible to the token. Each
thread becomes one conversation. The thread body is the first message; issue comments, PR review
comments (with file path and diff context), and review bodies follow chronologically.

One source, not two. PRs and issues share authentication, API client, pagination, rate limits,
and filtering logic. The only difference is whether review comments are fetched. If they need
to diverge semantically, that's a metadata field on the conversation, not a source boundary.

## Conversation shape

Each thread produces a standard `Conversation` with source `"github"`. The conversation ID is
`{owner}/{repo}/{pull|issues}/{number}`. Project is `{owner}/{repo}`. Title is the thread title.

The thread body is the first message, with role determined by authorship — `user` if the owner
wrote it, `assistant` otherwise. All subsequent comments follow chronologically in the same
role-mapping scheme. Threads where the owner has fewer than 2 messages produce no conversation.

## Role mapping

The owner's comments map to `user`. Everyone else maps to `assistant`. Non-owner messages are
prefixed with `[GitHub comment by @username]` — this prevents the refine step from discarding
observations about the owner's response to peer feedback, since the refine prompt rejects
observations framed as being about "the assistant."

## Discovery

Search uses `involves:{username}` which covers author, commenter, assignee, and mentioned.
This casts a wider net than `commenter:` alone, but the 2+ owner messages filter downstream
handles the noise — threads where the owner was merely mentioned or assigned get filtered out.

The GitHub search API caps results at 1000 per query. Date-segmented search bypasses this:
yearly intervals from 2008 to present, subdividing into months when a year's `total_count`
exceeds 1000. Uses `created:` ranges (non-overlapping partitions) for the initial sync and
`updated:>=TIMESTAMP` for incremental.

## Cache

Raw API data is cached locally at `~/.muse/cache/github/`. This is the first source that makes
network calls, so it's the first that needs a cache. The cache stores upstream of conversation
assembly — thread metadata plus the full comment payloads from each API endpoint, including
untruncated diff hunks and review states. If assembly logic changes (formatting, filtering,
role mapping), re-running compose rebuilds from cache without re-fetching.

```
~/.muse/cache/github/
├── state.json                                        # last complete sync timestamp + username
└── threads/{owner}/{repo}/{pull|issues}/{number}.json
```

The sync timestamp in `state.json` represents the last *complete* sync, not the most recent API
call. It advances only on full success. Interrupted syncs preserve already-cached threads; the
next run re-discovers but skips threads whose `UpdatedAt` hasn't changed. Incremental sync
searches `updated:>=LAST_SYNC` — typically a handful of threads.

## Filtering

Bot messages (e.g. `k8s-ci-robot`, `dependabot[bot]`) and prow commands (e.g. `/retest`,
`/lgtm`) are filtered at assembly time, not cache time. The cache stores everything the API
returns.

Bots are identified by `[bot]` suffix or membership in a known list (`k8s-ci-robot`,
`googlebot`, `codecov`, etc.). Prow commands are single-line messages matching a known command
set (`/retest`, `/lgtm`, `/approve`, etc.) — not a generic `/` prefix heuristic, because real
comments can start with a slash.

## Diff context

PR review comments include the file path and last 8 lines of the diff hunk from the API. This
provides enough location context to interpret the owner's reaction. Full PR diffs would bloat
conversations with code the observation pipeline strips during compression — assistant messages
are truncated to 500 chars. The signal is the owner's words, not the code. Diff hunks are the
majority of cache size by volume, which is expected and acceptable.

## Decisions

### Why not the GraphQL API?

The REST search API with date segmentation gets complete coverage. GraphQL would eliminate the
1000-result cap natively but introduces a different programming model, different auth scoping,
and a dependency on GitHub's GraphQL schema stability. The caching layer means the REST API's
pagination cost is paid once — the steady state is local reads regardless.

### Why filter at assembly, not cache?

The cache is raw API data. Filtering decisions change — new bot accounts appear, prow commands
evolve, the observation pipeline might benefit from messages we currently discard. Filtering at
assembly means updating a list, not re-fetching. Re-fetching is bounded by rate limits;
re-assembling is instantaneous.

### Why `involves:` instead of `commenter:` + `author:`?

`involves:` is a single query covering all participation types. The extra noise (mentioned,
assigned) is filtered by the 2+ owner messages requirement — threads where the owner never
wrote anything get dropped. Two queries instead of four, same result after filtering.

### Why 2+ owner messages, not 1?

Aligned with the observation pipeline's `extractTurns`, which requires 2+ user turns to
produce a turn pair. A thread where the owner wrote only the description and never engaged in
discussion has no back-and-forth — no corrections, no pushback, no preferences expressed in
response to others.

## Failure modes

**Rate limit exhaustion**: Individual thread fetch failures are skipped (`continue`). This is
acceptable because the incremental sync retries on the next run — the data loss is temporary.
The sync timestamp doesn't advance on incomplete runs, so nothing is permanently missed.

**Initial sync duration**: First run fetches all threads. For 4000+ PRs, this takes time
(bounded by GitHub's 5000 req/hour limit). Already-cached threads are skipped on re-run, so
interrupted syncs make incremental progress.

**Token expiration mid-sync**: Same as rate limits — individual failures skip, next run retries.

## Deferred

**Richer role model**: The muse's feedback was that mapping all non-owner voices to "assistant"
is a category error — a senior engineer's pushback is different from an AI's response. The
attribution prefix is the pragmatic v1 fix. A proper third role (`peer`) would require pipeline
changes to `extractTurns` and `compressConversation`. **Revisit when:** observation quality
from GitHub conversations shows empirical signal loss.

**Automatic rate limit backoff**: Currently relies on skip-and-retry-next-run. Proper
sleep-on-429 would make the initial sync more robust. **Revisit when:** users report
incomplete syncs that don't converge.

**GitHub Discussions**: Different API surface, different interaction pattern. Not in scope.
