package distill

import (
	"testing"
)

func TestIsRelevant(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		relevant bool
	}{
		// Genuine observations — should be relevant
		{"real observation", "Prefers explicit error handling over panic/recover patterns because crashes in production are harder to debug than returned errors", true},
		{"real short-ish", "Leads with the conclusion because readers skim", true},
		{"naming preference", "Uses concrete nouns for package names rather than abstract categories", true},

		// Empty / whitespace
		{"empty string", "", false},
		{"whitespace only", "   \n\t  ", false},

		// Too short
		{"too short", "ok", false},
		{"short word", "none found", false},

		// Placeholder tokens
		{"empty parens", "(empty)", false},
		{"empty response parens", "(empty response)", false},
		{"none", "(none)", false},
		{"n/a", "N/A", false},
		{"bare none", "None", false},
		{"bare empty", "Empty", false},

		// LLM meta-commentary
		{"no observations", "No observations were found in this conversation.", false},
		{"i dont see", "I don't see any candidate observations in this conversation.", false},
		{"couldnt find", "I couldn't find any distinctive patterns in this exchange.", false},
		{"there are no", "There are no observations that pass the distinctiveness test.", false},
		{"nothing distinctive", "Nothing distinctive was expressed in this conversation.", false},
		{"this conversation", "This conversation was mostly routine coding assistance.", false},
		{"after filtering", "After filtering, no observations survived the quality threshold.", false},
		{"after review", "After review, none of the candidates meet the bar.", false},
		{"no candidate", "No candidate observations found.", false},

		// Edge cases — should be relevant
		{"starts with i but real", "I think in terms of state machines when modeling concurrent systems", true},
		{"mentions none but real", "Prefers none-style error returns over exception-based flow because the call site should decide how to handle failure", true},

		// Parenthesized meta-commentary — should NOT be relevant
		{"parens no obs", "(No candidate observations were provided in the input)", false},
		{"parens understood", "(Understood — session cleared, no observations to filter.)", false},
		{"parens empty response", "(empty response — no observations survive filtering)", false},
		{"parens nothing passes", "(no observations pass the filter)", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRelevant(tt.input)
			if got != tt.relevant {
				t.Errorf("isRelevant(%q) = %v, want %v", tt.input, got, tt.relevant)
			}
		})
	}
}

func TestParseObservationItems(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "standard format",
			input: "Observation: Leads with the conclusion.\nObservation: Treats naming as architecture.",
			want:  []string{"Leads with the conclusion.", "Treats naming as architecture."},
		},
		{
			name:  "with bullet prefixes",
			input: "- Observation: First thing.\n- Observation: Second thing.",
			want:  []string{"First thing.", "Second thing."},
		},
		{
			name:  "with numbered prefixes",
			input: "1. Observation: First.\n2. Observation: Second.",
			want:  []string{"First.", "Second."},
		},
		{
			name:  "meta-commentary discarded",
			input: "Here are the observations:\nObservation: Real one.\nNothing else to report.",
			want:  []string{"Real one."},
		},
		{
			name:  "empty input",
			input: "",
			want:  nil,
		},
		{
			name:  "NONE response",
			input: "NONE",
			want:  nil,
		},
		{
			name:  "no prefix lines discarded",
			input: "I found the following patterns:\n\n(no observations pass the filter)",
			want:  nil,
		},
		{
			name:  "mixed valid and garbage",
			input: "Observation: A real observation.\n(empty)\nObservation: Another real one.\nI don't see any more.",
			want:  []string{"A real observation.", "Another real one."},
		},
		{
			name:  "double dash prefix preserved",
			input: "-- Observation: Should not lose content.",
			want:  nil, // "-- " is not a valid list prefix
		},
		{
			name:  "asterisk bullet",
			input: "* Observation: Bullet with asterisk.",
			want:  []string{"Bullet with asterisk."},
		},
		{
			name:  "multi-digit numbered prefix",
			input: "12. Observation: Twelfth item.",
			want:  []string{"Twelfth item."},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseObservationItems(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("parseObservationItems() returned %d items, want %d\n  got:  %v\n  want: %v", len(got), len(tt.want), got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("item[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}
