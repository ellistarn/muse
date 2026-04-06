# Editorial Composition

## Problem

A muse captures how the owner thinks. But the owner doesn't work in isolation. PRs go through
reviewers who care about specific things. The owner benefits from anticipating these perspectives
before engaging, to stop wasting the other person's time on things the owner could have caught
themselves.

Today muse builds one profile: the owner's. What's missing is the editorial board — the handful
of people whose reactions the owner wants to anticipate.

## Solution

### Peer muses

```bash
muse compose github/ellistarn --project karpenter
muse ask --peer github/ellistarn --project karpenter "Review this design"
```

`muse compose github/ellistarn` builds a muse for `ellistarn` from their public GitHub activity.
The GitHub source searches for conversations involving `ellistarn`, assigns `role=user` to their
messages, and runs the standard observe/label/compose pipeline. The result is a muse.md that
captures what `ellistarn` consistently cares about in code reviews.

`--project` narrows the scope to specific repositories. Unqualified names (e.g. `karpenter`) are
resolved by searching GitHub for repos matching that name where the target user has activity — all
matching repos are included. Names with a slash (e.g. `aws/karpenter-provider-aws`) are used as
exact repo matches. The resolved repos are logged so the operator sees what was selected.

Peer muses are stored separately from the owner's muse:

```
~/.muse/
├── muse.md                                    # owner's muse
├── peers/
│   ├── github-ellistarn/
│   │   └── karpenter/
│   │       ├── conversations/
│   │       ├── observations/
│   │       └── versions/{timestamp}/muse.md
│   └── github-jmdeal/
│       └── karpenter/
│           └── ...
```

### Identity pivot

The GitHub source resolves the authenticated user's username and uses it for search
(`involves:{username}`), role assignment (owner messages get `role=user`), and minimum participation
filtering (conversations with fewer than 2 owner messages are discarded).

For peer muses, the target username replaces the authenticated user in all three roles. The
authenticated user's token provides API access — the target doesn't need credentials since the
data is public.

Non-target participants (including the owner) get `role=assistant` with a `[GitHub comment by
@username]` attribution prefix, same as non-owner participants in the owner's muse. The observe
prompt treats `role=user` messages as the subject's thinking and `role=assistant` messages as
context the subject is responding to.

The minimum participation filter is lowered to 1 message for peer muses (vs 2 for the owner). At
threshold 2, Ellis has 38 karpenter conversations. At threshold 1, he has 1310. The quality concern
is real — single-message conversations include "LGTM" and "nit: spacing" alongside substantive
objections — but this is a compose-time problem. The observation pipeline discards low-signal
conversations during the refine step. Whether the refine step actually handles a 34x expansion
without diluting the muse is testable — the first validation should compare muse quality at
threshold 1 vs 2.

### Observe prompt

Peer muses are built entirely from GitHub conversations, which are human-to-human. The human
observe prompt extracts reasoning, awareness, and voice — voice extraction is exclusive to human
conversations (#145). Peer muses get the full observation pipeline without prompt changes.

### Using peer muses

```bash
muse ask --peer github/ellistarn --project karpenter "Review this design"
```

`muse ask --peer` loads the peer's muse.md instead of the owner's. `--project` selects the
project-scoped muse. Sessions persist per peer, so follow-up questions work:

```bash
muse ask --peer github/ellistarn --project karpenter "Tell me more about point 3"
```

## Decisions

### Why public data only?

Peer muses are built from conversations the authenticated user can already see. The muse automates
the reading, not the access. No credentials from the target person are needed.

### Why separate storage?

The owner's muse is built from conversations where the owner's messages are the signal. A peer's
muse is built from conversations where the peer's messages are the signal. Mixing them produces a
muse that represents neither person.

### Why project scoping?

A person reviews differently depending on context. A muse built from all of someone's GitHub
activity averages across contexts, diluting the signal. Project scoping captures how the person
thinks about a specific codebase. It also bounds data volume — a prolific reviewer with thousands
of conversations produces a noisy muse without scoping.

### Why reuse the human observe prompt?

The reviewer signal (pushback, positions defended, patterns flagged) is what the human observe
prompt already extracts. A reviewer-specific prompt would add code-level patterns on top.
Worth trying as a refinement after the base case works.

## Deferred

### Slack peer muses

The Slack source exists and the identity pivot generalizes. The operator would provide the target's
email, resolve to a user ID via `users.lookupByEmail`, and pivot identity the same way as GitHub.
Channel-level project scoping needs design work — substring matching on channel names is too coarse.
**Revisit when:** GitHub peer muses are validated.

### Project muse

Individual peer muses can be composed into a project muse that captures the project's review
culture. Observation-level merge (concatenating all reviewers' observations into one pipeline)
fails — multiple reviewers produce a fragmented label vocabulary that the theme stage can't
consolidate. Muse-level composition works: feed the individual muses to the compose LLM and
synthesize by topic, noting consensus and divergence.

None of this is a substitute for reading the code or talking to the reviewers. The individual
muses and the project muse are cheaply generated approximations. But for a developer with
software industry experience and not much Karpenter experience, they surface interesting things
— shared values the team enforces implicitly, divergences between reviewers that aren't
documented anywhere, and patterns that would take months of PR review to absorb on your own.

```bash
muse ask --peer github/karpenter-team --project karpenter "What would the team flag?"
```

### Multi-peer review

Multiple `--peer` flags to assemble perspectives from several reviewers in one command. Each peer's
muse would weigh in independently. **Revisit when:** single-peer ask is validated.

### Compose prompt for peer muses

The compose prompt says "capture how they think." A peer-specific prompt would say "capture what
this person consistently flags, what triggers their engagement, and what they let pass without
comment. Organize around review patterns and triggers, not general reasoning." These are different
extraction targets — the owner is the protagonist of their conversations, a peer is a reactor. The
current prompt produces usable peer muses because the observation pipeline extracts the right raw
signal. **Revisit when:** peer muses are compared against actual reviews and the structural gap
(reasoning-organized vs trigger-organized) is measured.

### Validation

How does the owner know a peer muse is good? For their own muse, they read it and it resonates or
doesn't. For a peer muse, the owner's model of the peer is exactly what's insufficient — that's
why they're building the muse. A calibration mechanism: run the peer muse against the last N PRs
the peer actually reviewed and compare the muse's predictions to what the peer said. **Revisit
when:** peer muses are in regular use and quality feedback is needed.

### Cross-muse discussion

Two peer muses discussing a design with each other. The owner asks a question, each peer responds,
and the owner sees where they agree and disagree. **Revisit when:** multi-peer mode exists.
