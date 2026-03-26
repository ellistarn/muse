# Data Source Interface

Muse reads conversations from external tools that control their own JSON schemas and change them
without notice. Users accumulate conversations across tool versions, so a single source directory
may contain files written under different schemas.

## Contract

A data source is a directory of conversation files under `conversations/<source>/`. Each file is a
JSON document representing one conversation. Muse requires two fields from each conversation:

- **`conversation_id`** — unique identifier for the conversation
- **messages** — the conversation content (turns between human and assistant)

The source name (e.g. `claude-code`, `kiro`) comes from the directory, not the file. A conversation
exists if its file exists, regardless of whether it has been observed.

Each source has a parser that deserializes its format into muse's internal `Conversation` type. Parsers handle schema variation across tool versions — when an upstream tool renames
a field, the parser accepts both names. Old files stay on disk as-is; backward-compatible
deserialization, not migration.

A file that cannot be parsed is an error. Muse fails fast with a clear message identifying the file
and the failure. A file that matches a known older schema is normal and handled by the parser. An
unknown format is the signal to update the parser.

## Decisions

### Why fail fast on unknown formats?

Silent skipping would mask the problem: muse would produce a muse.md from whatever subset of
conversations it could parse, and the user wouldn't know that half their data was ignored.

### Why accept old formats indefinitely?

When an upstream tool ships a new schema, the parser is updated to accept both old and new. Files on
disk are never migrated. If format changes accumulate beyond simple field renames, a
version-dispatch layer in the parser may be needed. Not yet.
