// Package sessions is the live-coding-session plane: the /v1/sessions surface
// behind the session list in the console, and the answer to "what is being worked
// on right now, and can I watch it".
//
// A coding session runs on a developer's own machine, not in the cluster, so the
// cluster cannot enumerate them — the machine has to say so. A host agent starts
// a terminal, publishes it through zrok (which is what gives it a public
// https://<share>.share.hanzo.ai URL without opening a port), and beats here.
// This surface holds the roster; it never proxies the terminal itself.
//
// Surface (org-scoped; /v1 only):
//
//	GET    /v1/sessions       the caller's live sessions      -> sessionsView
//	POST   /v1/sessions       register or heartbeat one       -> Session
//	DELETE /v1/sessions/:id   deregister on exit              -> 204
//
// LIVENESS IS A TTL, NOT A STATE MACHINE. A session is live if it beat within
// SessionTTL. There is no "stopped" transition to get wrong: a laptop that sleeps
// mid-session stops beating and drops off the list, and reappears when it wakes.
// Nothing has to observe the death for the roster to be right.
//
// ORG ISOLATION is enforced SERVER-SIDE on every request, from the validated
// principal, and is the mandatory predicate on every store statement. It is never
// read from a query param or body. A session URL is a live shell on someone's
// machine; there is no cross-org read path, and no admin override.
package sessions

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
	"github.com/hanzoai/cloud/apps/principal"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// SessionTTL is how long a session stays listed after its last heartbeat. Agents
// beat well inside it, so one dropped beat over a flaky link does not blink a
// session out of the UI.
const SessionTTL = 90 * time.Second

// pruneAfter is when a dead row is deleted outright. Reads already ignore
// anything past SessionTTL; this only bounds the file.
const pruneAfter = 24 * time.Hour

// maxField bounds every string a host sends. A host agent is trusted to be
// truthful about its own machine, not to be well behaved about lengths.
const maxField = 256

type service struct {
	store *Store
	log   luxlog.Logger
}

var mounted *service

// sessionsView is the wire shape for a list. The TTL travels with it so a client
// can grey out a session that is about to age out instead of hardcoding a guess.
type sessionsView struct {
	Sessions   []Session `json:"sessions"`
	TTLSeconds int       `json:"ttlSeconds"`
}

// Mount registers the sessions surface on app per HIP-0106.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("sessions.Mount: nil app")
	}
	if deps.Logger == nil {
		return fmt.Errorf("sessions.Mount: nil deps.Logger")
	}
	log := deps.Logger.New("subsystem", "sessions")
	if deps.DataDir == "" {
		return fmt.Errorf("sessions.Mount: empty DataDir")
	}
	if err := os.MkdirAll(deps.DataDir, 0o755); err != nil {
		return fmt.Errorf("sessions.Mount: data dir: %w", err)
	}
	store, err := openStore(filepath.Join(deps.DataDir, "sessions.db"))
	if err != nil {
		return fmt.Errorf("sessions.Mount: open sessions store: %w", err)
	}
	s := &service{store: store, log: log}
	mounted = s

	g := app.Group("/v1/sessions")
	g.Get("", s.list)
	g.Post("", s.beat)
	g.Delete("/:id", s.remove)

	log.Info("sessions surface mounted", "prefix", "/v1/sessions", "brand", deps.Brand)
	return nil
}

// Shutdown releases the sessions store. Idempotent.
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

// subject resolves the ORG that owns a session — the isolation KEY — for a
// VALIDATED principal only. Fails closed for an unvalidated request.
//
// Sessions key on the ORG, not the individual, because watching a teammate's
// build is the point of the surface. That is the one deliberate difference from
// prefs, which keys on the person because nobody else has a reason to read a
// theme. A user with no org yet keys on their own name, which is correct for
// exactly as long as they have no org to be qualified by.
func (s *service) subject(c *zip.Ctx) (string, bool) {
	if !principal.Validated(c) {
		return "", false
	}
	if owner := strings.TrimSpace(c.Org()); owner != "" && len(owner) <= principal.MaxOrgLen {
		return owner, true
	}
	name := strings.TrimSpace(c.User())
	if name == "" || len(name) > principal.MaxOrgLen {
		return "", false
	}
	return name, true
}

func (s *service) list(c *zip.Ctx) error {
	subject, ok := s.subject(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	now := time.Now()
	out, err := s.store.List(subject, now, SessionTTL)
	if err != nil {
		s.log.Error("list sessions", "err", err)
		return zip.ErrInternal("could not list sessions")
	}
	if out == nil {
		out = []Session{}
	}
	return c.JSON(http.StatusOK, sessionsView{Sessions: out, TTLSeconds: int(SessionTTL.Seconds())})
}

// beatBody is what a host agent sends. Everything except id and url is
// descriptive: the roster is still correct without it, just less useful.
type beatBody struct {
	ID        string `json:"id"`
	Host      string `json:"host"`
	Workspace string `json:"workspace"`
	Repo      string `json:"repo"`
	Branch    string `json:"branch"`
	Agent     string `json:"agent"`
	URL       string `json:"url"`
}

func (s *service) beat(c *zip.Ctx) error {
	subject, ok := s.subject(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	var body beatBody
	if err := json.Unmarshal(c.Body(), &body); err != nil {
		return zip.ErrBadRequest("body must be a JSON object")
	}
	id := clip(body.ID)
	if id == "" {
		return zip.ErrBadRequest("id is required")
	}
	// The URL is what the console will frame, so it must be a scheme we would
	// actually open. Anything else is a way to get a javascript: or file: URL
	// rendered as a link on a signed-in page.
	url := clip(body.URL)
	if !strings.HasPrefix(url, "https://") {
		return zip.ErrBadRequest("url must be https")
	}

	now := time.Now().Unix()
	v := Session{
		ID:        id,
		Subject:   subject,
		Host:      clip(body.Host),
		Workspace: clip(body.Workspace),
		Repo:      clip(body.Repo),
		Branch:    clip(body.Branch),
		Agent:     clip(body.Agent),
		URL:       url,
		StartedAt: now,
		BeatAt:    now,
	}
	if err := s.store.Beat(v); err != nil {
		s.log.Error("beat session", "err", err, "id", id)
		return zip.ErrInternal("could not record session")
	}
	// Opportunistic: pruning on write keeps the file bounded without a timer, and
	// a failure here has no bearing on the beat that just succeeded.
	if err := s.store.Prune(time.Now(), pruneAfter); err != nil {
		s.log.Debug("prune sessions", "err", err)
	}
	return c.JSON(http.StatusOK, v)
}

func (s *service) remove(c *zip.Ctx) error {
	subject, ok := s.subject(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	id := clip(c.Param("id"))
	if id == "" {
		return zip.ErrBadRequest("id is required")
	}
	if err := s.store.Delete(subject, id); err != nil {
		s.log.Error("delete session", "err", err, "id", id)
		return zip.ErrInternal("could not remove session")
	}
	return c.NoContent(http.StatusNoContent)
}

func clip(v string) string {
	v = strings.TrimSpace(v)
	if len(v) > maxField {
		return v[:maxField]
	}
	return v
}
