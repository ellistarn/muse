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
muse ask "your question"  # ask your muse a question
muse soul              # print soul.md
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

## Commands

### dream

Discover memories from conversation sources, reflect on them, and distill a
soul document.

```
muse dream [flags]
```

| Flag | Description |
|------|-------------|
| `--reflect` | Re-reflect on all memories, not just new ones |
| `--learn` | Skip reflect phase, re-distill soul from existing reflections |
| `--limit N` | Process at most N memories (default 100) |

### ask

Ask your muse a question directly from the command line. Streams the response
to stdout.

```
muse ask "Is X a good approach for Y?"
```

### soul

Print the current soul document.

```
muse soul [flags]
```

| Flag | Description |
|------|-------------|
| `--diff` | Summarize what changed since the last dream |

### listen

Start an MCP server over stdio. This is how agents interact with your muse
during coding sessions.

```
muse listen
```

### sync

Copy data between storage backends. Useful for migrating between local and S3
storage, or syncing across machines.

```
muse sync <src> <dst> [category...]
```

Where `src` and `dst` are `local` or `s3`, and optional categories are
`memories`, `reflections`, `souls`.

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

## Configuration

| Variable | Description |
|----------|-------------|
| `MUSE_BUCKET` | S3 bucket name for remote storage |
| `MUSE_MODEL` | Override the Bedrock model ID |
| `MUSE_CLAUDE_DIR` | Override Claude Code data directory |
| `MUSE_KIRO_DIR` | Override Kiro data directory |
| `MUSE_OPENCODE_DB` | Override OpenCode database path |
