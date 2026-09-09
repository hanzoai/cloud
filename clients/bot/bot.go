// Package bot is the one surface a Hanzo Bot is known and driven through.
//
// A bot is a loop. It runs on someone's own machine or in a sandbox this cloud
// placed it in, and either way it announces itself here, keeps saying so, and
// is listed until it stops. Suspension is why this is a registry and not a
// liveness ping: a bot that parks hands back a resume token — opaque here — and
// its row survives, so the same bot can come back on the same host or another.
// The registry never reads the token; it holds it.
//
// The same path is the gateway protocol OpenClaw's control UI speaks, so a UI
// that already speaks it drives this cloud with no change of its own. The
// protocol is a request/response envelope over one WebSocket: a client opens a
// socket, sends a request naming a method, and gets back a response carrying
// the id it sent; anything the server has to say on its own initiative arrives
// as an event on the same socket. frame.go states the envelope exactly, quoting
// the TypeScript that speaks it. This package owns the envelope, the method
// registry, the capability check, the connection set and the per-bot store —
// and owns no method: each family of methods registers its own.
//
//	POST   /v1/bot                     announce a bot        -> Bot (201)
//	GET    /v1/bot[?live&status=&where=&edition=&host=&project=]  -> [Bot]
//	GET    /v1/bot                     (upgrade) the protocol socket
//	POST   /v1/bot/call                run one request frame and answer it
//	GET    /v1/bot/:id                 one bot
//	PATCH  /v1/bot/:id                 heartbeat, status, url, model -> Bot
//	DELETE /v1/bot/:id                 forget a bot
//	POST   /v1/bot/:id/suspend         suspend, carrying a resume token -> Bot
//	POST   /v1/bot/:id/resume          resume a suspended bot -> Bot
//	POST   /v1/bot/:id/events          record something it reported -> Report (201)
//	GET    /v1/bot/:id/events          what it has reported  -> [Report]
//
// One GET answers two questions, because asking to upgrade is a fact about the
// request rather than a flag beside it: a client that asked for the socket gets
// the socket, and every other GET gets the roster. The single-frame door exists
// for a caller with one thing to ask and no reason to hold a connection open —
// a bot loop, a script, a test — and runs the same dispatch, so there is one
// surface and not two. It sits at a fixed path beside the ids because an id is
// at least eight characters (idRE), so no bot can ever be called "call".
//
// Identity is Hanzo IAM and nothing else. A caller is the org and user the
// identity boundary validated (principal.Org), and its capabilities are derived
// from that one fact in scope.go. There is no credential of this package's own:
// OpenClaw's device key handshake is not implemented and its `device` block is
// ignored, because a second way to say who you are is a second thing that can
// be wrong.
//
// A `?bot=<id>` on either protocol door binds the call to one bot, whose state
// lives in its own SQLite file. Without it the call acts on the org's own file,
// which is also where the registry lives. The id must name a bot the org
// announced, because the id chooses a file, and an id nobody announced binds to
// nothing.
//
// A family of methods lives in its own file and adds itself:
//
//	func init() {
//	    Register("sessions.list", Read, list)
//	    Declare("sessions.changed")
//	}
//
//	func list(c *Call) (any, error) {
//	    var p params
//	    if err := c.Bind(&p); err != nil { return nil, err }
//	    ...
//	    return result, nil
//	}
package bot

import (
	"errors"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// protocol is the version this surface serves.
// packages/gateway-protocol/src/version.ts: PROTOCOL_VERSION = 4.
const protocol = 4

// maxFrame bounds one inbound frame, and is advertised as policy.maxPayload so
// a client can check an attachment before sending it. It is the same 4 MiB the
// ZAP face allows.
const maxFrame = 4 << 20

// tickEvery is how often a connection hears from the server with nothing to
// say, advertised as policy.tickIntervalMs. See conn.beat.
const tickEvery = 30 * time.Second

// state is this subsystem's own data; the shared deps live in the embedded
// cloud.Base.
//
// ai and model are deps.AI and deps.AIDefaultModel, kept here because a turn
// needs them on every call and cloud.Base does not carry them. They are the
// cloud's own completions client — there is no second inference path, and a
// deployment with none configured gets the fail-closed client, which a turn
// reports as an error rather than answering as if it had a model.
type state struct {
	stores  *cloud.OrgStore[*Store] // one file per org, and one per bot
	hub     *hub
	ai      cloud.AIClient
	model   string
	version string
	started time.Time
}

// mounted is the live surface. Publish reads it from whatever goroutine raised
// an event, and Shutdown clears it, so it is held atomically rather than as a
// plain package variable.
var mounted atomic.Pointer[cloud.Service[state]]

// Mount wires /v1/bot onto app.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return errors.New("bot.Mount: nil zip.App")
	}
	if deps.Logger == nil {
		return errors.New("bot.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return errors.New("bot.Mount: empty DataDir")
	}
	version := strings.TrimSpace(deps.Version)
	if version == "" {
		version = cloud.Version
	}
	s := &cloud.Service[state]{Base: cloud.NewBase(deps, "bot"), State: state{
		stores:  cloud.NewOrgStore(deps.DataDir, "bot", openStore),
		hub:     newHub(),
		ai:      deps.AI,
		model:   strings.TrimSpace(deps.AIDefaultModel),
		version: version,
		started: time.Now(),
	}}
	mounted.Store(s)
	routes(app, s)
	s.Log.Info("bot mounted", "brand", s.Brand, "methods", len(methodNames()), "events", len(eventNames()))
	return nil
}

// Shutdown stops the work in flight, ends every open connection telling each
// one why, and closes every open file. Idempotent.
//
// The turns go first: each one holds a file this is about to close, and a turn
// that outlived the surface would write its answer into a store nobody can read
// and publish an event nobody receives.
func Shutdown() error {
	s := mounted.Swap(nil)
	if s == nil {
		return nil
	}
	chatHaltAll()
	s.State.hub.closeAll("gateway stopping")
	return s.State.stores.CloseAll()
}

// routes registers the surface. The fixed paths register before their :id
// siblings so the first-match scan resolves them first.
func routes(app *zip.App, s *cloud.Service[state]) {
	g := app.Group("/v1")
	g.Post("/bot", cloud.Handle(s, announce))
	g.Get("/bot", cloud.Handle(s, door))
	g.Post("/bot/call", cloud.Handle(s, call))
	g.Get("/bot/:id", cloud.Handle(s, get))
	g.Patch("/bot/:id", cloud.Handle(s, update))
	g.Delete("/bot/:id", cloud.Handle(s, forget))
	g.Post("/bot/:id/suspend", cloud.Handle(s, suspend))
	g.Post("/bot/:id/resume", cloud.Handle(s, resume))
	g.Post("/bot/:id/events", cloud.Handle(s, record))
	g.Get("/bot/:id/events", cloud.Handle(s, reports))
}

// idRE constrains a bot id to a URL-safe token, so a launcher can mint one
// locally and refer to the bot before the announce round-trip returns. It is
// the boundary guard on the :id path segment and on the `?bot=` a protocol call
// binds with. Shape is only the first half of the question there: resolve asks
// the registry whether the id names a bot of this org, because the id chooses a
// file, and an id nobody announced would let one caller mint files on the
// shared volume without limit — the handles they hold are the whole process's.
var idRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{7,63}$`)

// caller is the validated identity behind a frame: the tenant it acts in, the
// person it is, the ledger it spends from, the project it narrows to, the bot
// it is bound to, and what it may do. Every field comes from the identity
// boundary; nothing here is a client-supplied value.
//
// org and bill are two different orgs on the same request. org is the data
// scope — whose file is read and written. bill is the ledger that pays, which
// for a platform SuperAdmin acting in another org is the admin org rather than
// the one being acted on (clients/principal.BillingOrg), so an inference debit
// lands on the caller's own ledger and never on the tenant whose data was used.
type caller struct {
	org     string
	user    string
	bill    string
	project string
	bot     string
	grant   Grant
}

// resolve reads the caller before any frame is read. It refuses an unvalidated
// request the way every other subsystem does: an org with no validated user is
// an off-gateway forge, not a tenant.
func resolve(s *cloud.Service[state], c *zip.Ctx) (caller, error) {
	org, ok := principal.Org(c)
	if !ok {
		return caller{}, zip.ErrForbidden("a validated org is required")
	}
	name := strings.TrimSpace(c.Query("bot"))
	if name != "" && !idRE.MatchString(name) {
		return caller{}, zip.ErrBadRequest("bad bot id")
	}
	// The store this call reads and writes is chosen by org and bot together
	// (cloud.OrgStore.For), and that call takes validated values. The org is
	// IAM's. The bot is the client's until it is looked up in the org's own
	// registry here, once, at the one place a binding is established.
	if name != "" {
		st, err := s.State.stores.For(org, "")
		if err != nil {
			s.Log.Error("open bot store", "org", org, "err", err)
			return caller{}, zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
		}
		if _, err := readBot(c.Context(), st, name); err != nil {
			return caller{}, zip.ErrForbidden("no such bot")
		}
	}
	bill, _ := principal.BillingOrg(c)
	return caller{
		org:     org,
		user:    strings.TrimSpace(c.User()),
		bill:    bill,
		project: principal.Project(c),
		bot:     name,
		grant:   grantFor(c),
	}, nil
}
