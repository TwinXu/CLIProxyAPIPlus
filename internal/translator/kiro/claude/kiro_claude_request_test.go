package claude

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestBuildKiroPayloadInjectsNoThinkingPrompt(t *testing.T) {
	// Reasoning is produced natively by the Kiro backend (reasoningContentEvent), so the
	// request must carry NO <thinking_mode>/<thinking_instruction>/<max_thinking_length>
	// prompt. The old "fake reasoning" injection ordered the model to wrap reasoning in
	// literal <thinking> tags; the model obeyed on the modern runtime.*.kiro.dev endpoint
	// and leaked those tags + planning text into the visible answer.
	bodies := []string{
		`{"model":"claude-opus-4-1","max_tokens":32000,"thinking":{"type":"enabled","budget_tokens":8192},"messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"claude-opus-4-1","max_tokens":32000,"thinking":{"type":"adaptive"},"output_config":{"effort":"high"},"messages":[{"role":"user","content":"hi"}]}`,
	}
	for i, body := range bodies {
		out, thinkingEnabled := BuildKiroPayload([]byte(body), "claude-opus-4-1", "", "CLI", false, false, nil, nil)
		if !thinkingEnabled {
			t.Fatalf("case %d: thinkingEnabled = false, want true", i)
		}
		if !gjson.ValidBytes(out) {
			t.Fatalf("case %d: invalid JSON: %s", i, string(out))
		}
		content := gjson.GetBytes(out, "conversationState.currentMessage.userInputMessage.content").String()
		for _, marker := range []string{"<thinking_mode>", "<thinking_instruction>", "<max_thinking_length>", "wrap your reasoning"} {
			if strings.Contains(content, marker) {
				t.Errorf("case %d: content must not contain injected %q, content=%s", i, marker, content)
			}
		}
	}
}

func TestBuildKiroPayloadAdaptiveEffortNoneDisables(t *testing.T) {
	// effort "none" is an explicit "do not think" signal even on the adaptive path.
	body := []byte(`{
		"model":"claude-opus-4-1",
		"max_tokens":32000,
		"thinking":{"type":"adaptive"},
		"output_config":{"effort":"none"},
		"messages":[{"role":"user","content":"hi"}]
	}`)

	out, thinkingEnabled := BuildKiroPayload(body, "claude-opus-4-1", "", "CLI", false, false, nil, nil)
	if thinkingEnabled {
		t.Fatalf("thinkingEnabled = true, want false (effort=none)")
	}
	content := gjson.GetBytes(out, "conversationState.currentMessage.userInputMessage.content").String()
	if strings.Contains(content, "<thinking_mode>enabled</thinking_mode>") {
		t.Fatalf("effort=none must not inject thinking prompt, content=%s", content)
	}
}
