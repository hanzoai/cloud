// Package claw serves the OpenClaw gateway protocol from Hanzo Cloud, so that
// the web UI that already speaks it drives this cloud with no change of its
// own.
//
// The protocol is a request/response envelope over one WebSocket. A client
// opens a socket, sends a request naming a method, and gets back a response
// carrying the id it sent; anything the server has to say on its own initiative
// arrives as an event on the same socket. frame.go states the envelope exactly,
// quoting the TypeScript that speaks it. This package owns the envelope, the
// method registry, the capability check, the connection set and the per-bot
// store — and owns no method: each family of methods registers its own.
//
// Identity is Hanzo IAM and nothing else. A caller is the org and user the
// identity boundary validated (principal.Org), and its capabilities are derived
// from that one fact in scope.go. There is no credential of this package's own:
// OpenClaw's device key handshake is not implemented and its `device` block is
// ignored, because a second way to say who you are is a second thing that can
// be wrong.
//
//	GET  /v1/claw   upgrade to the protocol socket
//	POST /v1/claw   run one request frame and answer it
//
// The socket is the door the UI uses. The single-frame door exists for a caller
// that has one thing to ask and no reason to hold a connection open — a bot
// loop, a script, a test — and runs the same dispatch, so there is one surface
// and not two.
//
// A `?bot=<id>` on either door binds the call to one bot, whose state lives in
// its own SQLite file. Without it the call acts on the org's own file. The id
// must name a bot the org announced, because the id chooses a file: the
// registry that owns bots answers whether it does (clients/bot.Has), and an id
// nobody owns binds to nothing.
//
// A family of methods lives in its own file and adds itself:
//
//	func init() {
//	    Register("sessions.list", Read, list)
//	    Announce("sessions.changed")
//	}
//
//	func list(c *Call) (any, error) {
//	    var p params
//	    if err := c.Bind(&p); err != nil { return nil, err }
//	    ...
//	    return result, nil
//	}
package claw

import (
	"errors"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/bot"
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

// state is claw's own data; the shared deps live in the embedded cloud.Base.
//
// ai and model are deps.AI and deps.AIDefaultModel, kept here because a turn
// needs them on every call and cloud.Base does not carry them. They are the
// cloud's own completions client — there is no second inference path, and a
// deployment with none configured gets the fail-closed client, which a turn
// reports as an error rather than answering as if it had a model.
type state struct {
	stores  *cloud.OrgStore[*Store] // one claw.db per org, and one per bot
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

// Mount wires /v1/claw onto app.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return errors.New("claw.Mount: nil zip.App")
	}
	if deps.Logger == nil {
		return errors.New("claw.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return errors.New("claw.Mount: empty DataDir")
	}
	version := strings.TrimSpace(deps.Version)
	if version == "" {
		version = cloud.Version
	}
	s := &cloud.Service[state]{Base: cloud.NewBase(deps, "claw"), State: state{
		stores:  cloud.NewOrgStore(deps.DataDir, "claw", openStore),
		hub:     newHub(),
		ai:      deps.AI,
		model:   strings.TrimSpace(deps.AIDefaultModel),
		version: version,
		started: time.Now(),
	}}
	mounted.Store(s)
	routes(app, s)
	s.Log.Info("claw protocol mounted", "methods", len(methodNames()), "events", len(eventNames()))
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

// routes registers the surface: one path, two verbs.
func routes(app *zip.App, s *cloud.Service[state]) {
	g := app.Group("/v1")
	g.Get("/claw", cloud.Handle(s, stream))
	g.Post("/claw", cloud.Handle(s, call))
}

// botRE constrains the shape of the bot a call may bind to. It is the id shape
// clients/bot mints and accepts. Shape is only the first half of the question:
// resolve asks that registry whether the id names a bot of this org, because
// the id chooses a file and an id nobody owns would let one caller mint files
// on the shared volume without limit, and the handles they hold are the whole
// process's.
var botRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{7,63}$`)

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
func resolve(c *zip.Ctx) (caller, error) {
	org, ok := principal.Org(c)
	if !ok {
		return caller{}, zip.ErrForbidden("a validated org is required")
	}
	name := strings.TrimSpace(c.Query("bot"))
	if name != "" && !botRE.MatchString(name) {
		return caller{}, zip.ErrBadRequest("bad bot id")
	}
	// The store this call reads and writes is chosen by org and bot together
	// (cloud.OrgStore.For), and that call takes validated values. The org is
	// IAM's. The bot is the client's until it is looked up here, once, at the
	// one place a binding is established.
	if name != "" && !bot.Has(c.Context(), org, name) {
		return caller{}, zip.ErrForbidden("no such bot")
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
