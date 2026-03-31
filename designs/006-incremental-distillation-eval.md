# Incremental Distillation Evaluation

## Problem

We implemented `--method=incremental` but have no evidence it produces a muse of comparable quality
to clustering. Before shipping it as a recommended path, we need a direct comparison.

## Experiment

A/B comparison: generate two muses from the same observation corpus using different methods, then
evaluate both with an LLM judge and human review.

### Arms

- **Muse A**: `muse distill --method=clustering` (current default)
- **Muse B**: `muse distill --method=incremental` (starts from empty, bootstraps from all observations)

Both run against the same stored observations. No new conversations discovered during the test.

### Why bootstrap from empty?

Starting incremental from an existing clustering muse conflates the two methods. Starting from empty
isolates what incremental produces on its own. This is the harder test — real-world usage will
usually fold small batches into an established muse, which is an easier problem.

## Evaluation

### LLM judge

Input: both muses (blinded as "Muse A" / "Muse B") plus ~50 randomly sampled observations for
grounding. The judge does not know which method produced which.

**Dimensions** (each scored 1-5):

- **Coverage** — does the muse capture distinctive patterns present in the observations? Not
  completeness but "would you notice something important missing."
- **Accuracy** — does it avoid overclaiming? Are hedged observations stated with appropriate
  confidence?
- **Density** — information per sentence. Is every sentence load-bearing?
- **Voice** — does it read as the person speaking, not a report about them?
- **Actionability** — if an AI used this muse as context, would it behave differently than without it?

After scoring, the judge produces a **difference report**: specific claims present in one muse but
absent from the other, claims stated with different confidence levels, and any contradictions between
the two.

### Human eval (jamesmt@)

Read both muses. Mark which you'd actually use. Free-form notes on what each got right or wrong.
No structured scoring — your reaction is the ground truth.

### Cost comparison

Total input tokens, output tokens, dollars, and wall-clock time for each method.

## Output

A single report containing:

1. LLM judge scores (table)
2. LLM difference report (specific claims)
3. Human notes
4. Cost/time comparison

## Implementation

### Step 1: Generate muses

```bash
muse distill --method=clustering    # produces Muse A
# copy muse A aside
muse distill --method=incremental   # produces Muse B (bootstraps from empty)
```

The incremental run needs to start from no existing muse to be a fair test. Either clear the stored
muse first or have the eval harness handle this.

### Step 2: Sample observations

Pull ~50 observations at random from storage. These ground the LLM judge — it needs to see what the
observations actually say to evaluate coverage and accuracy.

### Step 3: LLM judge

Single Opus call with a judge prompt. Inputs: sampled observations, Muse A text, Muse B text
(randomized assignment so the judge can't infer method from label). Output: structured scores and
difference report.

### Step 4: Human review

Present both muses to jamesmt@ for reading. Collect free-form notes.

### Step 5: Assemble report

Combine all artifacts into the output report.

## Deferred

- **Longitudinal test**: run incremental daily for a month, compare to periodic clustering rebuilds.
  This experiment only tests cold-start reconstruction.
- **Order sensitivity**: shuffle observation batches and measure variance. Matters for ongoing use
  but not for this first comparison.
- **Multi-judge**: use multiple LLM judge calls to reduce variance. One call is sufficient for a
  first read.
