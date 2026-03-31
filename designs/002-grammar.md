# Grammar

This document specifies the operations muse performs. User-facing commands may compose or rename them. The document evolves with the system.

## Types

```
Conversation   — messages between a human and an assistant
Observation    — a discrete insight about how the owner thinks, works, or relates to their context
Muse           — a document that models the owner's thinking
```

## Operations

```
observe : (Source, Text) → [Observation]
compose : (Muse, [Observation]) → Muse
ask     : (Muse, Question) → Answer
```

### observe

Extracts observations from text. The source type tells the prompt where to find signal:

| Source | Signal |
|---|---|
| Conversation | Human turns — corrections, pushback, preferences |
| PR review | Your comments, what you approved, what you challenged |
| Personal notes | Everything (first-person by default) |
| Shared doc | Your annotations and feedback |

The output is always `[Observation]`. Source affects the extraction prompt, not the output type.
See 003-sources.md for the source contract — how sources find, fetch, and normalize conversation data.

Observations include relational knowledge — "my boss insists on test coverage," "the team resists
ORMs" — because the owner's thinking includes their model of the people and constraints around them.
No identity model needed. The muse models one person's worldview, and that worldview includes
other people.

### compose

Folds new observations into the existing muse. Multiple strategies exist
(clustering, map-reduce, incremental). See the distillation designs for details.

### ask

Sends a question to the muse. Stateless, one-shot. The muse is loaded as the system prompt.

## Commands

```
muse compose [source...]        # observe new conversations and compose the muse
muse ask <question>             # ask the muse a question
muse show                       # print the muse
muse show --diff                # what changed in the last composition
muse sync <src> <dst>           # copy data between local and S3
```

`compose` combines observe and compose into a single command. The operations are defined
separately because they have distinct types and can be implemented independently.
