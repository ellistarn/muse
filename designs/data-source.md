# Data Source Interface

## Context

Muse reads conversations written by external tools (Claude Code, Kiro, OpenCode). Each tool
controls its own JSON schema and changes it without notice. Users accumulate conversations across
tool versions, so a single `~/.muse/conversations/claude-code/` directory may contain files written
under different schemas.

During the incremental composition eval, ~100 of 234 claude-code conversations failed to parse
because Claude Code renamed `session_id` to `conversation_id`. The observe pipeline treated this as
a fatal error.

## Data Source Contract

A data source is a directory of conversation files under `conversations/<source>/`. Each file is a
JSON document representing one conversation. Muse requires two fields from each conversation:

- **`conversation_id`** — unique identifier for the conversation
- **messages** — the conversation content (turns between human and assistant)

The source name (e.g. `claude-code`, `kiro`) comes from the directory, not the file.

### Parsing

Each source has a parser that deserializes its conversation format into muse's internal
`Conversation` struct. Parsers handle schema variation across tool versions. When an upstream tool
renames a field, the parser accepts both names — backward-compatible deserialization, not migration.
Old files stay on disk as-is.

### Validation

A conversation that cannot be parsed is an error. Muse fails fast with a clear message identifying
the file and the parse failure. This surfaces format changes immediately rather than silently
dropping data.

The distinction: a file that doesn't match any known schema for its source is a real error worth
failing on. A file that matches a known older schema is normal and handled by the parser.

### Discovery

Muse discovers conversations by listing files in each source directory. Discovery is independent of
observation — a conversation exists if its file exists, regardless of whether it has been observed.

## Why fail fast

When a new upstream format appears, the user needs to know immediately. Silent skipping would mask
the problem: muse would produce a muse.md from whatever subset of conversations it could parse, and
the user wouldn't know that half their data was ignored. A fatal error on an unknown format is the
signal to update the parser.

## What this means for format changes

When an upstream tool ships a new schema:

1. Identify the field mapping between old and new schemas
2. Update the parser to accept both
3. No migration of files on disk — accept old formats indefinitely

If format changes accumulate beyond simple field renames (structural changes, new message types), a
version-dispatch layer in the parser may be needed. Not yet.
