package executor

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	kiroclaude "github.com/router-for-me/CLIProxyAPI/v6/internal/translator/kiro/claude"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

// runKiroNonStream feeds frames through the buffered reader and assembles the Claude
// JSON body the way executeWithRetry does, so these tests exercise the same two steps
// a stream:false request takes.
func runKiroNonStream(t *testing.T, frames ...[]byte) []map[string]any {
	t.Helper()
	e := &KiroExecutor{}
	parsed, err := e.parseEventStream(bytes.NewReader(bytes.Join(frames, nil)), "kiro-claude-opus-5")
	if err != nil {
		t.Fatalf("parseEventStream() error = %v", err)
	}
	body := kiroclaude.BuildClaudeResponseWithReasoning(parsed.Content, parsed.Reasoning, parsed.ToolUses,
		"kiro-claude-opus-5", usage.Detail{}, parsed.StopReason)

	var resp struct {
		Content []map[string]any `json:"content"`
	}
	if err = json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal response: %v (body: %s)", err, body)
	}
	return resp.Content
}

// blockTypes names the content blocks in order, for a readable failure message.
func blockTypes(blocks []map[string]any) []string {
	types := make([]string, 0, len(blocks))
	for _, block := range blocks {
		name, _ := block["type"].(string)
		types = append(types, name)
	}
	return types
}

// The buffered reader had no case for reasoningContentEvent, so the reasoning fell
// through to default, was logged as an unknown event, and every stream:false response
// arrived without the thinking block its streaming twin carried.
func TestKiroNonStreamEmitsThinkingBlock(t *testing.T) {
	blocks := runKiroNonStream(t,
		kiroFrame("reasoningContentEvent", `{"reasoningContentEvent":{"text":"Weighing the options."}}`),
		kiroFrame("reasoningContentEvent", `{"reasoningContentEvent":{"text":" Settled.","signature":"sig-from-kiro"}}`),
		kiroFrame("assistantResponseEvent", `{"assistantResponseEvent":{"content":"Here you go."}}`),
	)

	if got := blockTypes(blocks); len(got) != 2 || got[0] != "thinking" || got[1] != "text" {
		t.Fatalf("content blocks = %v, want [thinking text]", got)
	}
	if got := blocks[0]["thinking"]; got != "Weighing the options. Settled." {
		t.Errorf("thinking = %q, want the reasoning events concatenated in order", got)
	}
	if got := blocks[0]["signature"]; got != "sig-from-kiro" {
		t.Errorf("signature = %q, want the one the backend attached", got)
	}
	if got := blocks[1]["text"]; got != "Here you go." {
		t.Errorf("text = %q, want %q", got, "Here you go.")
	}
}

// Anthropic requires a signature on a thinking block, so one is derived when the
// backend attaches none.
func TestKiroNonStreamDerivesMissingSignature(t *testing.T) {
	blocks := runKiroNonStream(t,
		kiroFrame("reasoningContentEvent", `{"reasoningContentEvent":{"text":"No signature on this one."}}`),
		kiroFrame("assistantResponseEvent", `{"assistantResponseEvent":{"content":"Done."}}`),
	)
	if got, _ := blocks[0]["signature"].(string); got == "" {
		t.Fatalf("signature is empty, want a derived one")
	}
}

// A backend that emits reasoning natively never writes <thinking> tags, so a tag in
// the text is the model's own prose. Scanning for it would eat the rest of the answer
// into a thinking block -- the false positive the streaming reader avoids by disabling
// tag parsing once a reasoning event has arrived.
func TestKiroNonStreamKeepsLiteralThinkingTagAsText(t *testing.T) {
	blocks := runKiroNonStream(t,
		kiroFrame("reasoningContentEvent", `{"reasoningContentEvent":{"text":"They asked about the tag."}}`),
		kiroFrame("assistantResponseEvent", `{"assistantResponseEvent":{"content":"Write <thinking> to open the block."}}`),
	)

	if got := blockTypes(blocks); len(got) != 2 || got[0] != "thinking" || got[1] != "text" {
		t.Fatalf("content blocks = %v, want [thinking text]", got)
	}
	if got, _ := blocks[1]["text"].(string); !strings.Contains(got, "<thinking>") {
		t.Errorf("text = %q, want the literal tag left in place", got)
	}
}

// Without a native reasoning event the legacy tag path still applies, for whatever
// endpoint or model still answers that way.
func TestKiroNonStreamStillReadsThinkingTags(t *testing.T) {
	blocks := runKiroNonStream(t,
		kiroFrame("assistantResponseEvent", `{"assistantResponseEvent":{"content":"<thinking>Planning.</thinking>The answer."}}`),
	)

	if got := blockTypes(blocks); len(got) != 2 || got[0] != "thinking" || got[1] != "text" {
		t.Fatalf("content blocks = %v, want [thinking text]", got)
	}
	if got := blocks[0]["thinking"]; got != "Planning." {
		t.Errorf("thinking = %q, want %q", got, "Planning.")
	}
}

// An empty reasoning event still proves the backend reasons natively, so it disables
// the tag scan on its own. Both readers key off the event's arrival rather than its
// text; a stream and a buffered read of the same response must not disagree here.
func TestKiroNonStreamEmptyReasoningEventStillDisablesTagScan(t *testing.T) {
	blocks := runKiroNonStream(t,
		kiroFrame("reasoningContentEvent", `{"reasoningContentEvent":{"signature":"sig"}}`),
		kiroFrame("assistantResponseEvent", `{"assistantResponseEvent":{"content":"<thinking>Planning.</thinking>The answer."}}`),
	)

	if got := blockTypes(blocks); len(got) != 1 || got[0] != "text" {
		t.Fatalf("content blocks = %v, want [text]", got)
	}
	if got, _ := blocks[0]["text"].(string); !strings.Contains(got, "<thinking>") {
		t.Errorf("text = %q, want the literal tags left in place", got)
	}
}
