// Package prefs is the per-USER preference plane for the unified Hanzo Cloud
// binary: the /v1/prefs surface behind the user menu on every Hanzo surface
// (console, insights, and anything else that renders "signed in as").
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
// NOT SETTINGS. clients/settings is per-ORG, per-product configuration with KMS
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
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
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
	Prefs     json.RawMessage `json:"prefs"`
	UpdatedAt int64           `json:"updatedAt,omitempty"`
}

// Mount registers the prefs surface on app per HIP-0106.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("prefs.Mount: nil app")
	}
	if deps.Logger == nil {
		return fmt.Errorf("prefs.Mount: nil deps.Logger")
	}
	log := deps.Logger.New("subsystem", "prefs")
	if deps.DataDir == "" {
		return fmt.Errorf("prefs.Mount: empty DataDir")
	}
	if err := os.MkdirAll(deps.DataDir, 0o755); err != nil {
		return fmt.Errorf("prefs.Mount: data dir: %w", err)
	}
	store, err := openStore(filepath.Join(deps.DataDir, "prefs.db"))
	if err != nil {
		return fmt.Errorf("prefs.Mount: open prefs store: %w", err)
	}
	s := &service{store: store, log: log}
	mounted = s

	g := app.Group("/v1/prefs")
	g.Get("", s.getPrefs)
	g.Patch("", s.patchPrefs)

	log.Info("prefs surface mounted", "prefix", "/v1/prefs", "brand", deps.Brand)
	return nil
}

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

func (s *service) getPrefs(c *zip.Ctx) error {
	subject, ok := s.subject(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	p, err := s.store.Get(c.Context(), subject)
	if err == errNotFound {
		// Never written any — an honest empty document. NOT a 404: "I have no
		// preferences yet" is a successful answer, and the menu must render.
		return c.JSON(http.StatusOK, prefsView{Prefs: json.RawMessage(`{}`)})
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "get prefs: %v", err)
	}
	return c.JSON(http.StatusOK, prefsView{Prefs: json.RawMessage(p.Doc), UpdatedAt: p.UpdatedAt})
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
