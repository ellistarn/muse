# Sessions

A session is the continuity of a conversation across process boundaries — not a durable artifact. It
exists so `muse ask` can sustain a multi-turn exchange the way an MCP client does: by accumulating
message history. The conversation persists. The judgment governing it does not — the system prompt is
always derived fresh from the current muse.md, because the owner's muse evolves independently of any
conversation in flight.

Sessions are not conversation sources. The compose pipeline observes what the owner said to agents and
peers — signal about how the owner thinks. A session is the owner talking *to* their muse, not
through it. Slurping sessions into the compose pipeline would feed the muse's own output back into
its derivation — a distortion loop, not a signal source.

## Model

A session holds a message history: alternating user and assistant turns. It has an ID (UUID, assigned
on first save) and a system prompt (stored as metadata for debugging). Two sessions never share
state. A session grows until the user starts a new one.

Both MCP and CLI sessions are persisted to disk so there is a record of what was discussed. The
difference is in how sessions are resumed:

- **MCP**: each client connection starts a fresh session. The MCP server never reads the `latest`
  pointer and never writes it. When the client disconnects, the session remains on disk but is not
  automatically resumed by any future connection.

- **CLI**: `muse ask` resumes the latest session by default, because the CLI invocation boundary is
  artificial — the user is in the same conversation, they just pressed Enter in their shell. `--new`
  starts a fresh session when the user's intent has actually changed.

The `latest` pointer is a CLI UX concern — it is updated by `muse ask` after each turn, never by the
MCP path. MCP sessions persist to the same directory but never interact with the `latest` pointer in
either direction.

## Storage

```
~/.muse/sessions/
├── {uuid}.json     # serialized session (id, system, messages)
└── latest          # plain text file containing the current session ID
```

This directory is not scanned by any conversation provider or by `ListConversations`, which walks
only `~/.muse/conversations/`. The separation is structural, not conventional — the compose pipeline
has no code path that reaches `~/.muse/sessions/`.

Session files are best-effort. A write failure during persistence does not fail the ask — the
response has already been streamed — but the failure is logged to stderr so the user knows continuity
may be lost.

## System prompt refresh

The system prompt is always recomputed from the current muse.md, even when resuming a session. The
messages are the continuity the user cares about. The system prompt is the judgment layer, and the
owner's latest muse.md should govern. A session created before a recompose picks up the new muse
automatically.

The prior system prompt is stored in the session file for debugging — it records what governed the
conversation at each point — but it is never sent to the model on resume.

## Lifetime

Sessions accumulate messages without bound. There is no TTL or automatic pruning. `--new` is the
escape hatch when context grows stale or hits model limits. Context window limits will eventually
force truncation or failure — the right solution is likely explicit (user-initiated), not automatic,
but it isn't this design's problem.
