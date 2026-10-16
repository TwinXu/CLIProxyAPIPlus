package claude

import (
	"bytes"
	"cmp"
	"compress/zlib"
	"encoding/base64"
	"encoding/binary"
	"hash/crc32"
	"hash/maphash"
	"io"
	"net/http"
	"slices"
	"sync"

	"github.com/tidwall/gjson"
)

// Kiro decodes every image it receives and rejects the whole request when one
// fails. Measured on 2026-10-16: bad base64, svg+xml, an image/jpg label, PNG
// bytes labelled image/jpeg, files cut short and an empty WebP envelope all
// returned 400, and so did whole files with one malformed header field.
// testdata/kiro_measured_images.json holds 128 such files with Kiro's answers.
// Kiro is not Pillow: Pillow 11 disagreed on 14 of the first 78.
//
// Decoding untrusted images inside this shared proxy is a hazard of its own:
// crafted files make the standard decoders allocate hundreds of megabytes or
// loop for minutes. A tool image is therefore checked without decoding, in
// time linear in its size and with constant memory: the header fields Kiro was
// seen to check, the PNG chunk CRCs and inflated pixel rows, and the JPEG and
// GIF block structure through the end marker. Coded image data is not checked;
// Kiro took a JPEG whose entropy-coded bytes were all zero and a GIF whose last
// frame was cut short.

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

// pngComplete walks every chunk through IEND, checking lengths, CRCs and the
// header fields Kiro reads, and inflates the pixel data to confirm it holds
// exactly the rows the header declares. Kiro ignores bytes after IEND.
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
	s := pngState{width: binary.BigEndian.Uint32(header[0:4]), height: binary.BigEndian.Uint32(header[4:8]), colorType: header[9]}
	idat := 0
	for {
		start := pos
		var data []byte
		if typ, data, pos, ok = pngChunk(b, pos); !ok || !s.chunk(string(typ), data) {
			return false
		}
		switch string(typ) {
		case "IDAT":
			idat = cmp.Or(idat, start)
		case "IEND":
			return idat != 0 && pngRowsComplete(b, idat, rows)
		}
	}
}

// pngState is what the chunks after IHDR are checked against.
type pngState struct {
	width, height uint32
	colorType     byte
	sawPLTE       bool
	seq           uint32 // the next APNG sequence number
}

// pngTRNSLength is the tRNS length for each colour type that may carry one:
// a sample per channel for grey and RGB, any number of alphas for a palette.
var pngTRNSLength = map[byte]int{0: 2, 2: 6, 3: -1}

// chunk checks a chunk after IHDR. Kiro was seen to refuse each case refused
// here; it took short or malformed bKGD, sBIT, hIST, tIME, text, iCCP, eXIf,
// cICP, mDCV and cLLI chunks, and longer gAMA, cHRM, pHYs and sRGB ones.
func (s *pngState) chunk(typ string, d []byte) bool {
	switch typ {
	case "IHDR":
		return false
	case "PLTE":
		s.sawPLTE = true
		return len(d) > 0 && len(d)%3 == 0 && len(d) <= 3*256
	case "IDAT":
		return s.colorType != 3 || s.sawPLTE
	case "tRNS":
		want, ok := pngTRNSLength[s.colorType]
		return ok && (want < 0 || len(d) == want)
	case "gAMA":
		return len(d) >= 4
	case "cHRM":
		return len(d) >= 32
	case "pHYs":
		return len(d) >= 9 && d[8] <= 1
	case "sRGB":
		return len(d) == 0 || d[0] <= 3
	case "acTL":
		return len(d) >= 8 && binary.BigEndian.Uint32(d) > 0
	case "fcTL":
		return s.next(d) && len(d) >= 26 && s.frameFits(d)
	case "fdAT":
		return s.next(d)
	}
	return true
}

// next consumes the APNG sequence number that leads fcTL and fdAT data.
func (s *pngState) next(d []byte) bool {
	if len(d) < 4 || binary.BigEndian.Uint32(d) != s.seq {
		return false
	}
	s.seq++
	return true
}

// frameFits checks an fcTL frame: nonempty, inside the image, and with a
// defined dispose and blend operation.
func (s *pngState) frameFits(d []byte) bool {
	w, h := uint64(binary.BigEndian.Uint32(d[4:])), uint64(binary.BigEndian.Uint32(d[8:]))
	x, y := uint64(binary.BigEndian.Uint32(d[12:])), uint64(binary.BigEndian.Uint32(d[16:]))
	return w > 0 && h > 0 && x+w <= uint64(s.width) && y+h <= uint64(s.height) && d[24] <= 2 && d[25] <= 1
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
	return b[pos+4 : pos+8], b[pos+8 : end : end], end + 4, true
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

// jpegComplete walks the markers through EOI, checking every table, frame and
// scan header; each scan's entropy-coded data is skipped to the next marker.
func jpegComplete(b []byte) bool {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return false
	}
	var h jpegHeader
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
			return h.scans > 0
		case marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7):
			continue
		}
		if len(b)-pos < 2 {
			return false
		}
		length := int(binary.BigEndian.Uint16(b[pos:]))
		if length < 2 || length > len(b)-pos || !h.segment(marker, b[pos+2:pos+length:pos+length]) {
			return false
		}
		pos += length
		if marker == 0xDA {
			pos = jpegScanEnd(b, pos)
		}
	}
}

// jpegHeader is what the segments read so far have defined.
type jpegHeader struct {
	components  [4]jpegComponent
	ncomp       int // zero until the frame header
	progressive bool
	quant       uint16    // bit i: quantization table i is defined; never above 3
	huffman     [2]uint16 // DC and AC; bit i: Huffman table i is defined; never above 3
	scans       int
}

type jpegComponent struct{ id, quant byte }

// segment checks one marker segment and records what it defines. Kiro was
// seen to refuse each case refused here; it took unused tables, empty DQT and
// DHT segments, EXIF data it could not parse, and sequential scans with odd
// spectral or approximation fields.
func (h *jpegHeader) segment(marker byte, s []byte) bool {
	switch {
	case marker == 0xDB:
		return h.defineQuant(s)
	case marker == 0xC4:
		return h.defineHuffman(s)
	case isJPEGFrameMarker(marker):
		// Huffman-coded baseline, extended and progressive frames only.
		return marker <= 0xC2 && h.frame(s, marker == 0xC2)
	case marker == 0xDA:
		h.scans++
		return h.scans <= kiroMaxJPEGScans && h.scan(s)
	case marker == 0xDD:
		return len(s) == 2
	}
	return true
}

// defineQuant records the tables of a DQT segment, each a precision and id
// byte followed by 64 values of one or two bytes.
func (h *jpegHeader) defineQuant(s []byte) bool {
	for len(s) > 0 {
		precision, id := s[0]>>4, s[0]&0x0F
		n := 1 + 64*(1+int(precision))
		if precision > 1 || id > 3 || len(s) < n {
			return false
		}
		h.quant |= 1 << id
		s = s[n:]
	}
	return true
}

// defineHuffman records the tables of a DHT segment, each a class and id byte,
// sixteen code counts, then that many symbols; DC symbols are at most 15.
func (h *jpegHeader) defineHuffman(s []byte) bool {
	for len(s) > 0 {
		if len(s) < 17 {
			return false
		}
		class, id := s[0]>>4, s[0]&0x0F
		n, ok := huffmanSymbols(s[1:17])
		if !ok || class > 1 || id > 3 || len(s) < 17+n ||
			(class == 0 && slices.ContainsFunc(s[17:17+n], func(v byte) bool { return v > 15 })) {
			return false
		}
		h.huffman[class] |= 1 << id
		s = s[17+n:]
	}
	return true
}

// huffmanSymbols returns how many symbols sixteen code counts declare, and
// whether at most 256 codes fit their lengths with the all-ones code of each
// length left free.
func huffmanSymbols(counts []byte) (int, bool) {
	n, code := 0, 0
	for i, c := range counts {
		n += int(c)
		if code += int(c); code >= 2<<i {
			return 0, false
		}
		code <<= 1
	}
	return n, n <= 256
}

// frame checks the frame header: 8-bit samples, a size within the side limit,
// and 1, 3 or 4 components with distinct ids and sampling factors 1 to 4. An
// image has one frame. A table id is checked when a scan uses the component.
func (h *jpegHeader) frame(s []byte, progressive bool) bool {
	if h.ncomp != 0 || len(s) < 6 || s[0] != 8 {
		return false
	}
	n := int(s[5])
	if (n != 1 && n != 3 && n != 4) || len(s) != 6+3*n ||
		!sideOK(uint32(binary.BigEndian.Uint16(s[1:3]))) || !sideOK(uint32(binary.BigEndian.Uint16(s[3:5]))) {
		return false
	}
	for i := range n {
		c := s[6+3*i : 9+3*i]
		hs, vs := c[1]>>4, c[1]&0x0F
		if _, dup := h.component(c[0]); dup || hs < 1 || hs > 4 || vs < 1 || vs > 4 {
			return false
		}
		h.components[i], h.ncomp = jpegComponent{id: c[0], quant: c[2]}, i+1
	}
	h.progressive = progressive
	return true
}

func (h *jpegHeader) component(id byte) (int, bool) {
	for k := range h.ncomp {
		if h.components[k].id == id {
			return k, true
		}
	}
	return 0, false
}

// scan checks a scan header: distinct components of the frame, at least one,
// each with its quantization table and the Huffman tables this scan decodes
// with defined.
func (h *jpegHeader) scan(s []byte) bool {
	if len(s) < 1 {
		return false
	}
	n := int(s[0])
	if n < 1 || len(s) != 1+2*n+3 {
		return false
	}
	ss, se, ah, al := s[1+2*n], s[2+2*n], s[3+2*n]>>4, s[3+2*n]&0x0F
	if h.progressive && !jpegProgressionOK(n, ss, se, ah, al) {
		return false
	}
	needDC, needAC := !h.progressive || (ss == 0 && ah == 0), !h.progressive || ss > 0
	var used uint8
	for i := range n {
		k, ok := h.component(s[1+2*i])
		dc, ac := s[2+2*i]>>4, s[2+2*i]&0x0F
		if !ok || used&(1<<k) != 0 || h.quant&(1<<h.components[k].quant) == 0 ||
			(needDC && h.huffman[0]&(1<<dc) == 0) || (needAC && h.huffman[1]&(1<<ac) == 0) {
			return false
		}
		used |= 1 << k
	}
	return true
}

// jpegProgressionOK checks a progressive scan: a DC scan covers coefficient 0
// alone, an AC scan one component and a band within 1 to 63, and a refinement
// scan the bit below the previous one.
func jpegProgressionOK(n int, ss, se, ah, al byte) bool {
	band := (ss == 0 && se == 0) || (ss > 0 && ss <= se && se <= 63 && n == 1)
	return band && (ah == 0 || al == ah-1) && al <= 13
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

// gifComplete walks the blocks through the trailer, checking every frame
// descriptor and extension header. Kiro ignores bytes after the trailer.
func gifComplete(b []byte) bool {
	if len(b) < 13 || (string(b[:6]) != "GIF87a" && string(b[:6]) != "GIF89a") ||
		!sideOK(uint32(binary.LittleEndian.Uint16(b[6:8]))) || !sideOK(uint32(binary.LittleEndian.Uint16(b[8:10]))) {
		return false
	}
	globalTable, frames := b[10]&0x80 != 0, 0
	for pos := 13 + gifTableLen(b[10]); pos >= 0 && pos < len(b); {
		switch b[pos] {
		case 0x3B:
			return frames > 0
		case 0x21:
			pos = gifExtension(b, pos)
		case 0x2C:
			pos = gifFrame(b, pos, globalTable)
			frames++
		default:
			return false
		}
	}
	return false
}

// gifExtensionBlock is the size of the first sub-block of each extension Kiro
// takes, or -1 when any size goes: plain text, graphic control, comment and
// application. Kiro took an application extension of the wrong size.
var gifExtensionBlock = map[byte]int{0x01: 12, 0xF9: 4, 0xFE: -1, 0xFF: -1}

// gifExtension checks the extension at pos and returns the position after it,
// or -1.
func gifExtension(b []byte, pos int) int {
	if len(b)-pos < 3 {
		return -1
	}
	size, ok := gifExtensionBlock[b[pos+1]]
	if !ok || (size >= 0 && int(b[pos+2]) != size) {
		return -1
	}
	return gifSkipSubBlocks(b, pos+2)
}

// gifFrame checks the image descriptor at pos and returns the position after
// the frame's data, or -1. The frame must be nonempty and end within the side
// limit (Kiro takes frames outside the logical screen), have a colour table,
// and use an LZW code size of 2 to 8 bits.
func gifFrame(b []byte, pos int, globalTable bool) int {
	if len(b)-pos < 10 {
		return -1
	}
	d := b[pos:]
	left, top := uint32(binary.LittleEndian.Uint16(d[1:3])), uint32(binary.LittleEndian.Uint16(d[3:5]))
	w, h := uint32(binary.LittleEndian.Uint16(d[5:7])), uint32(binary.LittleEndian.Uint16(d[7:9]))
	code := pos + 10 + gifTableLen(d[9])
	if w == 0 || h == 0 || !sideOK(left+w) || !sideOK(top+h) || (!globalTable && d[9]&0x80 == 0) ||
		code >= len(b) || b[code] < 2 || b[code] > 8 {
		return -1
	}
	return gifSkipSubBlocks(b, code+1)
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
