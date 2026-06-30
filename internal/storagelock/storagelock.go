// Package storagelock refuses to boot the canonical Hanzo cloud
// orchestrator against Postgres.
//
// The Go cloud binary (HIP-0106) is an orchestrator that mounts every
// Hanzo subsystem (iam, base, kms, commerce, ai, ...) into one process.
// It has no storage of its own — every subsystem owns its own
// per-(org, user) SQLite via hanzoai/base (HIP-0302).
//
// The legacy deployed image (cloud-api Python/TS) used a shared
// PostgreSQL database `hanzo_cloud` on `postgres.hanzo.svc`. That
// service is being retired in favour of the Go orchestrator. The
// lockdown ensures the new binary never picks up a leaked
// `DATABASE_URL` env from a manifest that still carries the legacy
// driverName=postgres / dbName=hanzo_cloud knobs.
//
// Per the Hanzo PG → SQLite migration plan
// (~/work/hanzo/CLAUDE_PG_TO_SQLITE_MIGRATION.md, service #4 — cloud).
package storagelock

import (
	"errors"
	"fmt"
	"strings"
)

// forbiddenEnvs is the closed set of env vars that the legacy cloud-api
// Python deployment used to pin its Postgres backend. They are
// dispositive — non-empty at boot for the Go orchestrator means a
// regression copying from the legacy manifest.
var forbiddenEnvs = []string{
	"DATABASE_URL",
	"DATABASE_HOST",
	"POSTGRES_URL",
	"POSTGRES_DSN",
	"POSTGRES_HOST",
	"CLOUD_DATABASE_URL",
	"CLOUD_POSTGRES_URL",
	"HANZO_CLOUD_DB",
	// Legacy cloud-api Python/TS driverName/dbName pair. driverName ==
	// "postgres" is the smoking-gun configuration.
	"driverName",
	"dbName",
}

// pgSchemes is the prefix set that marks a DSN as a Postgres URL.
var pgSchemes = []string{
	"postgres://",
	"postgresql://",
	"postgres+",
}

// ErrPostgresForbidden is returned when an env var pins cloud at PG.
var ErrPostgresForbidden = errors.New("cloud: Postgres is not a supported storage backend (use per-tenant SQLite via hanzoai/base)")

// Violation captures one offending (var, value) pair.
type Violation struct {
	Var   string
	Value string
}

// Violations enumerates every offending env var found in the supplied
// lookup function. The lookup is parameterised so tests can drive it
// without mutating os.Environ.
//
// For driverName specifically, only "postgres" / "postgresql" values
// are violations; any other value (e.g. "sqlite") is benign and would
// be intentionally set by an operator running a transitional config.
func Violations(lookup func(string) string) []Violation {
	if lookup == nil {
		return nil
	}
	var out []Violation
	for _, k := range forbiddenEnvs {
		v := strings.TrimSpace(lookup(k))
		if v == "" {
			continue
		}
		// driverName is the legacy cloud-api Python knob; we only
		// reject "postgres" / "postgresql". Other values (e.g.
		// "sqlite") are intentional transition signals from the
		// operator and must not crash the pod.
		if k == "driverName" {
			low := strings.ToLower(v)
			if low != "postgres" && low != "postgresql" {
				continue
			}
		}
		// dbName by itself is not a violation unless paired with a
		// postgres driver; but since "hanzo_cloud" is the exact legacy
		// name and the Go binary never reads dbName, we report it as
		// an informational violation only when it has the legacy
		// value. Other values pass through.
		if k == "dbName" {
			if v != "hanzo_cloud" {
				continue
			}
		}
		out = append(out, Violation{Var: k, Value: v})
	}
	return out
}

// IsPostgres reports whether the value parses as a Postgres DSN.
func IsPostgres(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	for _, s := range pgSchemes {
		if strings.HasPrefix(v, s) {
			return true
		}
	}
	return false
}

// CheckEnv returns nil when the environment is clean, or a wrapped
// ErrPostgresForbidden enumerating every violation. cloud's main wires
// this and fails fast on any non-nil return.
func CheckEnv(lookup func(string) string) error {
	vs := Violations(lookup)
	if len(vs) == 0 {
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%v:", ErrPostgresForbidden)
	for _, v := range vs {
		kind := classify(v.Var, v.Value)
		fmt.Fprintf(&b, " %s=%s (%s)", v.Var, redact(v.Value), kind)
	}
	return errors.New(b.String())
}

func classify(k, v string) string {
	switch {
	case IsPostgres(v):
		return "Postgres DSN"
	case k == "driverName":
		return "legacy Python driver pin"
	case k == "dbName":
		return "legacy Python db pin"
	default:
		return "set"
	}
}

// redact masks credentials in a DSN so the lockdown log does not leak
// passwords. Non-DSN values pass through.
func redact(v string) string {
	if !IsPostgres(v) {
		return v
	}
	atIdx := strings.LastIndex(v, "@")
	if atIdx < 0 {
		return v
	}
	schemeEnd := strings.Index(v, "://")
	if schemeEnd < 0 || schemeEnd+3 >= atIdx {
		return v
	}
	return v[:schemeEnd+3] + "***" + v[atIdx:]
}
