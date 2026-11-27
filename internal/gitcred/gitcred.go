package gitcred

import (
	"os"
	"strings"
)

// Stem is the prefix every per-host credential variable carries.
const Stem = "GIT_MIRROR_TOKEN"

// Env is the name of the variable that holds the credential for host.
func Env(host string) string {
	var b strings.Builder
	b.WriteString(Stem)
	b.WriteByte('_')
	for _, r := range strings.ToUpper(strings.TrimSpace(host)) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// For returns the credential held for host, or "" when this deployment holds none.
func For(host string) string {
	if strings.TrimSpace(host) == "" {
		return ""
	}
	return strings.TrimSpace(os.Getenv(Env(host)))
}
