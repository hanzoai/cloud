package magic

import "testing"

// The four recognized formats, each by its real signature.
func TestRecognizedTypes(t *testing.T) {
	cases := map[string]struct {
		data []byte
		want string
	}{
		"png":    {append([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, 0, 0, 0, 13), "image/png"},
		"jpeg":   {[]byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0x10, 'J', 'F', 'I', 'F'}, "image/jpeg"},
		"gif87a": {[]byte("GIF87a\x01\x00\x01\x00\x00\x00"), "image/gif"},
		"gif89a": {[]byte("GIF89a\x01\x00\x01\x00\x00\x00"), "image/gif"},
		"webp":   {[]byte("RIFF\x24\x00\x00\x00WEBPVP8 "), "image/webp"},
		"pdf":    {[]byte("%PDF-1.7\n1 0 obj\n"), "application/pdf"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := Type(c.data); got != c.want {
				t.Fatalf("Type = %q, want %q", got, c.want)
			}
		})
	}
}

// The whole point of the package: things that are NOT pictures must not come back
// as a type a browser will render. SVG is the one that matters — it is a program,
// and a viewer that renders it under image/svg+xml runs its <script> in the
// origin that served it.
func TestActiveContentIsNotAnImage(t *testing.T) {
	for name, data := range map[string]string{
		"svg":        `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`,
		"html":       `<!doctype html><html><body><script>alert(1)</script></body></html>`,
		"xml":        `<?xml version="1.0"?><root/>`,
		"empty":      "",
		"plain text": "hello",
	} {
		t.Run(name, func(t *testing.T) {
			if got := Type([]byte(data)); got != "" {
				t.Fatalf("Type = %q, want \"\" — only the allow-list may be served inline", got)
			}
		})
	}
}

// A signature is only a PREFIX, so a truncated one must not match: the guards are
// length checks, and an off-by-one here is an index panic on a hostile upload.
func TestTruncatedSignaturesAreUnrecognizedNotAPanic(t *testing.T) {
	full := map[string][]byte{
		"png":  {0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A},
		"jpeg": {0xFF, 0xD8, 0xFF},
		"gif":  []byte("GIF89a"),
		"webp": []byte("RIFF\x00\x00\x00\x00WEBP"),
		"pdf":  []byte("%PDF-"),
	}
	for name, sig := range full {
		t.Run(name, func(t *testing.T) {
			for n := range sig {
				if got := Type(sig[:n]); got != "" {
					t.Fatalf("Type(%d of %d bytes) = %q, want \"\"", n, len(sig), got)
				}
			}
			if Type(sig) == "" {
				t.Fatalf("the complete %s signature must still be recognized", name)
			}
		})
	}
}

// A RIFF container that is not WEBP (a .wav, say) shares the first four bytes, so
// the second half of the check is load-bearing.
func TestRiffThatIsNotWebp(t *testing.T) {
	if got := Type([]byte("RIFF\x24\x00\x00\x00WAVEfmt ")); got != "" {
		t.Fatalf("Type = %q, want \"\" — RIFF alone is not WEBP", got)
	}
}

// A file may satisfy two formats at once. What the bytes OPEN with decides, and a
// caller pairs the answer with nosniff, so a document that is also markup is served
// as the document and never runs as markup.
func TestPolyglotIsNamedByItsOpeningBytes(t *testing.T) {
	for name, c := range map[string]struct {
		data []byte
		want string
	}{
		"pdf carrying markup": {[]byte("%PDF-1.7\n<script>alert(1)</script>\n%%EOF"), "application/pdf"},
		"markup carrying pdf": {[]byte("<html><body>%PDF-1.7</body></html>"), ""},
		"png carrying markup": {append([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, []byte("<script>alert(1)</script>")...), "image/png"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := Type(c.data); got != c.want {
				t.Fatalf("Type = %q, want %q", got, c.want)
			}
		})
	}
}
