#!/usr/bin/env bash
#
# Incremental vs Clustering composition comparison.
#
# Creates two sandboxed copies of ~/.muse, runs each composition method
# against its own copy, samples observations, and runs an LLM judge.
#
# Usage:
#   ./eval/incremental-compare.sh
#
# Prerequisites:
#   - ANTHROPIC_API_KEY or AWS credentials configured
#
# Output:
#   eval/results/<timestamp>/
#     muse-clustering.md
#     muse-incremental.md
#     observations-sample.txt
#     judge-report.md
#     cost.txt

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
MUSE_HOME="${HOME}/.muse"
TIMESTAMP="$(date +%Y%m%dT%H%M%S)"
RESULTS_DIR="${SCRIPT_DIR}/results/${TIMESTAMP}"

# Always build from source to ensure we're testing current code
echo "Building muse from source..."
(cd "$PROJECT_DIR" && go build -o "${PROJECT_DIR}/muse" .)
MUSE="${PROJECT_DIR}/muse"

echo "=== Incremental vs Clustering Eval ==="
echo "Results: ${RESULTS_DIR}"
echo ""

mkdir -p "$RESULTS_DIR"

# ── Snapshot current data ────────────────────────────────────────────────
echo "Snapshotting ${MUSE_HOME}..."
SANDBOX_A="${RESULTS_DIR}/sandbox-clustering"
SANDBOX_B="${RESULTS_DIR}/sandbox-incremental"

# Copy observations and conversations only (not muse versions — we want
# each arm to generate fresh). Using rsync for efficiency.
for dir in "$SANDBOX_A" "$SANDBOX_B"; do
    mkdir -p "$dir"
    if [ -d "${MUSE_HOME}/conversations" ]; then
        rsync -a "${MUSE_HOME}/conversations/" "${dir}/conversations/"
    fi
    if [ -d "${MUSE_HOME}/observations" ]; then
        rsync -a "${MUSE_HOME}/observations/" "${dir}/observations/"
    fi
    if [ -d "${MUSE_HOME}/compose" ]; then
        rsync -a "${MUSE_HOME}/compose/" "${dir}/compose/"
    fi
done

# Remove conversations with missing conversation_id (old schema uses session_id)
for dir in "$SANDBOX_A" "$SANDBOX_B"; do
    find "$dir/conversations" -name '*.json' -type f 2>/dev/null | while read -r f; do
        if ! python3 -c "import json,sys; d=json.load(open(sys.argv[1])); assert d.get('conversation_id','')" "$f" 2>/dev/null; then
            echo "  Removing invalid: $(basename "$f")"
            rm "$f"
        fi
    done
done

echo "  Sandbox A (clustering): ${SANDBOX_A}"
echo "  Sandbox B (incremental): ${SANDBOX_B}"
echo ""

# ── Arm A: Clustering ───────────────────────────────────────────────────
echo "--- Arm A: clustering ---"
MUSE_DIR="$SANDBOX_A" "$MUSE" compose --method=clustering 2>&1 | tee "${RESULTS_DIR}/log-clustering.txt"
MUSE_DIR="$SANDBOX_A" "$MUSE" show > "${RESULTS_DIR}/muse-clustering.md" 2>/dev/null || true
echo ""

# ── Arm B: Incremental (from empty) ─────────────────────────────────────
echo "--- Arm B: incremental (bootstrap from empty) ---"
MUSE_DIR="$SANDBOX_B" "$MUSE" compose --method=incremental 2>&1 | tee "${RESULTS_DIR}/log-incremental.txt"
MUSE_DIR="$SANDBOX_B" "$MUSE" show > "${RESULTS_DIR}/muse-incremental.md" 2>/dev/null || true
echo ""

# ── Sample observations ─────────────────────────────────────────────────
echo "Sampling observations..."

find "${SANDBOX_A}/observations" -name '*.md' -type f 2>/dev/null | sort -R | head -50 | while read -r f; do
    echo "---"
    cat "$f"
    echo ""
done > "${RESULTS_DIR}/observations-sample.txt"

OBS_COUNT=$(grep -c '^---$' "${RESULTS_DIR}/observations-sample.txt" || echo 0)
echo "  Sampled ${OBS_COUNT} observations"
echo ""

# ── Cost summary ─────────────────────────────────────────────────────────
{
    echo "=== Cost Comparison ==="
    echo ""
    echo "--- Clustering ---"
    grep -E 'Muse composed|Observed|Processed|tokens' "${RESULTS_DIR}/log-clustering.txt" || true
    echo ""
    echo "--- Incremental ---"
    grep -E 'Muse (composed|updated)|Observed|Processed|tokens' "${RESULTS_DIR}/log-incremental.txt" || true
} > "${RESULTS_DIR}/cost.txt"

cat "${RESULTS_DIR}/cost.txt"
echo ""

# ── LLM Judge ───────────────────────────────────────────────────────────
echo "Running LLM judge..."

# Randomize assignment so judge can't infer method from label
SEED=$RANDOM
case $((SEED % 2)) in
  0) M1=clustering M2=incremental ;;
  1) M1=incremental M2=clustering ;;
esac
LABEL_MAP="Muse A = ${M1}, Muse B = ${M2}"

FILE_A="${RESULTS_DIR}/muse-${M1}.md"
FILE_B="${RESULTS_DIR}/muse-${M2}.md"

JUDGE_QUESTION="You are evaluating two muse documents — profiles that capture how a specific person thinks. You have a sample of the observations they were built from. You do not know which method produced which muse.

Score each muse on these dimensions (1-5 scale, 5 is best):

1. Coverage — does the muse capture distinctive patterns present in the observations? Not completeness, but would you notice something important missing?
2. Accuracy — does it avoid overclaiming? Are hedged observations stated with appropriate confidence?
3. Density — information per sentence. Is every sentence load-bearing, or is there filler?
4. Voice — does it read as the person speaking, not a report about them?
5. Actionability — if an AI used this muse as context, would it behave differently than without it?

Then produce a difference report: specific claims present in one muse but absent from the other, claims stated with different confidence, and any contradictions.

Output format:

### Scores

| Dimension     | Muse A | Muse B |
|---------------|--------|--------|
| Coverage      |        |        |
| Accuracy      |        |        |
| Density       |        |        |
| Voice         |        |        |
| Actionability |        |        |

### Difference Report

[Specific claim-level differences]

### Summary

[1-2 sentence overall comparison]

---

## Observations (sample)

$(cat "${RESULTS_DIR}/observations-sample.txt")

## Muse A

$(cat "$FILE_A")

## Muse B

$(cat "$FILE_B")"

MUSE_DIR="$SANDBOX_A" "$MUSE" ask "$JUDGE_QUESTION" > "${RESULTS_DIR}/judge-report.md" 2>/dev/null

echo ""
echo "=== Judge Report ==="
cat "${RESULTS_DIR}/judge-report.md"

# Record the blinding key
echo "" >> "${RESULTS_DIR}/judge-report.md"
echo "---" >> "${RESULTS_DIR}/judge-report.md"
echo "_Blinding key: ${LABEL_MAP}_" >> "${RESULTS_DIR}/judge-report.md"

echo ""
echo "---"
echo "Blinding key: ${LABEL_MAP}"
echo ""
echo "Results saved to: ${RESULTS_DIR}"
echo ""
echo "Next steps:"
echo "  1. Read both muses side by side:"
echo "     diff ${RESULTS_DIR}/muse-clustering.md ${RESULTS_DIR}/muse-incremental.md"
echo "  2. Add your notes to ${RESULTS_DIR}/human-notes.md"
