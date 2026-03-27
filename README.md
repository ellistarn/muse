# Muse

Your muse is an AI that thinks like you, derived from your conversation history across Claude Code, Kiro, and
OpenCode.

## Install

```
go install github.com/ellistarn/muse@latest
```

## Getting Started

```bash
muse compose              # discover conversations and compose muse.md
muse ask "your question"  # ask your muse directly
muse listen               # start MCP server
muse show                 # print muse.md
```

Work directly with your muse as agent:

```json
// OpenCode — ~/.config/opencode/opencode.json
{ "instructions": ["~/.muse/muse.md"] }
```

Or run as an MCP server so other agents can work with your muse:

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
- **Kiro** —
  `~/Library/Application Support/Kiro/User/globalStorage/kiro.kiroagent/workspace-sessions/`
- **OpenCode** — `~/.local/share/opencode/opencode.db`
- **Codex** — `~/.codex/`
- **Slack** (opt-in) — Set `MUSE_SLACK_TOKEN` and run `muse compose slack`.

## Storage

By default, data is stored locally at `~/.muse/`. To use an S3 bucket instead (for sharing across
machines or hosted deployment), set the `MUSE_BUCKET` environment variable:

```bash
export MUSE_BUCKET=$USER-muse
```

```bash
muse sync local s3                  # push everything to S3
muse sync s3 local conversations    # pull conversations from S3
```

Run `muse --help` for detailed usage.
