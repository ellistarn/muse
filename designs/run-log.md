# Run Log

## Problem

API cost and usage data is ephemeral. Each command prints token counts and cost to stderr,
then the data is gone. There's no way to answer "how much did I spend this week" or "which
command costs the most" without manually collecting output.

## Design

An append-only JSONL log at `~/.muse/runs.jsonl`. Each muse command that calls an LLM writes
one record after the pipeline completes. A `muse usage` command reads the log and prints
summaries.

### Record Schema

```jsonl
{
  "ts":         "2026-03-17T14:30:00Z",
  "command":    "distill",
  "input_hash": "abc123",
  "cached":     false,
  "in_tokens":  50000,
  "out_tokens": 5000,
  "cost":       1.20,
  "discovered": 5,
  "observed":   5,
  "pruned":     42
}
```

| Field        | Type    | Description                                         |
|-------------|---------|-----------------------------------------------------|
| ts          | string  | UTC timestamp of the invocation                     |
| command     | string  | Command name (`distill`, `ask`)                     |
| input_hash  | string  | Optional hash for cache-hit detection               |
| cached      | bool    | Whether the result was served from cache            |
| in_tokens   | int     | Total input tokens consumed                         |
| out_tokens  | int     | Total output tokens consumed                        |
| cost        | float   | Estimated cost in USD                               |
| discovered  | int     | Conversations discovered (distill only)             |
| observed    | int     | Conversations observed (distill only)               |
| pruned      | int     | Observations pruned (distill only)                  |

Command-specific fields (`discovered`, `observed`, `pruned`) are omitted when zero via
`omitempty`. Every command writes `ts`, `command`, `in_tokens`, `out_tokens`, and `cost`.

### Logger

The `runlog.Logger` type wraps a file path and exposes a single `Log(Record) error` method
that opens the file in append mode, writes one JSON line, and closes. The logger creates
parent directories on first write.

A nil `*Logger` is safe to call — `Log` on a nil receiver is a no-op. This avoids nil checks
at every call site. Commands construct the logger via `newRunLog()`, which returns nil if
`$HOME` can't be determined.

### Reading and Summarizing

`Read(path, since)` scans the JSONL file and returns records with timestamps after `since`.
A zero `since` returns all records. A missing file returns nil, nil — not an error.
Malformed lines are skipped silently.

`Summary(records)` groups records by command and returns `CommandSummary` structs (command,
runs, input tokens, output tokens, cost) sorted by cost descending.

### `muse usage` Command

```
muse usage              # last 30 days, grouped by command
muse usage --days 7     # last week
muse usage --detail     # show each run individually
```

Default output groups by command with totals:

```
API usage (last 30 days):

  distill          12 runs   450k in    38k out  $14.20
  ask               8 runs    40k in     6k out  $0.32

  total            20 runs   490k in    44k out  $14.52
```

`--detail` lists every run before the summary:

```
  2026-03-17 14:30  distill       50k in     5k out  $1.20
  2026-03-17 14:35  ask            5k in     0k out  $0.04  (cached)
```

### Integration Points

Each command writes a record after its pipeline completes. The write is best-effort — a
warning is printed to stderr on failure, but the command still succeeds. This keeps usage
tracking out of the critical path.

Commands that write records: `distill` (with discovered/observed/pruned), `ask`.
