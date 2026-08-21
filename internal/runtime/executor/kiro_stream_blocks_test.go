package executor

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"strings"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

// kiroFrame encodes one AWS event-stream frame the way the Kiro API sends them:
// a 12-byte prelude, the :event-type header, the JSON payload, and a trailing
// CRC. Both CRCs are zero because readEventStreamMessage skips them.
func kiroFrame(eventType, payload string) []byte {
	var headers []byte
	const name = ":event-type"
	headers = append(headers, byte(len(name)))
	headers = append(headers, name...)
	headers = append(headers, 7) // string value type
	headers = append(headers, byte(len(eventType)>>8), byte(len(eventType)))
	headers = append(headers, eventType...)

	total := 12 + len(headers) + len(payload) + 4
	frame := make([]byte, 0, total)
	var word [4]byte
	binary.BigEndian.PutUint32(word[:], uint32(total))
	frame = append(frame, word[:]...)
	binary.BigEndian.PutUint32(word[:], uint32(len(headers)))
	frame = append(frame, word[:]...)
	frame = append(frame, 0, 0, 0, 0) // prelude CRC, not validated
	frame = append(frame, headers...)
	frame = append(frame, payload...)
	frame = append(frame, 0, 0, 0, 0) // message CRC, not validated
	return frame
}

// runKiroStream feeds the frames through streamToChannel and returns the SSE text
// the client would receive.
func runKiroStream(t *testing.T, frames ...[]byte) string {
	t.Helper()
	e := &KiroExecutor{}
	out := make(chan cliproxyexecutor.StreamChunk, 512)
	claudeBody := []byte(`{"model":"kiro-claude-opus-5","messages":[{"role":"user","content":"hi"}]}`)

	done := make(chan string, 1)
	go func() {
		var sb strings.Builder
		for chunk := range out {
			if chunk.Err != nil {
				continue
			}
			sb.Write(chunk.Payload)
		}
		done <- sb.String()
	}()

	e.streamToChannel(context.Background(), bytes.NewReader(bytes.Join(frames, nil)), out,
		sdktranslator.FromString("claude"), "kiro-claude-opus-5", claudeBody, claudeBody, nil, true)
	close(out)
	return <-done
}

// sseEvents extracts the parsed `data:` objects from an SSE body.
func sseEvents(t *testing.T, body string) []map[string]any {
	t.Helper()
	var events []map[string]any
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var ev map[string]any
		if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &ev) == nil {
			events = append(events, ev)
		}
	}
	return events
}

// assertBlocksBalanced fails if any content_block_start lacks a matching
// content_block_stop at the same index.
func assertBlocksBalanced(t *testing.T, body string) {
	t.Helper()
	opened := map[float64]string{}
	var order []float64
	for _, ev := range sseEvents(t, body) {
		idx, _ := ev["index"].(float64)
		switch ev["type"] {
		case "content_block_start":
			blockType := ""
			if cb, ok := ev["content_block"].(map[string]any); ok {
				blockType, _ = cb["type"].(string)
			}
			opened[idx] = blockType
			order = append(order, idx)
		case "content_block_stop":
			delete(opened, idx)
		}
	}
	if len(opened) > 0 {
		for _, idx := range order {
			if blockType, still := opened[idx]; still {
				t.Errorf("content_block_start index=%v type=%q never received a content_block_stop", idx, blockType)
			}
		}
		t.Logf("stream body:\n%s", body)
	}
}

// A native reasoningContentEvent opens a thinking block that only the tag-based
// parser used to close, so a stream that reasoned and then spoke, called a tool,
// or simply ended left that block open.
func TestKiroStreamClosesThinkingBlock(t *testing.T) {
	reasoning := kiroFrame("reasoningContentEvent", `{"reasoningContentEvent":{"text":"Weighing the options."}}`)

	tests := []struct {
		name   string
		frames [][]byte
	}{
		{
			name:   "reasoning then text",
			frames: [][]byte{reasoning, kiroFrame("assistantResponseEvent", `{"assistantResponseEvent":{"content":"Here you go."}}`)},
		},
		{
			name: "reasoning then tool use",
			frames: [][]byte{reasoning, kiroFrame("toolUseEvent",
				`{"toolUseEvent":{"toolUseId":"tu_1","name":"Bash","input":"{\"command\":\"uname -a\"}","stop":true}}`)},
		},
		{
			name:   "reasoning only",
			frames: [][]byte{reasoning},
		},
		{
			name: "reasoning, text, then tool use",
			frames: [][]byte{reasoning,
				kiroFrame("assistantResponseEvent", `{"assistantResponseEvent":{"content":"Running it."}}`),
				kiroFrame("toolUseEvent", `{"toolUseEvent":{"toolUseId":"tu_2","name":"Bash","input":"{\"command\":\"ls\"}","stop":true}}`)},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assertBlocksBalanced(t, runKiroStream(t, tc.frames...))
		})
	}
}

// Kiro reports the AWS enum spelling; clients match the lowercase spec values.
func TestKiroStreamNormalizesStopReason(t *testing.T) {
	body := runKiroStream(t,
		kiroFrame("assistantResponseEvent", `{"assistantResponseEvent":{"content":"Done."}}`),
		kiroFrame("messageStopEvent", `{"stopReason":"END_TURN"}`),
	)
	for _, ev := range sseEvents(t, body) {
		if ev["type"] != "message_delta" {
			continue
		}
		delta, _ := ev["delta"].(map[string]any)
		if got := delta["stop_reason"]; got != "end_turn" {
			t.Fatalf("stop_reason = %v, want %q", got, "end_turn")
		}
		return
	}
	t.Fatalf("no message_delta event found in:\n%s", body)
}
