package claude

import (
	"encoding/json"
	"strings"
	"testing"
)

// buildBodyWithTrailingRole returns a Claude request carrying tools whose last
// message uses the given role.
func buildBodyWithTrailingRole(role string) []byte {
	body := map[string]any{
		"model":      "claude-opus-5",
		"max_tokens": 900,
		"tools": []any{
			map[string]any{"name": "Bash", "description": "Run a shell command.",
				"input_schema": map[string]any{"type": "object",
					"properties": map[string]any{"command": map[string]any{"type": "string"}},
					"required":   []any{"command"}}},
			map[string]any{"name": "Read", "description": "Read a file.",
				"input_schema": map[string]any{"type": "object",
					"properties": map[string]any{"file_path": map[string]any{"type": "string"}},
					"required":   []any{"file_path"}}},
		},
		"messages": []any{
			map[string]any{"role": "user", "content": "Run date -Is and report the output."},
			map[string]any{"role": role, "content": "SYSTEM-REMINDER-SENTINEL"},
		},
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	return encoded
}

// The Claude Agent SDK appends a trailing role:"system" reminder on the first
// turn of a fresh session. processMessages only ever handled "user" and
// "assistant", so that message left currentUserMsg nil, the payload fell back to
// a bare currentMessage, and userInputMessageContext -- the only place tools live
// -- was never built. Kiro then answered a toolless request: the model narrated
// '<invoke name="Bash">' as prose instead of calling anything.
func TestBuildKiroPayloadKeepsToolsWhenLastMessageIsSystem(t *testing.T) {
	for _, role := range []string{"user", "system"} {
		t.Run("trailing_"+role, func(t *testing.T) {
			payload, _ := BuildKiroPayload(buildBodyWithTrailingRole(role),
				"claude-opus-5", "", "AI_EDITOR", false, false, nil, nil)
			if len(payload) == 0 {
				t.Fatal("BuildKiroPayload returned an empty payload")
			}
			got := strings.Count(string(payload), `"toolSpecification"`)
			if got != 2 {
				t.Errorf("tools dropped: got %d toolSpecification entries, want 2", got)
			}
			if !strings.Contains(string(payload), "SYSTEM-REMINDER-SENTINEL") {
				t.Errorf("trailing %s message content was dropped from the payload", role)
			}
		})
	}
}
