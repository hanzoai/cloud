// Package sandbox is the box scheduler: the control plane for the executor
// every other subsystem in this binary was already pointing at.
//
// A "box" is one Pod running boxd (cmd/boxd) with a project volume attached. It
// is the SAME primitive at three lifetimes — a stateless /v1/exec call is a box
// with no volume that is claimed, used and released; a coding run is a box that
// lives for a turn; a hanzo.app editing session is a box that suspends and
// resumes across days. One object, one lifecycle, three TTLs.
//
// THE HARD BOUNDARY, and it is the whole design: this package NEVER EXECUTES
// ANYTHING. There is no os/exec here, exactly as apps/exec states of itself. It
// schedules Pods and proxies HTTP to them. Everything that runs submitted code
// runs inside a box, on the far side of a NetworkPolicy, in a pod with no
// service-account token, on a tainted node pool. If a reviewer finds os/exec in
// this package, the design has failed and the review should say so.
//
//	POST   /v1/sandbox/boxes             {project, class, ref?, ttlSec?} -> Box
//	GET    /v1/sandbox/boxes             ?project=&status=              -> {boxes:[Box]}
//	GET    /v1/sandbox/boxes/:id                                        -> Box
//	DELETE /v1/sandbox/boxes/:id         ?purge=1 drops the volume too
//	POST   /v1/sandbox/boxes/:id/suspend                                -> Box
//	POST   /v1/sandbox/boxes/:id/resume  {ref?}                         -> Box
//	ANY    /v1/sandbox/boxes/:id/fs/*    -> boxd /v1/box/fs/*
//	ANY    /v1/sandbox/boxes/:id/proc/*  -> boxd /v1/box/proc/*
//
// AUTH is the ordinary one: IAM terminates user identity at the gateway and this
// package reads principal.Org. It never mints a credential and never sees a
// password. The service key it presents to a box is the same KMS-sourced
// CODE_EXEC_API_KEY the rest of this path already carries — one credential for
// the whole executor surface, not one per subsystem.
//
// WHY THE PROXY: boxes have no public address, by design. Terminating auth
// anywhere but the IAM edge would mean inventing a second auth path, which is
// banned. So every fs call costs one in-cluster hop, and that is the correct
// trade.
package sandbox

import (
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// classes is the CLOSED set of box shapes. Each is one image tag and one network
// posture; the two are not independent, which is why this is one field and not
// two. See charts/app/values/hanzo/box-pool.yaml.
//
//	exec    — a chat/functions code run. No volume, DNS-only egress, seconds.
//	dev     — a coding box. Project volume, egress to git/api/pkg only, hours.
//	desktop — dev plus X/VNC for computer-use. Same egress as dev.
var classes = map[string]bool{"exec": true, "dev": true, "desktop": true}

type state struct {
	stores *cloud.OrgStore[*Store]
	pool   *pool
	key    string // CODE_EXEC_API_KEY: what cloud presents to a box
}

// storeFor is the ONE way this package reaches a store, named through
// cloud.OrgNamespace — the single door a validated org walks through. org MUST
// already be validated (principal.Org, never a body field).
func storeFor(s *cloud.Service[state], org string) (*Store, error) {
	ns, err := cloud.OrgNamespace(org, "")
	if err != nil {
		return nil, err
	}
	return s.State.stores.For(ns)
}

func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("sandbox.Mount: nil app")
	}
	if deps.Logger == nil {
		return fmt.Errorf("sandbox.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("sandbox.Mount: empty DataDir")
	}
	b := cloud.NewBase(deps, "sandbox")
	s := &cloud.Service[state]{Base: b, State: state{
		stores: cloud.NewOrgStore(b, "sandbox", openStore),
		pool:   newPool(),
		key:    strings.TrimSpace(os.Getenv("CODE_EXEC_API_KEY")),
	}}
	routes(app, s)
	s.Log.Info("sandbox mounted",
		"namespace", s.State.pool.ns, "image", s.State.pool.image,
		"cluster", s.State.pool.ready() == nil, "keyed", s.State.key != "")
	return nil
}

func routes(app cloud.Router, s *cloud.Service[state]) {
	app.Get("/v1/sandbox/boxes", cloud.Handle(s, list))
	app.Post("/v1/sandbox/boxes", cloud.Handle(s, create))

	g := app.Group("/v1/sandbox/boxes")
	g.Get("/:id", cloud.Handle(s, get))
	g.Delete("/:id", cloud.Handle(s, del))
	g.Post("/:id/suspend", cloud.Handle(s, suspend))
	g.Post("/:id/resume", cloud.Handle(s, resume))
	// The fs and proc surfaces are ONE greedy route each rather than an
	// enumeration: boxd owns what it serves below them, and a listed subtree
	// here would 404 every path left out of the list. Same reasoning apps/exec
	// records for the interpreter's subpaths.
	g.All("/:id/fs", cloud.Handle(s, forward))
	g.All("/:id/fs/*", cloud.Handle(s, forward))
	g.All("/:id/proc/*", cloud.Handle(s, forward))
	g.All("/:id/git/*", cloud.Handle(s, forward))
}

func org(c *zip.Ctx) (string, bool) { return principal.Org(c) }
func idParam(c *zip.Ctx) string     { return strings.TrimSpace(c.Param("id")) }

// create claims a box. It is the only path that talks to the scheduler.
func create(s *cloud.Service[state], c *zip.Ctx) error {
	o, ok := org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	var body struct {
		Project string `json:"project"`
		Class   string `json:"class"`
		Ref     string `json:"ref"`
		Image   string `json:"image"`
		TTLSec  int    `json:"ttlSec"`
	}
	if err := c.Bind(&body); err != nil {
		return err
	}
	class := strings.ToLower(strings.TrimSpace(body.Class))
	if class == "" {
		class = "dev"
	}
	if !classes[class] {
		return zip.ErrBadRequest("class must be one of exec, dev, desktop")
	}
	project := sanitize(body.Project)
	if project == "" {
		return zip.ErrBadRequest("project required")
	}
	store, err := storeFor(s, o)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}

	// One live box per (org, project), and the refusal is deliberate.
	//
	// The project volume is do-block-storage, which is RWO — one node at a time.
	// A second concurrent box for the same project would either fail to attach
	// or silently get a cold ephemeral volume, and "silently cold" is the worse
	// of the two: the user sees a box that works and reinstalls everything on
	// every call. So it is refused, in the open, with the id of the box that
	// already holds the volume.
	if class != "exec" {
		if live, err := store.Live(c.Context(), o, project); err == nil && live.ID != "" {
			return zip.Errorf(http.StatusConflict,
				"project %q already has a live box (%s); suspend or delete it first", project, live.ID)
		}
	}

	id, _ := genID("box")
	bx := Box{
		ID: id, Org: o, Project: project, Class: class, Ref: body.Ref,
		Image: firstNonEmpty(body.Image, s.State.pool.imageFor(class)),
		PVC:   pvcName(o, project), Status: "pending",
		CreatedAt: time.Now().Unix(), LastUsedAt: time.Now().Unix(),
	}
	if body.TTLSec > 0 {
		bx.ExpiresAt = bx.CreatedAt + int64(body.TTLSec)
	}
	if err := store.Put(c.Context(), bx); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "put: %v", err)
	}

	// Claim from the warm pool. A failure here is recorded on the box and
	// returned as 503 — the row stays so the operator can see what was asked
	// for and why it did not happen, rather than the request vanishing.
	host, err := s.State.pool.claim(c.Context(), bx)
	if err != nil {
		bx.Status, bx.Error = "error", err.Error()
		_ = store.Put(c.Context(), bx)
		return zip.Errorf(http.StatusServiceUnavailable, "no box available: %v", err)
	}
	bx.Status, bx.Host = "running", host
	if err := store.Put(c.Context(), bx); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "put: %v", err)
	}
	return c.JSON(http.StatusCreated, bx)
}

func list(s *cloud.Service[state], c *zip.Ctx) error {
	o, ok := org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	store, err := storeFor(s, o)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	boxes, err := store.List(c.Context(), o, sanitize(c.Query("project")), strings.TrimSpace(c.Query("status")))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{"boxes": boxes})
}

func get(s *cloud.Service[state], c *zip.Ctx) error {
	bx, _, err := load(s, c)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, bx)
}

func del(s *cloud.Service[state], c *zip.Ctx) error {
	bx, store, err := load(s, c)
	if err != nil {
		return err
	}
	if rerr := s.State.pool.release(c.Context(), bx); rerr != nil {
		s.Log.Warn("release box", "id", bx.ID, "err", rerr)
	}
	// purge=1 drops the VOLUME as well, and it is opt-in because the volume is
	// the only copy of the user's checkout and caches. Deleting a box is cheap
	// and reversible; deleting their node_modules and their uncommitted work is
	// neither.
	if c.Query("purge") == "1" {
		if perr := s.State.pool.purge(c.Context(), bx); perr != nil {
			s.Log.Warn("purge volume", "pvc", bx.PVC, "err", perr)
		}
	}
	if err := store.Delete(c.Context(), bx.Org, bx.ID); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	c.Status(http.StatusNoContent)
	return nil
}

func suspend(s *cloud.Service[state], c *zip.Ctx) error {
	bx, store, err := load(s, c)
	if err != nil {
		return err
	}
	// Suspend deletes the POD and keeps the VOLUME. That is the whole trick: a
	// resume is `git fetch && checkout` because the checkout, node_modules, the
	// pnpm store and the cargo registry are all still on the disk.
	if rerr := s.State.pool.release(c.Context(), bx); rerr != nil {
		return zip.Errorf(http.StatusBadGateway, "suspend: %v", rerr)
	}
	bx.Status, bx.Host = "suspended", ""
	if err := store.Put(c.Context(), bx); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "put: %v", err)
	}
	return c.JSON(http.StatusOK, bx)
}

func resume(s *cloud.Service[state], c *zip.Ctx) error {
	bx, store, err := load(s, c)
	if err != nil {
		return err
	}
	var body struct {
		Ref string `json:"ref"`
	}
	_ = c.Bind(&body)
	if body.Ref != "" {
		bx.Ref = body.Ref
	}
	host, cerr := s.State.pool.claim(c.Context(), bx)
	if cerr != nil {
		return zip.Errorf(http.StatusServiceUnavailable, "resume: %v", cerr)
	}
	bx.Status, bx.Host, bx.LastUsedAt = "running", host, time.Now().Unix()
	if err := store.Put(c.Context(), bx); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "put: %v", err)
	}
	return c.JSON(http.StatusOK, bx)
}

// load resolves :id to a box THIS org owns. The org comes from the validated
// principal and the query is scoped by it, so a caller cannot address another
// org's box by guessing an id — a miss is 404, not 403, because "that box
// belongs to someone else" is itself information.
func load(s *cloud.Service[state], c *zip.Ctx) (Box, *Store, error) {
	o, ok := org(c)
	if !ok {
		return Box{}, nil, zip.ErrForbidden("X-Org-Id required")
	}
	store, err := storeFor(s, o)
	if err != nil {
		return Box{}, nil, zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	bx, err := store.Get(c.Context(), o, idParam(c))
	if err == errNotFound {
		return Box{}, nil, zip.ErrNotFound("box not found")
	}
	if err != nil {
		return Box{}, nil, zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	return bx, store, nil
}

func sanitize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == '/', r == '.', r == ' ':
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 48 {
		out = out[:48]
	}
	return out
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			return x
		}
	}
	return ""
}

func atoiOr(s string, def int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && v > 0 {
		return v
	}
	return def
}

// Prose for the surface, declared beside the route table. These handlers bind
// through zip.Ctx rather than being typed ops, so zipdoc has no doc comment to
// lift and the document would otherwise publish operationIds and nothing else.
func init() {
	openapi.Describe("/v1/sandbox/boxes", http.MethodPost,
		"Claim a sandbox box",
		"Claims an isolated execution box for the caller's org and returns it. A box is one "+
			"pod running the box daemon with the project's volume attached: `exec` is a "+
			"short-lived interpreter with no volume and no network, `dev` adds the project "+
			"checkout and a toolchain, `desktop` adds a virtual display for computer use.\n\n"+
			"A claim comes from a WARM POOL where one is available, so the usual cost is a "+
			"label patch and a volume attach rather than an image pull.\n\n"+
			"One live box per project: the project volume is single-attach, so a second "+
			"concurrent claim is REFUSED with the id of the box already holding it rather "+
			"than silently given an empty volume that reinstalls everything.")
	openapi.Describe("/v1/sandbox/boxes", http.MethodGet,
		"Every box the caller's org holds",
		"Lists this org's boxes with their class, status, project, image and volume. "+
			"Filterable by project and status. Scoped to the caller's org by the validated "+
			"identity, never by a value in the request.")
	openapi.Describe("/v1/sandbox/boxes/:id", http.MethodGet,
		"One box", "The box's current state, or 404 if this org does not hold it.")
	openapi.Describe("/v1/sandbox/boxes/:id", http.MethodDelete,
		"Release a box",
		"Releases the pod. The project VOLUME is kept — it holds the checkout and the "+
			"dependency caches, which is what makes the next claim fast. Pass purge=1 to "+
			"drop the volume too; that is irreversible and deletes uncommitted work.")
	openapi.Describe("/v1/sandbox/boxes/:id/suspend", http.MethodPost,
		"Suspend a box, keeping its disk",
		"Releases the pod and keeps the volume, so a later resume is a fetch and a checkout "+
			"rather than a clone and a cold install.")
	openapi.Describe("/v1/sandbox/boxes/:id/resume", http.MethodPost,
		"Resume a suspended box",
		"Claims a pod and reattaches the project volume, optionally moving to a different "+
			"git ref. The checkout and the caches are already on the disk.")
	for _, m := range openapi.Methods() {
		openapi.Describe("/v1/sandbox/boxes/:id/fs/*", m,
			"The box's filesystem",
			"Read, write, list, search and delete inside the box's project directory. "+
				"Forwarded to the box daemon unchanged; the box resolves every path under the "+
				"project root, so a path cannot address anything outside it.\n\n"+
				"NOTHING RUNS IN cloud. This is a proxy: boxes have no public address, and "+
				"terminating auth anywhere but the IAM edge would mean a second auth path.")
		openapi.Describe("/v1/sandbox/boxes/:id/proc/*", m,
			"Run a command in the box",
			"Runs a command in the box's project directory and returns its exit code, stdout "+
				"and stderr. A non-zero exit is a SUCCESSFUL call carrying a failed program — "+
				"the HTTP status stays 200, because 'the tests failed' and 'the box is broken' "+
				"are different facts.")
		openapi.Describe("/v1/sandbox/boxes/:id/git/*", m,
			"Clone into the box, or push what it changed",
			"Clones a repository into the box's project volume, or commits and pushes the "+
				"branch the agent worked on. Git credentials travel in the request BODY only — "+
				"never a URL, never a command line, never a log.")
	}
}
