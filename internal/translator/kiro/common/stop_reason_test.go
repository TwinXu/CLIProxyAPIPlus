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
		// content_filtered is a table entry now, not a passthrough case.
		{"CONTENT_FILTERED", "content_filtered"},
		// The passthrough guard: unknown reasons are lowercased, not dropped,
		// because downstream mappers own their own vocabulary.
		{"something_new", "something_new"},
		// Separator variants of a known reason. These are what the folding
		// buys: lowercasing alone leaves the separator in place, so a
		// hyphen- or space-separated spelling used to fall through to the
		// passthrough and reach clients as "end-turn" / "end turn".
		{"end-turn", "end_turn"},
		{"END TURN", "end_turn"},
		{"TOOL-USE", "tool_use"},
		{"END\tTURN", "end_turn"},
		{"end.turn", "end_turn"},
		// Reasons the table gained. The camelCase spellings are the ones that
		// used to come out wrong — "contentFiltered" lowercased to
		// "contentfiltered", which the OpenAI mapper does not recognize.
		{"contentFiltered", "content_filtered"},
		{"guardrailIntervened", "guardrail_intervened"},
		{"modelContextWindowExceeded", "model_context_window_exceeded"},
		{"GUARDRAIL_INTERVENED", "guardrail_intervened"},
		{"MODEL_CONTEXT_WINDOW_EXCEEDED", "model_context_window_exceeded"},
		// Single-word reasons need no table entry: lowercasing is enough.
		{"REFUSAL", "refusal"},
	}
	for _, tc := range tests {
		if got := NormalizeStopReason(tc.in); got != tc.want {
			t.Errorf("NormalizeStopReason(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
