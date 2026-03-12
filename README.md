# Muse

A muse is the distilled essence of how you think. It absorbs your memories from agent interactions,
distills them into skills, and uses those skills to when asked questions.

## How it works

**Push** pulls memories from local agent databases on your machine (OpenCode, Claude Code, Kiro,
etc.) and pushes them to storage. Your muse learns from these memories by dreaming.

**Dream** reads your uploaded memories, focusing on the feedback you give to models: where they get
things wrong, what you correct, what you reinforce. It reflects on each memory individually, then
compresses the reflections into skills that capture your expertise. Skills are guidance, not
information: they teach models how you want things done without leaking underlying data. Dreaming is
lossy by design, keeping what matters and forgetting what doesn't.

Each dream snapshots your previous skills before overwriting them, so you have a full history of how
your muse has evolved. Reflections are persisted so you can re-synthesize skills later with better
models or prompts (`dream --learn`) without re-processing all your memories.

**Inspect** prints your current skills so you can see what your muse knows. Use `inspect --diff` to
get an LLM-generated summary of what changed since the last dream.

**Ask** asks your muse a question and gets back guidance shaped by your skills. Available both as a
CLI command and as an MCP tool (via `listen`). Each call is stateless, a one-shot interaction with
no session history or persistence.

**Listen** starts an MCP server that exposes the **ask** tool so agents can query your muse
programmatically.

## How ask works

When you ask a question, your muse looks through its skills to find what's relevant, reads them, and
responds with guidance shaped by your patterns. It may pull in multiple skills across several rounds
of reasoning, but all of that happens internally. You only see the final answer.

Each call is stateless. Your muse has no memory of previous questions and no conversation history.
It knows what it's learned from dreaming and nothing else. If it doesn't have a relevant skill, it
says so.

## Usage

```
export MUSE_BUCKET=$USER-muse
export MUSE_MODEL=claude-sonnet-4-20250514

muse push              # push memories to storage
muse dream             # distill skills from memories
muse dream --learn     # re-synthesize skills from existing reflections
muse inspect           # print all skills
muse inspect --diff    # summarize what changed since the last dream
muse ask "question"    # ask your muse a question
muse listen            # start the MCP server
```

## Install

```
go install github.com/ellistarn/muse/cmd/muse@latest
```

Then add your muse as an MCP server so agents can ask it questions. For local use, add a stdio
server to your agent's MCP config. Name the server after whoever's muse it is:

```json
{
  "mcpServers": {
    "ellis": {
      "command": "muse",
      "args": ["listen"]
    }
  }
}
```

For other operations like pushing memories or inspecting skills, use the muse CLI directly.

The MCP server can also be deployed as a hosted remote server so your muse is available to agents
running anywhere.

## Storage

S3-compatible storage with the following layout:

```
skills/{name}/SKILL.md                          # distilled skills (https://agentskills.io)
memories/{source}/{id}.json                     # human session history
dreams/reflections/{source}/{id}.md             # per-memory reflections
dreams/history/{timestamp}/skills/{name}/SKILL.md  # skill snapshots from previous dreams
```
