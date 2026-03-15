# Plan: PII Defense in Depth

The muse should know HOW you think, not WHAT you're working on. Philosophy survives filtering. Code, names, and secrets do not need to.

Currently there is zero programmatic PII filtering. The only protection is prompt instructions saying "don't include personal info." That's one layer, and it's the least reliable kind.

## Design Principles

- Filter early, filter often. Every boundary crossing is a chance to scrub.
- Deterministic beats probabilistic. Regex catches what LLMs miss. LLMs catch what regex misses. Use both.
- The signal we want is abstract. "Prefers composition over inheritance" survives scrubbing. `AKIA3EXAMPLE...` does not need to.
- Fail closed on secrets. If something looks like a key, kill it. A lost observation costs nothing; a leaked secret costs everything.

## The Five Layers

### Layer 1: Pre-reflect scrub (before LLM sees raw conversations)

New package: `internal/scrub/scrub.go`

A deterministic text sanitizer that runs regex-based replacement on conversation text before it hits the LLM during the reflect phase. Raw conversations stay in S3 (so you can re-reflect with different scrub rules later), but the LLM never sees unscrubbed data.

What it catches:

| Pattern | Replacement |
|---------|-------------|
| AWS access keys (`AKIA[0-9A-Z]{16}`) | `[AWS_KEY]` |
| AWS secret keys (40-char base64 after "secret") | `[AWS_SECRET]` |
| High-entropy strings (API tokens, JWTs, GitHub PATs) | `[TOKEN]` |
| Email addresses | `[EMAIL]` |
| AWS account IDs (12-digit standalone numbers) | `[ACCOUNT_ID]` |
| Absolute file paths | basename only |
| IP addresses (v4 and v6) | `[IP]` |
| URLs with auth tokens in query params | `[URL]` |
| Common secret env var patterns (`PASSWORD=...`, `TOKEN=...`) | `[REDACTED_ENV]` |

What it preserves:
- Relative paths and package names (architectural signal)
- Function/method names
- Error messages (with secrets scrubbed out)
- Natural language discussion

Supports user-provided custom redaction terms via config (your name, company names, project names).

Hooks into `reflectOnSession` in `distill.go`, scrubbing the formatted conversation before the LLM call.

### Layer 2: Strip tool data in `formatSession`

Tool call inputs/outputs are the highest-PII payload: file contents, command outputs, API responses. The human's words and the assistant's reasoning carry all the signal.

Change `formatSession` to skip tool-result messages or replace them with `[tool]: {tool name}`. Preserve which tools were used, drop the payload.

This is part of the improve-reflections plan and should ship first.

### Layer 3: Post-skill validation

After learn generates skills, run the regex scrubber over each skill's content. Log warnings for any residual matches. Scrub them out before writing to S3.

This is belt-and-suspenders. If the reflect prompt and the learn prompt both failed to strip something, the deterministic pass catches it.

### Layer 4: Serving-time scrub

In `Ask()`, scrub the LLM response before returning it. Last line of defense. If something leaked through Layers 1-3 into skills, and the LLM regurgitates it, the deterministic scrubber catches it on the way out.

### Layer 5: Storage hygiene

After a successful distill, purge processed conversations from S3. Raw conversations are only needed until they're reflected on. Add `PurgeProcessedConversations` to the storage client.

Also: S3 bucket should have server-side encryption enabled and public access blocked.

## Configuration

User-provided custom redaction terms via config file or env var:

```yaml
# ~/.muse/config.yaml
redact:
  - "Acme Corp"
  - "etarn"
  - "Project Chimera"
```

The scrubber loads these at initialization and adds them to the pattern list. This handles PII that's specific to the user and impossible for generic regex to catch.

## Files changed

| File | Change |
|------|--------|
| `internal/scrub/scrub.go` (new) | Deterministic regex scrubber |
| `internal/scrub/patterns.go` (new) | Pattern definitions |
| `internal/scrub/scrub_test.go` (new) | Table-driven tests for every pattern |
| `internal/distill/distill.go` | Scrub conversation before reflect, scrub skills after learn |
| `internal/muse/muse.go` | Scrub response in `Ask()` |
| `internal/storage/s3.go` | Add `PurgeProcessedConversations()` |

## Implementation order

1. **Layer 2** (strip tool data in formatSession) -- ships with improve-reflections plan, no new package needed.
2. **`internal/scrub` package** -- the foundation everything else depends on. Ship with thorough tests.
3. **Layer 1** (pre-reflect scrub) -- immediate risk reduction, hooks into existing reflect call.
4. **Layer 3** (post-skill scrub) -- cheap deterministic pass on output.
5. **Layer 4** (serving-time scrub) -- another cheap pass.
6. **Layer 5** (conversation purge) -- reduce exposure window for raw data at rest.

## What this does NOT include

- **No summarization gate.** An LLM-based summarization step before reflect would double LLM cost per conversation. Start with deterministic scrubbing and see if it's sufficient. Add the summarization layer later if residual PII risk warrants the cost.
- **No client-side encryption.** Raw conversations transit the network over HTTPS (SDK default). If the threat model grows, add client-side encryption later.
- **Regex will have false positives.** A 12-digit number that isn't an AWS account ID will get redacted. This is the correct tradeoff: false positives lose signal, false negatives leak data.
