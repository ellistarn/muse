# Plan: Improve Observations

The observe step is the atomic unit of the compose pipeline: one conversation in, observations out. Even as the pipeline around it evolves, observe stays. Getting it right is the highest-leverage move.

Three changes, all in `internal/compose/`. No new packages.

## 1. Rewrite `observePrompt` in `prompts.go`

The current prompt has the right categories (corrections, reinforcements, opinions, expertise) but three problems:

- It doesn't push hard enough for specificity. "Prefers clean code" passes the current bar.
- `NO_OBSERVATIONS` is a footnote instead of the expected default. This biases the LLM toward inventing signal.
- It doesn't tell the LLM what a good observation looks like vs a bad one.

New prompt direction:

```
You are analyzing a conversation between a human and an AI coding assistant.
Most conversations are routine — the AI does what's asked, the human accepts.
That's fine. Output NO_OBSERVATIONS and move on.

Only extract observations when you see genuine signal about how this person
thinks differently:

- Corrections: the human pushed back on something the AI did. What did they
  want instead, and why?
- Reinforcements: the human explicitly praised or repeated a pattern. What
  was it?
- Opinions: the human stated a preference about code, architecture, tools,
  or process that goes beyond "standard good practice."
- Expertise: the human knew something the AI didn't. What domain knowledge
  did they demonstrate?

Each observation should be a single sentence that captures the *why*, not
just the *what*. "Prefers composition over inheritance" is weak. "Avoids
struct embedding in Go because it hides the dependency graph and makes
refactoring brittle" is strong.

Ignore:
- The specific code, project, or task (we want patterns, not data)
- Routine interactions where the AI performed correctly
- Tool calls and their raw outputs
- Generic good practices that any senior engineer would agree with

If the conversation reveals nothing distinctive, output NO_OBSERVATIONS.
This is the expected outcome for most conversations.
```

Key differences from current:

- **NO_OBSERVATIONS is the default**, not the exception. The opening lines set this expectation.
- **"Why not just what"** pushes observations toward reasoning, which is inherently more specific and harder to produce as slop.
- **Concrete good/bad example** shows the LLM the quality bar without imposing rigid structure.
- **"Generic good practices that any senior engineer would agree with"** is an explicit ignore category.

## 2. Update `skills/observe/SKILL.md` to match

The SKILL.md is the human-readable source of truth. The prompt is its operational form. Keep them in sync. Same changes: default to "produce nothing," emphasize reasoning over surface patterns, add the good/bad example.

## 3. Strip tool data in `formatSession` at `compose.go:284-293`

`formatSession` currently dumps `[role]: content` for every message. Tool call inputs/outputs are the highest-PII payload (file contents, command outputs, API responses). The role/content pairs already capture the human's words and the assistant's reasoning, which is where all the signal lives.

Change: if a message role is `tool` or the content is a tool result, skip it or replace it with `[tool]: {tool name}` (preserve which tools were used, drop the payload). This is a minimal code change in `formatSession`.

## Files changed

| File | Change |
|------|--------|
| `internal/compose/prompts.go:6-21` | Rewrite `observePrompt` |
| `skills/observe/SKILL.md` | Update to match new prompt |
| `internal/compose/distill.go:284-293` | Strip tool payloads in `formatSession` |

## What this does NOT include

- **No `internal/scrub` package yet.** That's a separate effort for regex-based PII scrubbing. The tool-data stripping is the highest-value subset of that work and doesn't need a new package.
- **No changes to `learnPrompt`.** We're focusing on observation quality. Learn consumes whatever observe produces.
- **No structured observation format.** Freeform text, LLM-consumed.
- **No pipeline architecture changes.** Observe stays as a single LLM call per conversation.

## How to validate

Run `muse compose --reobserve` on existing conversations and diff the old vs new observations. Look for:

- More `NO_OBSERVATIONS` outputs (good -- means it's not inventing signal)
- Observations that include reasoning ("because...") rather than just preferences
- Fewer observations that could apply to any developer
