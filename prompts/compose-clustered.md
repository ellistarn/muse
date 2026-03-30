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

**Thesis.** Given who this person is, what is the principle that runs through how they work? If a majority of core clusters can be read as the same principle applied to different domains, name that principle. It follows the identity in the opening. Not every cluster will fit the thesis; one or two genuinely independent patterns placed after the thesis-driven sections reads as honest. Five sections forced into a thesis reads as confabulation.

**Structure.** The muse is structured as an argument from the thesis. Each section is a subthesis that the governing claim requires — if a section can't be motivated by the thesis, it doesn't belong. Sections are not topic buckets; they're derivations. Order them so that territory expands from the most concrete application (code, structure, APIs) outward to the most abstract (cognition, other people, communication). The ordering itself is an argument. A section that can't be derived from the thesis is either misplaced content or evidence the thesis is incomplete — revise the thesis before adding an orphan section.

Output your triage, identity, thesis, and section ordering as a plan in your thinking before writing anything.

## Phase 3 — Compose

Write muse.md following the structure from Phase 2. You may incorporate supporting material where it enriches a core section, but no section should exist solely for supporting material. Redundant clusters are discarded.

Guidelines for composition:
- The muse must sound like the person wrote it. Cluster summaries carry voice signal from the person's actual words — let that shape register, phrasing, and conviction level throughout. If the person is terse, be terse. If they hedge with precision, hedge with precision. Don't normalize their voice into something polished or upbeat.
- The muse is a system prompt — text competing for attention in a context window. Target 3000–4000 tokens. Every token must earn its place by changing the model's behavior on a real question. A claim that restates what a well-prompted model already does — "I update when evidence changes," "I communicate directly" — is dead weight. Prioritize content that diverges from model defaults: domain-specific vocabulary, specific rules, non-obvious heuristics. Cut general reasoning style and communication philosophy first — those are where models need the least steering. Content operates at three altitudes: principles (how the person thinks — compress hard), heuristics (if/then rules for classes of situations — these make the muse predictive), and rules (specific already-decided things — reproduce verbatim or drop, never paraphrase). If space forces a tradeoff, cut exposition before cutting rules.
- Do not use bold labels, formatted subsection headers, or bullet-list taxonomies within sections. The muse should read as natural prose written by the person, not as a structured template filled in by an LLM. Let structure emerge from the writing — a principle stated, then the heuristics that follow from it, then the specific rules — without announcing each tier. Use `##` for section headings only. Within sections, use paragraph breaks, dashes for lists of rules, and inline emphasis sparingly.
- Do not invent language the person wouldn't use. The cluster summaries carry the person's actual words and phrasings — use those. If a summary says the person talks about "failure modes first," don't upgrade it to "failure topology" or "failure-driven design." If you can't find the person's own phrasing for a concept, use plain English rather than generating a polished formulation. The muse should sound like the person wrote it, not like an LLM wrote it *about* them.
- Treat each cluster as one distinct idea, regardless of how many observations it contains. A cluster with 40 observations and a cluster with 3 observations each contribute one idea to the muse. Volume means the person revisits a pattern often — it does not mean the pattern deserves more space. The muse should represent the *breadth* of the person's thinking, not the frequency distribution of their conversations. If the same principle appears in multiple clusters, it gets stated once; the freed space goes to ideas from smaller clusters that would otherwise be squeezed out.
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
