# Muse

A muse is the distilled essence of how you think. It absorbs your memories from agent interactions,
distills them into a soul document ([soul.md](https://soul.md)), and embodies your unique thought
processes when asked questions.

## Install

```
go install github.com/ellistarn/muse/cmd/muse@latest
```

## Getting Started

```bash
muse dream             # discover memories and distill your soul
muse soul              # print the soul document
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

## Storage

By default, data is stored locally at `~/.muse/`. To use an S3 bucket instead
(for sharing across machines or hosted deployment), set the `MUSE_BUCKET`
environment variable:

```bash
export MUSE_BUCKET=$USER-muse
```

Run `muse --help` for detailed usage.
