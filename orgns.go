package cloud

import (
	"github.com/hanzoai/cloud/cek"

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
// beside a real one. What stays here is the DOOR: which cloud values are
// allowed to become a name.
//
// THE ISOLATION ARGUMENT, in one paragraph. A namespace is reachable only
// through OrgNamespace or PlatformNamespace. OrgNamespace's input is folded
// through namespace.Sanitize — which refuses any org carrying a whitespace,
// control or format rune, and disambiguates every other fold with a hash of the
// raw owner, so it is injective — and then through the segment rule, which
// admits only [a-z0-9][a-z0-9_-]* and folds case. PlatformNamespace takes no
// input at all. So the only way to obtain a namespace is to hold an org string,
// and the only org strings in this codebase come from principal.Org (a
// validated IAM claim) or from a server-side resolution an in-process caller
// states as its contract. A namespace built from a query parameter, a body
// field, a header or a caller-supplied id would have to pass through
// OrgNamespace too — which is the point of having exactly one door.

// OrgNamespace names the database an org's records live in — or, when project
// is non-empty, the database that org's records for one project live in.
//
// org MUST be the VALIDATED principal value (principal.Org, and for the project
// scope principal.Project), never a raw request body or header. It IS
// namespace.OrgProject: the org is folded through the ONE injective slugger, so
// two distinct orgs can never share a namespace, and the project rides in the
// GROUP slot, which is what a group is for.
//
// It stays spelled here, as cloud's name for that door rather than a second
// implementation of it, because "which values may name a database" is a
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

// nsOnDisk reads a namespace back out of the directory name OrgNamespace wrote.
//
// It is the inverse of the door above, not a second one: the segment it is
// given was produced by namespace.Sanitize when the store was created, so
// folding it through Sanitize AGAIN would be wrong — a slug that already
// carries a disambiguation suffix looks exactly like a raw owner that needs
// one, and would be re-suffixed into the name of a different, empty database.
//
// It exists so the one construction in this package that does not start at a
// principal is visible and greppable rather than an inline call.
func nsOnDisk(slug string) (namespace.Namespace, error) { return namespace.Org(slug) }

// PlatformNamespace names the deployment's own partition of an otherwise
// per-org subsystem: the records a per-org store holds that belong to no single
// org, such as a platform-wide HMAC key.
//
// It takes no argument, because there is one deployment. That is the whole
// improvement over the constant it replaces: the platform partition used to be
// disjoint from every org's because its slug carried a "_" and the slugger
// never emits one — a true argument, but one that has to be re-derived by
// whoever reads the code next, and one that stops being true the day someone
// widens the slugger. Now the two are disjoint because they are different
// KINDS, which no edit to a slugger can undo.
func PlatformNamespace() namespace.Namespace { return namespace.System() }

// nsPrincipal maps a namespace to the principal whose key opens its file. The
// system namespace is NOT an org — it is the deployment's own partition — so it
// keys under Global exactly like every other platform store, and only a real
// entity gets an owner-bound key.
//
// It is the one thing in this file that could not move to hanzoai/namespace:
// a key derivation is cek's, not a name's.
func nsPrincipal(ns namespace.Namespace) cek.Principal {
	if ns.Kind() == namespace.KindSystem {
		return cek.Global
	}
	return cek.Org(ns.ID())
}
