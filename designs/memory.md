# Memory

The muse keeps everything it observes. The observation corpus is the primary artifact — a growing
collection of the owner's reasoning, knowledge, and voice, indexed by thematic labels. muse.md is
the identity core: a small, stable document that captures who the owner is, not everything they know.
Recall assembles context-appropriate prompts at query time from the identity core and relevant
observations.

## Corpus

The corpus stores two kinds of signal:

**Identity** — how the owner thinks. Reasoning patterns, awareness (self-model and audience-model),
voice. These transfer across domains. "Trace to root cause before engaging with the fix" applies
whether the domain is Kubernetes, API design, or organizational process. Identity is what makes the
muse behave like the owner regardless of topic.

**Knowledge** — what the owner knows, believes, and has decided. Positions on specific topics,
domain expertise that shapes judgment, mental models of their ecosystem, learned lessons from
failures. "Karpenter should own every customer-facing integration with the Kubernetes API" is
knowledge — a specific position produced by reasoning. The muse needs it as a fact about what the
owner believes, not as a pattern to re-derive. If the muse only has the reasoning, it might arrive
at a different conclusion than the owner actually holds.

Knowledge and identity are often two views of the same moment. An observation about etcd write
amplification invalidating designs at scale is knowledge (a domain-specific constraint). It's also an
instance of "check whether the substrate constraint invalidates the design before engaging with the
design" — a reasoning pattern. Both are worth storing. They serve different retrieval needs: the
reasoning pattern surfaces for structural questions ("how should I evaluate this design?"), the
knowledge surfaces for domain questions ("what should I watch out for at Kubernetes scale?").
Multi-labeling captures this — the observation lives at the intersection of a domain label and a
reasoning-pattern label.

Not everything the owner knows qualifies. The filter: does this knowledge make the muse behave more
like the owner, or just more informed? Raw technical facts the owner happens to know but that don't
shape their judgment belong in a reference system, not the muse. The muse doesn't need the etcd
default storage limit — it needs to know that the owner treats etcd write pressure as a first-class
design constraint. The fact is retrievable from anywhere; the salience is what's specific to this
person.

Knowledge that qualifies: positions the owner has taken, domain expertise that shapes how they
evaluate tradeoffs, mental models of their ecosystem (who owns what, what's competing with what),
organizational knowledge (how their team works, institutional dynamics they navigate, where bandwidth
is scarce). Organizational knowledge is high-leverage — it's invisible to everyone outside the room
and changes recommendations substantially.

An observation is the atomic unit. Each observation stores:

- **Text**: the insight, position, or learned lesson
- **Quote**: the owner's actual words when phrasing carries voice signal (optional)
- **Date**: when the conversation occurred
- **Labels**: 1-3 thematic tags naming the pattern or domain

Labels are the index. They name what an observation reflects — "root cause over symptom fixing,"
"cost-asymmetry as dispatch heuristic," "kubernetes-ownership-scope." An observation belongs to
multiple labels when it genuinely operates at the intersection of themes. Labels are the knowledge
graph; multi-label observations are the edges.

The corpus grows monotonically. New conversations produce new observations. Old observations persist.
Nothing is deleted. The corpus is the source of truth. Everything else — the identity core,
query-time assembly — is a view over the corpus.

## Identity Core

muse.md is the identity core: ~2k tokens, always present as a system instruction. It captures
identity — how the owner thinks, not what they know. It is the skeleton that recalled knowledge
hangs on.

Contains: central reasoning patterns at sufficient resolution to extrapolate, relational structure
(how patterns connect — "ownership boundaries drive API opinions and naming"), voice register
demonstrated through the owner's actual phrasing, meta-cognitive habits. The ~2k token budget is the
governing constraint — someone with three deeply interconnected patterns needs fewer sections than
someone with eight independent ones.

Does not contain: knowledge. No specific stances, domain positions, organizational models, or
comprehensive topic coverage. Those live in the corpus and surface through recall when relevant. The
identity core tells the muse how to think. Retrieved knowledge tells it what to think about.

Recomposed when the corpus changes substantially. Between recompositions, stable. Identity core
composition is the one step that requires editorial judgment — deciding what's central, how patterns
relate, what voice to demonstrate. It is the most sensitive step in the system and the hardest to get
right.

## Recall

When asked a question, the muse assembles a context-appropriate prompt from the identity core and
relevant observations — both reasoning patterns and domain knowledge.

**Classify**: the query is classified into relevant thematic labels. Classification uses the identity
core's relational structure to expand beyond literal matches — a question about API design also
surfaces ownership-boundary and naming labels when the identity core encodes that these themes are
connected in the owner's thinking. A domain question may surface both knowledge labels ("kubernetes
scaling") and reasoning labels ("substrate constraints first") because the muse's value is not just
what the owner knows but how they'd apply it. Single LLM call, observe-tier model.

**Retrieve**: observations matching the classified labels, ranked by quality (observations with
quotes rank higher — they carry voice) and recency.

**Accumulate**: within a session, retrieved observations accumulate across turns. New turns add
observations but don't remove previously retrieved ones — topic drift within a conversation usually
signals that topics are connected, not that the earlier context is irrelevant. This is a design
belief, not empirically validated.

When accumulated observations exceed a token ceiling, all observations are re-ranked against the
current conversational context and the lowest-ranked are dropped. Re-ranking is the only option that
maintains coherence as a conversation drifts — dropping by age loses conversational context, dropping
by original rank misses that relevance shifts as topics evolve.

**Assemble**: the system prompt is built from the identity core + accumulated observations. One
synthesis step between the owner's words and the output.

## Ingestion

Observations enter the corpus through ingestion. Conversations are discovered from sources, and
observations are extracted, filtered, labeled, and stored.

The pipeline guarantees:

- **Quality**: would this observation change how the muse responds to a relevant question? This
  covers reasoning patterns, voice, specific stances, domain expertise, and organizational
  knowledge. Raw facts that don't shape the owner's judgment are out of scope — the salience is
  what's specific to this person, not the fact itself. Observations about model defaults disguised
  as owner traits are rejected.
- **Provenance**: every observation traces to a specific conversation. Content is traceable to
  observed behavior independent of any model context.
- **Labels**: every observation carries 1-3 thematic labels from a normalized vocabulary. Labels are
  assigned in parallel with shared vocabulary for convergence, then normalized to merge synonyms.
- **Incrementality**: only new or changed conversations are processed. Cached artifacts store
  fingerprints; matching fingerprints are skipped.

Ingestion populates the corpus. It does not produce the identity core.

## Decisions

### Why a corpus instead of a document?

A document has a fixed token budget. Compressing a person into a fixed-size document forces every
observation to compete for the same attention budget — voice flattens because synthesis replaces
concrete language with abstraction, natural weighting disappears because compression equalizes what
should be uneven, and rare-but-defining patterns get squeezed out. A corpus has no budget. The
constraint moves to query time, where it's applied selectively — only relevant observations compete
for attention, in context of the actual question.

### Why label-based retrieval instead of embeddings?

Labels are human-readable, debuggable, and work with any LLM provider. When retrieval returns wrong
observations, you can inspect which labels were classified and why. Empirically, embedding space
between observations was too uniform for useful discrimination (median cosine distance 0.92). Labels
require no additional infrastructure — no embedding model, no vector storage, no provider-specific
dependencies.

### Why accumulate observations across turns?

A conversation that starts with API design and drifts into naming usually drifts because the topics
are connected. Dropping earlier observations when new ones arrive loses the connection that makes the
response coherent. This is a design assumption. If sessions routinely produce incoherent
accumulations, the model is wrong.

### Why re-rank at the ceiling instead of dropping oldest or lowest?

Dropping oldest loses conversational coherence — the earliest observations often set the frame.
Dropping by original rank misses that relevance shifts as the conversation evolves. Re-ranking
against the current context is the only option that adapts.

### Why multi-label observations?

An observation about "naming that implies wrong ownership boundaries" genuinely operates in both
domains. Single-labeling forces a choice that breaks the connection. Multi-labeling preserves it —
the observation appears when either topic is recalled. Labels are edges. Multi-labeling is what makes
the corpus a graph rather than a list.

### Why separate the identity core from the corpus?

Different update cadences — the core is stable, the corpus grows continuously. Different consumption
— the core is always loaded, the corpus is retrieved selectively. Different composition — the core
requires editorial judgment, corpus observations are stored as-is. The core captures identity
(reasoning, awareness, voice). The corpus captures identity and knowledge. The core tells the muse
how to think; retrieved knowledge tells it what to think about.

### Why store knowledge alongside identity?

A compressed document couldn't afford domain knowledge — 6k tokens barely covers reasoning patterns.
A corpus has no budget constraint. Knowledge is stored at full fidelity and surfaced only when
relevant, so it doesn't compete with identity for the same attention budget.

Knowledge and reasoning are often two views of the same moment. The observation "etcd write
amplification invalidates designs at scale" is both a domain fact and an instance of "check substrate
constraints before engaging with the design." Multi-labeling preserves both views. Storing only the
reasoning pattern means the muse might not flag etcd specifically when it matters. Storing only the
knowledge means the muse won't make the analogous move for a different substrate. Both are needed.

The boundary: does this knowledge make the muse behave more like the owner, or just more informed?
The muse is a projection of the owner's judgment, not an encyclopedia of their domain.

## Deferred

### Identity core composition

How the identity core is produced from the corpus — the editorial judgments about centrality,
relational structure, and voice demonstration. This is the hardest problem in the system and
deserves its own design.

### Embedding-augmented retrieval

If label-based classification proves too coarse — relevant observations exist but aren't retrieved
because they don't share a label with the query — embeddings could augment label retrieval.
**Revisit when:** observed recall failures are traceable to label granularity.

### Evaluation framework

No systematic measurement of whether changes improve output quality. **Revisit when:** the
architecture is stable enough that the bottleneck is quality tuning, not structural issues.

### Feedback from muse interactions

Muse interactions produce correction signals that could feed back into the corpus. Risk: feedback
loops amplify errors. **Revisit when:** the muse is well-calibrated enough that corrections are
signal.

### Observation decay

Old observations from abandoned thinking patterns persist, diluting the label vocabulary and
potentially surfacing stale material. Recency weighting at retrieval mitigates but may not suffice.
**Revisit when:** corpus size causes retrieval quality degradation.
