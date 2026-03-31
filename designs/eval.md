# Eval

The eval measures whether a muse produces better judgment. It scores responses on six dimensions,
blindly, and outputs a profile — not a verdict.

## Blindness

The judge never knows which response has the muse. Responses are randomly assigned to Response A and
Response B. The judge evaluates each independently. The assignment is randomized per question so the
judge can't learn a positional pattern.

## Dimensions

Split into two judge calls to prevent gestalt anchoring — a single judge forming one impression of
quality and varying all scores around it. The split is between what's directly observable in the text
and what requires interpretive judgment about reasoning quality.

### Observable (what the response does)

1. **Positional clarity**: Can you identify what the response recommends? Not "commits more" — the
   position is legible regardless of how firmly it's held.
2. **Completeness**: Addresses what matters and engages with the specific question asked. A thorough
   answer to the wrong question scores low. A response that redirects to a different framing only
   scores high if the reframe earns its weight.
3. **Specificity of mechanism**: Gives you something actionable. "Consider the risks" scores low.
   "The dangerous case is X because Y" scores high. The difference is whether you could start acting
   on the response.

### Epistemic (how well it reasons)

4. **Calibration**: Confidence tracks epistemic state. Firm where warranted, uncertain where
   warranted, and you can tell the difference. A response that commits hard everywhere is poorly
   calibrated. So is one that hedges everything.
5. **Reasoning transparency**: Visible mental models that transfer to novel situations. Not
   assertion-from-authority, not "trust me." The test: could you apply the reasoning to a different
   problem?
6. **Intellectual honesty**: Names costs, acknowledges what it's trading away, surfaces what it
   doesn't know. Presents its recommendation as a tradeoff it's making, not a free lunch.

## Questions

### Universal (~22 fixed)

Domain-agnostic questions across six categories: architecture, tradeoffs, failure recovery, people,
scoping, and meta-reasoning. These test whether the muse improves general judgment regardless of
domain.

At least four questions are tagged as tension pairs — situations where common principles conflict
(e.g., "ship fast" vs "don't create wrong abstractions"). Tension pairs test whether the muse
resolves conflicts coherently or samples from a bag of heuristics.

### Domain (~10 generated)

An LLM reads the muse.md, identifies the owner's domain, and generates:

- 4 in-domain questions (the muse owner's territory)
- 3 adjacent-domain questions (structurally similar fields)
- 3 out-of-domain questions (unrelated fields)

These measure **transferability**: does the muse improvement hold up as questions move away from the
owner's domain? A flat curve means the muse captured reasoning. A steep dropoff means it captured
conclusions.

## Scoring

Each response is scored independently on all six dimensions on a 3-point scale (1-3). Every point is
anchored with a concrete description — no interpolation between defined levels. A 3-point scale
produces more reliable signal than a 5-point scale with phantom intermediate values, and with 30+
questions the granularity recovers in aggregate through reduced measurement error.

The epistemic judge also states which response demonstrates better overall judgment and why. This
pairwise preference captures gestalt signal that individual dimension scores miss.

## Output

A profile: per-dimension averages for base and muse responses, deltas, a transferability breakdown by
question category, and an overall preference count. With verbose mode, full per-case detail.

## Caching

Base and muse responses are cached on disk, keyed on (prompt, model) and (prompt, model, muse_hash)
respectively. Generated domain questions are cached on muse_hash. Judge calls are never cached —
they're cheap and benefit from prompt iteration.
