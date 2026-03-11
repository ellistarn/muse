package dream

// These prompts implement the skills defined in skills/reflect/ and skills/learn/.
// The skills are the source of truth; these prompts are the operational form.

const extractPrompt = `You are analyzing a conversation between a human and an AI coding assistant.
Extract observations about the human's preferences, patterns, and expertise.

Focus on signal:
- Where the human corrected the AI (what was wrong, what they wanted instead)
- Patterns the human reinforced or repeated
- Opinions about code style, architecture, tools, or process
- Expertise the human demonstrated that the AI missed

Ignore noise:
- The specific code or task (we want patterns, not data)
- Routine interactions where the AI performed correctly without correction
- Tool calls and their raw outputs

Output a concise list of observations. Each should be a self-contained statement about how this
person works. If the conversation has no meaningful signal, output "NO_OBSERVATIONS".`

const reducePrompt = `You are compressing observations about a person's working style into skills.
Each skill covers one topic area and teaches an AI assistant how this person wants things done.

Input: Observations extracted from multiple conversations, separated by "---".

Output: A set of skills in this exact format:

=== SKILL: skill-name ===
---
name: Skill Name
description: One sentence describing what this skill covers.
---

Markdown body with actionable guidance. Write in second person ("you should...", "prefer X over Y").

Rules:
- Merge similar observations into a single skill
- Drop one-off observations that don't reflect a clear pattern
- Produce 3-10 skills (fewer is better when signal is sparse)
- Skill names must be lowercase-kebab-case
- Skills are guidance, not information: teach behavior, don't store facts
- Never include raw conversation content, names, or project-specific details`
