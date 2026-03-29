# Sources

A source normalizes platform-specific conversation data into muse's `Conversation` type (see
grammar.md). The source owns everything about how to find, fetch, and parse its data. Muse owns
everything after normalization — it does not know or care whether a conversation came from a local
JSONL file, a SQLite database, or a paginated API.

A source implements `Name` and `Conversations`. Conversations returns all conversations available
from the source, normalized into muse's internal type. A progress callback is available for sources
that do slow work — network sources use it to report sync status. A source that does not apply to
this machine — its data directory does not exist, its database is not installed — returns nothing.
This is not an error. A source that should apply but fails returns an error.

## Conversation boundaries

For most sources, the conversation boundary is obvious — one session is one conversation. Platforms
with continuous or overlapping interaction are harder.

A Slack channel is a continuous stream of interleaved threads spanning days or weeks. There is no
natural conversation boundary — it must be imposed. A GitHub PR is closer to a natural boundary, but
review comments, issue comments, and review bodies are three separate API surfaces that must be
merged chronologically to reconstruct what actually happened.

Sources that make novel decisions beyond this design document them in their own designs
(github-source.md, slack-source.md). A source where one session maps to one conversation — like any
local AI coding tool — doesn't need its own design.

## Role mapping

Every source marks which messages are the owner's. The owner's messages get role `user`, everyone
else gets `assistant`, because the observation pipeline sends conversations to LLMs that expect this
vocabulary. Non-owner identity is preserved in message content since the role field can't carry it.
What counts as signal (corrections, pushback, positions taken) is the observation pipeline's concern,
not the source's.

## Format compatibility

Sources are adapters against formats they don't control. Local sources read data written by Claude
Code, Kiro, OpenCode, and Codex — tools that change their schemas without notice. Users accumulate
conversations across tool versions, so a single data directory may contain files written under
different schemas.

Parsers are written against current upstream formats. When an upstream tool renames a field or
restructures its data, the parser breaks and must be updated to accept both old and new formats.
Files on disk are never migrated — backward-compatible deserialization is the only option since muse
doesn't own these files.

An unknown format should be a real error — the source should fail fast with a clear message
identifying the file and the failure. Most parsers currently skip unparseable files silently, which
means muse can produce a muse.md from a subset of conversations without the user knowing.

## Opt-in boundary

**`muse compose` with no arguments never makes network calls.**

Local sources read from the filesystem or local databases — things already on the machine because the
user ran the tool. They are scanned unconditionally. If the data isn't there, the source returns
nothing. Network sources reach out to APIs and require explicit selection: `muse compose github`,
`muse compose slack`.

When the user explicitly names a source, failure is fatal — the user asked for it and silence would
mask the problem. This includes missing credentials: if `muse compose github` is run without a token,
that is an error, not a silent no-op. When running with defaults, individual source failures are
warnings and the source is skipped — a broken local database shouldn't block the other sources that
work.

## Caching and sync

Network sources are constrained by rate limits and data volume. These constraints drive two patterns:

**Caching.** Network data is cached to disk upstream of conversation assembly. The cache stores
fetched content after API-level assembly (merging related endpoints, resolving references) but before
filtering, role mapping, or chunking. Changes to assembly logic don't require re-fetching.

**Incremental sync.** First run fetches historical data, subject to API limits. Subsequent runs fetch
only the delta since last sync.

## Registration and configuration

Sources are registered in a hardcoded list. There is no plugin system — every source requires
platform-specific parsing and testing against real data. Each source uses environment variables for
path and credential overrides. There is no config file.
