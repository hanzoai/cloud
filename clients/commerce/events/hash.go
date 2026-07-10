package events

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// SHA256Hex returns the lowercase hex SHA256 hash of a trimmed, lowercased string.
// Facebook CAPI requires user_data fields (em, ph) to be SHA256-hashed.
// Returns empty string for empty input.
func SHA256Hex(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
