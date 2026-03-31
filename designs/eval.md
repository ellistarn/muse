# Eval

The eval measures whether a muse produces better judgment. It scores responses on three dimensions
that correspond to what a muse actually adds — reasoning, voice, and awareness — blindly, and
outputs a profile.

## Blindness

The judge never knows which response has the muse. Responses are randomly assigned to Response A and
Response B. The judge evaluates each independently. The assignment is randomized per question so the
judge can't learn a positional pattern.

## Dimensions

A single judge call scores both responses on three dimensions. These are orthogonal — a response can
nail reasoning but sound like a helpful assistant (reasoning without voice), or have perfect voice
but hedge where it should commit (voice without awareness). The old six-dimension split used
correlated quality metrics that moved in lockstep; the new three correspond to what a muse claims to
add.

### Reasoning

Does the response make specific reasoning moves that reflect particular judgment? The rubric names
the muse's actual moves: tracing symptoms to structural causes, transplanting analogs from other
domains, refusing false spectrums via categorical distinctions, checking whether a constraint should
be accepted before working within it. The judge pattern-matches against a known set rather than
making a subjective "is this distinctive" call.

### Voice

Does the response make structural commitments a generic helpful assistant wouldn't? Not stylistic
markers (short sentences, dry humor) but structural properties: collapsing ambiguity into a position,
reframing questions when the framing is wrong, compressing rather than enumerating. The test: does
the response come from a perspective, not a process?

### Awareness

Does the response demonstrate actionable self-awareness? Not declaring uncertainty but adjusting
behavior based on it. Does it name a bias and adjust for it? Distinguish confidence levels and act
differently at different levels? Surface what it's trading away and engage with the cost? The rubric
scores on demonstrated adjustment, not performative metacognition.

## Preference

A second, independent judge call sees both responses and produces only a pairwise preference plus a
one-sentence rationale. No dimension scores — just "which response demonstrates better judgment?"

The gap between dimension scores and preference is itself a measurement. If preference diverges from
what the dimensions predict, the muse is adding something the three named axes don't capture. If they
converge, the dimensions are sufficient.

## Questions

### Universal (~22 fixed)

Domain-agnostic questions across six categories: architecture, tradeoffs, failure recovery, people,
scoping, and meta-reasoning. These test whether the muse improves general judgment regardless of
domain.

At least four questions are tagged as tension pairs — situations where common principles conflict
(e.g., "ship fast" vs "don't create wrong abstractions"). Tension pairs test whether the muse
resolves conflicts coherently or samples from a bag of heuristics.

### Domain (~10 generated)

An LLM reads the muse.md, identifies the owner's domain, and generates questions at three distance
levels (in-domain, adjacent-domain, out-of-domain). These measure **transferability**: is the muse
delta on domain questions different from universal questions? If they're the same, the muse
transfers. If domain is higher, it captured conclusions rather than reasoning.

## Scoring

Each response is scored independently on all three dimensions on a 5-point scale (1-5). Every point
is anchored with a concrete description. A score of 3 represents a competent response from a strong
general-purpose model. Scores of 4-5 are reserved for responses that demonstrate particular judgment,
distinctive commitment, or genuine self-awareness beyond general competence. This calibration puts the
discrimination range where the actual signal lives.

## Output

A profile: per-dimension averages for base and muse responses, deltas, a transferability comparison
(domain vs universal), and an overall preference count. With verbose mode, full per-case detail.

## Caching

Base and muse responses are cached on disk, keyed on (prompt, model) and (prompt, model, muse_hash)
respectively. Generated domain questions are cached on muse_hash. Judge calls are never cached —
they're cheap and benefit from prompt iteration.
