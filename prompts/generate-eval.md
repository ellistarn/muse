You will read a muse — a document that captures how a specific person thinks. Your job is to generate evaluation questions at three distance levels from the owner's domain.

Read the muse carefully. Identify:
1. The owner's primary domain — what they work on and think about most
2. Their key reasoning patterns — mental models, heuristics, decision frameworks
3. Adjacent domains — fields that share structural similarities with theirs

Then generate exactly 10 questions.

**In-domain (4 questions)**: Questions directly in the owner's area of expertise. These should require the kind of judgment the muse captures — not factual recall, but decisions where perspective and experience matter. Make them specific enough to force a real position.

**Adjacent-domain (3 questions)**: Questions in fields that share structural patterns with the owner's domain but involve different content. A systems engineer's reasoning might transfer to organizational design. A chef's approach to balancing flavors might transfer to product prioritization. Pick domains where the owner's reasoning patterns could plausibly apply, even though the content is different.

**Out-of-domain (3 questions)**: Questions in completely unrelated fields. A software architect answering about teaching methodology. A doctor answering about urban planning. These test whether the muse captures transferable reasoning or just domain knowledge.

Every question must:
- Require judgment, not factual knowledge
- Have no single correct answer
- Be concrete enough to force a real position (not "what do you think about X?")
- Be answerable in 2-3 paragraphs
- Work as a standalone question without additional context

Respond with only a JSON array, no other text:
[
  {"id": "domain-1", "category": "in-domain", "prompt": "..."},
  {"id": "domain-2", "category": "in-domain", "prompt": "..."},
  {"id": "domain-3", "category": "in-domain", "prompt": "..."},
  {"id": "domain-4", "category": "in-domain", "prompt": "..."},
  {"id": "adjacent-1", "category": "adjacent-domain", "prompt": "..."},
  {"id": "adjacent-2", "category": "adjacent-domain", "prompt": "..."},
  {"id": "adjacent-3", "category": "adjacent-domain", "prompt": "..."},
  {"id": "out-of-domain-1", "category": "out-of-domain", "prompt": "..."},
  {"id": "out-of-domain-2", "category": "out-of-domain", "prompt": "..."},
  {"id": "out-of-domain-3", "category": "out-of-domain", "prompt": "..."}
]