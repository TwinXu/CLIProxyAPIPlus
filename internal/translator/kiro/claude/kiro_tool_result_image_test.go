package claude

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
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

// pngBomb is 66 bytes declaring an 8000x8000 16-bit RGBA image with no pixels;
// the standard decoder allocates 512 MB for it before failing.
const pngBomb = "iVBORw0KGgoAAAANSUhEUgAAH0AAAB9AEAYAAABWYXG3AAAACUlEQVR4nGMAAAABAAFe/335AAAAAElFTkSuQmCC"

// allocated returns the bytes f allocates.
func allocated(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

func TestToolImageCheckBoundsMemoryBeforeDecoding(t *testing.T) {
	var ok bool
	grew := allocated(func() {
		_, ok = kiroToolImageFromBlock(gjson.Parse(`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + pngBomb + `"}}`))
	})
	if ok {
		t.Fatal("an image declaring 8000x8000 16-bit pixels was forwarded")
	}
	if grew > 8<<20 {
		t.Fatalf("checking the bomb allocated %d bytes, want under 8 MiB", grew)
	}
}

// Valid files in the layouts hardest to parse: progressive 4:4:4 JPEG, Adam7
// interlaced 8- and 16-bit RGBA PNGs, and a Pillow GIF.
func TestWholeImagesInTrickyLayoutsAreForwarded(t *testing.T) {
	for name, format := range map[string]string{"progressive_444.jpg": "jpeg", "interlaced_rgba8.png": "png", "interlaced_rgba16.png": "png", "pillow_gradient.gif": "gif"} {
		b, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		img, ok := kiroToolImageFromBlock(gjson.Parse(`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + base64.StdEncoding.EncodeToString(b) + `"}}`))
		if !ok || img.Format != format {
			t.Errorf("%s: forwarded=%v format=%q, want %s", name, ok, img.Format, format)
		}
	}
}

// testPNG assembles a PNG from a header, raw (already filtered) rows and extra
// chunks placed before IDAT.
func testPNG(w, h uint32, depth, colorType, interlace byte, rows []byte, extra ...[]byte) []byte {
	header := binary.BigEndian.AppendUint32(binary.BigEndian.AppendUint32(nil, w), h)
	header = append(header, depth, colorType, 0, 0, interlace)
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	zw.Write(rows)
	zw.Close()
	out := append([]byte("\x89PNG\r\n\x1a\n"), pngChunkBytes("IHDR", header)...)
	for _, e := range extra {
		out = append(out, e...)
	}
	out = append(out, pngChunkBytes("IDAT", z.Bytes())...)
	return append(out, pngChunkBytes("IEND", nil)...)
}

func pngChunkBytes(typ string, data []byte) []byte {
	c := binary.BigEndian.AppendUint32(nil, uint32(len(data)))
	c = append(append(c, typ...), data...)
	return binary.BigEndian.AppendUint32(c, crc32.ChecksumIEEE(c[4:]))
}

func TestPNGStructureIsChecked(t *testing.T) {
	rgbRow := []byte{0, 10, 20, 30} // filter 0, one RGB pixel
	good := testPNG(1, 1, 8, 2, 0, rgbRow)
	if !pngComplete(good) {
		t.Fatal("the builder's own valid PNG was refused")
	}
	badCRC := append([]byte(nil), good...)
	badCRC[len(badCRC)-1] ^= 0xFF // the IEND CRC, which only the CRC check reads
	compressionOne := append([]byte(nil), good...)
	compressionOne[8+8+10] = 1 // IHDR compression method
	compressionOne = append(compressionOne[:8], append(pngChunkBytes("IHDR", compressionOne[16:29]), compressionOne[33:]...)...)
	idatFirst := append([]byte("\x89PNG\r\n\x1a\n"), good[33:len(good)-12]...)
	idatFirst = append(idatFirst, good[8:33]...)
	idatFirst = append(idatFirst, good[len(good)-12:]...)
	// The header chunk renamed, so the file has none.
	notIHDRFirst := append([]byte("\x89PNG\r\n\x1a\n"), pngChunkBytes("abCD", good[16:29])...)
	notIHDRFirst = append(notIHDRFirst, good[33:]...)
	noIDAT := append(append([]byte(nil), good[:33]...), pngChunkBytes("IEND", nil)...)
	cases := map[string][]byte{
		"compression method 1":    compressionOne,
		"IDAT before IHDR":        idatFirst,
		"first chunk not IHDR":    notIHDRFirst,
		"no IDAT":                 noIDAT,
		"bad CRC":                 badCRC,
		"filter type 5":           testPNG(1, 1, 8, 2, 0, []byte{5, 10, 20, 30}),
		"missing row":             testPNG(1, 2, 8, 2, 0, rgbRow),
		"extra pixel data":        testPNG(1, 1, 8, 2, 0, append(rgbRow, 0)),
		"bad bit depth for RGB":   testPNG(1, 1, 4, 2, 0, []byte{0, 0xAB, 0xC0}), // rows sized for 4-bit RGB
		"palette image, no PLTE":  testPNG(1, 1, 8, 3, 0, []byte{0, 0}),
		"interlace method 2":      testPNG(1, 1, 8, 2, 2, rgbRow),
		"wider than the max side": testPNG(kiroMaxImageSide+1, 1, 8, 0, 0, append([]byte{0}, make([]byte, kiroMaxImageSide+1)...)),
	}
	for name, b := range cases {
		if pngComplete(b) {
			t.Errorf("%s: accepted", name)
		}
	}
	if !pngComplete(testPNG(1, 1, 8, 3, 0, []byte{0, 0}, pngChunkBytes("PLTE", []byte{1, 2, 3}))) {
		t.Error("palette image with PLTE was refused")
	}
	if !pngComplete(append(append([]byte(nil), good...), 0)) {
		t.Error("a byte after IEND made the PNG refused")
	}
}

// Codex review round 4 found inputs on which the standard decoders allocate or
// loop without bound. The structural checks must stay cheap on all of them.
func TestStructuralChecksStayCheapOnHostileInputs(t *testing.T) {
	// Grayscale with tRNS, Adam7, 4000x4000: the decoder would expand it to NRGBA.
	var rows []byte
	for _, p := range adam7Passes {
		pw, ph := passLen(4000, p.x0, p.dx), passLen(4000, p.y0, p.dy)
		if pw > 0 {
			rows = append(rows, make([]byte, ph*(1+pw))...)
		}
	}
	grayTRNS := testPNG(4000, 4000, 8, 0, 1, rows, pngChunkBytes("tRNS", []byte{0, 0}))
	if grew := allocated(func() {
		if !pngComplete(grayTRNS) {
			t.Error("valid interlaced grayscale PNG with tRNS was refused")
		}
	}); grew > 1<<20 {
		t.Errorf("checking a 4000x4000 PNG allocated %d bytes, want under 1 MiB", grew)
	}

	// Two hundred thousand tiny ancillary chunks.
	chunks := make([][]byte, 0, 200000)
	for i := 0; i < cap(chunks); i++ {
		chunks = append(chunks, pngChunkBytes("aaAa", []byte{0}))
	}
	manyChunks := testPNG(1, 1, 8, 2, 0, []byte{0, 1, 2, 3}, chunks...)
	if grew := allocated(func() { pngComplete(manyChunks) }); grew > 1<<20 {
		t.Errorf("walking 200000 chunks allocated %d bytes, want under 1 MiB", grew)
	}

	// A progressive JPEG header followed by more scans than any real file has.
	jpeg := []byte{0xFF, 0xD8}
	jpeg = append(jpeg, 0xFF, 0xDB, 0x00, 0x43, 0x00)
	jpeg = append(jpeg, make([]byte, 64)...)
	jpeg = append(jpeg, 0xFF, 0xC2, 0x00, 0x0B, 0x08, 0x00, 0x08, 0x00, 0x08, 0x01, 0x01, 0x11, 0x00)
	jpeg = append(jpeg, 0xFF, 0xC4, 0x00, 0x14, 0x10, 0x01) // AC table 0, one code
	jpeg = append(jpeg, make([]byte, 16)...)
	scan := []byte{0xFF, 0xDA, 0x00, 0x08, 0x01, 0x01, 0x00, 0x01, 0x3F, 0x10, 0x00}
	for i := 0; i < kiroMaxJPEGScans; i++ {
		jpeg = append(jpeg, scan...)
	}
	if !jpegComplete(append(append([]byte(nil), jpeg...), 0xFF, 0xD9)) {
		t.Error("a JPEG with exactly kiroMaxJPEGScans scans was refused")
	}
	if jpegComplete(append(append(jpeg, scan...), 0xFF, 0xD9)) {
		t.Error("a JPEG with more than kiroMaxJPEGScans scans was accepted")
	}
}

func TestJPEGAndGIFStructureIsChecked(t *testing.T) {
	good, _ := base64.StdEncoding.DecodeString(eightPixelJpeg)
	if !jpegComplete(good) {
		t.Fatal("a valid JPEG was refused")
	}
	// without removes every segment with the given marker.
	without := func(marker byte) []byte {
		out := []byte{0xFF, 0xD8}
		for pos := 2; pos < len(good); {
			m := good[pos+1]
			if m == 0xDA {
				return append(out, good[pos:]...)
			}
			end := pos + 2 + int(binary.BigEndian.Uint16(good[pos+2:]))
			if m != marker {
				out = append(out, good[pos:end]...)
			}
			pos = end
		}
		return out
	}
	tooWide := append([]byte(nil), good...)
	sof := bytes.Index(tooWide, []byte{0xFF, 0xC0})
	binary.BigEndian.PutUint16(tooWide[sof+7:], kiroMaxImageSide+1)
	for name, b := range map[string][]byte{
		"no DHT":             without(0xC4),
		"no DQT":             without(0xDB),
		"no frame header":    without(0xC0),
		"no EOI":             good[:len(good)-2],
		"wider than allowed": tooWide,
	} {
		if jpegComplete(b) {
			t.Errorf("JPEG %s: accepted", name)
		}
	}

	gif, _ := base64.StdEncoding.DecodeString(eightPixelGif)
	if !gifComplete(gif) {
		t.Fatal("a valid GIF was refused")
	}
	zeroWidth := append([]byte(nil), gif...)
	zeroWidth[6], zeroWidth[7] = 0, 0
	tableLen := gifTableLen(gif[10])
	noFrames := append(append([]byte(nil), gif[:13+tableLen]...), 0x3B)
	for name, b := range map[string][]byte{
		"no trailer": gif[:len(gif)-1],
		"zero width": zeroWidth,
		"no frames":  noFrames,
	} {
		if gifComplete(b) {
			t.Errorf("GIF %s: accepted", name)
		}
	}
}

// A 4096x4096 RGBA PNG inflates to 64 MB of rows; Kiro takes it, and checking
// it must not hold those rows.
func TestLargePNGIsForwarded(t *testing.T) {
	const side = 4096
	header := binary.BigEndian.AppendUint32(binary.BigEndian.AppendUint32(nil, side), side)
	header = append(header, 8, 6, 0, 0, 0)
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	row := make([]byte, 1+side*4)
	for range side {
		zw.Write(row)
	}
	zw.Close()
	png := append([]byte("\x89PNG\r\n\x1a\n"), pngChunkBytes("IHDR", header)...)
	png = append(png, pngChunkBytes("IDAT", z.Bytes())...)
	png = append(png, pngChunkBytes("IEND", nil)...)
	if grew := allocated(func() {
		if !pngComplete(png) {
			t.Error("a 4096x4096 RGBA PNG was refused")
		}
	}); grew > 1<<20 {
		t.Errorf("checking it allocated %d bytes, want under 1 MiB", grew)
	}
}

// kiroTakesButNotForwarded are JPEG sampling layouts Kiro took that no common
// encoder writes; only the standard subsamplings are forwarded.
var kiroTakesButNotForwarded = map[string]bool{
	"S9 MCU 18 blocks (Y 4x4)": true,
	"V5 sampling 22/21/11":     true,
	"V9 sampling 44/11/11":     true,
	"V10 sampling 42/21/21":    true,
	"X10 sampling 22/12/21":    true,
}

// Every file Kiro was sent on 2026-10-16 to learn what it refuses: the checks
// must forward exactly the ones it took, apart from kiroTakesButNotForwarded.
func TestToolImageChecksAgreeWithKiro(t *testing.T) {
	raw, err := os.ReadFile("testdata/kiro_measured_images.json")
	if err != nil {
		t.Fatal(err)
	}
	var measured struct {
		Cases []struct {
			Name       string `json:"name"`
			KiroStatus int    `json:"kiro_status"`
			Data       string `json:"data"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &measured); err != nil {
		t.Fatal(err)
	}
	if len(measured.Cases) != 158 {
		t.Fatalf("read %d measured cases, want 158", len(measured.Cases))
	}
	for _, c := range measured.Cases {
		want := c.KiroStatus == 200 && !kiroTakesButNotForwarded[c.Name]
		if forwarded := checkToolImage(c.Data).format != ""; forwarded != want {
			t.Errorf("%s: forwarded=%v, Kiro answered %d", c.Name, forwarded, c.KiroStatus)
		}
		if kiroTakesButNotForwarded[c.Name] && c.KiroStatus != 200 {
			t.Errorf("%s: listed as taken by Kiro, but Kiro answered %d", c.Name, c.KiroStatus)
		}
	}
}

// The header fields the Kiro measurements do not reach on their own: each case
// breaks one rule the checks enforce and keeps the rest of the file valid.
func TestAPNGChunksAreChecked(t *testing.T) {
	rows := []byte{0, 1, 2, 3, 4, 5, 6, 0, 1, 2, 3, 4, 5, 6} // 2x2 RGB
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	zw.Write(rows)
	zw.Close()
	fcTL := func(seq, w, h, x, y uint32, dispose, blend byte) []byte {
		d := binary.BigEndian.AppendUint32(nil, seq)
		for _, v := range []uint32{w, h, x, y} {
			d = binary.BigEndian.AppendUint32(d, v)
		}
		return append(d, 0, 1, 0, 1, dispose, blend)
	}
	fdAT := func(seq uint32) []byte { return append(binary.BigEndian.AppendUint32(nil, seq), z.Bytes()...) }
	// apng holds the default image and a second frame described by second.
	apng := func(second, data []byte) []byte {
		b := testPNG(2, 2, 8, 2, 0, rows, pngChunkBytes("acTL", []byte{0, 0, 0, 2, 0, 0, 0, 0}), pngChunkBytes("fcTL", fcTL(0, 2, 2, 0, 0, 0, 0)))
		iend := len(b) - 12
		out := append(append([]byte(nil), b[:iend]...), pngChunkBytes("fcTL", second)...)
		out = append(out, pngChunkBytes("fdAT", data)...)
		return append(out, b[iend:]...)
	}
	if !pngComplete(apng(fcTL(1, 2, 2, 0, 0, 2, 1), fdAT(2))) {
		t.Fatal("a valid APNG was refused")
	}
	for name, b := range map[string][]byte{
		"fdAT out of sequence":    apng(fcTL(1, 2, 2, 0, 0, 0, 0), fdAT(9)),
		"frame height 0":          apng(fcTL(1, 2, 0, 0, 0, 0, 0), fdAT(2)),
		"frame past the right":    apng(fcTL(1, 2, 2, 1, 0, 0, 0), fdAT(2)),
		"frame past the bottom":   apng(fcTL(1, 2, 2, 0, 1, 0, 0), fdAT(2)),
		"blend operation 2":       apng(fcTL(1, 2, 2, 0, 0, 0, 2), fdAT(2)),
		"fcTL cut to eight bytes": apng(fcTL(1, 2, 2, 0, 0, 0, 0)[:8], fdAT(2)),
	} {
		if pngComplete(b) {
			t.Errorf("APNG %s: accepted", name)
		}
	}
}

type jpegSegment struct {
	marker byte
	data   []byte
}

// splitJPEG returns the segments of b through its first scan header and the
// bytes after them.
func splitJPEG(b []byte) ([]jpegSegment, []byte) {
	var segs []jpegSegment
	for pos := 2; ; {
		end := pos + 2 + int(binary.BigEndian.Uint16(b[pos+2:]))
		segs = append(segs, jpegSegment{b[pos+1], b[pos+4 : end]})
		if b[pos+1] == 0xDA {
			return segs, b[end:]
		}
		pos = end
	}
}

func joinJPEG(segs []jpegSegment, rest []byte) []byte {
	out := []byte{0xFF, 0xD8}
	for _, s := range segs {
		out = append(append(out, 0xFF, s.marker), byte((len(s.data)+2)>>8), byte(len(s.data)+2))
		out = append(out, s.data...)
	}
	return append(out, rest...)
}

func TestJPEGHeaderFieldsAreChecked(t *testing.T) {
	good, _ := base64.StdEncoding.DecodeString(eightPixelJpeg)
	segs, rest := splitJPEG(good) // APP0, DQT 0, DQT 1, SOF0, DHT x4, SOS
	sof, sos := segs[3].data, segs[8].data
	with := func(i int, data []byte) []byte {
		edited := append([]jpegSegment(nil), segs...)
		edited[i].data = data
		return joinJPEG(edited, rest)
	}
	retag := func(marker byte) []byte {
		edited := append([]jpegSegment(nil), segs...)
		edited[3].marker = marker
		return joinJPEG(edited, rest)
	}
	before := func(extra jpegSegment) []byte {
		return joinJPEG(append([]jpegSegment{extra}, segs...), rest)
	}
	edit := func(b []byte, at int, v ...byte) []byte {
		out := append([]byte(nil), b...)
		copy(out[at:], v)
		return out
	}
	counts := func(class byte, c ...byte) []byte {
		return append([]byte{class}, append(c, make([]byte, 16-len(c))...)...)
	}
	manySymbols := append(counts(0x12, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 255, 255), make([]byte, 510)...)
	secondFrame := append(append([]byte(nil), rest[:len(rest)-2]...), joinJPEG([]jpegSegment{{0xC0, edit(sof, 6, 7, 0x11, 0, 8, 0x11, 0, 9, 0x11, 0)}}, []byte{0xFF, 0xD9})[2:]...)
	twoComponents := joinJPEG(append(append([]jpegSegment(nil), segs[:3]...), append([]jpegSegment{{0xC0, append(edit(sof[:6], 5, 2), sof[6:12]...)}},
		append(append([]jpegSegment(nil), segs[4:8]...), jpegSegment{0xDA, append(edit(sos[:5], 0, 2), sos[7:]...)})...)...), rest)
	cases := map[string][]byte{
		"no scan":                      joinJPEG(segs[:8], []byte{0xFF, 0xD9}),
		"lossless frame":               retag(0xC3),
		"arithmetic-coded frame":       retag(0xC9),
		"quantization precision 2":     before(jpegSegment{0xDB, append([]byte{0x20}, make([]byte, 192)...)}),
		"quantization table id 4":      before(jpegSegment{0xDB, append([]byte{0x04}, make([]byte, 64)...)}),
		"Huffman table under 17 bytes": before(jpegSegment{0xC4, []byte{0x00, 1, 2, 3}}),
		"Huffman table of 510 symbols": before(jpegSegment{0xC4, manySymbols}),
		"Huffman class 2":              before(jpegSegment{0xC4, append(counts(0x20, 1), 0)}),
		"Huffman table id 4":           before(jpegSegment{0xC4, append(counts(0x04, 1), 0)}),
		"Huffman symbols missing":      before(jpegSegment{0xC4, counts(0x10, 1)}),
		"second frame after the scan":  joinJPEG(segs, secondFrame),
		"two components":               twoComponents,
		"frame header a byte long":     with(3, append(append([]byte(nil), sof...), 0)),
		"duplicate id, scanned once":   joinJPEG(append(append([]jpegSegment(nil), segs[:3]...), append([]jpegSegment{{0xC0, edit(sof, 9, 1)}}, append(append([]jpegSegment(nil), segs[4:8]...), jpegSegment{0xDA, []byte{1, 1, 0x00, 0, 63, 0}})...)...), rest),
		"sampling factor 5":            with(3, edit(sof, 7, 0x51)),
		"scan of no components":        with(8, []byte{0, 0, 63, 0}),
		"scan header a byte long":      with(8, append(append([]byte(nil), sos...), 0)),
		"DC table undefined":           with(8, edit(sos, 2, 0x20)),
		"AC table undefined":           with(8, edit(sos, 2, 0x02)),
	}
	for name, b := range cases {
		if jpegComplete(b) {
			t.Errorf("JPEG %s: accepted", name)
		}
	}

	// A progressive AC scan covers one component.
	progressive := func(scan ...byte) []byte {
		return joinJPEG([]jpegSegment{
			{0xDB, append([]byte{0x00}, make([]byte, 64)...)},
			{0xC2, []byte{8, 0, 8, 0, 8, 3, 1, 0x11, 0, 2, 0x11, 0, 3, 0x11, 0}},
			{0xC4, append(counts(0x10, 1), 0)},
			{0xDA, scan},
		}, []byte{0xFF, 0xD9})
	}
	if !jpegComplete(progressive(1, 1, 0x00, 1, 63, 0)) {
		t.Error("a progressive AC scan of one component was refused")
	}
	if jpegComplete(progressive(2, 1, 0x00, 2, 0x00, 1, 63, 0)) {
		t.Error("a progressive AC scan of two components was accepted")
	}
}

func TestGIFHeaderFieldsAreChecked(t *testing.T) {
	gif, _ := base64.StdEncoding.DecodeString(eightPixelGif)
	desc := 13 + gifTableLen(gif[10])
	edit := func(at int, v ...byte) []byte {
		out := append([]byte(nil), gif...)
		copy(out[at:], v)
		return out
	}
	insert := func(ext ...byte) []byte {
		return append(append(append([]byte(nil), gif[:desc]...), ext...), gif[desc:]...)
	}
	if !gifComplete(insert(append(append([]byte{0x21, 0x01, 12}, make([]byte, 12)...), 0)...)) {
		t.Fatal("a plain text extension of 12 bytes was refused")
	}
	for name, b := range map[string][]byte{
		"plain text extension of 11 bytes": insert(append(append([]byte{0x21, 0x01, 11}, make([]byte, 11)...), 0)...),
		"unknown extension, no data":       insert(0x21, 0x99, 0),
		"frame at x 1, width 0":            edit(desc+1, 1, 0, 0, 0, 0, 0),
		"frame at y 1, height 0":           edit(desc+3, 1, 0, 8, 0, 0, 0),
		"frame ending past the side limit": edit(desc+1, binary.LittleEndian.AppendUint16(nil, kiroMaxImageSide)...),
		"frame ending below the limit":     edit(desc+3, binary.LittleEndian.AppendUint16(nil, kiroMaxImageSide)...),
	} {
		if gifComplete(b) {
			t.Errorf("GIF %s: accepted", name)
		}
	}
}

func TestToolImagesOverAnthropicsSizeLimitAreRefused(t *testing.T) {
	big := testPNG(1, 1, 8, 2, 0, []byte{0, 1, 2, 3}, pngChunkBytes("aaAa", make([]byte, kiroMaxToolImageBytes)))
	if !pngComplete(big) {
		t.Fatal("the oversized PNG is not structurally valid")
	}
	if checkToolImage(base64.StdEncoding.EncodeToString(big)).format != "" {
		t.Error("a PNG over kiroMaxToolImageBytes was accepted")
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

// A verdict is cached per data string, so a second sighting (the next turn's
// history) must give the same result as the first.
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
