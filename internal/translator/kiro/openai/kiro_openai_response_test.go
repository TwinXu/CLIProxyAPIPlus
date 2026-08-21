package openai

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
)

func TestBuildOpenAIResponseIncludesKiroCacheUsage(t *testing.T) {
	out := BuildOpenAIResponse("hello", nil, "kiro-claude", usage.Detail{
		InputTokens:              10,
		OutputTokens:             2,
		CacheReadInputTokens:     7,
		CacheCreationInputTokens: 3,
		CachedTokens:             10,
		TotalTokens:              22,
	}, "end_turn")

	if got := gjson.GetBytes(out, "usage.prompt_tokens").Int(); got != 20 {
		t.Fatalf("prompt_tokens = %d, want uncached+cached 20", got)
	}
	if got := gjson.GetBytes(out, "usage.prompt_tokens_details.cached_tokens").Int(); got != 10 {
		t.Fatalf("cached_tokens = %d, want 10", got)
	}
	if got := gjson.GetBytes(out, "usage.total_tokens").Int(); got != 22 {
		t.Fatalf("total_tokens = %d, want upstream total 22", got)
	}
}

func TestConvertKiroStreamToOpenAIIncludesKiroCacheUsage(t *testing.T) {
	var param any
	chunks := ConvertKiroStreamToOpenAI(
		context.Background(),
		"kiro-claude",
		nil,
		nil,
		[]byte(`event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":10,"output_tokens":2,"cache_read_input_tokens":7,"cache_creation_input_tokens":3}}
`),
		&param,
	)

	var usageChunk []byte
	for _, chunk := range chunks {
		if gjson.GetBytes(chunk, "usage").Exists() {
			usageChunk = chunk
		}
	}
	if len(usageChunk) == 0 {
		t.Fatalf("expected usage chunk, got %d chunks", len(chunks))
	}
	if got := gjson.GetBytes(usageChunk, "usage.prompt_tokens").Int(); got != 20 {
		t.Fatalf("prompt_tokens = %d, want uncached+cached 20", got)
	}
	if got := gjson.GetBytes(usageChunk, "usage.prompt_tokens_details.cached_tokens").Int(); got != 10 {
		t.Fatalf("cached_tokens = %d, want 10", got)
	}
}

// The OpenAI surface derives reasoning_content from the Claude response's thinking
// blocks, so a non-streaming Kiro response that carries the backend's native
// reasoning has to arrive with it rather than dropped alongside it.
func TestConvertKiroNonStreamToOpenAICarriesNativeReasoning(t *testing.T) {
	claudeResponse := []byte(`{"type":"message","role":"assistant","model":"kiro-claude-opus-5",` +
		`"content":[{"type":"thinking","thinking":"Weighing the options.","signature":"sig"},` +
		`{"type":"text","text":"Here you go."}],"stop_reason":"end_turn",` +
		`"usage":{"input_tokens":10,"output_tokens":5}}`)

	out := ConvertKiroNonStreamToOpenAI(context.Background(), "kiro-claude-opus-5", nil, nil, claudeResponse, nil)

	if got := gjson.GetBytes(out, "choices.0.message.reasoning_content").String(); got != "Weighing the options." {
		t.Fatalf("reasoning_content = %q, want the thinking block", got)
	}
	if got := gjson.GetBytes(out, "choices.0.message.content").String(); got != "Here you go." {
		t.Fatalf("content = %q, want the text block only", got)
	}
}
