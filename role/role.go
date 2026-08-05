// Package role resolves the HA role of a Hanzo Cloud process: the single
// authoritative WRITER (owns the RWO data dir — the ZapDB KMS store, the SQLite
// audit chain, the durable task store, and per-tenant SQLite) or a stateless
// READER (no RWO PVC, no exclusive store lock; hydrates read-only replicas from
// the S3/vfs replication stream and serves read paths).
//
// INVARIANT — exactly one process may open the RWO stores for WRITE at a time.
// The writer is a StatefulSet with replicas:1 + Recreate; readers are a
// RollingUpdate Deployment with no RWO mount. This package makes the role an
// explicit, first-class value instead of an implicit "there is only one pod"
// assumption, so readers can be added without ever risking a second writer.
//
// DEFAULT — an unset CLOUD_ROLE resolves to Writer, so a deployment that does
// not set the variable behaves EXACTLY as cloud does today (one writer pod).
// This package has zero effect until something reads it: it imports nothing from
// cloud and mutates no global state.
package role

import (
	"fmt"
	"os"
	"strings"
)

// EnvVar is the environment variable that selects the role. Unset ⇒ Writer.
const EnvVar = "CLOUD_ROLE"

// Role is the HA role of this process.
type Role string

const (
	// Writer owns the RWO data dir and is the ONLY process permitted to open the
	// ZapDB KMS store, the SQLite audit chain, and per-tenant SQLite for write.
	// It streams its WAL/backup to S3/vfs for readers to hydrate from.
	Writer Role = "writer"

	// Reader holds no RWO PVC and never takes an exclusive store lock. It opens
	// read-only replicas hydrated from the writer's replication stream and serves
	// GET/read traffic, enabling rolling upgrades + read horizontal scale.
	Reader Role = "reader"
)

// IsWriter reports whether this role owns the RWO stores.
func (r Role) IsWriter() bool { return r == Writer }

// IsReader reports whether this role is a read-only replica.
func (r Role) IsReader() bool { return r == Reader }

// String implements fmt.Stringer.
func (r Role) String() string { return string(r) }

// FromEnv resolves the role from CLOUD_ROLE.
//
//   - unset / empty      → Writer, nil   (byte-identical to today's single pod)
//   - "writer" / "reader" (any case, trimmed) → that role, nil
//   - anything else      → Writer, error (fail-secure: the caller MUST refuse to
//     boot on a malformed role rather than silently guess — guessing "reader"
//     could drop a real writer, guessing "writer" could double-open a store)
//
// The returned error is non-nil ONLY for an explicitly-set invalid value; the
// caller decides to fail closed. The Role is always usable (Writer on error) so
// a caller that chooses to log-and-continue still lands on the safe default.
func FromEnv() (Role, error) {
	return parse(os.Getenv(EnvVar))
}

func parse(raw string) (Role, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(Writer):
		return Writer, nil
	case string(Reader):
		return Reader, nil
	default:
		return Writer, fmt.Errorf("role: invalid %s=%q (want %q or %q)", EnvVar, raw, Writer, Reader)
	}
}
