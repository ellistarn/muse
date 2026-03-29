# Grammar

This document specifies the operations muse performs. User-facing commands may compose or rename
them. The document evolves with the system.

The Muse defines what a muse is. Memory defines how it stores and retrieves. This document defines
the interface.

## Types

```
Conversation   — messages between a human and an assistant or peer
Memory         — identity or knowledge: how the owner thinks, what they know, how they sound
Identity       — muse.md; the relational structure of the owner's thinking (~2k tokens)
```

## Operations

```
observe  : (Source, Text) → [Memory]
ingest   : [Conversation] → [Memory]
compose  : [Memory] → Identity
ask      : (Identity, [Memory], Question) → Answer
```

### observe

Extracts memories from text. The source type tells the prompt where to find signal:

| Source | Signal |
|---|---|
| Conversation | Human turns — corrections, pushback, preferences, stances, domain positions |
| PR review | Your comments, what you approved, what you challenged |
| Slack | Arguments, decisions, persuasion between peers |
| Personal notes | Everything (first-person by default) |

The output is always `[Memory]`. Source affects the extraction prompt, not the output type.

Memories capture identity (how the owner thinks) and knowledge (what they know, believe, and have
decided). Multi-labeling preserves the connections between them.

Memories include relational knowledge — "my boss insists on test coverage," "the team resists ORMs"
— because the owner's thinking includes their model of the people and constraints around them.

### ingest

Discovers new conversations, runs observe, filters for quality, assigns thematic labels, normalizes
the label vocabulary, and stores memories. Incremental — only processes what has changed.

### compose

Produces the identity from memories. Editorial judgment — decides what's central, how patterns
relate, what voice to demonstrate. The identity is small (~2k tokens), stable between compositions,
and always loaded as a system instruction.

### ask

Answers a question using the identity and relevant memories. Internally navigates the retrieval
chain — classifies the query into thematic labels, retrieves matching memories, follows through to
source conversations when the question demands evidence. Accumulates memories across turns within a
session.

## Commands

```
muse compose [source...]        # ingest new conversations, compose identity
muse ask <question>             # single-turn ask
muse listen                     # MCP server, multi-turn ask
muse show                       # print identity + memory stats
muse sync <src> <dst>           # copy data between local and S3
```

`compose` combines ingest and compose into a single command. The operations are defined
separately because they have distinct types and can be implemented independently.
