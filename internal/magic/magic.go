// Package magic identifies an image format from the bytes themselves.
//
// This is an ALLOW-LIST, not a detector. It exists because the only safe answer to
// "what type is this upload?" is one derived from the stored bytes: a filename, a
// Content-Type part header and a client's word are all attacker-chosen, and a
// crafted .svg/.html served back under the type its NAME claimed executes script in
// the viewer's origin. So a caller serves what Type() returns and nothing else —
// "" means "not a raster image I will render", and the caller's job is then to
// serve it inert (application/octet-stream + attachment) or refuse it outright.
//
// The four formats here are the ones a browser renders as a picture and cannot be
// talked into treating as a document. SVG is deliberately absent and must stay
// absent: it is XML with <script> in it, so it is a program, not a picture.
//
// Deterministic and dependency-free — deliberately NOT net/http.DetectContentType,
// whose table sniffs HTML/XML and evolves between Go releases, which is exactly the
// unpinned behavior a security allow-list must not inherit.
package magic

import "bytes"

// Type returns the canonical MIME type of a recognized raster image, or "" for
// everything else. The signature checks are length-guarded, so a short or empty
// input is simply unrecognized rather than a panic.
func Type(data []byte) string {
	switch {
	case len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}):
		return "image/png"
	case len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return "image/jpeg"
	case len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a"):
		return "image/gif"
	// WEBP is a RIFF container: "RIFF" <4-byte little-endian size> "WEBP".
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "image/webp"
	}
	return ""
}
