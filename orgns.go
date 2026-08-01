package cloud

import (
	"fmt"
	"path"
	"path/filepath"

	"github.com/hanzoai/cloud/cek"

	// namespace is the ONE thing that names the database an entity's data lives
	// in. It is a value with constructors, not a string with a validator,
	// because the string form IS the path: a name a validator lets through late
	// has already been written to disk and shipped to the object store by then.
	"github.com/hanzoai/namespace"
)

// orgns.go is the ONE place cloud turns a validated principal into the NAME of
// the database that principal's records live in, and the ONE place that name
// becomes a location.
//
// Before this there were two spellings of the same fact — a local path built by
// filepath.Join and an object key built by org.DBPath — which is one fact with
// two homes and therefore a fact that can disagree with itself. Both are now
// rendered from one namespace by nsKey, so a store's file and its durable slot
// cannot drift apart.
//
// THE ISOLATION ARGUMENT, in one paragraph. A namespace is reachable only
// through OrgNamespace or PlatformNamespace. OrgNamespace's input is folded
// through SanitizeOrg — which refuses any org carrying a whitespace, control or
// format rune, and disambiguates every other fold with a hash of the raw owner,
// so it is injective — and then through namespace.Org, which admits only
// [a-z0-9][a-z0-9_-]* and folds case. PlatformNamespace takes no input at all.
// So the only way to obtain a namespace is to hold an org string, and the only
// org strings in this codebase come from principal.Org (a validated IAM claim)
// or from a server-side resolution an in-process caller states as its contract.
// A namespace built from a query parameter, a body field, a header or a
// caller-supplied id would have to pass through OrgNamespace too — which is the
// point of having exactly one door.

// OrgNamespace names the database an org's records live in — or, when project
// is non-empty, the database that org's records for one project live in.
//
// org MUST be the VALIDATED principal value (principal.Org, and for the project
// scope principal.Project), never a raw request body or header. It is folded
// through SanitizeOrg, the ONE injective org slugger, so two distinct orgs can
// never share a namespace: a case fold on a case-insensitive filesystem, or a
// "-"/"." fold, would otherwise collapse them onto one file.
//
// The project rides in the namespace's GROUP slot, which is what a group is for
// — "which of this entity's related types this file holds". The subsystem does
// NOT: one OrgStore is one subsystem, so the subsystem is a property of the
// registry and not of the name. That is why nsKey takes it separately.
//
// It is stricter than the old path resolver in exactly one place, and the
// strictness is the point. SanitizeOrg emits a leading "-" for the two org ids
// that fold to nothing printable (an org named "!!!", whose folded form is
// empty, and an org literally named "-abc", which takes the identity
// fast-path). namespace.Org refuses a segment that does not begin with
// [a-z0-9], so those two now error here rather than quietly minting a directory
// called "-<hash>". No IAM org can be named either of those things, and an
// error at the door is the correct answer for a name that should not exist.
func OrgNamespace(org, project string) (namespace.Namespace, error) {
	slug := SanitizeOrg(org)
	if slug == "" {
		return namespace.Namespace{}, fmt.Errorf("cloud: invalid org %q", org)
	}
	ns, err := namespace.Org(slug)
	if err != nil {
		return namespace.Namespace{}, fmt.Errorf("cloud: invalid org %q: %w", org, err)
	}
	if project == "" {
		return ns, nil
	}
	projSlug := SanitizeOrg(project)
	if projSlug == "" {
		return namespace.Namespace{}, fmt.Errorf("cloud: invalid project %q", project)
	}
	g, err := namespace.NewGroup(projSlug)
	if err != nil {
		return namespace.Namespace{}, fmt.Errorf("cloud: invalid project %q: %w", project, err)
	}
	return ns.WithGroup(g), nil
}

// MustOrgNamespace is OrgNamespace for an org fixed in the source — a test, a
// seed, a constant in a migration. It panics, which is correct for a value that
// is wrong before the program runs and wrong for anything from a request.
func MustOrgNamespace(org, project string) namespace.Namespace {
	ns, err := OrgNamespace(org, project)
	if err != nil {
		panic(err)
	}
	return ns
}

// nsOnDisk reads a namespace back out of the directory name OrgNamespace wrote.
//
// It is the inverse of the door above, not a second one: the segment it is
// given was produced by SanitizeOrg when the store was created, so folding it
// through SanitizeOrg AGAIN would be wrong — a slug that already carries a
// disambiguation suffix looks exactly like a raw owner that needs one, and
// would be re-suffixed into the name of a different, empty database.
//
// It exists so the one construction in this package that does not start at a
// principal is visible and greppable rather than an inline call.
func nsOnDisk(slug string) (namespace.Namespace, error) { return namespace.Org(slug) }

// PlatformNamespace names the deployment's own partition of an otherwise
// per-org subsystem: the records a per-org store holds that belong to no single
// tenant, such as a platform-wide HMAC key.
//
// It takes no argument, because there is one deployment. That is the whole
// improvement over the constant it replaces: the platform partition used to be
// disjoint from every tenant's because its slug carried a "_" and SanitizeOrg
// never emits one — a true argument, but one that has to be re-derived by
// whoever reads the code next, and one that stops being true the day someone
// widens the slugger. Now the two are disjoint because they are different
// KINDS, which no edit to a slugger can undo.
func PlatformNamespace() namespace.Namespace { return namespace.System() }

// orgsRoot is the single directory every namespace's files live under. It is
// spelled once, here, so nsKey and the on-disk sweeps cannot disagree about
// where a store is.
const orgsRoot = "orgs"

// reservedPlatformSlug is how the system namespace renders in the on-disk
// layout that predates it. It is a rune SanitizeOrg never emits, so the
// directory it names cannot collide with a tenant's — and it is spelled here,
// once, purely so the files that already exist keep their path.
const reservedPlatformSlug = "_platform"

// nsKey renders a namespace as the place a subsystem's file for it lives:
// relative to DataDir on disk, and byte-identically the object key durability
// ships it to. One rendering, two consumers, so the local file and the remote
// slot are the same name by construction.
//
//	org/{slug}                → orgs/{slug}/{subsystem}.db
//	org/{slug}/{project}      → orgs/{slug}/projects/{project}/{subsystem}.db
//	system                    → orgs/_platform/{subsystem}.db
//
// Every other kind is an error: user and repo namespaces are real namespaces,
// but cloud's org-DB layer has no layout for them, and inventing one silently
// is how a second convention starts.
func nsKey(ns namespace.Namespace, subsystem string) (string, error) {
	if subsystem == "" {
		return "", fmt.Errorf("cloud: empty subsystem")
	}
	switch ns.Kind() {
	case namespace.KindSystem:
		return path.Join(orgsRoot, reservedPlatformSlug, subsystem+".db"), nil
	case namespace.KindOrg:
		if g := ns.Group().String(); g != "" {
			return path.Join(orgsRoot, ns.ID(), "projects", g, subsystem+".db"), nil
		}
		return path.Join(orgsRoot, ns.ID(), subsystem+".db"), nil
	case "":
		return "", fmt.Errorf("cloud: the zero namespace names no database")
	default:
		return "", fmt.Errorf("cloud: no org-DB layout for a %s namespace", ns.Kind())
	}
}

// nsPath is nsKey resolved against a deployment's DataDir.
func nsPath(dataDir string, ns namespace.Namespace, subsystem string) (string, error) {
	if dataDir == "" {
		return "", fmt.Errorf("cloud: empty dataDir")
	}
	key, err := nsKey(ns, subsystem)
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, filepath.FromSlash(key)), nil
}

// nsPrincipal maps a namespace to the principal whose key opens its file. The
// system namespace is NOT a tenant — it is the deployment's own partition — so
// it keys under Global exactly like every other platform store, and only a real
// entity gets an owner-bound key.
func nsPrincipal(ns namespace.Namespace) cek.Principal {
	if ns.Kind() == namespace.KindSystem {
		return cek.Global
	}
	return cek.Org(ns.ID())
}
