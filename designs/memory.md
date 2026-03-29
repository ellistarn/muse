# Memory

The muse keeps everything it observes. Memories are the primary artifact — a growing collection of
the owner's reasoning, knowledge, and voice, indexed by thematic labels. muse.md is the identity:
a small, stable document that captures who the owner is, not everything they know. Recall assembles
context-appropriate prompts at query time from the identity and relevant memories.

## Memories

Memories store two kinds of signal:

**Identity** — how the owner thinks. Patterns, heuristics, and mental models that transfer across
domains. Identity is what makes the muse behave like the owner regardless of topic.

**Knowledge** — what the owner knows, believes, and has decided. The muse needs this as fact about
what the owner holds, not as a pattern to re-derive. If the muse only has the reasoning, it might
arrive at a different conclusion than the owner actually holds.

Knowledge and identity are often two views of the same moment. A memory about etcd write
amplification invalidating designs at scale is knowledge (a domain-specific constraint). It's also an
instance of "check whether the substrate constraint invalidates the design before engaging with the
design" — a reasoning pattern. Both are worth storing. They serve different retrieval needs.
Multi-labeling captures this — the memory lives at the intersection of a domain label and a
reasoning-pattern label.

Not everything the owner knows qualifies. The filter: does this knowledge make the muse behave more
like the owner, or just more informed? The muse doesn't need the etcd default storage limit — it
needs to know that the owner treats etcd write pressure as a first-class design constraint. The fact
is retrievable from anywhere; the salience is what's specific to this person.

Knowledge includes outcomes — what was built, decided, shipped, influenced, and fixed. Conversations
are the work artifact. A design review is evidence of technical judgment. A Slack thread where the
owner redirects a team is evidence of leadership. The muse sees outcomes because the conversations
contain them.

A memory is the atomic unit. Each memory stores:

- **Text**: the insight, position, or learned lesson
- **Quote**: the owner's actual words when phrasing carries voice signal (optional)
- **Date**: when the conversation occurred
- **Labels**: 1-3 thematic tags naming the pattern or domain
- **Source**: which conversation produced this memory (source, conversation ID, location)

Labels are the index. They name what a memory reflects — "root cause over symptom fixing,"
"cost-asymmetry as dispatch heuristic," "kubernetes-ownership-scope." A memory belongs to multiple
labels when it genuinely operates at the intersection of themes. Labels are the knowledge graph;
multi-label memories are the edges.

Source is the provenance chain. Every memory traces back to a specific moment in a specific
conversation. This enables three levels of retrieval resolution:

- **Identity** — how does this person think? (always loaded)
- **Memory** — what's their position/knowledge on this topic? (retrieved by label)
- **Conversation** — what actually happened? (followed via source reference)

Most questions stop at memories. Fact-finding and evidence — performance reviews, decision
archaeology, "when did I actually say this" — follow through to conversations. The muse navigates
this chain internally based on what the question demands.

Memories grow monotonically. New conversations produce new memories. Old memories persist. Nothing is
deleted. Memories are the primary retrieval artifact. Conversations are the source of truth,
reachable through the source field on each memory.

## Identity

muse.md is the identity: ~2k tokens, always present as a system instruction. It captures how the
owner thinks, not what they know. It is the skeleton that recalled knowledge hangs on.

Contains: central reasoning patterns at sufficient resolution to extrapolate, relational structure
(how patterns connect — "ownership boundaries drive API opinions and naming"), voice register
demonstrated through the owner's actual phrasing, meta-cognitive habits. The ~2k token budget is the
governing constraint — someone with three deeply interconnected patterns needs fewer sections than
someone with eight independent ones.

Does not contain: knowledge. No specific stances, domain positions, organizational models, or
comprehensive topic coverage. Those surface through recall when relevant. The identity tells the muse
how to think. Retrieved knowledge tells it what to think about.

Recomposed when memories change substantially. Between recompositions, stable. Identity composition is
the one step that requires editorial judgment — deciding what's central, how patterns relate, what
voice to demonstrate. It is the most sensitive step in the system and the hardest to get right.

## Recall

Recall is the internal mechanism behind `ask`. The caller asks a question; the muse navigates the
retrieval chain — identity, memories, conversations — based on what the question demands. The
interface stays clean: one tool, the muse handles the rest.

**Classify**: the query is classified into relevant thematic labels. Classification uses the
identity's relational structure to expand beyond literal matches — a question about API design also
surfaces ownership-boundary and naming labels when the identity encodes that these themes are
connected in the owner's thinking. A domain question may surface both knowledge labels ("kubernetes
scaling") and reasoning labels ("substrate constraints first") because the muse's value is not just
what the owner knows but how they'd apply it. Single LLM call, observe-tier model.

**Retrieve**: memories matching the classified labels, ranked by quality (memories with quotes rank
higher — they carry voice) and recency. When the question demands evidence or specifics — outcomes,
decisions, timelines — the muse follows source references from memories to the originating
conversations and grounds its response in that material.

**Accumulate**: within a session, retrieved memories accumulate across turns. New turns add memories
but don't remove previously retrieved ones — topic drift within a conversation usually signals that
topics are connected, not that the earlier context is irrelevant. This is a design belief, not
empirically validated.

When accumulated memories exceed a token ceiling, all memories are re-ranked against the current
conversational context and the lowest-ranked are dropped. Re-ranking is the only option that
maintains coherence as a conversation drifts — dropping by age loses conversational context, dropping
by original rank misses that relevance shifts as topics evolve.

**Assemble**: the system prompt is built from the identity + accumulated memories (and conversation
context when needed). One synthesis step between the owner's words and the output.

## Ingestion

Memories enter through ingestion. Conversations are discovered from sources, and memories are
extracted, filtered, labeled, and stored.

The pipeline guarantees:

- **Quality**: would this memory change how the muse responds to a relevant question? This covers
  reasoning patterns, voice, specific stances, domain expertise, and organizational knowledge. Raw
  facts that don't shape the owner's judgment are out of scope — the salience is what's specific to
  this person, not the fact itself. Memories about model defaults disguised as owner traits are
  rejected.
- **Provenance**: every memory traces to a specific conversation. Content is traceable to observed
  behavior independent of any model context.
- **Labels**: every memory carries 1-3 thematic labels from a normalized vocabulary. Labels are
  assigned in parallel with shared vocabulary for convergence, then normalized to merge synonyms.
- **Incrementality**: only new or changed conversations are processed. Cached artifacts store
  fingerprints; matching fingerprints are skipped.

Ingestion populates memories. It does not produce the identity.

## Decisions

### Why label-based retrieval instead of embeddings?

Labels are human-readable, debuggable, and work with any LLM provider. When retrieval returns wrong
memories, you can inspect which labels were classified and why. Empirically, embedding space between
memories was too uniform for useful discrimination (median cosine distance 0.92). Labels require no
additional infrastructure — no embedding model, no vector storage, no provider-specific dependencies.

### Why accumulate memories across turns?

A conversation that starts with API design and drifts into naming usually drifts because the topics
are connected. Dropping earlier memories when new ones arrive loses the connection that makes the
response coherent. This is a design assumption. If sessions routinely produce incoherent
accumulations, the model is wrong.

### Why re-rank at the ceiling instead of dropping oldest or lowest?

Dropping oldest loses conversational coherence — the earliest memories often set the frame. Dropping
by original rank misses that relevance shifts as the conversation evolves. Re-ranking against the
current context is the only option that adapts.

### Why store knowledge alongside identity?

A compressed document couldn't afford domain knowledge — 6k tokens barely covers reasoning patterns.
Memories have no budget constraint. Knowledge is stored at full fidelity and surfaced only when
relevant, so it doesn't compete with identity for the same attention budget.

Knowledge and reasoning are often two views of the same moment. A memory that "etcd write
amplification invalidates designs at scale" is both a domain fact and an instance of "check substrate
constraints before engaging with the design." Multi-labeling preserves both views. Storing only the
reasoning pattern means the muse might not flag etcd specifically when it matters. Storing only the
knowledge means the muse won't make the analogous move for a different substrate. Both are needed.

The boundary: does this knowledge make the muse behave more like the owner, or just more informed?
The muse is a projection of the owner's judgment, not an encyclopedia of their domain.

## Deferred

### Identity composition

How the identity is produced from memories — the editorial judgments about centrality, relational
structure, and voice demonstration. This is the hardest problem in the system and deserves its own
design.

### Embedding-augmented retrieval

If label-based classification proves too coarse — relevant memories exist but aren't retrieved
because they don't share a label with the query — embeddings could augment label retrieval.
**Revisit when:** observed recall failures are traceable to label granularity.

### Evaluation framework

No systematic measurement of whether changes improve output quality. **Revisit when:** the
architecture is stable enough that the bottleneck is quality tuning, not structural issues.

### Feedback from muse interactions

Muse interactions produce correction signals that could feed back as memories. Risk: feedback loops
amplify errors. **Revisit when:** the muse is well-calibrated enough that corrections are signal.

### Memory confidence

A settled position ("Karpenter owns k8s integrations") is different from an exploratory one ("I
suspect we'll need 3-4 systems"). The memory text carries this signal in its language, but a
structured field would make it retrievable — "give me settled positions on X" versus "what are they
still working through." **Revisit when:** the muse needs to distinguish conviction levels
programmatically rather than inferring from text.

### Memory decay

Old memories from abandoned thinking patterns persist, diluting the label vocabulary and potentially
surfacing stale material. Recency weighting at retrieval mitigates but may not suffice. **Revisit
when:** memory volume causes retrieval quality degradation.
