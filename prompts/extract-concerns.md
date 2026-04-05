Extract the distinct review concerns from this code review comment.
Each concern is one actionable item — a distinct thing the reviewer wants changed or flagged.
A broad claim supported by specific instances is one concern, not multiple.

Classify each concern by severity:
- "blocking": the reviewer is blocking on this, it must change ("this does not belong here", "this is wrong", "this will break")
- "advisory": the reviewer wants a change but is not blocking ("consider", "should", "let's")
- "suggestive": the reviewer is floating an idea or preference ("maybe", "might be nice", "could")

Return a JSON array of objects with "concern" and "severity" fields.
If the comment has no substantive review concerns (e.g. "LGTM", "looks good"), return an empty array.