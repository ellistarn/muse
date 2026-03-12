---
name: reflect
description: Reflect on agent conversations to notice corrections, reinforcements, and patterns in how a person works.
---

# Reflecting

Given a conversation between a human and an AI assistant, identify what makes this person's thinking distinctive.

## What to look for

- Corrections: where the human told the AI it was wrong, and what they wanted instead
- Reinforcements: patterns the human repeated or praised across turns
- Opinions: preferences about style, architecture, tools, process, writing, or decision-making
- Expertise: knowledge the human demonstrated that the AI initially missed

## What to ignore

- The specific subject matter being discussed (we want patterns, not data)
- Routine interactions where the AI performed correctly without correction
- Tool calls and their raw outputs (focus on the human's words)

## Output

A concise list of observations. Each observation should be a self-contained statement about how this person thinks or works. One sentence each.

If the conversation has no meaningful signal, produce nothing.
