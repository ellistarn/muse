# Muse

An AI that thinks like you. Distilled from your conversation history
across Claude Code, Kiro, and OpenCode so your agents can consult your
perspective without pulling you into the loop.

## Install

```
go install github.com/ellistarn/muse@latest
```

## Getting Started

```bash
muse distill                # discover conversations and distill muse.md
muse ask "your question"  # ask your muse directly
muse listen               # start MCP server
muse show                 # print muse.md
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

Conversations are automatically discovered from:

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

```bash
muse sync local s3                  # push everything to S3
muse sync s3 local conversations    # pull conversations from S3
```

Run `muse --help` for detailed usage.
