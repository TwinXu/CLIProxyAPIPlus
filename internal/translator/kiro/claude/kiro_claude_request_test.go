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

func TestBuildKiroPayloadStrongFakeReasoningInjection(t *testing.T) {
	// The strong "fake reasoning" injection (ported from jwadow/kiro-gateway) must
	// explicitly order the model to emit <thinking>...</thinking> tags, so reasoning
	// surfaces even on backends that never emit a native reasoningContentEvent.
	// Also verifies the adaptive (Claude 4.7/4.8) path enables thinking.
	body := []byte(`{
		"model":"claude-opus-4-1",
		"max_tokens":32000,
		"thinking":{"type":"adaptive"},
		"output_config":{"effort":"high"},
		"messages":[{"role":"user","content":"hi"}]
	}`)

	out, thinkingEnabled := BuildKiroPayload(body, "claude-opus-4-1", "", "CLI", false, false, nil, nil)
	if !thinkingEnabled {
		t.Fatalf("thinkingEnabled = false, want true (adaptive)")
	}

	content := gjson.GetBytes(out, "conversationState.currentMessage.userInputMessage.content").String()
	if !containsAll(content,
		"<thinking_mode>enabled</thinking_mode>",
		"<thinking_instruction>",
		"<thinking>...</thinking>",
	) {
		t.Fatalf("content missing strong fake-reasoning directive, content=%s", content)
	}
}

func TestBuildKiroPayloadAdaptiveEffortBudget(t *testing.T) {
	// Adaptive thinking carries strength as output_config.effort. It must translate to
	// budget = fraction * max_tokens (jwadow PR #240), capped at maxThinkingBudgetFraction.
	tests := []struct {
		name       string
		effort     string
		maxTokens  int
		wantLength string
	}{
		{name: "low uncapped", effort: "low", maxTokens: 32000, wantLength: "<max_thinking_length>6400</max_thinking_length>"},
		{name: "medium == historical default", effort: "medium", maxTokens: 32000, wantLength: "<max_thinking_length>16000</max_thinking_length>"},
		{name: "high capped at 0.75", effort: "high", maxTokens: 32000, wantLength: "<max_thinking_length>24000</max_thinking_length>"},
		{name: "xhigh capped at 0.75", effort: "xhigh", maxTokens: 32000, wantLength: "<max_thinking_length>24000</max_thinking_length>"},
		{name: "case-insensitive", effort: "XHigh", maxTokens: 32000, wantLength: "<max_thinking_length>24000</max_thinking_length>"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{
				"model":"claude-opus-4-1",
				"max_tokens":%d,
				"thinking":{"type":"adaptive"},
				"output_config":{"effort":%q},
				"messages":[{"role":"user","content":"hi"}]
			}`, tt.maxTokens, tt.effort))

			out, thinkingEnabled := BuildKiroPayload(body, "claude-opus-4-1", "", "CLI", false, false, nil, nil)
			if !thinkingEnabled {
				t.Fatalf("thinkingEnabled = false, want true (effort=%s)", tt.effort)
			}
			content := gjson.GetBytes(out, "conversationState.currentMessage.userInputMessage.content").String()
			if !strings.Contains(content, tt.wantLength) {
				t.Fatalf("effort=%s: content missing %q, content=%s", tt.effort, tt.wantLength, content)
			}
		})
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

func containsAll(s string, needles ...string) bool {
	for _, needle := range needles {
		if !strings.Contains(s, needle) {
			return false
		}
	}
	return true
}
