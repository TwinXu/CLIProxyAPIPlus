package claude

import (
	"bytes"
	"encoding/base64"
	"hash/maphash"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"sync"

	"github.com/tidwall/gjson"
)

// Kiro decodes every image it receives and rejects the whole request when one
// fails. Measured on 2026-10-16: bad base64, svg+xml, an image/jpg label, PNG
// bytes labelled image/jpeg, a bare PNG signature, PNG, JPEG, GIF and WebP files
// cut short, and an empty WebP envelope all returned 400; a GIF whose last frame
// was cut short passed, so Kiro, like image.Decode, reads only the first frame.
// A tool image is therefore forwarded only after it has been decoded here.

// kiroSniffedImageFormats lists the tool image formats this package can decode,
// keyed by sniffed content type. Kiro also takes WebP, but the standard library
// cannot decode it, so a damaged WebP would fail the whole request; WebP tool
// images are not forwarded.
var kiroSniffedImageFormats = map[string]string{
	"image/png":  "png",
	"image/jpeg": "jpeg",
	"image/gif":  "gif",
}

// kiroMaxImageSide is the largest side forwarded.
const kiroMaxImageSide = 8000

// kiroMaxDecodeBytes bounds what one check may allocate. A tiny file can declare
// a huge image, so the bound is applied to the header before decoding.
const kiroMaxDecodeBytes = 128 << 20

// kiroDecodeSlot lets one decode run at a time, so checks add at most
// kiroMaxDecodeBytes to the process.
var kiroDecodeSlot = make(chan struct{}, 1)

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
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > kiroMaxImageSide || cfg.Height > kiroMaxImageSide {
		return false
	}
	if decodeAllocEstimate(format, cfg, len(b)) > kiroMaxDecodeBytes {
		return false
	}
	kiroDecodeSlot <- struct{}{}
	defer func() { <-kiroDecodeSlot }()
	_, _, err = image.Decode(bytes.NewReader(b))
	return err == nil
}

// decodeAllocEstimate overestimates what image.Decode allocates for an image.
// Besides the pixels, decoders buffer in proportion to the encoded size; a
// Pillow GIF of 128x128 took 3.85 bytes per pixel, most of it outside the image.
func decodeAllocEstimate(format string, cfg image.Config, encodedLen int) uint64 {
	return uint64(cfg.Width)*uint64(cfg.Height)*decodeBytesPerPixel(format, cfg.ColorModel) + 4*uint64(encodedLen)
}

// decodeBytesPerPixel overestimates what the standard decoders allocate per pixel.
// Measured worst cases (bytes per pixel): progressive 4:4:4 JPEG 15.0, GIF 3.85,
// interlaced 8-bit RGBA PNG 8.3, interlaced 16-bit RGBA PNG 16.5.
func decodeBytesPerPixel(format string, model color.Model) uint64 {
	switch format {
	case "jpeg":
		// A progressive JPEG keeps every DCT coefficient besides the pixels.
		return 20
	case "gif":
		// Only the first frame is decoded.
		return 4
	}
	// PNG: three times the pixel size of the model, since an interlaced image
	// is decoded into pass images and then copied into the result.
	switch model {
	case color.GrayModel, color.AlphaModel:
		return 3
	case color.Gray16Model, color.Alpha16Model:
		return 6
	case color.RGBA64Model, color.NRGBA64Model:
		return 24
	}
	return 12
}
