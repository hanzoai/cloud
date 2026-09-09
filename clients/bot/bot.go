// Package bot is the one surface a Hanzo Bot is known and driven through.
//
// A bot is a loop, and one instance of that loop is a run. A run starts either
// on somebody's own machine or in a sandbox placed for them, and /v1/bot/runs
// is where both are: the machine tells this cloud about the first, and whatever
// placed the second is asked about it (cloud.Deps.Runs), so "my bots" is one
// list however the machine underneath was found. Suspension is why this is a
// registry and not a liveness ping: a run that parks hands back a resume token
// — opaque here — and its row survives, so the same run can come back on the
// machine it left or on another. The registry never reads the token; it holds
// it.
//
//	GET    /v1/bot                     (upgrade) the protocol socket
//	POST   /v1/bot                     run one request frame and answer it
//	GET    /v1/bot/runs[?live&status=&where=&surface=&host=&project=]  -> {bots:[Run]}
//	POST   /v1/bot/runs                a run that has started    -> Run (201)
//	                                   asking for one to start   -> 501
//	GET    /v1/bot/runs/:id            one run
//	PATCH  /v1/bot/runs/:id            heartbeat: status, sessionUrl, model -> Run
//	POST   /v1/bot/runs/:id/stop       stop a run                -> {runId,status}
//	DELETE /v1/bot/runs/:id            forget a run and all it holds
//	POST   /v1/bot/runs/:id/suspend    park it, holding a resume token -> Run
//	POST   /v1/bot/runs/:id/resume     bring it back with that token   -> Run
//	POST   /v1/bot/runs/:id/events     record something it reported -> Report (201)
//	GET    /v1/bot/runs/:id/events     what it has reported      -> [Report]
//
// Start a run where you have a machine for it and it announces itself with
// where:local; start one in the cloud and something has to place it, which
// nothing here does, so that POST is 501 and says what is absent rather than
// minting a run nobody is carrying out. Stop it at /stop, which is the only end
// there is: the heartbeat refuses the status and names that door, suspension is
// a rest rather than an end, and forgetting a run ends it through the same halt
// on its way to disposing of everything it held.
//
// /v1/bot/runs is the roster's whole address, so the addresses beside it stay
// their owners': /v1/bot/members projects these runs into an org's spaces and
// /v1/bot/runtime relays an executor's own operational paths, and neither is
// served here. The shape of a run is theirs too (plugin/bot/openapi.json,
// BotRun): runId, task, surface, status, sessionUrl and an RFC 3339 startedAt
// are the published names, and what this registry knows on top of them — where,
// host, model, project, the parking stamps — is added beside them.
//
// /v1/bot itself is the protocol the control UI speaks, so a UI that already
// speaks it drives this cloud with no change of its own. The protocol is a
// request/response envelope over one WebSocket: a client opens a socket, sends
// a request naming a method, and gets back a response carrying the id it sent;
// anything the server has to say on its own initiative arrives as an event on
// the same socket. frame.go states the envelope exactly, quoting the TypeScript
// that speaks it. This package owns the envelope, the method registry, the
// capability check, the connection set and the per-run store — and owns no
// method: each family of methods registers its own.
//
// The socket and the roster do not compete for one GET, because asking to
// upgrade is a fact about the request rather than a flag beside it: a client
// that asked for the socket gets the socket, and every other GET is passed along
// to whatever serves pages at this address. The single-frame door is the POST,
// for a caller with one thing to ask and no reason to hold a connection open — a
// bot loop, a script, a test — and it runs the same dispatch, so there is one
// surface and not two.
//
// Identity is Hanzo IAM and nothing else. A caller is the org and user the
// identity boundary validated (principal.Org), and its capabilities are derived
// from that one fact in scope.go. There is no credential of this package's own:
// the upstream device key handshake is not implemented and the `device` block
// of ConnectParams is ignored, because a second way to say who you are is a
// second thing that can be wrong.
//
// A `?bot=<id>` on either protocol door binds the call to one run, whose state
// lives in its own SQLite file. Without it the call acts on the org's own file,
// which is also where the registry lives. The id must name a run the org has,
// because the id chooses a file, and an id nobody announced binds to nothing.
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
	stores *cloud.OrgStore[*Store] // one file per org, and one per run
	hub    *hub
	ai     cloud.AIClient
	model  string
	// runs is the executor that places runs in sandboxes, or nil where a
	// deployment places none. The roster asks it for what it holds so that one
	// list answers for the runs out there and the runs that announced
	// themselves here.
	runs    cloud.RunClient
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
		runs:    deps.Runs,
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

// routes registers the surface: the protocol at /v1/bot, the roster one segment
// under it. Nothing else under /v1/bot is claimed, so the published addresses
// beside the roster — members, members/sync, runtime — stay their owners'.
//
// The fixed paths register before their :id siblings so the first-match scan
// resolves them first.
func routes(app *zip.App, s *cloud.Service[state]) {
	g := app.Group("/v1")
	g.Get("/bot", cloud.Handle(s, door))
	g.Post("/bot", cloud.Handle(s, call))
	g.Get("/bot/runs", cloud.Handle(s, list))
	g.Post("/bot/runs", cloud.Handle(s, begin))
	g.Get("/bot/runs/:id", cloud.Handle(s, get))
	g.Patch("/bot/runs/:id", cloud.Handle(s, update))
	g.Delete("/bot/runs/:id", cloud.Handle(s, forget))
	g.Post("/bot/runs/:id/stop", cloud.Handle(s, stop))
	g.Post("/bot/runs/:id/suspend", cloud.Handle(s, suspend))
	g.Post("/bot/runs/:id/resume", cloud.Handle(s, resume))
	g.Post("/bot/runs/:id/events", cloud.Handle(s, record))
	g.Get("/bot/runs/:id/events", cloud.Handle(s, reports))
}

// idRE constrains a run id to a URL-safe token, so a machine can mint one
// locally and refer to the run before the round-trip returns. It is the
// boundary guard on the :id path segment and on the `?bot=` a protocol call
// binds with. Shape is only the first half of the question there: resolve asks
// the registry whether the id names a run of this org, because the id chooses a
// file, and an id nobody announced would let one caller mint files on the
// shared volume without limit — the handles they hold are the whole process's.
var idRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{7,63}$`)

// caller is the validated identity behind a frame: the tenant it acts in, the
// person it is, the ledger it spends from, the project it narrows to, the run
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
		return caller{}, zip.ErrBadRequest("bad run id")
	}
	// The store this call reads and writes is chosen by org and run together
	// (cloud.OrgStore.For), and that call takes validated values. The org is
	// IAM's. The run is the client's until it is looked up in the org's own
	// registry here, once, at the one place a binding is established.
	if name != "" {
		st, err := storeFor(s, org, "")
		if err != nil {
			return caller{}, err
		}
		if _, err := readRun(c.Context(), st, name); err != nil {
			return caller{}, zip.ErrForbidden("no such run")
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
