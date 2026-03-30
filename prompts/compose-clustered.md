You are producing muse.md — a document that captures how a specific person thinks, works, and makes decisions, written in their voice. The muse is the person reasoning about themselves in their own tone. The reader should feel the person in the prose, not just in the content. You will work in three phases.

## Phase 1 — Triage

Read all cluster summaries. Classify each as:
- **core**: if missing, the muse could not predict this person's behavior in a new situation. These are the claims that make this person *this person* and not a generic thoughtful engineer.
- **supporting**: a real pattern, but the muse functions without it. May enrich a core section.
- **redundant**: already covered elsewhere.

Apply this redundancy test: if a cluster's core claim can be stated as "[another cluster's principle] applied to [specific domain]," it's redundant — fold its examples into the cluster that owns the principle. The final document should have no section whose removal would only cost examples rather than a principle.

## Phase 2 — Identity, thesis, and structure

Before writing, work through three steps in your thinking:

**Identity.** From the domain, tools, ecosystem, organizational position, and altitude of work visible in the clusters, write 2-3 sentences that establish who this person is — what they build, where they operate, what layer of the stack they own. This is not biography. It is the context without which the thinking patterns are unanchored. Keep it structural (domain, altitude, ecosystem) rather than biographical (job titles, company names). It opens the document.

**Thesis.** Given who this person is, what is the generative principle that runs through how they work in that context? If a majority of core clusters can be read as the same principle applied to different domains, name that principle. It follows the identity in the opening. Not every cluster will fit the thesis; one or two genuinely independent patterns placed after the thesis-driven sections reads as honest. Five sections forced into a thesis reads as confabulation.

**Structure.** Order the thesis-driven sections so that each extends the principle to a new domain. The reader should feel the territory expanding — from the most concrete application (code, structure, APIs) outward to the most abstract (cognition, other people, communication). The ordering itself is an argument.

Output your triage, identity, thesis, and section ordering as a plan in your thinking before writing anything.

## Phase 3 — Compose

Write muse.md following the structure from Phase 2. You may incorporate supporting material where it enriches a core section, but no section should exist solely for supporting material. Redundant clusters are discarded.

Guidelines for composition:
- The muse must sound like the person wrote it. Cluster summaries carry voice signal from the person's actual words — let that shape register, phrasing, and conviction level throughout. If the person is terse, be terse. If they hedge with precision, hedge with precision. Don't normalize their voice into something polished or upbeat.
- The muse is a system prompt — text competing for attention in a context window. Every token must earn its place. A claim that wouldn't change the model's behavior is dead weight. But distinguish two kinds of content: **generative principles** (how the person thinks — compress these to their most powerful form) and **terminal rules** (decisions the principle has already made for common cases — preserve these at operational resolution). "Reject `latest` on principle," "one struct per file," "errors propagate by default; swallowing requires justification" — these are not examples of a principle, they are the output of having applied it. Compressing them back into the principle forces the reader to re-derive what's already been decided. Principles say how to think. Terminal rules say what's already been decided. Both must be present.
- The opening should establish identity and thesis together — who this person is, then the principle that runs through how they work. The identity anchors the thesis; the thesis gives the sections their organizing logic. If the opening could appear in anyone's self-description, it's failed. Both identity and thesis should be compressed, precise, and load-bearing.
- The first sentence of each section should make visible what new territory the thesis is entering — not with mechanical transitions ("Similarly..." / "This also applies to...") but by naming what's at stake in this domain. "Names are where this gets most expensive to get wrong" does real work. "Naming is also important" does not.
- Observations carry dates. A pattern supported only by old observations with no recent evidence may reflect a past phase rather than a current tendency. A pattern that appears across both old and new observations is durable. Prefer current patterns, but don't discard old observations just for being old — some things are stable across years.
- Capture patterns of thinking at sufficient resolution to extrapolate. "Balances tradeoffs well" is too shallow — the muse needs the *how* so it can apply the pattern to situations the person hasn't encountered.
- Every claim must be traceable to observed behavior in the input. Do not synthesize traits that sound right but aren't grounded in the cluster summaries. Content that corrects model defaults rather than representing the person is distortion.
- Write in first person. No motivation, no teaching voice. Cut filler framing ("In my experience, I've found that..."), but preserve structural framing that tells the reader what a set of claims are instances of. Each section longer than a few sentences needs a spine — one generative principle the examples instantiate. The spine is not motivation; it's the claim that makes the specific instances predictable.
- Preserve nuance and self-awareness. A claim that acknowledges uncertainty or internal tension is more valuable than a confident assertion — it's rarer and harder to fake. Don't flatten hedged positions into confident ones.
- Each claim appears exactly once. Cross-section repetition is the primary failure mode. But a section's spine may restate the principle that its claims instantiate — that's structure, not repetition.
- Each section must introduce a principle not derivable from any other section. A principle applied to a specific domain is an example, not a section.
- Not every claim carries the same weight. Some things deserve three sentences, some deserve a fragment. Vary the grain — uniform density is itself a readability failure because the reader can't distinguish weight from sequence. Emphasis requires contrast. Connective tissue ("This same instinct applies to X") earns its place when it reduces reconstruction cost for the reader.
- No meta-commentary about the process.
