# Muse

A muse absorbs memories from your conversations, distills them into a soul
document (soul.md), and embodies your unique thought processes when asked
questions.

## Install

```
go install github.com/ellistarn/muse/cmd/muse@latest
```

## Getting Started

```bash
muse dream             # discover memories and distill soul.md
muse soul              # print soul.md
muse ask "your question"  # ask your muse directly
```

Wire up the MCP server so agents can ask your muse questions:

```json
{
  "mcpServers": {
    "${USER}": {
      "command": "muse",
      "args": ["listen"]
    }
  }
}
```

## Sources

Memories are automatically discovered from:

- **Claude Code** — `~/.claude/projects/`
- **Kiro** — `~/Library/Application Support/Kiro/User/globalStorage/kiro.kiroagent/workspace-sessions/`
- **OpenCode** — `~/.local/share/opencode/opencode.db`

## Storage

By default, data is stored locally at `~/.muse/`. To use an S3 bucket instead
(for sharing across machines or hosted deployment), set the `MUSE_BUCKET`
environment variable:

```bash
export MUSE_BUCKET=$USER-muse
```

Use `muse sync <local|s3> <local|s3> [memories|reflections|souls]` to copy data
between backends.

Run `muse --help` for detailed usage.
