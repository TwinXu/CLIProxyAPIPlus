package claude

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"hash/maphash"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"sync"

	"github.com/tidwall/gjson"
)

// Kiro decodes every image it receives and rejects the whole request when one
// fails. Measured on 2026-10-16: bad base64, svg+xml, an image/jpg label, PNG
// bytes labelled image/jpeg, a bare PNG signature, and PNG, JPEG, GIF and WebP
// files cut short all returned 400. A tool image is therefore forwarded only
// after it has been decoded here.

// kiroSniffedImageFormats lists the formats Kiro accepts, keyed by sniffed content type.
var kiroSniffedImageFormats = map[string]string{
	"image/png":  "png",
	"image/jpeg": "jpeg",
	"image/gif":  "gif",
	"image/webp": "webp",
}

// kiroMaxImageSide bounds the pixels a check may allocate. Larger images are not
// forwarded.
const kiroMaxImageSide = 8000

// kiroToolImageVerdict is the outcome of checking one image's base64 data.
type kiroToolImageVerdict struct {
	format    string // empty when the image must not be forwarded
	canonical bool   // the data already is standard padded base64
}

// kiroToolImageVerdicts remembers verdicts, since the history resends the same
// images on every turn and a full decode costs tens of milliseconds.
var kiroToolImageVerdicts = struct {
	sync.Mutex
	seed    maphash.Seed
	entries map[uint64]kiroToolImageVerdict
}{seed: maphash.MakeSeed(), entries: make(map[uint64]kiroToolImageVerdict)}

const kiroToolImageVerdictLimit = 4096

// kiroToolImageFromBlock converts a tool-result image only when Kiro will accept
// it. The format is taken from the bytes, not from the declared media type.
func kiroToolImageFromBlock(block gjson.Result) (KiroImage, bool) {
	if block.Get("source.type").String() != "base64" {
		return KiroImage{}, false
	}
	data := block.Get("source.data").String()
	verdict := cachedToolImageVerdict(data)
	if verdict.format == "" {
		return KiroImage{}, false
	}
	if !verdict.canonical {
		// The decoder tolerates line breaks; send Kiro the standard encoding.
		decoded, _ := base64.StdEncoding.DecodeString(data)
		data = base64.StdEncoding.EncodeToString(decoded)
	}
	return KiroImage{Format: verdict.format, Source: KiroImageSource{Bytes: data}}, true
}

func cachedToolImageVerdict(data string) kiroToolImageVerdict {
	cache := &kiroToolImageVerdicts
	key := maphash.String(cache.seed, data)
	cache.Lock()
	verdict, hit := cache.entries[key]
	cache.Unlock()
	if hit {
		return verdict
	}
	verdict = checkToolImage(data)
	cache.Lock()
	if len(cache.entries) >= kiroToolImageVerdictLimit {
		clear(cache.entries)
	}
	cache.entries[key] = verdict
	cache.Unlock()
	return verdict
}

func checkToolImage(data string) kiroToolImageVerdict {
	decoded, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return kiroToolImageVerdict{}
	}
	format, ok := kiroSniffedImageFormats[http.DetectContentType(decoded)]
	if !ok || !kiroImageDecodes(format, decoded) {
		return kiroToolImageVerdict{}
	}
	return kiroToolImageVerdict{format: format, canonical: base64.StdEncoding.EncodeToString(decoded) == data}
}

func kiroImageDecodes(format string, b []byte) bool {
	if format == "webp" {
		// The standard library has no WebP decoder; check the container instead.
		// This catches a truncated file but not damage inside it.
		return webpContainerComplete(b)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > kiroMaxImageSide || cfg.Height > kiroMaxImageSide {
		return false
	}
	_, _, err = image.Decode(bytes.NewReader(b))
	return err == nil
}

// webpContainerComplete reports whether the RIFF size of b, which the sniffer
// already identified as WebP, covers exactly the bytes present.
func webpContainerComplete(b []byte) bool {
	return len(b) >= 12 && uint64(binary.LittleEndian.Uint32(b[4:8]))+8 == uint64(len(b))
}
