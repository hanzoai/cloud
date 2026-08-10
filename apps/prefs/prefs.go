// Package prefs is your own settings — theme, density, pinned nav — following you
// across every Hanzo app.
//
// It is the per-USER preference plane for the unified Hanzo Cloud binary: the
// /v1/prefs surface behind the user menu on every Hanzo surface (console, insights,
// and anything else that renders "signed in as").
//
// ONE preference store, EVERY surface. A user's theme, density, and pinned nav
// follow them between products instead of each app keeping its own copy in its
// own localStorage — which is what makes the same person look like two different
// users depending on which tab they are in.
//
// Surface (all user-scoped; /v1 only):
//
//	GET   /v1/prefs   the caller's own document          -> prefsView
//	PATCH /v1/prefs   shallow key-wise merge into it     -> prefsView
//
// PATCH, not PUT: a surface saves the keys it owns (the console saves `theme`,
// insights saves `density`) without having to send back keys it does not know
// about — a PUT would make every client responsible for preserving every other
// client's keys, and the first one to forget silently deletes them.
//
// USER ISOLATION is enforced SERVER-SIDE on every request. The subject is the
// canonical `<owner>/<name>` identity built from values the identity boundary
// minted from a VALIDATED credential (HIP-0026), and is the mandatory predicate
// on every store statement. It is NEVER read from a query param or body, and
// there is no "read another user's prefs" path at all: not for an org admin, not
// for a platform SuperAdmin. Preferences are personal, and no operational task
// requires reading someone else's.
//
// NOT SETTINGS. apps/settings is per-ORG, per-product configuration with KMS
// custody for secret fields. This is per-USER UI state with no secrets. They are
// different tenancy keys answering different questions, so they are different
// planes — collapsing them would put one user's theme under an org key and make
// an org admin the owner of everyone's UI.
package prefs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/openapi"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// maxDoc bounds a stored preference document. Preferences are a handful of small
// scalars; the bound exists so a client cannot turn a personal, unaudited row
// into general-purpose storage.
const maxDoc = 16 * 1024

// maxKeys bounds how many distinct preference keys one user may hold, for the
// same reason.
const maxKeys = 128

type service struct {
	store *Store
	log   luxlog.Logger
}

var mounted *service

// prefsView is the wire shape. Doc is passed through verbatim as raw JSON — the
// server does not interpret a preference's meaning, only its shape, so a surface
// can add a key without a server change.
type prefsView struct {
	// Prefs is the caller's preference document: an opaque JSON object whose keys
	// the surfaces own, returned verbatim. `{}` when nothing has been saved.
	Prefs json.RawMessage `json:"prefs"`
	// UpdatedAt is when the document was last written, unix seconds. Absent when
	// nothing has been saved.
	UpdatedAt int64 `json:"updatedAt,omitempty"`
}

// Mount registers the prefs surface on app per HIP-0106.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("prefs.Mount: nil app")
	}
	log := luxlog.Default().New("subsystem", "prefs")
	if deps.DataDir == "" {
		return fmt.Errorf("prefs.Mount: empty DataDir")
	}
	store, err := openStore(deps.DataDir)
	if err != nil {
		return fmt.Errorf("prefs.Mount: open prefs store: %w", err)
	}
	s := &service{store: store, log: log}
	mounted = s

	routes(app, s)

	log.Info("prefs surface mounted", "prefix", "/v1/prefs", "brand", deps.Brand)
	return nil
}

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// The prose for the one operation here that cannot be a typed op. GetPrefs is a
// typed op and zipdoc lifts its doc comment; PATCH stays an untyped handler (see
// routes for why), so there is no comment for anything to lift and the published
// document would carry an operationId and nothing else — an SDK method and a CLI
// command that cannot explain themselves. Declared through the same registry
// Register uses, so it renders only while the router actually serves the route.
func init() {
	openapi.Describe("/v1/prefs", http.MethodPatch,
		"Save the preference keys your surface owns, leaving every other key alone",
		"Merges a JSON object key-wise into the signed-in caller's OWN preference "+
			"document and answers with the whole document after the merge, so a surface "+
			"saves `theme` without having to send back the `density` another surface owns. "+
			"The merge is SHALLOW and the key space is open: an unnamed key is left "+
			"untouched, a named key is replaced whole, and a key sent with a `null` value "+
			"is DELETED. The subject is the `<owner>/<name>` identity built from the "+
			"validated credential and is the mandatory predicate on the write, so there is "+
			"no path to another user's preferences — not for an org admin, not for a "+
			"platform SuperAdmin. Fails closed: no validated principal is 403; an empty "+
			"body or a literal `null` is 400; and a patch or a resulting document over "+
			"16 KiB or 128 keys is 413.")
}

// routes is the ONE place the surface is wired, so a test drives the same router
// the binary serves rather than a reconstruction of it.
//
// Both verbs are declared at their WHOLE path rather than as an EMPTY leaf on a
// /v1/prefs group: joinPath normalises "" to "/", so the group form named
// /v1/prefs/ — a path this API has never served — in the document, the
// operationId, the MCP tool and every generated SDK's URL.
func routes(app cloud.Router, s *service) {
	// A typed op receives only a context, so the request facts its signature drops
	// reach it from that context. Whoever composes the app parks them there, at the
	// root, ahead of every leaf; this surface installs no middleware of its own. One
	// that it installed for itself could only hang on a /v1/prefs node, and both
	// leaves below register through the root, so that node would carry middleware
	// over an empty subtree and zip refuses to compose it.

	zip.Get(cloud.ZipApp(app), "/v1/prefs", prefsOps{s: s}.getPrefs)

	// PATCH stays an UNTYPED handler, and it is the one route here that cannot be
	// typed without moving the wire. Three facts of its contract are unreachable
	// from a typed op, each of them live:
	//
	//   - the 16 KiB REQUEST-BYTE cap (maxDoc, enforced in decodePatch) answers 413.
	//     zip decodes the body before the handler and cloud's global BodyLimit is far
	//     larger, so a 17 KiB patch that answers 413 today would answer 200.
	//   - an EMPTY body answers 400 and a literal `null` body answers 400
	//     (decodePatch). zip's op.invoke SKIPS the decode for an empty body, and
	//     `null` decodes into a nil map without error, so both would become a
	//     successful no-op merge.
	//   - the patch's key space is OPEN — any key, and a null VALUE deletes its key
	//     (mergeDoc). The only In that carries that is map[string]any, whose
	//     typeName is "" so zip's hasRequestBody publishes NO request body at all:
	//     the document would describe a PATCH that takes nothing.
	//
	// Typing it needs a zip that can declare a byte-capped, body-REQUIRED op over an
	// open object. Until then this route is the escape hatch, deliberately.
	app.Patch("/v1/prefs", s.patchPrefs)
}

// prefsOps is the receiver the prefs ops hang off. A method value is the only bound
// form cmd/zipdoc can lift prose from, so ops are methods and not closures.
type prefsOps struct{ s *service }

// noInput is the input of an op the URL fully addresses.
type noInput struct{}

// Shutdown releases the prefs store. Idempotent.
func Shutdown(_ context.Context) error {
	if mounted == nil {
		return nil
	}
	var err error
	if mounted.store != nil {
		err = mounted.store.Close()
	}
	mounted = nil
	return err
}

// subject resolves the preference OWNER — the isolation KEY — for a VALIDATED
// principal only. Fails closed for an unvalidated request: with no verified
// identity there is no "own" document to read, so there is nothing to serve.
//
// The key is the CANONICAL `<owner>/<name>` identity, the same form IAM parses
// and clients/account's resolveCaller builds — never the bare X-User-Id. The
// bare name is NOT unique across orgs: `hanzo/z` and `admin/z` are two different
// people, and keying on `z` alone would hand one of them the other's document.
// A user with no org yet (first-run, pre-onboarding) keys on the bare name, which
// is correct for exactly as long as they have no org to be qualified by.
//
// Both halves are bounded before use, so an oversized forged header can never
// become a giant primary key.
func (s *service) subject(c *zip.Ctx) (string, bool) {
	if !principal.Validated(c) {
		return "", false
	}
	name := strings.TrimSpace(c.User())
	if name == "" || len(name) > principal.MaxOrgLen {
		return "", false
	}
	owner := strings.TrimSpace(c.Org())
	if owner == "" || len(owner) > principal.MaxOrgLen {
		return name, true
	}
	return owner + "/" + name, true
}

// subjectFrom resolves the preference OWNER for a typed op. It IS subject — the
// same two validated claims, read through the request cloud.Bridge parked, because
// the key is `<owner>/<name>` and principal.OrgFrom carries only the owner half.
// Fails closed off the HTTP path, where there is no principal and so no "own"
// document to serve.
func (s *service) subjectFrom(ctx context.Context) (string, bool) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return "", false
	}
	return s.subject(c)
}

// GetPrefs returns the signed-in caller's OWN preference document — the theme,
// density and pinned nav that follow them across every Hanzo surface. There is no
// path to another user's preferences: not for an org admin, not for a platform
// SuperAdmin, because the subject is built from the validated credential and is the
// mandatory predicate on the read. A caller who has never saved anything gets an
// empty document at 200, never a 404, so the user menu always renders.
func (o prefsOps) getPrefs(ctx context.Context, _ *noInput) (*prefsView, error) {
	s := o.s
	subject, ok := s.subjectFrom(ctx)
	if !ok {
		return nil, zip.ErrForbidden("a validated principal is required")
	}
	p, err := s.store.Get(ctx, subject)
	if err == errNotFound {
		// Never written any — an honest empty document. NOT a 404: "I have no
		// preferences yet" is a successful answer, and the menu must render.
		return &prefsView{Prefs: json.RawMessage(`{}`)}, nil
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get prefs: %v", err)
	}
	return &prefsView{Prefs: json.RawMessage(p.Doc), UpdatedAt: p.UpdatedAt}, nil
}

func (s *service) patchPrefs(c *zip.Ctx) error {
	subject, ok := s.subject(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	patch, err := decodePatch(c.Body(), maxDoc, maxKeys)
	if err != nil {
		return err
	}
	p, err := s.store.Merge(c.Context(), subject, patch, time.Now().Unix())
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "save prefs: %v", err)
	}
	return c.JSON(http.StatusOK, prefsView{Prefs: json.RawMessage(p.Doc), UpdatedAt: p.UpdatedAt})
}
