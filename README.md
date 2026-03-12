# Shade

A shade is a projection of how you think. It absorbs your memories from agent interactions, distills
them into skills, and serves those skills to any agent that asks.

## How it works

**Push** pulls memories from local agent databases on your machine (OpenCode, Claude Code, Kiro,
etc.) and pushes them to storage. Your shade learns from these memories by dreaming.

**Dream** reads your uploaded memories, focusing on the feedback you give to models: where they get
things wrong, what you correct, what you reinforce. It reflects on each memory individually, then
compresses the reflections into skills that capture your expertise. Skills are guidance, not
information: they teach models how you want things done without leaking underlying data. Dreaming is
lossy by design, keeping what matters and forgetting what doesn't.

Reflections are persisted so you can re-synthesize skills later with better models or prompts
(`dream --learn`) without re-processing all your memories.

**Listen** starts an MCP server that exposes a single tool: **ask**. An agent sends a question and
gets back guidance shaped by your skills. Each call is stateless, a one-shot interaction with no
session history or persistence.

## How ask works

When an agent asks a question, the shade looks through its skills to find what's relevant, reads
them, and responds with guidance shaped by your patterns. It may pull in multiple skills across
several rounds of reasoning, but all of that happens internally. The agent only sees the final
answer.

Each call is stateless. The shade has no memory of previous questions and no conversation history.
It knows what it's learned from dreaming and nothing else. If it doesn't have a relevant skill, it
says so.

## Usage

```
export SHADE_BUCKET=$USER-shade
export SHADE_MODEL=claude-sonnet-4-20250514

shade push              # push memories to storage
shade dream             # distill skills from memories
shade dream --learn     # re-synthesize skills from existing reflections
shade listen            # start the MCP server
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

For other operations like pushing memories or inspecting skills, use the shade CLI directly.

The MCP server can also be deployed as a hosted remote server so your shade is available to agents
running anywhere.

## Storage

S3-compatible storage with the following layout:

```
skills/{name}/SKILL.md               # distilled skills (https://agentskills.io)
memories/{source}/{id}.json          # human session history
dream/reflections/{source}/{id}.md   # per-memory reflections
```
