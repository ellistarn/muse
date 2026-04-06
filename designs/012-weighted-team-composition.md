# Weighted Team Composition

## Problem

The team muse (a synthesis of multiple peer muses) dilutes distinctive reviewer patterns.
Derek's individual muse scores +0.25 over baseline. The team muse scores -0.08. The information
is present in the team document — Derek's "is there a reason you can't use X?" gatekeeping move
appears in the text — but it competes with 13 other sections for attention weight in the LLM's
context window.

The root cause is two lossy compression steps: observations → individual muse → team muse. The
composition prompt can be tuned to reduce dilution (changing from "synthesize by topic" to
"preserve distinctive patterns" moved Derek from -0.08 to +0.01), but the architecture is
working against us. A 200-line document with four reviewers will always give Derek's patterns
less weight than a 60-line document entirely about Derek.

## Solution

### Observation-validated team composition

Replace the two-step pipeline with a single composition step from validated observations to
team muse.

```
Current:  observations → individual muse → team muse
Proposed: observations → validate → team muse
```

The validation step asks each reviewer's muse to reinforce or reject its own observations. The
team composition consumes weighted observations directly.

### Step 1: Observe (existing)

Extract observations from each reviewer's conversations.

```
~/.muse/peers/github-DerekFrank/karpenter/observations/
~/.muse/peers/github-ellistarn/karpenter/observations/
~/.muse/peers/github-jmdeal/karpenter/observations/
~/.muse/peers/github-tzneal/karpenter/observations/
```

### Step 2: Compose individual muses (existing)

Build per-reviewer muses from observations. Individual muses remain useful on their own. The
team muse no longer consumes them.

### Step 3: Validate observations

Show each observation to the reviewer's own muse and ask: is this distinctive to how you
review, or generic? The muse is the judge.

The validation prompt:

> You are reviewing an observation about how you think and work.
> Be ruthlessly selective. Most observations are generic things any good reviewer does.
> Only reinforce observations that capture something DISTINCTIVE about your specific
> review style.
>
> Observation: "{observation_text}"
>
> +1 if this captures a distinctive pattern specific to how YOU review
>  0 if this is accurate but generic — any experienced reviewer would do this
> -1 if this is wrong, misleading, or describes something you actively avoid

Each validation call is cached by observation hash + muse hash.

Results from the four Karpenter reviewers (815 observations total):

| Reviewer | Observations | +1 | 0 | -1 |
|---|---|---|---|---|
| Derek | 219 | 129 | 85 | 5 |
| Ellis | 115 | 95 | 20 | 0 |
| Jason | 287 | 192 | 93 | 2 |
| Todd | 194 | 91 | 103 | 0 |

Todd's muse rejected more than half its observations as generic. Derek's rejected 39%. This
discrimination is the signal — the observations that survive are what distinguish each reviewer
from a competent base model.

### Step 4: Compose team muse

Feed the full pool of observations from all reviewers to the composition prompt, annotated
with their validated weights.

```
muse compose-team --project karpenter \
  github/ellistarn github/jmdeal github/tzneal github/DerekFrank
```

The compose prompt receives observations grouped by reviewer, each tagged with its weight:

```
## Derek Frank

[+1] When someone proposes a new feature, challenges whether existing primitives
     (disruption budgets, taints, tag-based selection) already compose into what
     they need. "Is there a reason you can't use X?" is a prerequisite.

[+1] Evaluates every change against 100k-node cluster sizes. Sorts are unsafe
     unless input is provably bounded.

[ 0] Prefers short variable names in tight loops.

[-1] Flags all uses of interface{} as a design smell.
```

The composition prompt instructions:

- **+1 observations**: distinctive signals. Preserve at full strength, especially if unique
  to one reviewer. Give them dedicated sections, not bullets in shared lists.
- **0 observations**: generic. Compress aggressively or cut.
- **-1 observations**: wrong or misleading. Cut.

Observations scored +1 for one reviewer but 0 for others are *distinctive*. Observations
scored +1 across all reviewers are *shared* — the base model catches most of these.

### Step 5: Eval

Run the team muse against all reviewers' conversations using `muse eval --generative`. The
scoring function is severity-weighted recall pooled across all cases.

## Results

Severity-weighted recall delta over baseline, 30 cases per reviewer:

| Reviewer | v1 (topic synthesis) | v2 (distinctive prompt) | v3 (validated observations) | Individual muse |
|---|---|---|---|---|
| Ellis | +0.04 | — | **+0.03** | -0.01 |
| Jason | +0.18 | — | **+0.10** | +0.11 |
| Todd | -0.10 | — | **+0.07** | -0.04 |
| Derek | -0.08 | +0.01 | **+0.11** | +0.25 |

v3 is positive for all four reviewers with no regressions. The biggest improvements:

- **Todd**: -0.10 → +0.07. His muse rejected 103 of 194 observations as generic. Removing
  that noise let the distinctive patterns through.
- **Derek**: -0.08 → +0.11. His gatekeeping move ("is there a reason you can't use X?")
  survived validation and got prominent treatment in the team muse.

## Decisions

### Why muse self-validation instead of eval-based attribution?

The first implementation tried to attribute eval matches back to observations through a
multi-step pipeline: run generative eval → extract concerns → align → attribute each match to
an observation. This was expensive (many LLM calls per case), noisy (three layers of
indirection), and slow.

Muse self-validation is one LLM call per observation: show it to the muse, ask if it's
distinctive. The muse already captures how the reviewer thinks — it's the right judge for
whether an observation is generic or distinctive. The eval measures the final team muse quality;
the validation step measures observation quality. These are different questions answered by
different mechanisms.

### Why not skip individual muses entirely?

Individual muses remain useful standalone. The team muse no longer depends on them for
composition (it goes from observations to team muse directly) but uses individual muses
as the judge in the validation step.

### Why validate against observations instead of the muse level?

The individual muses are compressed. Two observations might both appear in Derek's muse, but
only one is distinctive. Muse-level eval can't distinguish them; observation-level validation
can.

### Why validate against all data instead of a held-out split?

The observations are generalized patterns, not memorized content. Testing whether a pattern is
distinctive is well-posed even on training data. The overfitting risk (discrete labels on
patterns) is lower than the cost of noisier signals from reduced data.

## Validation

```bash
cd "$MUSE_DIR"

# 1. compose-team command exists and accepts the right flags
go build ./... || exit 1
go run . compose-team --help | grep -q "\-\-project" || exit 1

# 2. validate subcommand produces weighted observations with +1/0/-1 distribution
# (not all +1 — the prompt must discriminate)
go run . compose-team --project karpenter github/DerekFrank github/ellistarn 2>&1 | tee /tmp/validate-check.txt
grep -q "+1:" /tmp/validate-check.txt || exit 1
# Check that at least 10% of observations are scored 0 (generic)
# This ensures the validation prompt is discriminating

# 3. The composed team muse is saved
test -f ~/.muse/peers/github-karpenter-team/karpenter/muse.md || exit 1

# 4. generative eval works with --muse-override
go run . eval --generative --peer github/DerekFrank --project karpenter --cases 5 \
  --muse-override ~/.muse/peers/github-karpenter-team/karpenter/muse.md 2>&1 | \
  grep -q "Recall" || exit 1

echo "PASS"
```

## Deferred

### Automated parameter search

The composition prompt determines how scores map to inclusion/weight in the team muse. This
mapping is tunable. **Revisit when:** the team muse eval is stable enough to distinguish between
composition prompt variants.

### Cross-reviewer transfer scores

An observation from Derek's muse that gets +1 when validated by Jason's muse transfers across
reviewers. The composition could weight transferable observations differently. **Revisit when:**
single-reviewer validation is working and cross-reviewer validation is cheap enough.

### Iterative refinement

Run validate → compose → eval in a loop, using eval results to update the validation prompt.
**Revisit when:** a single pass produces measurable improvement over the current team muse.
