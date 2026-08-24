package executor

import (
	"encoding/json"
	"strings"
	"testing"
)

// joinTextDeltas concatenates every content_block_delta text_delta in an SSE body,
// which is exactly what a streaming client accumulates as the assistant message.
func joinTextDeltas(t *testing.T, body string) string {
	t.Helper()
	var sb strings.Builder
	for _, ev := range sseEvents(t, body) {
		if ev["type"] != "content_block_delta" {
			continue
		}
		delta, ok := ev["delta"].(map[string]any)
		if !ok {
			continue
		}
		if delta["type"] != "text_delta" {
			continue
		}
		if text, ok := delta["text"].(string); ok {
			sb.WriteString(text)
		}
	}
	return sb.String()
}

// Once a native reasoningContentEvent has been seen, every assistantResponseEvent
// chunk used to be TrimSpace'd before being forwarded. Upstream splits a sentence
// at arbitrary offsets, so any chunk boundary that landed on a space silently
// deleted it: "let me run it" arrived as "letme run it". Whitespace-only chunks —
// the blank lines between paragraphs and the indentation of fenced code — were
// dropped entirely by the non-empty guard.
func TestKiroStreamPreservesWhitespaceAcrossChunkBoundaries(t *testing.T) {
	reasoning := kiroFrame("reasoningContentEvent", `{"reasoningContentEvent":{"text":"Deciding what to say."}}`)

	tests := []struct {
		name   string
		chunks []string
	}{
		{
			name:   "space at the trailing edge of a chunk",
			chunks: []string{"I do not have a real sh", "ell result to report yet - let ", "me run it."},
		},
		{
			name:   "space at the leading edge of a chunk",
			chunks: []string{"<parameter", " name=\"command\">date -Is"},
		},
		{
			name:   "whitespace-only chunk between paragraphs",
			chunks: []string{"first line", "\n\n", "second line"},
		},
		{
			name:   "indentation survives a chunk of its own",
			chunks: []string{"def main():", "\n", "    ", "return 0"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			frames := [][]byte{reasoning}
			for _, chunk := range tc.chunks {
				payload, err := marshalAssistantContent(chunk)
				if err != nil {
					t.Fatalf("marshal chunk %q: %v", chunk, err)
				}
				frames = append(frames, kiroFrame("assistantResponseEvent", payload))
			}

			body := runKiroStream(t, frames...)
			want := strings.Join(tc.chunks, "")
			if got := joinTextDeltas(t, body); got != want {
				t.Errorf("assistant text mangled\n got: %q\nwant: %q", got, want)
			}
			assertBlocksBalanced(t, body)
		})
	}
}

// The leading newlines a model emits between its reasoning and its first word are
// formatting noise from the thinking block, not content. They must not open a text
// block of their own, and they must not survive into the answer.
func TestKiroStreamDropsWhitespacePrologueAfterReasoning(t *testing.T) {
	payload, err := marshalAssistantContent("\n\n")
	if err != nil {
		t.Fatalf("marshal prologue: %v", err)
	}
	first, err := marshalAssistantContent("Answer.")
	if err != nil {
		t.Fatalf("marshal answer: %v", err)
	}

	body := runKiroStream(t,
		kiroFrame("reasoningContentEvent", `{"reasoningContentEvent":{"text":"Thinking."}}`),
		kiroFrame("assistantResponseEvent", payload),
		kiroFrame("assistantResponseEvent", first),
	)

	if got, want := joinTextDeltas(t, body), "Answer."; got != want {
		t.Errorf("whitespace prologue leaked into the answer\n got: %q\nwant: %q", got, want)
	}
	assertBlocksBalanced(t, body)
}

// marshalAssistantContent builds an assistantResponseEvent payload with content
// escaped as JSON, so chunks may contain quotes, newlines and tabs.
func marshalAssistantContent(content string) (string, error) {
	encoded, err := json.Marshal(content)
	if err != nil {
		return "", err
	}
	return `{"assistantResponseEvent":{"content":` + string(encoded) + `}}`, nil
}
