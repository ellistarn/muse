# Grammar

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
for the full design. Produces both an updated muse and a record of what was forgotten.

### ask

Sends a question to the muse. Stateless, one-shot. The muse is loaded as the system prompt.

### import

Interactive, human-in-the-loop. The muse reads the imported content, cross-references with what it
already knows about the owner, and asks targeted questions:

- "You mentioned the API proposal. What was your reaction to the interface design?"
- "There's a cost/quality tradeoff in this design. Where did you land?"
- "You didn't comment on the testing strategy. Was that agreement or did you not get to it?"

The owner answers. The muse asks a few more questions. The exchange is a conversation, and
conversations are already the input type for observe. Interactive import produces a conversation
about the imported content, and that conversation gets observed like any other.

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
