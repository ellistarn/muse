# Muse

A muse is the distilled essence of how you think. It absorbs your memories from agent interactions,
distills them into a soul document ([soul.md](https://soul.md)), and approximates your unique thought
processes when asked questions.

## How it works

**Push** pulls memories from local agent databases on your machine (OpenCode, Claude Code, Kiro,
etc.) and pushes them to storage. Your muse learns from these memories by dreaming.

**Dream** reads your uploaded memories, focusing on the feedback you give to models: where they get
things wrong, what you correct, what you reinforce. It reflects on each memory individually, then
compresses the reflections into a single soul document that captures your expertise. The soul is
guidance, not information: it teaches models how you want things done without leaking underlying
data. Dreaming is lossy by design, keeping what matters and forgetting what doesn't.

Each dream snapshots your previous soul before overwriting it, so you have a full history of how
your muse has evolved. Reflections are persisted so you can re-synthesize the soul later with better
models or prompts (`dream --learn`) without re-processing all your memories.

**Inspect** prints your current soul so you can see what your muse knows. Use `inspect --diff` to
get an LLM-generated summary of what changed since the last dream.

**Ask** asks your muse a question and gets back guidance shaped by your soul. Available both as a
CLI command and as an MCP tool (via `listen`). Each call is stateless, a one-shot interaction with
no session history or persistence.

**Listen** starts an MCP server that exposes the **ask** tool so agents can query your muse
programmatically.

## How ask works

When you ask a question, your muse's entire soul is included in the system prompt. The model reads
it and responds with guidance shaped by your patterns. It's a single-shot call with no tool use or
multi-round reasoning — just your soul and the question.

Each call is stateless. Your muse has no memory of previous questions and no conversation history.
It knows what it's learned from dreaming and nothing else. If it doesn't have relevant knowledge, it
says so.

## Usage

Run `muse --help` for detailed usage and `muse <command> --help` for command-specific details.

```
export MUSE_BUCKET=$USER-muse
export MUSE_MODEL=claude-sonnet-4-20250514

muse push              # push memories to storage
muse dream             # distill a soul from memories
muse dream --learn     # re-distill soul from existing reflections
muse inspect           # print the soul
muse inspect --diff    # summarize what changed since the last dream
muse ask "question"    # ask your muse a question
muse listen            # start the MCP server
```

## Install

```
go install github.com/ellistarn/muse/cmd/muse@latest
```

## Getting Started

Muse stores memories and souls in an S3 bucket you own. Muse uses your standard AWS credentials and
region.

```bash
muse push              # upload local agent sessions to S3
muse dream             # distill your soul from memories
muse inspect           # see what your muse learned
```

Once you have a soul, wire up the MCP server so agents can ask your muse questions. Name the server
after whoever's muse it is:

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

For other operations like pushing memories or inspecting the soul, use the muse CLI directly.

The MCP server can also be deployed as a hosted remote server so your muse is available to agents
running anywhere.

## Storage

S3-compatible storage with the following layout:

```
soul.md                                         # distilled soul (https://soul.md)
memories/{source}/{id}.json                     # human session history
dreams/reflections/{source}/{id}.md             # per-memory reflections
dreams/history/{timestamp}/soul.md              # soul snapshots from previous dreams
```
