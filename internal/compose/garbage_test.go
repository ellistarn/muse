package compose

import (
	"testing"
)

func TestIsEmpty(t *testing.T) {
	tests := []struct {
		input string
		empty bool
	}{
		// Truly empty
		{"", true},
		{"   ", true},
		{"\n\t", true},

		// Common LLM null markers — must be caught to avoid wasted refine calls
		{"None", true},
		{"none", true},
		{"None.", true},
		{"none.", true},
		{"N/A", true},
		{"n/a", true},
		{"empty", true},
		{"(none)", true},
		{"(empty)", true},
		{"(empty response)", true},

		// Whitespace around null markers
		{"  None  ", true},
		{"  N/A\n", true},

		// Real content — must NOT be treated as empty
		{"Observation: Prefers explicit error handling.", false},
		{"Some actual text here.", false},
		{"None of the above applies because...", false}, // starts with "None" but is real content
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := isEmpty(tt.input); got != tt.empty {
				t.Errorf("isEmpty(%q) = %v, want %v", tt.input, got, tt.empty)
			}
		})
	}
}

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
		{"parens understood", "(Understood — conversation cleared, no observations to filter.)", false},
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
		want  []Observation
	}{
		{
			name:  "standard format",
			input: "Observation: Leads with the conclusion.\nObservation: Treats naming as architecture.",
			want: []Observation{
				{Observation: "Leads with the conclusion."},
				{Observation: "Treats naming as architecture."},
			},
		},
		{
			name:  "with bullet prefixes",
			input: "- Observation: First thing.\n- Observation: Second thing.",
			want: []Observation{
				{Observation: "First thing."},
				{Observation: "Second thing."},
			},
		},
		{
			name:  "with numbered prefixes",
			input: "1. Observation: First.\n2. Observation: Second.",
			want: []Observation{
				{Observation: "First."},
				{Observation: "Second."},
			},
		},
		{
			name:  "meta-commentary discarded",
			input: "Here are the observations:\nObservation: Real one.\nNothing else to report.",
			want:  []Observation{{Observation: "Real one."}},
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
			want: []Observation{
				{Observation: "A real observation."},
				{Observation: "Another real one."},
			},
		},
		{
			name:  "double dash prefix preserved",
			input: "-- Observation: Should not lose content.",
			want:  nil, // "-- " is not a valid list prefix
		},
		{
			name:  "asterisk bullet",
			input: "* Observation: Bullet with asterisk.",
			want:  []Observation{{Observation: "Bullet with asterisk."}},
		},
		{
			name:  "multi-digit numbered prefix",
			input: "12. Observation: Twelfth item.",
			want:  []Observation{{Observation: "Twelfth item."}},
		},
		{
			name:  "quote paired with observation",
			input: "Quote: \"I'd rather find the right word and commit to it.\"\nObservation: Treats naming as a first-class design decision.",
			want: []Observation{
				{Quote: "I'd rather find the right word and commit to it.", Observation: "Treats naming as a first-class design decision."},
			},
		},
		{
			name:  "quote with smart quotes",
			input: "Quote: \u201cFewest words that fully carry the meaning.\u201d\nObservation: Optimizes for compression in communication.",
			want: []Observation{
				{Quote: "Fewest words that fully carry the meaning.", Observation: "Optimizes for compression in communication."},
			},
		},
		{
			name:  "mixed quotes and plain observations",
			input: "Quote: \"Delete it, don't simplify it.\"\nObservation: Defaults to deletion over simplification.\n\nObservation: Prefers structural constraints over stated rules.",
			want: []Observation{
				{Quote: "Delete it, don't simplify it.", Observation: "Defaults to deletion over simplification."},
				{Observation: "Prefers structural constraints over stated rules."},
			},
		},
		{
			name:  "orphan quote without observation",
			input: "Quote: \"Some quote.\"\nSome random line.\nObservation: Unrelated observation.",
			want: []Observation{
				{Observation: "Unrelated observation."},
			},
		},
		{
			name:  "quote with bullet prefix",
			input: "- Quote: \"Terse.\"\n- Observation: Values brevity.",
			want: []Observation{
				{Quote: "Terse.", Observation: "Values brevity."},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseObservationItems(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("parseObservationItems() returned %d items, want %d\n  got:  %v\n  want: %v", len(got), len(tt.want), got, tt.want)
			}
			for i := range got {
				if got[i].Observation != tt.want[i].Observation {
					t.Errorf("item[%d].Observation = %q, want %q", i, got[i].Observation, tt.want[i].Observation)
				}
				if got[i].Quote != tt.want[i].Quote {
					t.Errorf("item[%d].Quote = %q, want %q", i, got[i].Quote, tt.want[i].Quote)
				}
			}
		})
	}
}
