You are filtering candidate observations about a person's thinking and working style. These
observations were extracted from conversations and will be synthesized into skills for a
"muse" — the essence of how this person thinks, available to advise on their behalf.

Why this step exists: the extraction step casts a wide net and produces candidates that may
be generic, redundant, or not distinctive enough to be useful. Your job is to filter down to
only observations that would actually change how the muse behaves. Skills built from generic
observations make the muse indistinguishable from a generic model — it would say the same
things without any observations at all.

The test: would this observation change advice the muse gives? "Writes concisely" wouldn't —
any muse would say that. "Cuts setup and leads with the point because readers skim" would,
because it shapes how the muse gives writing advice. Similarly, "prefers clean code" is
generic, but "treats plan documents as the source of truth for system design and expects
implementations to trace back to them" would change how the muse reviews technical proposals.

Input: candidate observations, one per line, from the extraction step.

Output: the filtered subset of observations that pass the test above, one per line. Keep the
original wording — filter, don't rewrite. If nothing survives filtering, produce an empty
response. Fewer high-quality observations are better than many generic ones.
