# Import

Muse's source system is closed. Every source lives in the codebase, implements the Provider interface,
and is registered in a hardcoded list. Proprietary data sources — internal code review systems,
enterprise chat platforms, internal AI assistants — will never be added to the project. Users with
access to these systems have no way to feed that data into muse.

Import opens the source system to external tools without changing the source system itself. A plugin
is an executable that produces conversations. Muse discovers, invokes, validates, and stores the
output. The compose pipeline processes imported conversations identically to built-in sources — it
reads from storage, not from providers, so it does not know or care where a conversation originated.

## Plugin identity

A plugin is an executable named `muse-{name}` on `$PATH`. The `muse-` prefix is a namespace
convention — the same pattern as `git-*` subcommands or `kubectl-*` plugins. The source name is
`{name}`: `muse-code-reviews` → source `code-reviews`, stored at `conversations/code-reviews/`.
No registry, no config file, no registration step.

Plugins are available to muse if and only if they are executable and on `$PATH`. A plugin that is
removed from `$PATH` becomes unavailable. Its previously imported conversations and observations
persist in storage and continue to participate in compose.

## Invocation

`muse import {name}` runs one plugin. The argument is the source name — muse resolves it to
`muse-{name}` on `$PATH`. This is the same convention as `git credential-store` resolving to
`git-credential-store`, and consistent with `muse add slack` and `muse remove github-prs`, which
take source names, not binary names.

```
muse import code-reviews     # resolves to muse-code-reviews on $PATH
muse import internal-chat    # resolves to muse-internal-chat on $PATH
```

`muse import` with no arguments re-imports all previously imported sources — sources that have a
`conversations/{name}/` directory in storage. Bare `muse import` never invokes a plugin that has not
been explicitly imported before. First use is always explicit.

When re-importing, muse resolves each known source name back to `muse-{name}` on `$PATH`. If the
executable is no longer on `$PATH`, muse warns and skips that source. Previously imported
conversations and observations are not affected — they persist in storage and continue to participate
in compose. The warning ensures the user knows their import is stale rather than silently falling
behind.

This is the same opt-in boundary as network sources. `muse add slack` activates Slack; `muse compose`
re-syncs it on subsequent runs. `muse import code-reviews` activates the code-reviews plugin;
`muse import` re-runs it on subsequent runs. Presence of the conversation directory is the
activation record, the same mechanism built-in sources use.

## Output contract

A plugin receives one environment variable: `MUSE_OUTPUT_DIR`, pointing to a writable temporary
directory. The plugin writes its output there:

- One `.json` file per conversation, conforming to muse's `Conversation` schema. Files can be named
  anything — muse renames them to `{conversation_id}.json` during the move to storage.

- A `.muse-source.json` metadata file declaring the source type:
  ```json
  {"type": "human"}
  ```
  Valid values: `human` or `ai`. This is required. A plugin that does not write it has failed its
  contract.

The `Source` field in each conversation is overwritten by muse to match the source name. The plugin's
opinion about its own source name does not matter — muse is the system of record. This decouples
plugin authorship from muse naming: the same binary installed as `muse-code-reviews` and
`muse-cr` (via symlink) produces two independent sources.

## Source type

The compose pipeline routes conversations to different observe prompts based on whether the source is
human-to-human or human-to-AI. Wrong classification produces wrong observations — a silent
correctness failure that surfaces downstream in the muse with no obvious signal pointing back to the
cause.

The source type is declared by the plugin in `.muse-source.json` and persisted by muse at
`conversations/{name}/.muse-source.json`. The compose pipeline reads it from there. This keeps
compose reading from storage rather than reaching back into plugin invocation.

The type is written on every import run, not just the first. If a plugin changes its type
declaration, the stored metadata updates. The compose pipeline's `isHumanSource()` resolves the type
for unknown source names by checking the metadata file. Sources without metadata default to `ai` —
but import sources always have metadata because its absence is a validation error.

## Success and failure

Exit code 0 means success. Muse validates each JSON file in the output directory, overwrites the
`Source` field, writes valid conversations to storage via `PutConversation`, writes the source
metadata, and creates the observation directory to activate the source for compose.

Non-zero exit means failure. Muse discards the output directory entirely and reports the plugin's
stderr as the error message. No conversations are written. No source directory is created or
modified. Partial success is the plugin's problem — a plugin that wants to report partial results
writes what it has and exits 0.

Malformed JSON files are individually rejected with a diagnostic identifying the file and the
failure. Valid files from the same run are still imported. A conversation missing `ConversationID`
fails validation.

## Plugin configuration

Plugin configuration is the plugin's concern. If `muse-code-reviews` needs an API token, a date
range, or a server URL, it reads its own environment variables, its own config files, or its own
flags. Muse does not mediate, template, or proxy plugin configuration.

This is a deliberate absence. The space of possible plugin configurations is unbounded — credential
management, pagination strategies, incremental sync state, output filtering. Muse mediating any of
this creates a coupling surface that grows with every plugin. The plugin author knows their system;
muse knows conversations. The `MUSE_OUTPUT_DIR` environment variable and the Conversation JSON schema
are the entire interface.

## Visibility

`muse sources` displays import sources alongside built-in sources. Import sources are distinguished
by an `(import)` tag. Conversation and observation counts come from storage — the same mechanism used
for built-in sources. A plugin that is no longer on `$PATH` but has stored conversations still
appears in the source listing.

## Compose integration

None. `store.ListConversations()` walks all conversation directories. The compose pipeline processes
imported conversations identically to built-in ones — observation, labeling, theming, composition.
The observation directory created by `EnsureSourceDir()` makes import sources visible to
`ResolveSources()` and `ListObservationSources()`.

The only compose-level change is `isHumanSource()`, which checks the stored source metadata when
the source name is not in the hardcoded list of known human sources. This is a read from storage,
not a new dependency on plugin infrastructure.

## Deferred

**Structured progress.** Plugins write to stderr for diagnostics. Muse streams it with a prefix.
There is no structured progress protocol. **Revisit when:** a plugin takes long enough that silent
waiting is genuinely unacceptable, and stderr streaming is insufficient.

**Auto-import during compose.** `muse import` is explicit. Users run it before `muse compose` or
script both together. **Revisit when:** the manual step becomes a friction point for users who always
want both.

**Incremental import.** Muse currently writes all conversations the plugin produces, diffing against
storage by `ConversationID` and `UpdatedAt`. The plugin is responsible for its own incremental sync
logic. **Revisit when:** plugin authors need muse to pass the last-import timestamp or similar state.

**Plugin removal.** `muse remove {name}` could delete the conversation and observation directories
for an import source, the same as it does for built-in sources. Not committed here because the
existing `RemoveSource()` may already handle it — the source name is just a directory prefix.
