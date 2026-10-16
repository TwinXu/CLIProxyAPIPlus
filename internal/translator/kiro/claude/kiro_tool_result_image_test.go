package claude

import (
	"bytes"
	"encoding/base64"
	"image"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/tidwall/gjson"
)

// Kiro tool results carry text only. Images a tool returns used to be dropped
// silently, and an image-only result was rewritten as "Tool use was cancelled by
// the user", so agents that fetch images through tools never saw a pixel.

// onePixelPNG is a valid 1x1 PNG.
const onePixelPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVR4nGNg+M8AAAICAQB7CYF4AAAAAElFTkSuQmCC"

const getImageTool = `"tools":[{"name":"get_image","description":"Returns an image.","input_schema":{"type":"object","properties":{}}}]`

func toolResultTexts(result gjson.Result) []string {
	var texts []string
	for _, c := range result.Get("content").Array() {
		texts = append(texts, c.Get("text").String())
	}
	return texts
}

func currentUserInput(t *testing.T, body string) gjson.Result {
	t.Helper()
	out, _ := BuildKiroPayload([]byte(body), "claude-opus-4-1", "", "CLI", false, false, nil, nil)
	if !gjson.ValidBytes(out) {
		t.Fatalf("invalid payload: %s", string(out))
	}
	return gjson.GetBytes(out, "conversationState.currentMessage.userInputMessage")
}

func TestToolResultImageRidesOnCurrentMessage(t *testing.T) {
	msg := currentUserInput(t, `{"model":"claude-opus-4-1","max_tokens":1024,`+getImageTool+`,
		"messages":[
			{"role":"user","content":"Call get_image."},
			{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"get_image","input":{}}]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"toolu_1","content":[
					{"type":"image","source":{"type":"base64","media_type":"image/png","data":"`+onePixelPNG+`"}},
					{"type":"text","text":"fetched"}
				]},
				{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"SlBFR0RBVEE="}},
				{"type":"text","text":"What color is it?"}
			]}
		]}`)

	images := msg.Get("images").Array()
	if len(images) != 2 {
		t.Fatalf("images = %d, want 2 (tool image + user image): %s", len(images), msg.Get("images").Raw)
	}
	if images[0].Get("format").String() != "png" || images[0].Get("source.bytes").String() != onePixelPNG {
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

func TestToolImageNoteNamesItsPosition(t *testing.T) {
	msg := currentUserInput(t, `{"model":"claude-opus-4-1","max_tokens":1024,`+getImageTool+`,
		"messages":[
			{"role":"user","content":"Call get_image twice."},
			{"role":"assistant","content":[
				{"type":"tool_use","id":"toolu_a","name":"get_image","input":{}},
				{"type":"tool_use","id":"toolu_b","name":"get_image","input":{}}
			]},
			{"role":"user","content":[
				{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"SlBFR0RBVEE="}},
				{"type":"tool_result","tool_use_id":"toolu_a","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"`+onePixelPNG+`"}}]},
				{"type":"tool_result","tool_use_id":"toolu_b","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"`+onePixelPNG+`"}}]}
			]}
		]}`)

	if n := len(msg.Get("images").Array()); n != 3 {
		t.Fatalf("images = %d, want 3", n)
	}
	for i, want := range []string{"as image 2.", "as image 3."} {
		texts := toolResultTexts(msg.Get("userInputMessageContext.toolResults." + string(rune('0'+i))))
		if len(texts) != 1 || !strings.Contains(texts[0], want) {
			t.Fatalf("tool result %d texts = %q, want a note containing %q", i, texts, want)
		}
	}
}

func TestImageOnlyToolResultInHistoryKeepsImage(t *testing.T) {
	body := []byte(`{"model":"claude-opus-4-1","max_tokens":1024,` + getImageTool + `,
		"messages":[
			{"role":"user","content":"Call get_image."},
			{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"get_image","input":{}}]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"toolu_1","content":[
					{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + onePixelPNG + `"}}
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

// Kiro rejects the whole request for a malformed or unsupported image (measured
// 2026-10-16: bad base64, svg+xml, image/jpg and PNG bytes labelled image/jpeg
// all returned 400), so such tool images must not be forwarded.
func TestToolResultImageIsValidatedBeforeForwarding(t *testing.T) {
	svg := base64.StdEncoding.EncodeToString([]byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`))
	cases := []struct {
		name, source, wantFormat string
	}{
		{"url source", `{"type":"url","url":"https://example.com/a.png"}`, ""},
		{"malformed base64", `{"type":"base64","media_type":"image/png","data":"%%%"}`, ""},
		{"valid png head, corrupt tail", `{"type":"base64","media_type":"image/png","data":"` + onePixelPNG + `%%%"}`, ""},
		{"svg", `{"type":"base64","media_type":"image/svg+xml","data":"` + svg + `"}`, ""},
		{"png labelled jpeg", `{"type":"base64","media_type":"image/jpeg","data":"` + onePixelPNG + `"}`, "png"},
		{"png labelled jpg", `{"type":"base64","media_type":"image/jpg","data":"` + onePixelPNG + `"}`, "png"},
		{"jpeg labelled png", `{"type":"base64","media_type":"image/png","data":"` + eightPixelJpeg + `"}`, "jpeg"},
		{"gif labelled png", `{"type":"base64","media_type":"image/png","data":"` + eightPixelGif + `"}`, "gif"},
		{"webp is not forwarded", `{"type":"base64","media_type":"image/webp","data":"` + eightPixelWebp + `"}`, ""},
		{"empty webp envelope", `{"type":"base64","media_type":"image/webp","data":"UklGRggAAABXRUJQVlA4IA=="}`, ""},
		{"non-base64 source type with valid data", `{"type":"file","media_type":"image/png","data":"` + onePixelPNG + `"}`, ""},
		{"png signature only", `{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}`, ""},
		{"truncated png", `{"type":"base64","media_type":"image/png","data":"` + truncatedPng + `"}`, ""},
		{"truncated jpeg", `{"type":"base64","media_type":"image/jpeg","data":"` + truncatedJpeg + `"}`, ""},
		{"truncated gif", `{"type":"base64","media_type":"image/gif","data":"` + truncatedGif + `"}`, ""},
		{"truncated webp", `{"type":"base64","media_type":"image/webp","data":"` + truncatedWebp + `"}`, ""},
		{"wider than kiroMaxImageSide", `{"type":"base64","media_type":"image/png","data":"` + tooWidePNG + `"}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := currentUserInput(t, `{"model":"claude-opus-4-1","max_tokens":1024,`+getImageTool+`,
				"messages":[
					{"role":"user","content":"Call get_image."},
					{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"get_image","input":{}}]},
					{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[
						{"type":"image","source":`+tc.source+`},
						{"type":"text","text":"tool text"}
					]}]}
				]}`)
			images := msg.Get("images").Array()
			texts := strings.Join(toolResultTexts(msg.Get("userInputMessageContext.toolResults.0")), "\n")
			if !strings.Contains(texts, "tool text") || strings.Contains(texts, "cancelled") {
				t.Fatalf("tool result texts = %q, want the tool text kept and no cancellation", texts)
			}
			if tc.wantFormat == "" {
				if len(images) != 0 || !strings.Contains(texts, "could not be forwarded") {
					t.Fatalf("images = %s, texts = %q, want no image and the could-not-forward note", msg.Get("images").Raw, texts)
				}
				return
			}
			if len(images) != 1 || images[0].Get("format").String() != tc.wantFormat {
				t.Fatalf("images = %s, want one %s image", msg.Get("images").Raw, tc.wantFormat)
			}
		})
	}
}

func TestToolResultImageBytesAreCanonicalBase64(t *testing.T) {
	wrapped := onePixelPNG[:40] + `\n` + onePixelPNG[40:]
	msg := currentUserInput(t, `{"model":"claude-opus-4-1","max_tokens":1024,`+getImageTool+`,
		"messages":[
			{"role":"user","content":"Call get_image."},
			{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"get_image","input":{}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[
				{"type":"image","source":{"type":"base64","media_type":"image/png","data":"`+wrapped+`"}}
			]}]}
		]}`)
	if got := msg.Get("images.0.source.bytes").String(); got != onePixelPNG {
		t.Fatalf("bytes = %q, want the line break removed: %q", got, onePixelPNG)
	}
}

// Compaction can keep a tool_result whose tool_use is gone. The orphan result is
// dropped, and its image must go with it rather than linger without context.
func TestOrphanToolResultImageIsDroppedWithItsResult(t *testing.T) {
	t.Run("current message", func(t *testing.T) {
		msg := currentUserInput(t, `{"model":"claude-opus-4-1","max_tokens":1024,"messages":[
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"compacted","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"`+onePixelPNG+`"}}]},
				{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"SlBFR0RBVEE="}},
				{"type":"text","text":"Continue from the summary."}
			]}
		]}`)
		images := msg.Get("images").Array()
		if len(images) != 1 || images[0].Get("format").String() != "jpeg" {
			t.Fatalf("images = %s, want only the user's own jpeg", msg.Get("images").Raw)
		}
		if n := msg.Get("userInputMessageContext.toolResults.#").Int(); n != 0 {
			t.Fatalf("tool results = %d, want the orphan dropped", n)
		}
	})

	t.Run("history message", func(t *testing.T) {
		out, _ := BuildKiroPayload([]byte(`{"model":"claude-opus-4-1","max_tokens":1024,"messages":[
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"compacted","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"`+onePixelPNG+`"}}]},
				{"type":"text","text":"Summary of earlier work."}
			]},
			{"role":"assistant","content":"Understood."},
			{"role":"user","content":"Next step."}
		]}`), "claude-opus-4-1", "", "CLI", false, false, nil, nil)
		for i, h := range gjson.GetBytes(out, "conversationState.history").Array() {
			if n := len(h.Get("userInputMessage.images").Array()); n != 0 {
				t.Fatalf("history[%d] images = %d, want 0: %s", i, n, h.Raw)
			}
			if n := h.Get("userInputMessage.userInputMessageContext.toolResults.#").Int(); n != 0 {
				t.Fatalf("history[%d] keeps %d orphan tool results: %s", i, n, h.Raw)
			}
		}
	})
}

// toolUseIDs must name exactly the tool uses BuildAssistantMessageStruct emits;
// the orphan filter used to derive its set from that output.
func TestToolUseIDsMatchEmittedToolUses(t *testing.T) {
	inputs := map[string]string{
		"array content":     `[{"role":"assistant","content":[{"type":"text","text":"x"},{"type":"tool_use","id":"a","name":"n","input":{}}]}]`,
		"string content":    `[{"role":"assistant","content":"tool_use a"}]`,
		"object content":    `[{"role":"assistant","content":{"type":"tool_use","id":"a","name":"n","input":{}}}]`,
		"duplicate ids":     `[{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"n","input":{}},{"type":"tool_use","id":"a","name":"n","input":{}}]}]`,
		"empty id":          `[{"role":"assistant","content":[{"type":"tool_use","id":"","name":"n","input":{}}]}]`,
		"web_search":        `[{"role":"assistant","content":[{"type":"tool_use","id":"w","name":"web_search","input":{"query":"q"}}]}]`,
		"tool_use in user":  `[{"role":"user","content":[{"type":"tool_use","id":"u","name":"n","input":{}}]}]`,
		"text part with id": `[{"role":"assistant","content":[{"type":"text","id":"x","text":"hi"}]}]`,
	}
	for name, raw := range inputs {
		messages := gjson.Parse(raw).Array()
		emitted := make(map[string]bool)
		for _, msg := range messages {
			if msg.Get("role").String() == "assistant" {
				for _, tu := range BuildAssistantMessageStruct(msg).ToolUses {
					emitted[tu.ToolUseID] = true
				}
			}
		}
		got := toolUseIDs(messages)
		if len(got) != len(emitted) {
			t.Fatalf("%s: toolUseIDs = %v, emitted = %v", name, got, emitted)
		}
		for id := range emitted {
			if !got[id] {
				t.Fatalf("%s: toolUseIDs = %v, emitted = %v", name, got, emitted)
			}
		}
	}
}

// A verdict is cached per data string, so a second sighting (the next turn's
// history) must give the same result as the first.
// pngBomb is 66 bytes declaring an 8000x8000 16-bit RGBA image with no pixels;
// decoding it would allocate 512 MB before failing.
const pngBomb = "iVBORw0KGgoAAAANSUhEUgAAH0AAAB9AEAYAAABWYXG3AAAACUlEQVR4nGMAAAABAAFe/335AAAAAElFTkSuQmCC"

func TestToolImageCheckBoundsMemoryBeforeDecoding(t *testing.T) {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, ok := kiroToolImageFromBlock(gjson.Parse(`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + pngBomb + `"}}`))
	runtime.ReadMemStats(&after)
	if ok {
		t.Fatal("an image declaring 8000x8000 16-bit pixels was forwarded")
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 8<<20 {
		t.Fatalf("checking the bomb allocated %d bytes, want under 8 MiB", grew)
	}
}

// The memory bound is only as good as decodeAllocEstimate, so it must cover what
// the decoder really allocates for the worst layout of each format. The
// fixtures are a progressive 4:4:4 JPEG, interlaced 8- and 16-bit RGBA PNGs and
// a Pillow GIF, the heaviest cases measured.
func TestDecodeAllocEstimateCoversActualAllocation(t *testing.T) {
	for _, name := range []string{"progressive_444.jpg", "interlaced_rgba8.png", "interlaced_rgba16.png", "pillow_gradient.gif"} {
		b, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		cfg, format, err := image.DecodeConfig(bytes.NewReader(b))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		estimate := decodeAllocEstimate(format, cfg, len(b))
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		if _, _, err := image.Decode(bytes.NewReader(b)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		runtime.ReadMemStats(&after)
		if used := after.TotalAlloc - before.TotalAlloc; used > estimate {
			t.Errorf("%s: decoding allocated %d bytes, estimate %d", name, used, estimate)
		}
	}
}

func TestToolImageChecksAgreeUnderConcurrency(t *testing.T) {
	inputs := map[string]bool{onePixelPNG: true, eightPixelJpeg: true, eightPixelGif: true, truncatedPng: false, truncatedJpeg: false, pngBomb: false}
	var wg sync.WaitGroup
	errs := make(chan string, 64)
	for i := 0; i < 8; i++ {
		for data, want := range inputs {
			wg.Add(1)
			go func(data string, want bool) {
				defer wg.Done()
				if _, ok := kiroToolImageFromBlock(gjson.Parse(`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + data + `"}}`)); ok != want {
					errs <- data[:12]
				}
			}(data, want)
		}
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Errorf("concurrent check of %s... gave the wrong verdict", e)
	}
}

func TestToolImageVerdictIsStableAcrossTurns(t *testing.T) {
	wrapped := onePixelPNG[:30] + "\n" + onePixelPNG[30:]
	for turn := 0; turn < 2; turn++ {
		img, ok := kiroToolImageFromBlock(gjson.Parse(`{"type":"image","source":{"type":"base64","media_type":"image/png","data":` + strconvQuote(wrapped) + `}}`))
		if !ok || img.Source.Bytes != onePixelPNG {
			t.Fatalf("turn %d: wrapped image = %v %q, want canonical bytes", turn, ok, img.Source.Bytes)
		}
		if _, ok := kiroToolImageFromBlock(gjson.Parse(`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + truncatedPng + `"}}`)); ok {
			t.Fatalf("turn %d: truncated image forwarded", turn)
		}
	}
}

func strconvQuote(s string) string {
	return `"` + strings.ReplaceAll(s, "\n", `\n`) + `"`
}

// Fixtures generated with Pillow: 8x8 solid green images and an 8001x1 PNG.
const eightPixelJpeg = "/9j/4AAQSkZJRgABAQAAAQABAAD/2wBDAAgGBgcGBQgHBwcJCQgKDBQNDAsLDBkSEw8UHRofHh0aHBwgJC4nICIsIxwcKDcpLDAxNDQ0Hyc5PTgyPC4zNDL/2wBDAQkJCQwLDBgNDRgyIRwhMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjL/wAARCAAIAAgDASIAAhEBAxEB/8QAHwAAAQUBAQEBAQEAAAAAAAAAAAECAwQFBgcICQoL/8QAtRAAAgEDAwIEAwUFBAQAAAF9AQIDAAQRBRIhMUEGE1FhByJxFDKBkaEII0KxwRVS0fAkM2JyggkKFhcYGRolJicoKSo0NTY3ODk6Q0RFRkdISUpTVFVWV1hZWmNkZWZnaGlqc3R1dnd4eXqDhIWGh4iJipKTlJWWl5iZmqKjpKWmp6ipqrKztLW2t7i5usLDxMXGx8jJytLT1NXW19jZ2uHi4+Tl5ufo6erx8vP09fb3+Pn6/8QAHwEAAwEBAQEBAQEBAQAAAAAAAAECAwQFBgcICQoL/8QAtREAAgECBAQDBAcFBAQAAQJ3AAECAxEEBSExBhJBUQdhcRMiMoEIFEKRobHBCSMzUvAVYnLRChYkNOEl8RcYGRomJygpKjU2Nzg5OkNERUZHSElKU1RVVldYWVpjZGVmZ2hpanN0dXZ3eHl6goOEhYaHiImKkpOUlZaXmJmaoqOkpaanqKmqsrO0tba3uLm6wsPExcbHyMnK0tPU1dbX2Nna4uPk5ebn6Onq8vP09fb3+Pn6/9oADAMBAAIRAxEAPwDnaKKK+QPgz//Z"
const eightPixelGif = "R0lGODdhCAAIAIEAAACgAAAAAAAAAAAAACwAAAAACAAIAAAIDwABCBxIsKDBgwgTKkwYEAA7"
const eightPixelWebp = "UklGRjwAAABXRUJQVlA4IDAAAAAQAgCdASoIAAgAAUAmJaACdLoB+AH4AAPIAP7vdtf+mijCmPuTf/Z3NqpD+3cAAAA="
const truncatedJpeg = "/9j/4AAQSkZJRgABAQAAAQABAAD/2wBDAAgGBgcGBQgHBwcJCQgKDBQNDAsLDBkSEw8UHRofHh0aHBwgJC4nICIsIxwcKDcpLDAxNDQ0Hyc5PTgyPC4zNDL/2wBDAQkJCQwLDBgNDRgyIRwhMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjL/wAARCAAIAAgDASIAAhEBAxEB/8QAHwAAAQUBAQEBAQEAAAAAAAAAAAECAwQFBgcICQoL/8QAtRAAAgEDAwIEAwUFBAQAAAF9AQIDAAQRBRIhMUEGE1FhByJxFDKBkaEII0KxwRVS0fAkM2JyggkKFhcYGRolJicoKSo0NTY3ODk6Q0RFRkdISUpTVFVWV1hZWmNkZWZnaGlqc3R1dg=="
const truncatedGif = "R0lGODdhCAAIAIEAAACgAAAAAAAAAAAAACwA"
const truncatedWebp = "UklGRjwAAABXRUJQVlA4IDAAAAAQAgCdASoIAAgAAUAmJQ=="
const truncatedPng = "iVBORw0KGgoAAAANSUhEUgAAAAgAAAAICAIAAABLbSncAAAAE0k="
const tooWidePNG = "iVBORw0KGgoAAAANSUhEUgAAH0EAAAABCAIAAAAzw2RzAAAAMUlEQVR4nO3BAQ0AAAjAoNu/hFHdzAFMGwAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAvQNYPQCibHM58wAAAABJRU5ErkJggg=="
