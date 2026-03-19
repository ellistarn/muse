# Grammar

This document specifies the operations muse performs. User-facing commands may compose or rename them. The document evolves with the system.

## Types

```
Conversation   — messages between a human and an assistant
Observation    — a discrete insight about how the owner thinks, works, or relates to their context
Muse           — a document that models the owner's thinking
Forgotten      — what was removed or softened during an update, with reasons
```

## Operations

```
observe : (Source, Text) → [Observation]
update  : (Muse, [Observation]) → (Muse, [Forgotten])
ask     : (Muse, Question) → Answer
import  : (Muse, Text) → Conversation → observe → [Observation]
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

Observations include relational knowledge — "my boss insists on test coverage," "the team resists
ORMs" — because the owner's thinking includes their model of the people and constraints around them.
No identity model needed. The muse models one person's worldview, and that worldview includes
other people.

### update

Folds new observations into the existing muse. See [incremental-distillation.md](incremental-distillation.md)
for the full design. Produces two outputs:

1. **muse.md** — the updated muse
2. **forgotten.md** — what was removed or softened, with a reason

Every entry in the forgotten log has a cause: "contradicted by X" or "subsumed by Y." If the log
ever says "hasn't been mentioned recently," the update prompt is wrong. The forgotten log provides
audit (why did the muse stop mentioning X?) and recovery (feed a dropped observation back in).

### ask

Sends a question to the muse. Stateless, one-shot. The muse is loaded as the system prompt.

### import

Some of the strongest signal about how you think is never seen by muse. Voice profiles,
CLAUDE.md preferences, style guides — these are injected as system prompts or agent configuration,
and conversation parsers strip them. Muse sees the assistant's compliant behavior but not the
explicit instructions that caused it. A voice profile that says "never soften feedback so the
message doesn't land" produces terse conversations, but muse can't distinguish "instructed this
style" from "the model happened to respond this way."

`import` makes this signal observable. Interactive, human-in-the-loop. The muse reads the imported
content, cross-references with what it already knows about the owner, and asks targeted questions:

```
$ muse import ~/llm_templates/voice_profile.md

Muse: This profile bans em-dashes, "delve," and other terms you associate
      with AI-generated text. Is this a writing preference, or do you
      actively distrust content that uses them?

You:   Both. If I see em-dashes in a doc, I assume it wasn't reviewed.

Muse: You say "never soften feedback so the message doesn't land." Does
      that apply equally to peers, reports, and leadership?

You:   Peers and leadership yes. Reports I'm gentler with — I want them
       to hear it, not shut down.
```

The exchange is a conversation, and conversations are the input type for observe. Interactive import
produces a conversation about the imported content, and that conversation gets observed like any
other.

```
import : (Muse, Text) → Conversation → observe → [Observation]
```

High-quality observations because the owner states their views directly, guided by targeted
questions, rather than the LLM guessing from ambiguous text.

## Commands

```
muse observe [source...]        # extract observations from new conversations
muse update                     # fold new observations into the muse
muse ask <question>             # ask the muse a question
muse import <file>              # interactive import with muse-guided questions
muse show                       # print the muse
muse show --diff                # what changed in the last update
muse show --forgotten           # what faded in the last update
muse sync <src> <dst>           # copy data between local and S3
```

`observe` and `update` are decoupled. Today's `muse distill` is `muse observe && muse update`.
