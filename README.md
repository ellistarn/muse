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
muse eval                 # evaluate the muse against a base model
muse listen               # start MCP server
muse show                 # print muse.md
muse show -o muse.pdf     # export as PDF
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

Local sources are activated automatically on first run: **Claude Code**, **OpenCode**, **Codex**, **Kiro**.

Network and import sources require explicit opt-in:

```bash
muse add claude-ai                  # claude.ai export (see below)
muse add github-issues              # GitHub issues (requires gh auth)
muse add github-prs                 # GitHub PRs (requires gh auth)
muse add slack                      # Slack (set MUSE_SLACK_TOKEN and MUSE_SLACK_WORKSPACE)
muse remove github-prs              # stop including a source
muse sources                        # see what's active
```

### Claude AI Export

To include your claude.ai conversations:

1. Go to claude.ai → Settings → Export Data
2. Place the downloaded zip at `~/.muse/imports/claude-ai/`
3. Run `muse add claude-ai`

An extracted `conversations.json` in the same directory also works.
Override the directory with `MUSE_CLAUDE_AI_DIR`.

Sources are remembered across runs — `muse compose` processes whatever is active.

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
