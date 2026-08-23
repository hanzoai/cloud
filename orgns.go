package cloud

import (
	// namespace is the ONE thing that names the database an entity's data lives
	// in, and the ONE thing that turns that name into a location. It is a value
	// with constructors, not a string with a validator, because the string form
	// IS the path: a name a validator lets through late has already been written
	// to disk and shipped to the object store by then.
	"github.com/hanzoai/namespace"
)

// orgns.go is the ONE place cloud turns a validated principal into the NAME of
// the database that principal's records live in.
//
// The naming itself is not cloud's to own. A namespace, the injective slug it
// is built from, and the key and path it renders to are one primitive, and it
// lives in hanzoai/namespace so that every service reaches the same answer —
// these strings are directory names on live volumes and keys in live buckets,
// and a second implementation of them does not fail, it opens an empty database
// beside a real one. What stays here is the ENTRY POINT: which cloud values are
// allowed to become a name.
//
// THE ISOLATION ARGUMENT, in one paragraph. An ENTITY's namespace is reachable
// only through OrgNamespace. Its input is folded through namespace.Sanitize —
// which refuses any org carrying a whitespace, control or format rune, and
// disambiguates every other fold with a hash of the raw owner, so it is
// injective — and then through the segment rule, which admits only
// [a-z0-9][a-z0-9_-]* and folds case. So the only way to name an entity's
// database is to hold an org string, and the only org strings in this codebase
// come from principal.Org (a validated IAM claim) or from a server-side
// resolution an in-process caller states as its contract. A namespace built
// from a query parameter, a body field, a header or a caller-supplied id would
// have to pass through OrgNamespace too — which is the point of having one
// entry point.
//
// namespace.System is outside the argument rather than an exception to it: it
// takes no input, so nothing can be folded into it, and it names the deployment
// rather than an entity. A platform store says so where it opens.

// OrgNamespace names the database an org's records live in — or, when project
// is non-empty, the database that org's records for one project live in.
//
// org MUST be the VALIDATED principal value (principal.Org, and for the project
// scope principal.Project), never a raw request body or header. It IS
// namespace.OrgProject: the org is folded through the ONE injective slugger, so
// two distinct orgs can never share a namespace, and the project rides in the
// GROUP slot, which is what a group is for.
//
// It stays spelled here, as cloud's name for that entry point rather than a
// second implementation of it, because "which values may name a database" is a
// question about cloud's principals — and TestOnlyOrgnsBuildsANamespace answers
// it by proving this file is the only one that asks.
func OrgNamespace(org, project string) (namespace.Namespace, error) {
	return namespace.OrgProject(org, project)
}

// MustOrgNamespace is OrgNamespace for an org fixed in the source — a test, a
// seed, a constant in a migration. It panics, which is correct for a value that
// is wrong before the program runs and wrong for anything from a request.
func MustOrgNamespace(org, project string) namespace.Namespace {
	return namespace.MustOrgProject(org, project)
}

// Reserved names the database the DEPLOYMENT's own records live in, and it goes
// in BOTH slots above — OrgNamespace(Reserved, Reserved). The platform switches
// (apps/flags) and the launch registry (apps/admission) open there; no tenant's
// records ever do, because no tenant can name it: an org string is folded
// through namespace.Sanitize on the way in and "platform" is already taken by
// this one.
//
// It is a REAL org namespace rather than namespace.System(). The system name is
// the better name for the deployment's own partition, but it renders to a
// different file, and moving a live store is a migration rather than a rename.
// Left as it is deliberately, so the move is a decision somebody makes.
//
// It is NOT a company: nothing here says "Hanzo". The org that OWNS the rows
// every tenant sees is an ordinary IAM org id, and apps/taxonomy spells it out
// as the value it is.
const Reserved = "platform"

// nsOnDisk reads a namespace back out of the directory name OrgNamespace wrote.
//
// It is the inverse of the entry point above, not a second one: the segment
// it is given was produced by namespace.Sanitize when the store was created,
// so folding it through Sanitize AGAIN would be wrong — a slug that already
// carries a disambiguation suffix looks exactly like a raw owner that needs
// one, and would be re-suffixed into the name of a different, empty database.
//
// It exists so the one construction in this package that does not start at a
// principal is visible and greppable rather than an inline call.
func nsOnDisk(slug string) (namespace.Namespace, error) { return namespace.Org(slug) }
