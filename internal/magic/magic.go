package magic

import "bytes"

// Type returns the canonical MIME type of a recognized file, or "" for everything
// else. The signature checks are length-guarded, so a short or empty input is
// simply unrecognized rather than a panic.
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
	// A PDF opens with "%PDF-". The prefix is required at offset zero — readers
	// tolerate leading junk, browsers do not need to, and the stricter rule is the
	// one that cannot be walked toward another format.
	case len(data) >= 5 && string(data[:5]) == "%PDF-":
		return "application/pdf"
	}
	return ""
}
