# Run Log

## Problem

API cost and usage data is ephemeral — printed to stderr and lost. No way to answer "how much
did I spend this week" or "which command costs the most."

## Design

Append-only JSONL log at `~/.muse/runs.jsonl`. Each command that calls an LLM writes one record
after completion. `muse usage` reads the log and prints summaries.

### Record

```jsonl
{"ts":"2026-03-17T14:30:00Z","command":"distill","in_tokens":50000,"out_tokens":5000,"cost":1.20,"discovered":5,"observed":5,"pruned":42}
```

| Field | Type | Description |
|---|---|---|
| ts | string | UTC timestamp |
| command | string | `distill`, `ask` |
| in_tokens | int | Input tokens consumed |
| out_tokens | int | Output tokens consumed |
| cost | float | Estimated cost in USD |
| discovered | int | Conversations discovered (distill only) |
| observed | int | Conversations observed (distill only) |
| pruned | int | Observations pruned (distill only) |

Distill-specific fields are omitted when zero.

### Logger

`runlog.Logger` wraps a file path. `Log(Record) error` opens in append mode, writes one JSON
line, closes. Creates parent directories on first write. Nil receiver is a no-op.

### `muse usage`

```
muse usage              # last 30 days, grouped by command
muse usage --days 7     # last week
muse usage --detail     # show each run individually
```

```
API usage (last 30 days):

  distill          12 runs   450k in    38k out  $14.20
  ask               8 runs    40k in     6k out  $0.32

  total            20 runs   490k in    44k out  $14.52
```

### Integration

Best-effort write after pipeline completion. Warning on failure, command still succeeds.
