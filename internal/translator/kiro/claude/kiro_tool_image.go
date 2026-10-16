package claude

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/binary"
	"hash/crc32"
	"hash/maphash"
	"io"
	"net/http"
	"sync"

	"github.com/tidwall/gjson"
)

// Kiro decodes every image it receives and rejects the whole request when one
// fails. Measured on 2026-10-16: bad base64, svg+xml, an image/jpg label, PNG
// bytes labelled image/jpeg, a bare PNG signature, PNG, JPEG, GIF and WebP files
// cut short, and an empty WebP envelope all returned 400; a GIF whose last frame
// was cut short passed.
//
// Decoding untrusted images inside this shared proxy is a hazard of its own:
// crafted files make the standard decoders allocate hundreds of megabytes or
// loop for minutes. A tool image is therefore checked for structure only, in
// time linear in its size and with constant memory: PNG chunks, CRCs and the
// inflated pixel rows; JPEG markers through EOI; GIF blocks through the trailer.
// A JPEG or GIF that is whole but carries corrupt entropy-coded data still
// reaches Kiro and fails the request; only a decoder would catch that.

// kiroToolImageFormat names a format for Kiro and checks that a file is whole.
type kiroToolImageFormat struct {
	name     string
	complete func([]byte) bool
}

// kiroToolImageFormats is keyed by sniffed content type. Kiro also takes WebP,
// but its structure cannot be checked as cheaply, so WebP tool images are not
// forwarded.
var kiroToolImageFormats = map[string]kiroToolImageFormat{
	"image/png":  {"png", pngComplete},
	"image/jpeg": {"jpeg", jpegComplete},
	"image/gif":  {"gif", gifComplete},
}

const (
	// kiroMaxToolImageBytes is Anthropic's per-image limit.
	kiroMaxToolImageBytes = 5 << 20
	// kiroMaxImageSide is the largest side forwarded.
	kiroMaxImageSide = 8000
	// kiroMaxPNGRowBytes bounds the inflate work for one PNG.
	kiroMaxPNGRowBytes = 64 << 20
	// kiroMaxJPEGScans is far above the ten or so scans real progressive files use.
	kiroMaxJPEGScans = 100
)

// kiroToolImageVerdict is the outcome of checking one image's base64 data.
type kiroToolImageVerdict struct {
	format    string // empty when the image must not be forwarded
	canonical bool   // the data already is standard padded base64
}

// kiroToolImageVerdicts remembers verdicts, since the history resends the same
// images on every turn.
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
	// Line breaks may inflate the encoding, hence the slack before decoding.
	if len(data) > 2*base64.StdEncoding.EncodedLen(kiroMaxToolImageBytes) {
		return kiroToolImageVerdict{}
	}
	decoded, err := base64.StdEncoding.DecodeString(data)
	if err != nil || len(decoded) > kiroMaxToolImageBytes {
		return kiroToolImageVerdict{}
	}
	f, ok := kiroToolImageFormats[http.DetectContentType(decoded)]
	if !ok || !f.complete(decoded) {
		return kiroToolImageVerdict{}
	}
	return kiroToolImageVerdict{format: f.name, canonical: base64.StdEncoding.EncodeToString(decoded) == data}
}

func sideOK(n uint32) bool { return n > 0 && n <= kiroMaxImageSide }

var pngSignature = []byte("\x89PNG\r\n\x1a\n")

// pngComplete walks every chunk, checking lengths and CRCs, and inflates the
// pixel data to confirm it holds exactly the rows the header declares.
func pngComplete(b []byte) bool {
	if !bytes.HasPrefix(b, pngSignature) {
		return false
	}
	typ, header, pos, ok := pngChunk(b, len(pngSignature))
	if !ok || string(typ) != "IHDR" {
		return false
	}
	rows, ok := pngRows(header)
	if !ok {
		return false
	}
	paletted := header[9] == 3
	idat, sawPLTE := 0, false
	for {
		start := pos
		if typ, _, pos, ok = pngChunk(b, pos); !ok {
			return false
		}
		switch string(typ) {
		case "PLTE":
			sawPLTE = true
		case "IDAT":
			if paletted && !sawPLTE {
				return false
			}
			if idat == 0 {
				idat = start
			}
		case "IEND":
			return idat != 0 && pos == len(b) && pngRowsComplete(b, idat, rows)
		}
	}
}

// pngChunk returns the type and data of the chunk at pos and the position after
// it, verifying the CRC.
func pngChunk(b []byte, pos int) (typ, data []byte, next int, ok bool) {
	if len(b)-pos < 12 || uint64(binary.BigEndian.Uint32(b[pos:])) > uint64(len(b)-pos-12) {
		return nil, nil, 0, false
	}
	end := pos + 8 + int(binary.BigEndian.Uint32(b[pos:]))
	if crc32.ChecksumIEEE(b[pos+4:end]) != binary.BigEndian.Uint32(b[end:]) {
		return nil, nil, 0, false
	}
	return b[pos+4 : pos+8], b[pos+8 : end], end + 4, true
}

// pngRowSpan is a run of rows of the same length, filter byte included.
type pngRowSpan struct{ count, length uint64 }

var adam7Passes = [7]struct{ x0, y0, dx, dy uint64 }{
	{0, 0, 8, 8}, {4, 0, 8, 8}, {0, 4, 4, 8}, {2, 0, 4, 4}, {0, 2, 2, 4}, {1, 0, 2, 2}, {0, 1, 1, 2},
}

// pngRows validates the IHDR data and returns the rows its pixel stream holds.
func pngRows(h []byte) ([]pngRowSpan, bool) {
	if len(h) != 13 || h[10] != 0 || h[11] != 0 || h[12] > 1 {
		return nil, false
	}
	w, ht := binary.BigEndian.Uint32(h[0:4]), binary.BigEndian.Uint32(h[4:8])
	channels, ok := pngChannels(h[9], h[8])
	if !ok || !sideOK(w) || !sideOK(ht) {
		return nil, false
	}
	bits := uint64(h[8]) * channels
	span := func(width, height uint64) pngRowSpan {
		if width == 0 {
			return pngRowSpan{}
		}
		return pngRowSpan{count: height, length: 1 + (width*bits+7)/8}
	}
	if h[12] == 0 {
		return []pngRowSpan{span(uint64(w), uint64(ht))}, true
	}
	spans := make([]pngRowSpan, 0, len(adam7Passes))
	for _, p := range adam7Passes {
		spans = append(spans, span(passLen(uint64(w), p.x0, p.dx), passLen(uint64(ht), p.y0, p.dy)))
	}
	return spans, true
}

func passLen(n, start, step uint64) uint64 {
	if n <= start {
		return 0
	}
	return (n - start + step - 1) / step
}

func pngChannels(colorType, depth byte) (uint64, bool) {
	switch colorType {
	case 0:
		return 1, depth == 1 || depth == 2 || depth == 4 || depth == 8 || depth == 16
	case 3:
		return 1, depth == 1 || depth == 2 || depth == 4 || depth == 8
	case 2:
		return 3, depth == 8 || depth == 16
	case 4:
		return 2, depth == 8 || depth == 16
	case 6:
		return 4, depth == 8 || depth == 16
	}
	return 0, false
}

// pngRowsComplete inflates the IDAT stream that starts at the chunk at idat and
// reports whether it holds exactly the given rows, each led by a valid filter.
func pngRowsComplete(b []byte, idat int, spans []pngRowSpan) bool {
	var want uint64
	for _, s := range spans {
		want += s.count * s.length
	}
	if want > kiroMaxPNGRowBytes {
		return false
	}
	zr, err := zlib.NewReader(&pngIDATReader{b: b, pos: idat})
	if err != nil {
		return false
	}
	rows := &pngRowChecker{spans: spans}
	n, err := io.Copy(rows, io.LimitReader(zr, int64(want)+1))
	return err == nil && uint64(n) == want && rows.ok()
}

// pngIDATReader yields the data of the consecutive IDAT chunks starting at pos.
type pngIDATReader struct {
	b    []byte
	pos  int
	data []byte
}

func (r *pngIDATReader) Read(p []byte) (int, error) {
	for len(r.data) == 0 {
		typ, data, next, ok := pngChunk(r.b, r.pos)
		if !ok || string(typ) != "IDAT" {
			return 0, io.EOF
		}
		r.data, r.pos = data, next
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

// pngRowChecker consumes inflated rows and rejects a filter type above 4.
type pngRowChecker struct {
	spans   []pngRowSpan
	left    uint64 // bytes left in the current row
	bad     bool
	pending bool // a row has begun whose filter byte is still to come
}

func (c *pngRowChecker) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 && !c.bad {
		if c.left == 0 {
			if !c.nextRow() {
				c.bad = true
				break
			}
			if p[0] > 4 {
				c.bad = true
				break
			}
			p, c.left = p[1:], c.left-1
			continue
		}
		skip := min(uint64(len(p)), c.left)
		p, c.left = p[skip:], c.left-skip
	}
	return n, nil
}

// nextRow starts the next row and reports whether one remains.
func (c *pngRowChecker) nextRow() bool {
	for len(c.spans) > 0 && c.spans[0].count == 0 {
		c.spans = c.spans[1:]
	}
	if len(c.spans) == 0 {
		return false
	}
	c.spans[0].count--
	c.left = c.spans[0].length
	return true
}

func (c *pngRowChecker) ok() bool { return !c.bad && c.left == 0 }

// jpegComplete walks the markers through EOI. A frame header and both kinds of
// tables must precede the first scan, and each scan's entropy-coded data is
// skipped to the next marker.
func jpegComplete(b []byte) bool {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return false
	}
	sawSOF, sawDQT, sawDHT, scans := false, false, false, 0
	for pos := 2; ; {
		if pos >= len(b) || b[pos] != 0xFF {
			return false
		}
		for pos < len(b) && b[pos] == 0xFF {
			pos++
		}
		if pos >= len(b) {
			return false
		}
		marker := b[pos]
		pos++
		switch {
		case marker == 0xD9:
			return scans > 0
		case marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7):
			continue
		}
		if len(b)-pos < 2 {
			return false
		}
		length := int(binary.BigEndian.Uint16(b[pos:]))
		if length < 2 || length > len(b)-pos {
			return false
		}
		segment := b[pos+2 : pos+length]
		pos += length
		switch {
		case isJPEGFrameMarker(marker):
			if len(segment) < 6 || !sideOK(uint32(binary.BigEndian.Uint16(segment[1:3]))) || !sideOK(uint32(binary.BigEndian.Uint16(segment[3:5]))) {
				return false
			}
			sawSOF = true
		case marker == 0xDB:
			sawDQT = true
		case marker == 0xC4:
			sawDHT = true
		case marker == 0xDA:
			if scans++; !sawSOF || !sawDQT || !sawDHT || scans > kiroMaxJPEGScans {
				return false
			}
			pos = jpegScanEnd(b, pos)
		}
	}
}

// isJPEGFrameMarker reports SOF0-SOF15, leaving out DHT, JPG and DAC.
func isJPEGFrameMarker(m byte) bool {
	return m >= 0xC0 && m <= 0xCF && m != 0xC4 && m != 0xC8 && m != 0xCC
}

// jpegScanEnd returns where the marker ending the entropy-coded data at pos
// begins: an 0xFF followed by neither a stuffed zero nor a restart code.
func jpegScanEnd(b []byte, pos int) int {
	for ; pos+1 < len(b); pos++ {
		if b[pos] == 0xFF && b[pos+1] != 0x00 && (b[pos+1] < 0xD0 || b[pos+1] > 0xD7) {
			return pos
		}
	}
	return len(b)
}

// gifComplete walks the blocks through the trailer.
func gifComplete(b []byte) bool {
	if len(b) < 13 || (string(b[:6]) != "GIF87a" && string(b[:6]) != "GIF89a") ||
		!sideOK(uint32(binary.LittleEndian.Uint16(b[6:8]))) || !sideOK(uint32(binary.LittleEndian.Uint16(b[8:10]))) {
		return false
	}
	frames := 0
	for pos := 13 + gifTableLen(b[10]); pos >= 0 && pos < len(b); {
		switch b[pos] {
		case 0x3B:
			return frames > 0
		case 0x21: // extension: label, then sub-blocks
			pos = gifSkipSubBlocks(b, pos+2)
		case 0x2C: // descriptor, local table, LZW code size, then sub-blocks
			if len(b)-pos < 10 {
				return false
			}
			pos = gifSkipSubBlocks(b, pos+10+gifTableLen(b[pos+9])+1)
			frames++
		default:
			return false
		}
	}
	return false
}

func gifTableLen(flags byte) int {
	if flags&0x80 == 0 {
		return 0
	}
	return 3 << (flags&0x07 + 1)
}

// gifSkipSubBlocks returns the position after the terminator of the sub-blocks
// starting at pos, or -1 when they run past the end.
func gifSkipSubBlocks(b []byte, pos int) int {
	for pos < len(b) {
		size := int(b[pos])
		pos += 1 + size
		if size == 0 {
			return pos
		}
	}
	return -1
}
