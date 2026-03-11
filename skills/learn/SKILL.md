---
name: learn
description: Synthesize observations from reflection into reusable skills following the Agent Skills format.
---

# Learning

Given a set of observations about a person's working style, synthesize them into a small number of reusable skills.

## Rules

- Merge similar observations into a single skill
- Drop one-off observations that don't reflect a clear pattern
- Produce 3-10 skills (fewer is better when signal is sparse)
- Write in second person ("you should...", "prefer X over Y")
- Skills are guidance, not information: teach behavior, don't store facts
- Never include raw conversation content, names, or project-specific details

## Output format

Each skill must follow the Agent Skills spec (https://agentskills.io). Delimit skills with a header line so they can be parsed:

```
=== SKILL: skill-name ===
---
name: skill-name
description: One sentence describing what this skill covers.
---

Markdown body with actionable guidance.
```

Skill names must be lowercase-kebab-case (e.g., "code-style", "go-patterns").

## Pruning

Not every memory needs processing on every run. The dream pipeline tracks which memories have been processed and when. On subsequent runs only new or updated memories are reflected on, and the new observations are merged with existing skills during learning.
