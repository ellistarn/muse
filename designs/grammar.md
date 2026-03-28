# Grammar

This document specifies the operations muse performs. User-facing commands may compose or rename
them. The document evolves with the system.

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
recall   : (Identity, [Memory], Question) → Answer
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

Memories capture identity (reasoning patterns, awareness, voice) and knowledge (positions, domain
expertise, organizational models, learned lessons, outcomes). A memory about etcd write amplification
is knowledge; the reasoning move "check substrate constraints first" is identity. A design review
that resolved an API ownership question is an outcome. All are extracted. Multi-labeling preserves
the connections.

Memories include relational knowledge — "my boss insists on test coverage," "the team resists ORMs"
— because the owner's thinking includes their model of the people and constraints around them.

### ingest

Discovers new conversations, runs observe, filters for quality, assigns thematic labels, normalizes
the label vocabulary, and stores memories. Incremental — only processes what has changed.

### compose

Produces the identity from memories. Editorial judgment — decides what's central, how patterns
relate, what voice to demonstrate. The identity is small (~2k tokens), stable between compositions,
and always loaded as a system instruction.

### recall

Assembles a context-appropriate prompt from the identity and relevant memories, then responds.
Classifies the query into thematic labels (expanded through the identity's relational structure),
retrieves matching memories — both reasoning patterns and domain knowledge — and accumulates them
across turns within a session.

## Commands

```
muse compose [source...]        # ingest new conversations, compose identity
muse ask <question>             # single-turn recall
muse listen                     # MCP server, multi-turn recall
muse show                       # print identity + memory stats
muse sync <src> <dst>           # copy data between local and S3
```

`compose` combines ingest and compose into a single command. The operations are defined
separately because they have distinct types and can be implemented independently.
