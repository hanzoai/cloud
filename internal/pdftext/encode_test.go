package pdftext

// encode_test.go holds the ENCODERS the tests build documents with. They are
// written from the specification, not from the decoders, and the two that could
// share a mistake with a decoder — LZW's code widths and the predictors — are
// checked against independent implementations in filter_test.go.

// lzwEncode is PDF's LZW (§7.4.4): a clear code first, codes 9 to 12 bits wide
// most significant bit first, the end code last. early is EarlyChange: the code
// widens when the table reaches one short of the next power of two (1) or at it
// (0). The table is cleared before it fills, as writers do.
func lzwEncode(data []byte, early int) []byte {
	const clear, eod = 256, 257
	var (
		out   []byte
		acc   uint64
		bits  int
		width = 9
		next  = 258
		table = map[string]int{}
	)
	emit := func(code int) {
		acc = acc<<width | uint64(code)
		bits += width
		for bits >= 8 {
			out = append(out, byte(acc>>(bits-8)))
			bits -= 8
		}
	}
	reset := func() {
		table = map[string]int{}
		for i := range 256 {
			table[string([]byte{byte(i)})] = i
		}
		next, width = 258, 9
	}
	reset()
	emit(clear)
	var w []byte
	for _, c := range data {
		wc := append(append([]byte(nil), w...), c)
		if _, ok := table[string(wc)]; ok {
			w = wc
			continue
		}
		emit(table[string(w)])
		table[string(wc)] = next
		next++
		if next+early-1 >= 1<<width && width < 12 {
			width++
		}
		if next >= 4093 {
			emit(clear)
			reset()
		}
		w = []byte{c}
	}
	if len(w) > 0 {
		emit(table[string(w)])
		next++
		if next+early-1 >= 1<<width && width < 12 {
			width++
		}
	}
	emit(eod)
	if bits > 0 {
		out = append(out, byte(acc<<(8-bits)))
	}
	return out
}

// rowsOf cuts data into predictor rows of n bytes, the last zero-padded.
func rowsOf(data []byte, n int) [][]byte {
	var rows [][]byte
	for i := 0; i < len(data); i += n {
		row := make([]byte, n)
		copy(row, data[i:min(len(data), i+n)])
		rows = append(rows, row)
	}
	return rows
}

// pngEncode applies the PNG row filters (§7.4.4.4, PNG §9), cycling each row
// through None, Sub, Up, Average and Paeth so every filter is exercised.
func pngEncode(data []byte, columns, colors, bpc int) []byte {
	return png(data, columns, colors, bpc, func(r int) byte { return byte(r % 5) })
}

// pngUp is every row filtered Up, which is what producers write in a
// cross-reference stream.
func pngUp(data []byte, columns int) []byte {
	return png(data, columns, 1, 8, func(int) byte { return 2 })
}

func png(data []byte, columns, colors, bpc int, tag func(int) byte) []byte {
	n := (colors*bpc*columns + 7) / 8
	bpp := max(1, (colors*bpc+7)/8)
	prior := make([]byte, n)
	var out []byte
	for r, row := range rowsOf(data, n) {
		t := tag(r)
		out = append(out, t)
		for i, x := range row {
			var a, c int
			if i >= bpp {
				a, c = int(row[i-bpp]), int(prior[i-bpp])
			}
			b := int(prior[i])
			var p int
			switch t {
			case 1:
				p = a
			case 2:
				p = b
			case 3:
				p = (a + b) / 2
			case 4:
				est := a + b - c
				pa, pb, pc := est-a, est-b, est-c
				if pa < 0 {
					pa = -pa
				}
				if pb < 0 {
					pb = -pb
				}
				if pc < 0 {
					pc = -pc
				}
				switch {
				case pa <= pb && pa <= pc:
					p = a
				case pb <= pc:
					p = b
				default:
					p = c
				}
			}
			out = append(out, x-byte(p))
		}
		prior = row
	}
	return out
}

// tiffEncode applies TIFF predictor 2: each sample becomes its difference from
// the same component one pixel to the left, modulo 2^bpc. Samples are unpacked to
// integers, differenced and repacked, so no bit arithmetic is shared with the
// decoder.
func tiffEncode(data []byte, columns, colors, bpc int) []byte {
	n := (colors*bpc*columns + 7) / 8
	var out []byte
	for _, row := range rowsOf(data, n) {
		var s []int
		for bit := 0; bit+bpc <= len(row)*8; bit += bpc {
			v := 0
			for k := 0; k < bpc; k++ {
				v = v<<1 | int(row[(bit+k)/8]>>(7-(bit+k)%8)&1)
			}
			s = append(s, v)
		}
		d := make([]int, len(s))
		for i := range s {
			d[i] = s[i]
			if i >= colors {
				d[i] = (s[i] - s[i-colors]) & (1<<bpc - 1)
			}
		}
		packed := make([]byte, n)
		for i, v := range d {
			for k := 0; k < bpc; k++ {
				if v>>(bpc-1-k)&1 == 1 {
					bit := i*bpc + k
					packed[bit/8] |= 1 << (7 - bit%8)
				}
			}
		}
		out = append(out, packed...)
	}
	return out
}
