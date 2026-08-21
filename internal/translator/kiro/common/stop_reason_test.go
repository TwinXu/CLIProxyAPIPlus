package common

import "testing"

func TestNormalizeStopReason(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		// The spellings Kiro actually sends.
		{"TOOL_USE", "tool_use"},
		{"END_TURN", "end_turn"},
		{"MAX_TOKENS", "max_tokens"},
		// Already correct values survive untouched.
		{"tool_use", "tool_use"},
		{"end_turn", "end_turn"},
		// Concatenated spellings, and surrounding whitespace.
		{"ToolUse", "tool_use"},
		{"EndTurn", "end_turn"},
		{"MaxTokens", "max_tokens"},
		{"StopSequence", "stop_sequence"},
		{"PauseTurn", "pause_turn"},
		{"  TOOL_USE  ", "tool_use"},
		// An empty reason stays empty so callers can apply their own fallback.
		{"", ""},
		// Unknown reasons are lowercased, not dropped: downstream mappers own
		// their own vocabulary and need to still see them.
		{"CONTENT_FILTERED", "content_filtered"},
		{"something_new", "something_new"},
	}
	for _, tc := range tests {
		if got := NormalizeStopReason(tc.in); got != tc.want {
			t.Errorf("NormalizeStopReason(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
