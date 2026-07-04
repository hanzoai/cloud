package automations

import (
	"crypto/rand"
	"encoding/hex"
)

// genID returns a prefixed, collision-resistant id (prefix + 128 random bits).
func genID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(b[:]), nil
}

// validOrg accepts a DNS-1123-ish label. The org is folded into per-org engine
// namespaces + the SQLite store key, so it is validated strictly at every boundary.
// Identical rule to clients/integrations' tenant boundary (kept local because that
// copy is unexported; both mirror the SAME platform org-slug shape).
func validOrg(org string) bool {
	if org == "" || len(org) > 63 {
		return false
	}
	for _, r := range org {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}
