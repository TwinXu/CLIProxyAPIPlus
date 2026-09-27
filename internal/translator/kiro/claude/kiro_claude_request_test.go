package claude

import (
	"net/http"
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

// max_tokens=-1 means "use the maximum", which is the model's own ceiling. A fixed
// 32000 capped the Opus 4.7+/5/5.5 tier at a quarter of what it accepts. The
// model ids here are backend spellings, as mapModelToKiro hands them over.
func TestBuildKiroPayloadMaxTokensMinusOneUsesModelCeiling(t *testing.T) {
	for _, tc := range []struct {
		modelID string
		want    int64
	}{
		{"claude-opus-5.5", 128000},
		{"claude-opus-5", 128000},
		{"claude-sonnet-4.6", 64000},
		{"claude-haiku-4.5", 64000},
		// Not in the catalogue: keep the old ceiling rather than guess.
		{"brand-new-model", 32000},
	} {
		t.Run(tc.modelID, func(t *testing.T) {
			body := []byte(`{"model":"x","max_tokens":-1,"messages":[{"role":"user","content":"hi"}]}`)
			payload, _ := BuildKiroPayload(body, tc.modelID, "arn:test", "AI_EDITOR", false, false, nil, nil)
			if got := gjson.GetBytes(payload, "inferenceConfig.maxTokens").Int(); got != tc.want {
				t.Fatalf("inferenceConfig.maxTokens = %d, want %d\npayload: %s", got, tc.want, payload)
			}
		})
	}
}

// Claude Code sends the interleaved-thinking beta header on every request,
// including ones that switch thinking off. The body's explicit setting is the
// request's own and must win; the header alone still enables thinking.
func TestThinkingExplicitDisableOutranksBetaHeader(t *testing.T) {
	headers := http.Header{}
	headers.Set("Anthropic-Beta", "interleaved-thinking-2025-05-14")

	for _, tc := range []struct {
		name string
		body string
		want bool
	}{
		{"thinking disabled", `{"thinking":{"type":"disabled"}}`, false},
		{"zero budget", `{"thinking":{"type":"enabled","budget_tokens":0}}`, false},
		{"reasoning_effort none", `{"reasoning_effort":"none"}`, false},
		{"header alone", `{}`, true},
		{"adaptive", `{"thinking":{"type":"adaptive"}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsThinkingEnabledWithHeaders([]byte(tc.body), headers); got != tc.want {
				t.Fatalf("IsThinkingEnabledWithHeaders(%s) = %t, want %t", tc.body, got, tc.want)
			}
		})
	}

	// End to end: a disabled request must not carry adaptive thinking to the backend.
	body := []byte(`{"model":"x","max_tokens":1024,"thinking":{"type":"disabled"},"messages":[{"role":"user","content":"hi"}]}`)
	payload, enabled := BuildKiroPayload(body, "claude-opus-5", "arn:test", "AI_EDITOR", false, false, headers, nil)
	if enabled {
		t.Fatal("thinking reported enabled despite thinking.type=disabled")
	}
	if gjson.GetBytes(payload, "additionalModelRequestFields").Exists() {
		t.Fatalf("additionalModelRequestFields must be absent\npayload: %s", payload)
	}
}
