package sandbox

// typed.go is the CRUD surface's typed half — one registry entry per operation,
// which is what the OpenAPI schema, the MCP tool, the CLI command and every
// generated SDK method are all projected from.
//
// The agent's door (POST /v1/sandbox/{lease,run,read,write,stop,end}) was typed
// first and deliberately: it is the shape an agent leases a computer through.
// This is the RESOURCE surface beside it — the one a console and a human use —
// and it was raw, so seven addresses a caller can see in the document could not
// be reached by any projection but REST.
//
// The two surfaces are not duplicates and must not be folded. The door is
// verb-shaped and holds a LEASE (lease/run/stop/end); this is noun-shaped and
// addresses a sandbox by id. Both call the same api.go core — Lease, List, Get,
// Run, End — so neither can drift from the other about what a sandbox IS.

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// zipdoc lifts the doc comment off each typed op and off every In/Out field into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// ops is the typed-op receiver. A bound METHOD is the only form cmd/zipdoc can
// lift prose from — a closure returned by a factory is a call expression with no
// doc comment to read, which is exactly why the two ticket routes below are two
// methods over one core rather than one `open(door)` factory.
type ops struct{ s *Service }

// orgFrom is the caller's validated org, off the context. It fails closed where
// there is no request: a CLI LocalInvoke has no attested caller, and a sandbox is
// leased against an org's ledger.
func orgFrom(ctx context.Context) (string, error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return "", zip.ErrForbidden("a validated principal is required")
	}
	o, ok := orgOf(c)
	if !ok {
		return "", principal.Refused(c)
	}
	return o, nil
}

// ── the collection ──────────────────────────────────────────────────────────

// leaseIn is what POST /v1/sandbox takes. It is the same shape the door's lease
// takes, spelled for a caller addressing the collection.
type leaseIn struct {
	// Class is what the sandbox is FOR: "exec" for a code-interpreter call,
	// "dev" for a workspace bound to a project, "desktop" for one with a screen.
	// It decides the image, the working directory and the isolation.
	Class string `json:"class" url:"-"`
	// Project binds the sandbox to one of the org's projects. Required for a dev
	// or desktop class, which are single-attach per project; an exec sandbox
	// carries none.
	Project string `json:"project,omitempty" url:"-"`
	// Image overrides the image the class would pick. Honoured only for a caller
	// the policy admits, and the sandbox that comes back names the image it GOT.
	Image string `json:"image,omitempty" url:"-"`
	// Runtime asks for an isolation: runc, gvisor, kata-clh or kata-fc. It is a
	// REQUEST, not a guarantee — the sandbox that comes back carries the runtime
	// it was actually given, which is the field to read.
	Runtime string `json:"runtime,omitempty" url:"-"`
	// TTLSec is how long the lease runs before the reaper may take it, in
	// seconds. Zero takes the class's own default.
	TTLSec int `json:"ttlSec,omitempty" url:"-"`
}

// CreateSandbox leases a sandbox — a real computer — for the caller's org.
//
// The class decides what it is for and therefore its image, working directory
// and isolation. A dev or desktop sandbox is SINGLE-ATTACH per project, so
// asking twice for one project resumes the one that exists rather than paying
// for a second; an exec sandbox carries no project and is bounded per org
// instead, refused 429 past the ceiling because the caller's correct response is
// to wait.
//
// Answers 201 with the sandbox as leased, which names the runtime it GOT — not
// the one that was asked for.
func (o ops) create(ctx context.Context, in *leaseIn) (*Sandbox, error) {
	org, err := orgFrom(ctx)
	if err != nil {
		return nil, err
	}
	c, _ := cloud.Request(ctx)
	m, err := Lease(o.s, ctx, org, principal.Ledger(c), principal.IsSuperAdmin(c), cloud.CallerBearer(c), Spec{
		Class: in.Class, Project: in.Project, Image: in.Image,
		Runtime: in.Runtime, TTLSec: in.TTLSec})
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// sandboxFilter narrows a listing. Both are STRINGS and both are compared to a
// value rather than parsed, so an unrecognised one narrows to nothing rather
// than being silently ignored.
type sandboxFilter struct {
	// Project narrows to sandboxes bound to one project. Empty lists every
	// project's.
	Project string `json:"-" url:"project"`
	// Status narrows to one lifecycle state — pending, running, stopped, ended.
	// Empty lists every state.
	Status string `json:"-" url:"status"`
}

// sandboxList is the caller org's sandboxes. Never another org's: the store is
// keyed on the validated org, so a sandbox belonging elsewhere is not filtered
// out of this answer — it is not reachable by any operation here.
type sandboxList struct {
	// Sandboxes are the caller org's sandboxes matching the filter. Never null.
	Sandboxes []Sandbox `json:"sandboxes"`
}

// ListSandboxes lists the caller org's sandboxes, newest first.
//
// `?project=` and `?status=` narrow it. Only the caller's org's: the store is
// keyed on the validated org, so another tenant's sandbox is not something this
// operation can return.
func (o ops) list(ctx context.Context, in *sandboxFilter) (*sandboxList, error) {
	org, err := orgFrom(ctx)
	if err != nil {
		return nil, err
	}
	out, err := List(o.s, ctx, org, in.Project, in.Status)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []Sandbox{}
	}
	return &sandboxList{Sandboxes: out}, nil
}

// ── one sandbox ─────────────────────────────────────────────────────────────

// sandboxRef addresses one sandbox. The id is the path segment: the URL is the
// addressing authority.
type sandboxRef struct {
	// ID is the sandbox to address, from the path.
	ID string `json:"id"`
}

// GetSandbox returns one sandbox: its class, project, image, the runtime it was
// given, its status and when its lease ends.
//
// An id the caller's org does not hold is the same 404 an unknown id gives — the
// store is keyed on the org, so a cross-tenant id simply is not there.
func (o ops) get(ctx context.Context, in *sandboxRef) (*Sandbox, error) {
	org, err := orgFrom(ctx)
	if err != nil {
		return nil, err
	}
	m, err := Get(o.s, ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// endIn ends one sandbox.
type endIn struct {
	// ID is the sandbox to end, from the path.
	ID string `json:"id"`
	// Purge also removes the sandbox's RECORD, not just the pod. Send "1" for it.
	// Anything else keeps the record, which is what lets an ended sandbox still be
	// listed and explained.
	Purge string `json:"-" url:"purge"`
}

// DeleteSandbox ends a sandbox and releases the compute behind it. Answers 204.
//
// ENDING IS NOT STOPPING. This releases the resource: the pod goes and anything
// only inside it goes with it. To end what a sandbox is RUNNING while keeping
// the sandbox — the checkout, the logs, the half-written file — the verb is
// POST /v1/sandbox/stop.
//
// `?purge=1` additionally removes the record, so the sandbox stops being listed
// at all rather than being listed as ended.
func (o ops) del(ctx context.Context, in *endIn) (*struct{}, error) {
	org, err := orgFrom(ctx)
	if err != nil {
		return nil, err
	}
	if err := End(o.s, ctx, org, strings.TrimSpace(in.ID), in.Purge == "1"); err != nil {
		return nil, err
	}
	return nil, nil
}

// execRequest runs one command in a sandbox the caller holds.
type execRequest struct {
	// ID is the sandbox to run in, from the path.
	ID string `json:"id"`
	// Argv is the command as an argument vector, which is the honest form: it
	// cannot be word-split by accident. Send this OR Command, not both.
	Argv []string `json:"argv,omitempty" url:"-"`
	// Command is a shell line, for a caller that holds one. It is a convenience
	// over Argv and is the only input that ever reaches a shell.
	Command string `json:"command,omitempty" url:"-"`
	// Stdin is fed to the command on its standard input.
	Stdin string `json:"stdin,omitempty" url:"-"`
	// Dir is the working directory to run in. Empty runs in the class's own
	// workdir — /mnt/data for exec, /work for dev.
	Dir string `json:"dir,omitempty" url:"-"`
	// TimeoutSec bounds the run in seconds. Zero takes the default.
	TimeoutSec int `json:"timeoutSec,omitempty" url:"-"`
}

// ExecInSandbox runs one command in a sandbox the caller holds and answers with
// its exit code, stdout and stderr.
//
// Send `argv` — an argument vector cannot be word-split by accident — or
// `command` for a shell line, which is the only input here that ever reaches a
// shell. A non-zero exit is a SUCCESSFUL call carrying a failed command: the
// status is 200 and the exit code is in the answer, because "the command failed"
// and "the call failed" are different facts.
func (o ops) exec(ctx context.Context, in *execRequest) (*ExecResult, error) {
	org, err := orgFrom(ctx)
	if err != nil {
		return nil, err
	}
	r, err := Run(o.s, ctx, org, strings.TrimSpace(in.ID), Cmd{
		Argv: in.Argv, Command: in.Command, Stdin: in.Stdin,
		Dir: in.Dir, TimeoutSec: in.TimeoutSec})
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// ── interactive tickets ─────────────────────────────────────────────────────

// ticket is a short-lived grant to open one interactive door on one sandbox.
type ticketGrant struct {
	// Ticket is the grant itself. It is single-purpose and short-lived, and it
	// travels in a query string because a WebSocket handshake carries no
	// Authorization header a browser can set.
	Ticket string `json:"ticket"`
	// ExpiresIn is how long the ticket is good for, in seconds.
	ExpiresIn int `json:"expiresIn"`
	// URL is the PATH to open, ticket included — not an absolute URL. Which host
	// this address wears in public is the edge's answer and not this process's, so
	// an absolute URL would be a guess; the client already knows the host it is
	// talking to. It names the PAGE, which is what a caller embeds — the page
	// finds its own socket, and a caller that wants the raw socket adds `/ws`.
	URL string `json:"url"`
}

// TerminalTicket mints a short-lived grant to open a terminal on a sandbox.
//
// The ticket travels in the query string of the URL it answers with, because a
// browser cannot set an Authorization header on a WebSocket handshake. It is
// single-purpose and short-lived for exactly that reason. A sandbox that is not
// running is 409 rather than a ticket that cannot be used.
func (o ops) terminalTicket(ctx context.Context, in *sandboxRef) (*ticketGrant, error) {
	return o.ticketFor(ctx, in, "terminal")
}

// ScreenTicket mints a short-lived grant to open the screen of a desktop
// sandbox. Same properties as the terminal ticket, for the other door.
func (o ops) screenTicket(ctx context.Context, in *sandboxRef) (*ticketGrant, error) {
	return o.ticketFor(ctx, in, "screen")
}

// ticketFor is the one mint both doors share, so neither can drift from the
// other about what a ticket is or how long it lasts.
func (o ops) ticketFor(ctx context.Context, in *sandboxRef, door string) (*ticketGrant, error) {
	org, err := orgFrom(ctx)
	if err != nil {
		return nil, err
	}
	m, _, err := find(o.s, ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, err
	}
	if m.Status != "running" {
		st := m.Status
		if st == "" {
			st = "unknown"
		}
		return nil, zip.Errorf(http.StatusConflict, "sandbox is %s", st)
	}
	tok, err := o.s.State.tickets.mint(time.Now(), org, m.ID)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "ticket: %v", err)
	}
	return &ticketGrant{
		Ticket:    tok,
		ExpiresIn: int(ticketTTL / time.Second),
		URL:       "/v1/sandbox/" + m.ID + "/" + door + "?ticket=" + tok,
	}, nil
}
