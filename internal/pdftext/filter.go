package pdftext

// filter.go decodes a PDF stream through its /Filter chain, every standard filter
// that can carry text: FlateDecode (with the PNG and TIFF predictors), LZWDecode,
// ASCII85Decode, ASCIIHexDecode and RunLengthDecode, and any chain of them applied
// in order (PDF 32000-1 §7.4). The image codecs (DCT, JPX, CCITTFax, JBIG2) carry
// pictures, not text, and are reported as such rather than decoded.

import (
	"bytes"
	"compress/flate"
	"compress/zlib"
	"encoding/ascii85"
	"errors"
	"fmt"
	"io"
)

// errTooBig is a stream that decodes past the bound it was given: a small, highly
// compressed file is a decompression bomb, so it is refused rather than inflated.
var errTooBig = errors.New("decodes past the limit")

// errPicture is a chain ending in an image codec: nothing in it is text.
var errPicture = errors.New("image data")

// step is one filter of a chain with its parameters.
type step struct {
	name  string
	parms *dict
}

// longNames spells out the abbreviations inline images use (§8.9.7, Table 94).
var longNames = map[string]string{
	"AHx": "ASCIIHexDecode",
	"A85": "ASCII85Decode",
	"LZW": "LZWDecode",
	"Fl":  "FlateDecode",
	"RL":  "RunLengthDecode",
	"CCF": "CCITTFaxDecode",
	"DCT": "DCTDecode",
}

// pictures are the codecs whose output is pixels.
var pictures = map[string]bool{
	"DCTDecode": true, "JPXDecode": true, "CCITTFaxDecode": true, "JBIG2Decode": true,
}

// canonical is a filter's full name.
func canonical(name string) string {
	if long, ok := longNames[name]; ok {
		return long
	}
	return name
}

// decode runs data through the chain in order and answers at most limit bytes.
// A stage whose input is damaged hands what it decoded before the damage to the
// next one, so a truncated stream still yields the text it holds; the error says
// it was damaged. errTooBig and errPicture stop the chain.
func decode(chain []step, data []byte, limit int) ([]byte, error) {
	var damaged error
	for _, s := range chain {
		out, err := decodeOne(s, data, limit)
		switch {
		case errors.Is(err, errTooBig), errors.Is(err, errPicture):
			return nil, err
		case err != nil && damaged == nil:
			damaged = fmt.Errorf("%s: %w", s.name, err)
		}
		data = out
	}
	return data, damaged
}

func decodeOne(s step, data []byte, limit int) ([]byte, error) {
	switch name := canonical(s.name); {
	case name == "FlateDecode":
		out, err := inflate(data, limit)
		return predict(out, s.parms, limit, err)
	case name == "LZWDecode":
		early := 1
		if v, ok := s.parms.int("EarlyChange"); ok {
			early = int(v)
		}
		out, err := unlzw(data, early, limit)
		return predict(out, s.parms, limit, err)
	case name == "ASCII85Decode":
		return un85(data, limit)
	case name == "ASCIIHexDecode":
		return unhex(data, limit)
	case name == "RunLengthDecode":
		return unrun(data, limit)
	case name == "Crypt":
		// Identity is the only crypt filter a reader without the document's key can
		// apply, and an encrypted document is not rewritten at all (plain).
		n, ok := s.parms.name("Name")
		if !ok || n == "Identity" {
			return data, nil
		}
		return nil, fmt.Errorf("crypt filter %q", n)
	case pictures[name]:
		return nil, errPicture
	default:
		return nil, fmt.Errorf("unknown filter %q", s.name)
	}
}

// inflate is zlib, or a raw deflate stream from a writer that left the header off.
func inflate(data []byte, limit int) ([]byte, error) {
	var r io.Reader
	if zr, err := zlib.NewReader(bytes.NewReader(data)); err == nil {
		r = zr
	} else {
		r = flate.NewReader(bytes.NewReader(data))
	}
	out, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if len(out) > limit {
		return nil, errTooBig
	}
	return out, err
}

// unlzw is the LZW of §7.4.4: codes 9 to 12 bits wide, most significant bit
// first, 256 clears the table and 257 ends the data. early is EarlyChange: 1 (the
// default) widens the code one entry before the table needs it, 0 when it does.
func unlzw(data []byte, early, limit int) ([]byte, error) {
	const clear, eod = 256, 257
	var (
		table = make([][]byte, 4096)
		next  = 258
		width = 9
		prev  []byte
		out   []byte
		acc   uint32
		bits  int
		pos   int
	)
	for i := range 256 {
		table[i] = []byte{byte(i)}
	}
	for {
		for bits < width && pos < len(data) {
			acc = acc<<8 | uint32(data[pos])
			pos++
			bits += 8
		}
		if bits < width {
			return out, nil // the data ended without an end code, as many writers leave it
		}
		code := int(acc>>(bits-width)) & (1<<width - 1)
		bits -= width
		switch {
		case code == clear:
			next, width, prev = 258, 9, nil
			continue
		case code == eod:
			return out, nil
		}
		var entry []byte
		switch {
		case code < next && table[code] != nil:
			entry = table[code]
		case code == next && prev != nil:
			entry = append(append(make([]byte, 0, len(prev)+1), prev...), prev[0])
		default:
			return out, fmt.Errorf("lzw: code %d past the table (%d)", code, next)
		}
		if len(out)+len(entry) > limit {
			return nil, errTooBig
		}
		out = append(out, entry...)
		if prev != nil && next < len(table) {
			table[next] = append(append(make([]byte, 0, len(prev)+1), prev...), entry[0])
			next++
		}
		prev = entry
		if next+early >= 1<<width && width < 12 {
			width++
		}
	}
}

// un85 is ASCII base-85 (§7.4.3): an optional <~, the groups, ~> at the end.
func un85(data []byte, limit int) ([]byte, error) {
	data = bytes.TrimLeft(data, " \t\r\n\f\x00")
	data = bytes.TrimPrefix(data, []byte("<~"))
	if i := bytes.Index(data, []byte("~>")); i >= 0 {
		data = data[:i]
	}
	// Five characters make four bytes, except 'z', which alone makes four zero
	// bytes — so the output is sized, and bounded, before anything is decoded.
	need := 4
	for _, c := range data {
		switch {
		case c == 'z':
			need += 5
		case c > ' ':
			need++
		}
	}
	need = need * 4 / 5
	if need > limit+4 {
		return nil, errTooBig
	}
	out := make([]byte, need+4)
	n, _, err := ascii85.Decode(out, data, true)
	if n > limit {
		return nil, errTooBig
	}
	return out[:n], err
}

// unhex is ASCII hexadecimal (§7.4.2): white space ignored, > ends it, and an odd
// last digit is followed by an implied 0.
func unhex(data []byte, limit int) ([]byte, error) {
	out := make([]byte, 0, len(data)/2)
	var hi byte
	half := false
	for _, c := range data {
		var v byte
		switch {
		case c >= '0' && c <= '9':
			v = c - '0'
		case c >= 'a' && c <= 'f':
			v = c - 'a' + 10
		case c >= 'A' && c <= 'F':
			v = c - 'A' + 10
		case c == '>':
			if half {
				out = append(out, hi<<4)
			}
			return out, nil
		case isWhite(c):
			continue
		default:
			return out, fmt.Errorf("asciihex: byte %q", c)
		}
		if half {
			if len(out) >= limit {
				return nil, errTooBig
			}
			out = append(out, hi<<4|v)
		} else {
			hi = v
		}
		half = !half
	}
	if half {
		out = append(out, hi<<4)
	}
	return out, nil
}

// unrun is RunLengthDecode (§7.4.5): a length byte n, then n+1 literal bytes when
// n < 128, or one byte repeated 257-n times when n > 128; 128 ends the data.
func unrun(data []byte, limit int) ([]byte, error) {
	var out []byte
	for i := 0; i < len(data); {
		n := int(data[i])
		i++
		switch {
		case n == 128:
			return out, nil
		case n < 128:
			end := min(i+n+1, len(data))
			if len(out)+end-i > limit {
				return nil, errTooBig
			}
			out = append(out, data[i:end]...)
			if end-i < n+1 {
				return out, errors.New("runlength: literal run past the end")
			}
			i = end
		default:
			if i >= len(data) {
				return out, errors.New("runlength: repeat past the end")
			}
			if len(out)+257-n > limit {
				return nil, errTooBig
			}
			out = append(out, bytes.Repeat(data[i:i+1], 257-n)...)
			i++
		}
	}
	return out, nil
}

// predict undoes the predictor Flate and LZW data may carry (§7.4.4.4): 2 is
// TIFF horizontal differencing, 10 to 15 are PNG, where each row names its own
// filter. derr is the decoder's own error, passed through.
func predict(data []byte, parms *dict, limit int, derr error) ([]byte, error) {
	pred, ok := parms.int("Predictor")
	if !ok || pred <= 1 {
		return data, derr
	}
	colors, bpc, columns := int64(1), int64(8), int64(1)
	if v, ok := parms.int("Colors"); ok && v > 0 {
		colors = v
	}
	if v, ok := parms.int("BitsPerComponent"); ok && v > 0 {
		bpc = v
	}
	if v, ok := parms.int("Columns"); ok && v > 0 {
		columns = v
	}
	if colors > 64 || columns > 1<<24 || bpc != 1 && bpc != 2 && bpc != 4 && bpc != 8 && bpc != 16 {
		return data, errors.Join(derr, fmt.Errorf("predictor: %d colors × %d bits × %d columns", colors, bpc, columns))
	}
	row := int((colors*bpc*columns + 7) / 8)
	switch {
	case pred == 2:
		return untiff(data, row, int(colors), int(bpc)), derr
	case pred >= 10 && pred <= 15:
		out, err := unpng(data, row, int(max(1, (colors*bpc+7)/8)), limit)
		return out, errors.Join(derr, err)
	default:
		return data, errors.Join(derr, fmt.Errorf("predictor %d", pred))
	}
}

// unpng reverses the PNG row filters: each row is a filter byte (0 None, 1 Sub,
// 2 Up, 3 Average, 4 Paeth) then row bytes; bpp is the distance to the byte the
// filter is relative to.
func unpng(data []byte, row, bpp, limit int) ([]byte, error) {
	out := make([]byte, 0, len(data)/(row+1)*row)
	prior := make([]byte, row)
	for len(data) > 0 {
		tag := data[0]
		cur := data[1:min(len(data), row+1)]
		data = data[min(len(data), row+1):]
		line := make([]byte, row)
		copy(line, cur)
		for i := range line {
			var left, upleft byte
			if i >= bpp {
				left, upleft = line[i-bpp], prior[i-bpp]
			}
			up := prior[i]
			switch tag {
			case 0:
			case 1:
				line[i] += left
			case 2:
				line[i] += up
			case 3:
				line[i] += byte((int(left) + int(up)) / 2)
			case 4:
				line[i] += paeth(left, up, upleft)
			default:
				return out, fmt.Errorf("png predictor: row filter %d", tag)
			}
		}
		if len(out)+len(cur) > limit {
			return nil, errTooBig
		}
		out = append(out, line[:len(cur)]...)
		prior = line
	}
	return out, nil
}

func paeth(a, b, c byte) byte {
	p := int(a) + int(b) - int(c)
	pa, pb, pc := abs(p-int(a)), abs(p-int(b)), abs(p-int(c))
	switch {
	case pa <= pb && pa <= pc:
		return a
	case pb <= pc:
		return b
	}
	return c
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// untiff reverses TIFF predictor 2: within each row, every sample is stored as its
// difference from the sample of the same component one pixel to the left, modulo
// the component's width.
func untiff(data []byte, row, colors, bpc int) []byte {
	out := append([]byte(nil), data...)
	for start := 0; start < len(out); start += row {
		line := out[start:min(start+row, len(out))]
		switch bpc {
		case 8:
			for i := colors; i < len(line); i++ {
				line[i] += line[i-colors]
			}
		case 16:
			for i := 2 * colors; i+1 < len(line); i += 2 {
				v := uint16(line[i])<<8 | uint16(line[i+1])
				l := uint16(line[i-2*colors])<<8 | uint16(line[i-2*colors+1])
				v += l
				line[i], line[i+1] = byte(v>>8), byte(v)
			}
		default:
			mask := uint(1)<<bpc - 1
			samples := len(line) * 8 / bpc
			for i := colors; i < samples; i++ {
				v := (sample(line, i, bpc) + sample(line, i-colors, bpc)) & mask
				put(line, i, bpc, v)
			}
		}
	}
	return out
}

// sample reads the i-th bpc-bit sample of a row, most significant bit first.
func sample(line []byte, i, bpc int) uint {
	bit := i * bpc
	return uint(line[bit/8]>>(8-bpc-bit%8)) & (1<<bpc - 1)
}

// put writes the i-th bpc-bit sample of a row.
func put(line []byte, i, bpc int, v uint) {
	bit := i * bpc
	shift := 8 - bpc - bit%8
	mask := byte((1<<bpc - 1) << shift)
	line[bit/8] = line[bit/8]&^mask | byte(v<<shift)&mask
}
