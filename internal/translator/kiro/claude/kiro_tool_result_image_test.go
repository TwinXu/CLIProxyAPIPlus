package claude

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// Kiro tool results carry text only. Images a tool returns used to be dropped
// silently, and an image-only result was rewritten as "Tool use was cancelled by
// the user", so agents that fetch images through tools never saw a pixel.

func toolResultTexts(result gjson.Result) []string {
	var texts []string
	for _, c := range result.Get("content").Array() {
		texts = append(texts, c.Get("text").String())
	}
	return texts
}

func TestToolResultImageRidesOnCurrentMessage(t *testing.T) {
	body := []byte(`{
		"model":"claude-opus-4-1","max_tokens":1024,
		"tools":[{"name":"get_image","description":"Returns an image.","input_schema":{"type":"object","properties":{}}}],
		"messages":[
			{"role":"user","content":"Call get_image."},
			{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"get_image","input":{}}]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"toolu_1","content":[
					{"type":"image","source":{"type":"base64","media_type":"image/png","data":"UE5HREFUQQ=="}},
					{"type":"text","text":"fetched"}
				]},
				{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"SlBFR0RBVEE="}},
				{"type":"text","text":"What color is it?"}
			]}
		]}`)

	out, _ := BuildKiroPayload(body, "claude-opus-4-1", "", "CLI", false, false, nil, nil)
	msg := gjson.GetBytes(out, "conversationState.currentMessage.userInputMessage")

	images := msg.Get("images").Array()
	if len(images) != 2 {
		t.Fatalf("images = %d, want 2 (tool image + user image): %s", len(images), msg.Get("images").Raw)
	}
	if images[0].Get("format").String() != "png" || images[0].Get("source.bytes").String() != "UE5HREFUQQ==" {
		t.Fatalf("first image is not the tool image: %s", images[0].Raw)
	}
	if images[1].Get("format").String() != "jpeg" {
		t.Fatalf("second image is not the user image: %s", images[1].Raw)
	}

	texts := toolResultTexts(msg.Get("userInputMessageContext.toolResults.0"))
	joined := strings.Join(texts, "\n")
	if !strings.Contains(joined, "attached to this message as image 1.") || !strings.Contains(joined, "fetched") {
		t.Fatalf("tool result texts = %q, want the image note and the original text", texts)
	}
	if strings.Contains(joined, "cancelled") {
		t.Fatalf("tool result must not claim cancellation: %q", texts)
	}
}

func TestImageOnlyToolResultInHistoryKeepsImage(t *testing.T) {
	body := []byte(`{
		"model":"claude-opus-4-1","max_tokens":1024,
		"tools":[{"name":"get_image","description":"Returns an image.","input_schema":{"type":"object","properties":{}}}],
		"messages":[
			{"role":"user","content":"Call get_image."},
			{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"get_image","input":{}}]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"toolu_1","content":[
					{"type":"image","source":{"type":"base64","media_type":"image/png","data":"UE5HREFUQQ=="}}
				]}
			]},
			{"role":"assistant","content":"It is green."},
			{"role":"user","content":"Thanks."}
		]}`)

	out, _ := BuildKiroPayload(body, "claude-opus-4-1", "", "CLI", false, false, nil, nil)

	var holder gjson.Result
	for _, h := range gjson.GetBytes(out, "conversationState.history").Array() {
		if h.Get("userInputMessage.userInputMessageContext.toolResults.#").Int() > 0 {
			holder = h.Get("userInputMessage")
		}
	}
	if !holder.Exists() {
		t.Fatalf("no history message carries the tool result: %s", string(out))
	}
	if n := len(holder.Get("images").Array()); n != 1 {
		t.Fatalf("history images = %d, want 1: %s", n, holder.Raw)
	}
	texts := toolResultTexts(holder.Get("userInputMessageContext.toolResults.0"))
	if len(texts) != 1 || !strings.Contains(texts[0], "attached to this message as image 1.") {
		t.Fatalf("tool result texts = %q, want only the image note", texts)
	}
}

func TestUnforwardableToolResultImageIsNotReportedAsCancelled(t *testing.T) {
	body := []byte(`{
		"model":"claude-opus-4-1","max_tokens":1024,
		"tools":[{"name":"get_image","description":"Returns an image.","input_schema":{"type":"object","properties":{}}}],
		"messages":[
			{"role":"user","content":"Call get_image."},
			{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"get_image","input":{}}]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"toolu_1","content":[
					{"type":"image","source":{"type":"url","url":"https://example.com/a.png"}}
				]}
			]}
		]}`)

	out, _ := BuildKiroPayload(body, "claude-opus-4-1", "", "CLI", false, false, nil, nil)
	msg := gjson.GetBytes(out, "conversationState.currentMessage.userInputMessage")

	if n := len(msg.Get("images").Array()); n != 0 {
		t.Fatalf("images = %d, want 0 for a url-sourced image", n)
	}
	texts := toolResultTexts(msg.Get("userInputMessageContext.toolResults.0"))
	if len(texts) != 1 || !strings.Contains(texts[0], "could not be forwarded") {
		t.Fatalf("tool result texts = %q, want the could-not-forward note", texts)
	}
}
