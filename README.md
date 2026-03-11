# Shade

A shade is a projection of how you think. It absorbs your memories from agent interactions, distills
them into skills, and serves those skills to any agent that asks.

## How it works

**Upload** pulls memories from local agent databases on your machine (OpenCode, Claude Code, Kiro,
etc.) and syncs them to storage. Your shade learns from these memories by dreaming.

**Dream** reads your uploaded memories, focusing on the feedback you give to models: where they get
things wrong, what you correct, what you reinforce. It compresses these patterns into skills that
capture your expertise. Skills are guidance, not information: they teach models how you want things
done without leaking underlying data. Dreaming is lossy by design, keeping what matters and
forgetting what doesn't.

**Listen** starts an MCP server. Agents can connect, ask questions, and get back guidance shaped by
your skills. Sessions are persistent across calls, identified by a caller-provided session ID.

## Usage

```
export SHADE_BUCKET=$USER-shade
export SHADE_MODEL=claude-sonnet-4-20250514

shade upload    # sync memories to storage
shade dream     # distill skills from memories
shade listen    # start the MCP server
```

## Install

```
go install github.com/ellistarn/shade/cmd/shade@latest
```

Then add your shade as an MCP server so agents can ask it questions. For local use, add a stdio
server to your agent's MCP config:

```json
{
  "mcpServers": {
    "shade": {
      "command": "shade",
      "args": ["listen"]
    }
  }
}
```

This exposes a single tool: **ask** (session_id + message). An agent asks a question and gets back
guidance shaped by your skills. For other operations like uploading memories or inspecting skills,
use the shade CLI directly.

The MCP server can also be deployed as a hosted remote server so your shade is available to agents
running anywhere.

## Runtime

Sessions run on opencode backed by S3-compatible storage

```
skills/{name}/SKILL.md      # distilled skills (https://agentskills.io)
memories/{source}/{id}.json # human session history
sessions/{id}.jsonl          # shade session history (append-per-turn)
dream/state.json            # tracks which memories have been dreamed about
```
