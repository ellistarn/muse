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
export MUSE_BUCKET=$USER-muse

muse push              # upload local agent sessions to storage
muse dream             # distill your soul from memories
muse inspect           # see what your muse learned
```

Once you have a soul, wire up the MCP server so agents can ask your muse questions:

```json
{
  "mcpServers": {
    "<your-name>": {
      "command": "muse",
      "args": ["listen"]
    }
  }
}
```

Run `muse --help` for detailed usage.
