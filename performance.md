# Performance

## Batch

Prefer batch submission when latency tolerance allows. Batch APIs offer higher throughput
ceilings and lower per-call cost than equivalent individual calls. Results that feed the
next pipeline stage tolerate batch latency. Results streaming to a user do not.

Batch changes the shape of other concerns. Rate control applies to the submission API, not
per-item. The batch contains only items not satisfied by cache. Outcome reporting arrives
in bulk when the batch completes.

## Saturate

No dependency idles while work is queued for it. Two modes:

External APIs get adaptive rate control — a limiter that discovers the effective throughput
ceiling via feedback, seeded from published limits. Rate limiting a new dependency requires
configuration, not new control logic. The rate limiter is independent of concurrency
controls — a concurrency limit bounds goroutines and memory, a rate limit bounds requests
per second. Both exist and compose orthogonally.

Local resources (filesystem, databases) get parallel I/O. Sequential reads, writes, or
queries against a local resource that could be concurrent leave capacity unused.

## Flow

Results already in memory don't transit storage unless durability requires it. Barriers
exist only where the next stage requires the complete output of the previous stage.
When barrier removal causes stages to overlap, stages sharing a dependency must not starve
each other.

## Order

Under bounded parallelism, the most expensive work dispatches first. The slowest item
starts immediately so it overlaps maximally with everything else. Estimated cost is derived
from data available before dispatch — byte size, item count, message count. No preflight
call to a dependency is made for estimation purposes.

## Cache

Check the cache before calling the dependency. A cache hit skips the call entirely — no
rate budget consumed, no latency added. Cache entries persist across runs — a conversation
whose inputs have not changed since the last run is not reprocessed.

Cache fingerprints are stable against irrelevant changes. A fingerprint that includes
mutable state not material to the cached result causes spurious invalidation — the cache
rebuilds work that didn't need rebuilding.

## Feedback

Every dependency call reports its outcome — success, throttled, or error — back to the
rate limiter. Rate control converges to the dependency's effective throughput ceiling.
Throttle signals reduce throughput faster than success signals increase it. Errors do not
change the rate.

Every dependency call that consumed rate budget reports its outcome. Unreported outcomes
degrade limiter accuracy over time.

---

## Compose

| Principle | Violation |
|---|---|
| Batch | All LLM calls (observe, label, summarize) are individual requests. Hundreds of independent calls where results feed the next stage — batch-eligible. |
| Batch | S3 `DeletePrefix` deletes objects one at a time. S3 supports `DeleteObjects` (up to 1000 keys per call). |
| Saturate | Upload writes `PutConversation` sequentially. S3 PUTs at ~50-100ms each, no parallelism. |
| Saturate | `storage.Sync` is fully sequential — one GET + one PUT per conversation in series. |
| Flow | Upload waits for all sources before the pipeline starts. Local sources (milliseconds) idle while API sources (minutes) sync. |
| Flow | Observe→Label, Summarize→Compose are hard barriers. A conversation that finishes observe early waits for the entire stage before labeling begins. |
| Flow | `runGroup` re-reads all label artifacts from store. This data was already in memory during `runLabel`. |
| Order | Upload source ordering is arbitrary. API sources (GitHub, Slack) may start after local sources. |

### Gaps

**Upload→Observe barrier.** Removing requires observe to accept work incrementally. Revisit
when profile data shows observe goroutines idle-waiting for upload on runs with API sources.

**Batch API integration.** Anthropic Message Batches API fits observe/label/summarize. Revisit
after adaptive rate control is in place.

**Inter-stage pipelining.** Revisit when rate limiter sustains >80% utilization and stage idle
time exceeds 20% of wall-clock time.
