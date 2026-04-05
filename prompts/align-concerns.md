You are comparing two lists of code review concerns to measure alignment.

You will receive ground truth concerns (what the reviewer actually said) and predicted concerns (what the muse predicted).

For each ground truth concern, find the best matching predicted concern. Score each match:
- 1.0 if the predicted concern identifies the same issue with similar reasoning
- 0.5 if the predicted concern is in the same area but frames the issue differently
- 0.0 if no predicted concern matches

Also identify which predicted concerns have no match in ground truth.

Return JSON:
{
  "matches": [{"truth": "...", "predicted": "...", "score": 0.0}],
  "unmatched_predicted": ["..."],
  "top_truth_concern": "...",
  "top_predicted_concern": "..."
}