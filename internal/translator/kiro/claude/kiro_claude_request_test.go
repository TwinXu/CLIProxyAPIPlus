package claude

import (
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestBuildKiroPayloadUsesClientThinkingBudget(t *testing.T) {
	tests := []struct {
		name       string
		budget     int
		wantLength string
	}{
		{
			name:       "custom budget",
			budget:     8192,
			wantLength: "<max_thinking_length>8192</max_thinking_length>",
		},
		{
			name:       "explicit placeholder-sized budget",
			budget:     24000,
			wantLength: "<max_thinking_length>24000</max_thinking_length>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{
				"model":"claude-opus-4-1",
				"max_tokens":32000,
				"thinking":{"type":"enabled","budget_tokens":%d},
				"messages":[{"role":"user","content":"hi"}]
			}`, tt.budget))

			out, thinkingEnabled := BuildKiroPayload(body, "claude-opus-4-1", "", "CLI", false, false, nil, nil)
			if !thinkingEnabled {
				t.Fatalf("thinkingEnabled = false, want true")
			}

			content := gjson.GetBytes(out, "conversationState.currentMessage.userInputMessage.content").String()
			if !gjson.ValidBytes(out) {
				t.Fatalf("invalid JSON: %s", string(out))
			}
			if !containsAll(content, "<thinking_mode>enabled</thinking_mode>", tt.wantLength) {
				t.Fatalf("content missing client thinking budget, content=%s", content)
			}
		})
	}
}

func TestBuildKiroPayloadDefaultsPlaceholderThinkingBudget(t *testing.T) {
	body := []byte(`{
		"model":"claude-opus-4-1",
		"max_tokens":32000,
		"thinking":{"type":"enabled"},
		"messages":[{"role":"user","content":"hi"}]
	}`)

	out, thinkingEnabled := BuildKiroPayload(body, "claude-opus-4-1", "", "CLI", false, false, nil, nil)
	if !thinkingEnabled {
		t.Fatalf("thinkingEnabled = false, want true")
	}

	content := gjson.GetBytes(out, "conversationState.currentMessage.userInputMessage.content").String()
	if !containsAll(content, "<thinking_mode>enabled</thinking_mode>", "<max_thinking_length>16000</max_thinking_length>") {
		t.Fatalf("content missing default thinking budget, content=%s", content)
	}
}

// TestBuildKiroPayloadTrailingNonUserMessage reproduces the "Improperly formed
// request" 400: when the last incoming message has a role other than
// user/assistant, processMessages used to leave currentUserMsg nil, fall through
// to the system-prompt-only fallback, and emit a history that ends with a user
// turn followed by another user turn. Kiro rejects that. The trailing history
// user turn must instead be promoted to currentMessage so history ends with an
// assistant turn.
func TestBuildKiroPayloadTrailingNonUserMessage(t *testing.T) {
	body := []byte(`{
		"model":"claude-opus-4-1",
		"max_tokens":32000,
		"system":"SYSTEM-XYZ",
		"messages":[
			{"role":"user","content":"do something"},
			{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Read","input":{"path":"a"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"file body"}]},
			{"role":"tool","content":"stray trailing message"}
		]
	}`)

	out, _ := BuildKiroPayload(body, "claude-opus-4-1", "", "CLI", false, false, nil, nil)
	if !gjson.ValidBytes(out) {
		t.Fatalf("invalid JSON: %s", string(out))
	}

	// currentMessage must be a real user turn (the promoted tool_result turn),
	// not the empty system-prompt-only fallback.
	content := gjson.GetBytes(out, "conversationState.currentMessage.userInputMessage.content").String()
	if !strings.Contains(content, "SYSTEM-XYZ") {
		t.Fatalf("currentMessage missing system prompt, content=%q", content)
	}
	if strings.HasSuffix(content, "--- END SYSTEM PROMPT ---\n") {
		t.Fatalf("currentMessage fell through to empty system-prompt-only fallback, content=%q", content)
	}
	if !strings.Contains(content, "Tool results provided.") {
		t.Fatalf("currentMessage missing promoted tool-result content, content=%q", content)
	}

	// The promoted turn must carry its tool result.
	if id := gjson.GetBytes(out, "conversationState.currentMessage.userInputMessage.userInputMessageContext.toolResults.0.toolUseId").String(); id != "toolu_1" {
		t.Fatalf("currentMessage missing tool result toolUseId=toolu_1, got %q", id)
	}

	// History must end with an assistant turn, never a user turn.
	histLen := gjson.GetBytes(out, "conversationState.history.#").Int()
	if histLen == 0 {
		t.Fatalf("history is empty, want it to end with an assistant turn")
	}
	last := gjson.GetBytes(out, fmt.Sprintf("conversationState.history.%d", histLen-1))
	if !last.Get("assistantResponseMessage").Exists() || last.Get("userInputMessage").Exists() {
		t.Fatalf("history must end with assistantResponseMessage, last=%s", last.Raw)
	}
}

func containsAll(s string, needles ...string) bool {
	for _, needle := range needles {
		if !strings.Contains(s, needle) {
			return false
		}
	}
	return true
}
