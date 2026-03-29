# The Muse

A muse is a distillation of how its owner thinks — their reasoning, awareness, and voice. It gives
models a specific point of view to work from rather than reasoning from general training alone. The
muse is derived from conversations — with other people, with agents, across any medium where the
owner is reacting, correcting, and choosing. A person's reasoning is most legible when they push back
on something, not when they present a finished position.

As an artifact, the muse is text that occupies a context window, competes for attention, and steers
generation — whether loaded as a static document or assembled at query time from stored material.
Every constraint that follows — specificity, faithfulness — is a consequence of this form.

A point of view is what makes one person's judgment different from another's. Models are capable but
general. A muse makes them specific, amplifying the owner's ability to steer. A muse that stops
evolving with its owner stops representing them.

## What makes a muse work

### Reasoning

Reasoning is how a person thinks — the patterns, heuristics, and mental models that shape their
decisions. It operates above any particular situation, which is why it transfers to situations the
person hasn't encountered.

A muse captures reasoning because reasoning gives models the ability to adapt. A conclusion alone is
brittle — without the reasoning that generated it, there's no way to tell when context has changed
enough that it no longer applies. But reasoning alone is ungrounded — the muse might re-derive a
position the owner already holds and arrive somewhere different. When both are present, reasoning
explains why a position is held and signals when it should be revisited.

This creates two distinct operations. When the owner has taken a position on something, the muse
should recall it and be able to explain the reasoning behind it. When the owner hasn't, the muse
reasons from patterns and arrives at something the owner would recognize. Knowledge — positions,
domain expertise, organizational context — tells the muse which operation applies. Reasoning without
knowledge produces plausible advice disconnected from the owner's actual commitments. Knowledge
without reasoning produces brittle positions that can't adapt.

### Awareness

Awareness is theory of mind directed in both directions. Inward: a person's model of their own
knowledge, gaps, and confidence. Outward: their model of who they're talking to and what that person
needs.

Awareness is what makes reasoning situational. Without it, the same reasoning produces the same
output regardless of context. Awareness selects, calibrates, and modulates — which reasoning to
apply, how much confidence to attach, what to foreground for this audience. The failure mode is
someone who gives the right answer to the wrong question — technically sound, completely misapplied.

A muse captures awareness because reasoning alone produces generic advice. Reasoning and awareness
together produce judgment — the capacity to think well and know where and how to apply that
thinking.

### Voice

Voice is how a person uses language — their register, phrasing, how they express conviction and
uncertainty. It carries meaning that content alone doesn't. Hedging signals genuine uncertainty.
Terseness signals something isn't up for debate. An audience uses voice to build a theory of mind
about the speaker. Strip the voice and the audience loses that information, even if the content is
preserved. Judgment without voice sounds like no one in particular.

A muse captures voice because models are an audience too. They learned language from humans, so they
respond to the same signals human audiences do. A muse written in the owner's actual voice puts the
model into a frame where subsequent tokens follow the owner's reasoning trajectory. Voice is how
language has always carried this information.

## What breaks a muse

Every token in a muse competes for the model's attention and earns its place only by faithfully
representing what's specific about the person. Context is finite. A token that isn't pulling its
weight doesn't just waste space — it actively degrades the tokens that are, by diluting what the
model attends to. The muse must be faithful. It grows in accuracy over time, not in length.

### Generic

The muse contains things anyone would say. Generic content wastes the model's attention on
information it already has. A chef's muse that says "uses fresh ingredients" tells you nothing —
every chef says this. The line occupies space without differentiating this chef from any other.

The test: _remove this line from the muse. Does it behave differently?_ If not, the line is dead
weight.

### Shallow

The muse captures real signal at too low a resolution to be useful — on any dimension, whether
reasoning, awareness, or voice. The chef balances dishes by
acidity — reaching for citrus when a plate feels flat, pulling back when brightness overwhelms the
base. But the muse only captures "balances flavors well." When a new dish needs help, the muse knows
the chef balances but not how. It can't apply the pattern to a dish it hasn't seen.

The test: _give the muse a situation the owner has never encountered. Can it extrapolate?_ If it can
only echo back known positions, the resolution is too low.

### Distorted

The muse doesn't faithfully represent the person. The distillation process itself changed the signal.

The chef communicates in short, blunt directives — "more acid," "kill the garnish," "plate's dead."
The extraction process turns this into polished food-writing prose. The content is roughly preserved,
but someone working with this muse would expect an articulate collaborator and get whiplash meeting
the actual chef. This happens because voice can only be demonstrated, not described — it's carried by
the specific words, and any process that restates them degrades the signal. The defense is
preservation: voice must reach the muse as the owner's actual words, not descriptions of how they
sound.

Distortion can also enter when the extraction process encodes frustration with model defaults as
owner traits. The chef's real terseness becomes indistinguishable from a generic instruction to be
less verbose. Manual curation carries the same risk — adding content to correct model behavior rather
than to represent the owner. The defense is provenance: content in the muse must be traceable to
observed behavior independent of any model context, not self-reported identity or corrections applied
to model output.

The test: _show the output to someone who works with the owner daily. Do they recognize the person?_

### Stale

The muse represents who the person was, not who they are. The chef went through a fermentation phase
three years ago — every menu built around koji, miso, long cures. They've since moved to raw
preparation, something they chose deliberately over the style the muse still treats as central. This
is worse than a gap. A gap is silent; a stale muse is confidently wrong, actively steering in a
direction the person has abandoned.

The test: _does this pattern reflect a current tendency or a past phase?_ The question is whether the
person would do this tomorrow, not whether they did it once.
