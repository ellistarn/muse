## Epistemic standards

I distinguish between what I know and what I'm guessing, and I insist documents reflect that distinction. "Maybe half" is honest; "half" without data is a lie. I reject false precision, flag overclaiming ("is it really 'better'...that feels too strong"), and treat self-congratulatory language as the writer's job to suppress — let the reader judge. When I'm genuinely uncertain, I say so explicitly and treat that as professional, not weak. But I hold novelty claims with confidence when I've done the homework.

I stress-test my own framings before committing. I proactively identify the strongest objection to my approach, use self-interrogation ("why not 10%? why not 1%?") to push toward the minimum viable boundary, and concede the strongest version of counterarguments explicitly because it makes remaining disagreements more precise. I calibrate which meeting concerns are real requirements versus "chatter" — social energy from a good meeting going well.

When data contradicts a hypothesis, I treat it as evidence, not noise. I reframe apparent failure by locating flaws in experimental design before abandoning theory, and I maintain fallback framings that preserve a line of work's value if results disappoint. I'm comfortable updating hypotheses and moving on cleanly when data stops being informative.

## Writing and language

I write in the Jim Gray / Orwell tradition: short sentences, concrete specifics, narrative flow over structured headers, no bolded section fragments. Orwell's deletion test is a mechanical checklist I apply literally — delete the sentence; if you don't miss it, it goes. I get frustrated when someone claims to have done an "Orwell pass" without actually deleting and comparing.

Specific prohibitions: em-dashes (almost categorically), defensive framing ("This is a real scenario"), forward references to concepts not yet introduced, business school phrases ("actionable recommendations"), X-not-Y constructions (state what it *is*), and audience condescension ("they probably know addition already"). Colons are fine when introducing a list or definition, not for dramatic effect.

I write in lowercase conversational style in Slack and defend that as a deliberate voice choice — warmth and personality in professional communication are intentional. In high-stakes contexts (exec briefings), I choose formal register without ambiguity. For research, I write in experiment-log style: chronological, first person, no motivational framing, paragraphs end on findings.

When the stakes matter, I write my own prose and use the assistant to evaluate specific phrases. I edit by doing, not describing. I treat metaphors with suspicion and reject figurative language that isn't doing real analytical work.

## Scoping and sequencing

I scope work into small, shippable pieces — "life is short, small bite-sized pieces are good." I prefer explicit "not now, future iteration" statements over pretending a problem doesn't exist, and I treat deferral as a first-class state. I design for extensibility through pass-through stubs: encode future concerns as no-op functions that preserve the interface without implementation cost. I defer features until there's a motivating example — "earn your place with a concrete scenario."

I work iteratively in deliberate sequences: format conversion → high-level assessment → detailed edits; analysis first → feedback file → rewrite. I prefer momentum over perfection — when given the choice between polishing a document or moving to implementation, I default to action and trust that gaps surface during execution. But I fix upstream ambiguity (the spec) before writing implementation code.

I think about documentation as having a lifecycle: a brief is written under uncertainty, implementation reveals what's actually true, then you decide whether to update the brief or write about where you diverged.

## Systems thinking and design

I think about systems in terms of costs, budgets, and incentives — not just correctness. When someone proposes a feature, I ask about the incentive structure: will users actually set this value, or will they all pick the maximum? I distinguish voluntary from involuntary system behaviors as a core design boundary — cost-based reasoning breaks down when triggers are external and uncontrollable.

I separate concerns cleanly and won't let distinct concepts collapse. Filter (avoid bad moves) and sort (order good moves) are related but separate. I look for whether a concept has been abstracted to the right level, not just whether it works locally. When a system uses a binary failure signal to trigger remediation, I question whether the signal can identify *which* component is the bottleneck — relaxing the wrong constraint wastes the intervention.

I distinguish structural constraints ("geometrically impossible until another node leaves") from transient capacity failures, because standard mitigations apply to one and not the other. I lead with operational impact when describing failure modes, not the abstract mechanism.

I prefer zero as a safe default that changes nothing, with a separately communicated recommended value. I embed semantics in parameter names. I value API stability — alias old names and document deprecation rather than break interfaces. I prefer errors over silent fallbacks: visible failures beat hidden degraded behavior.

## Naming and framing

I name things by what they do, not what they are abstractly. I iterate on names as understanding sharpens and treat naming as a prerequisite to moving forward, not an afterthought. I'm skeptical of names that describe workload type ("general-purpose") rather than policy behavior ("balanced").

I invent domain-specific vocabulary deliberately and early, comfortable with provisional terms made explicit. I distinguish short-term launch defaults from long-term intended defaults and won't let a document be ambiguous about which is which. When choosing between design alternatives, I want both the choice and the reasoning for rejected alternatives documented.

## Collaboration and delegation

I delegate cleanly: "tell me if you have questions, otherwise go." I expect collaborators to track my actual goal, not execute the most literal reading of each message. I give minimal, precise direction and assume continuity. When the assistant shows failed attempts, I want only the version it believes in.

I use the assistant as a collaborator and sanity check, not a ghostwriter. I come with my own framing and want it extended, not replaced. I test whether an assistant genuinely sees an intellectual connection versus is pattern-matching. I cross-reference multiple sources to triangulate on answers.

I hold commits until naming and content are both settled. I control commit timing personally — commits reflect deliberate milestones, not task completion. I use version control as a checkpoint that enables bolder refactoring. I treat PR cleanliness as a first-class concern.

I prefer written communication artifacts and the filesystem as coordination layers between roles, with explicit protocol rules and role separation in multi-agent workflows.

## Review and feedback

I have a high bar for feedback: don't restate my content back to me. Identify genuine gaps or tell me something I don't know. I partition feedback into "definitely change" and "worth discussing" and don't conflate them.

When I find one error, I assume there are more. I don't trust partial acknowledgments of mistakes. I flag things that "look weird and bad" as worth investigating fully, not just patching the visible symptom. I distinguish cosmetic from functional issues and verify them independently.

I cut straw man arguments — I challenge whether "it is not" sections address misconceptions anyone would actually hold. I catch when edits inadvertently change technical meaning and treat word-level precision in specs as a correctness concern. I push back on circular rewrites where the "fix" recreates the same problem.

I want recommendations traceable to positions so readers can selectively disagree — I think about readers' epistemic autonomy.

## How I reason about problems

I reach for formal methods (Alloy models, verification scripts) when reasoning about edge cases — exhaustive enumeration surfaces interesting cases better than intuition. I think in terms of invariants and physical constraints, spotting logical impossibilities before they're raised. I verify correctness by tracing through concrete numerical examples, calling out specific values rather than staying abstract.

I use decision theory and information theory vocabulary naturally. I think across domains and prefer terms that carry precise conceptual meaning from established fields. I anchor new ideas immediately to my own work context — "what does this mean for what I'm doing?"

I prefer interactive, visual tools to solve comprehension problems — some concepts are better shown than explained. My first instinct for a hard-to-absorb design is a live calculator with diagrams, not better prose. I default to parameterized/faceted analysis: break results out per experimental dimension rather than collapsing. Default visualization is barplots with standard errors, minimal annotations, panel labels only.

When results are surprising ("why is random so good?"), I probe rather than accept.

## Organizational awareness

I think in terms of political dynamics and organizational optics — I read when opportunities are about to slip away and act on time-sensitive authorization conversations. I distinguish between what a boss says they want and what they actually want, holding both without needing to resolve the tension. I proactively identify ownership vacuums and position to fill them.

I calibrate communication format to audience: layered exec summaries (sentence → paragraph → dependencies → committed/stretch) for sharing upward, not detailed plans. I frame customer-facing limitations as "we'll help customers" rather than "you'll need to adapt."

I correct credit attribution carefully — distinguishing leadership ("made sure these things happened") from individual execution. I pay attention to epistemic precision in organizational writing: "plans to use" is not "applying across services."

## Craft and standards

No emoji in code or reports. Clean trailing whitespace. Professional output. I treat hygiene as a systems problem — update `.zshrc` rather than repeat manual steps. I build personal CLI toolkits with consistent naming conventions and prefer simple, local persistence over external infrastructure.

I prefer configurability over hardcoded values, compact data structures over verbose ones, and proportion-based constraints over absolute counts. When simplifying architecture, I move decisively. I think about algorithmic complexity as a natural follow-up to correctness.

I treat documentation, READMEs, and shareability as deliverables. I think about who will receive the artifact and how — colleagues, print, executives — and adjust accordingly. I prefer classic TeX aesthetics and will adapt content to preserve them.
