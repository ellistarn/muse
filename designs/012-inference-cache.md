# Inference Cache

## Problem

A muse [1] is composed from observations. Observations are extracted from conversations by
sending each conversation (or window of a conversation) to an LLM with the observe prompt.
The observe prompt and the conversations are inputs to the LLM. They do not change between
runs unless the source is modified. The LLM is stochastic, but for the purposes of observation, a cached response is an
acceptable substitute for a fresh one.

Research on observation strategies [2] found that presenting the same conversation differently
to the observe prompt changes what it finds. Stripping assistant text and observing in small
windows produces three to five times more useful observations than sending the full compressed
conversation. Trying multiple presentation strategies per conversation and combining the
results produces even more. Experimenting with these strategies means running the observe
pipeline many times on the same conversations with varying parameters. Without caching, each
run pays full API costs for every inference call. This is avoidable when our inputs have not changed.

## Why observe is the caching target

The pipeline has five stages:

```
conversations → observe → cluster → summarize → compose → muse.md
```

Observe is the expensive stage. It makes one or more LLM calls per conversation, each sending
a system prompt and a formatted conversation window to the inference API. The type signature
is:

```
observe : (model, system_prompt, conversation_window, options) → observations | NONE
```

For a given strategy and conversation, we get a series of LLM calls that all have the same
input. There is a tradeoff between exploring what the LLM might say on the same input and
recognizing that we already have its answer on this exact topic. If the LLM is good, we expect
it to be consistent.

## Design

`CachedClient` wraps `inference.Client` with a disk-backed cache. It satisfies the `Client`
interface by delegating all methods to the inner client, with caching on `ConverseMessages`
only.

### Cache key

The cache key is the SHA-256 hash of the JSON-serialized call parameters. The key struct
embeds `ConverseOptions` directly, so adding a field to `ConverseOptions` automatically
includes it in the cache key. There is no parallel struct to maintain.

```go
type cacheKeyInput struct {
    Model    string          `json:"model"`
    System   string          `json:"system"`
    Messages []Message       `json:"messages"`
    Options  ConverseOptions `json:"options"`
}
```

A risk is that there are unmodeled parameters in the LLM calls whose values matter.
Temperature is the current example, because the observe pipeline does not vary it and it is
not in `ConverseOptions`. If that changes, the parameter must be added to `ConverseOptions`.
This is a real risk, but we do not expect it to be a common case. We support skipping the
cache (`--skip-cache`) and clearing it entirely when the user asks. By default, we think
people are happier with reuse of cached inference results.

### Storage backend

The cache stores entries as JSON files in a sharded layout. The default backend is the local
filesystem at `~/.muse/cache/inference/`. When the muse store is configured for S3
(`--bucket`), the cache uses S3 instead, making it persistent across machines and shareable
across team members.

Each cache entry is stored at:

```
{backend}/cache/inference/{key[0:2]}/{key}.json
```

The first two hex characters of the key provide a sharding prefix. On local filesystems this
creates subdirectories to keep listings manageable. On S3 the prefix is part of the object
key. Shard prefixes are created on first write.

If two environments resolve the same model name to different inference profiles, a shared
cache will return responses from whichever environment wrote the entry first. This is the same
stale-weights risk that exists with a local cache when a provider updates model weights. The
mitigation is the same: clear the cache.

### Read path

1. Compute cache key.
2. Read the cache entry and deserialize from JSON.
3. On success: return the stored response with zero-cost `Usage`.
4. On any failure (not found, corrupt JSON): fall through to the inner client.

A broken cache degrades to no cache.

### Write path

1. Call the underlying LLM provider (`inner.ConverseMessages`).
2. If the call succeeds: cache the response.
3. If the call returns a truncation: cache it. Truncation happens when the LLM's response
   exceeds `max_tokens`, which is rare for observe (4096 token budget) but possible on
   conversations that produce many observations. Truncated responses contain partial
   observations that the downstream pipeline parses the same way as complete ones.
   `parseObservationItems` extracts whatever `Observation:` lines are present. Caching a
   truncation is preferable to retrying, because the retry would hit the same token limit
   and truncate again.
4. If the call returns a transient error (rate limit, timeout, network): do not cache.

### Why compose is not cached

Compose is the only caller that uses streaming (`ConverseMessagesStream`). The cache does not
interact with streaming calls. Caching compose would prevent the owner from iterating on the
muse without clearing the cache. The economics are also different: compose runs once per
pipeline invocation, so caching saves one call, not hundreds.

### Usage and cost reporting

Cache hits return `Usage` with zero cost and zero token counts. The original cost from the
first call is stored in the cache entry. A future improvement could report the cumulative
savings from cache hits across a pipeline run.

### Skipping the cache

`--skip-cache` bypasses the cache entirely for a run, skipping both reads and writes. This is
useful when the user suspects stale entries or wants to measure current model behavior.

## Failure modes

**Stale model weights.** If a provider updates weights behind the same model name, the cache
returns responses from the old weights. Run `rm -rf ~/.muse/cache/inference/` to rebuild.

**Corrupt entry.** Partial writes or disk errors produce files that fail to deserialize. The
read path treats this as a cache miss. The next successful call overwrites the entry.

**Unbounded growth.** The cache stores a maximum of 1 GiB of entries by default (configurable).
When the limit is reached, the least-recently-used entries are evicted.

## Deferred

**Fuzzy matching.** We could imagine using an LLM to ask "are these queries semantically
identical?" but that costs about as much as an observe call, and the answer is low-value when
exact-match hits are free.

**Time-based expiration.** Entries do not expire based on time. They become less useful as
models update, but the total data storage is small enough to keep around. LRU eviction handles
growth.

## References

[1] https://github.com/ellistarn/muse

[2] Orwellian Observation: research/orwellian-observation.md
