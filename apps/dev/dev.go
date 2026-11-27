package dev

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/apps/dev/loop"
	"github.com/hanzoai/cloud/client"
	sandboxpeer "github.com/hanzoai/cloud/client/sandbox"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/brand"
	"github.com/hanzoai/cloud/sdk/principal"
)

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, the only way that prose reaches the published document.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

const (
	// class is the kind of cell a session leases: a coding one, with a disk that
	// outlives the lease.
	class = "dev"
	// Port is where the Dev server listens inside the cell.
	Port = "3773"
	// ready bounds the wait for the server to listen.
	ready = 60 * time.Second
)

// Socket is where the Dev server listens for ZAP inside the cell. The cloud pod
// reaches it through the sandbox plane's stream op and nothing else does.
const Socket = "/tmp/hanzo-dev/zap.sock"

// start launches the Dev server if its socket is absent, then waits for it. $1 is
// the person the server is pinned to, "<owner>/<name>"; it is passed as an
// argument, never spliced into the line. One idempotent script, so a resumed cell
// and a fresh one take the same path.
//
// $2 is the issuer of the token the person signed in with. Dev checks every bearer on
// its stream itself, against ONE issuer (HANZO_DEV_IAM_ISSUER, defaulting to its
// brand's hanzo.ai), and a person signed in at hanzo.id holds a token naming that one:
// the cell refused every such stream with an empty 401 (#907). A cell already running
// keeps the issuer it started with; ending the session starts the next one afresh.
const start = `mkdir -p /tmp/hanzo-dev
[ -S ` + Socket + ` ] || {
  command -v hanzo-dev >/dev/null 2>&1 || { echo "hanzo-dev is not installed in this cell" >&2; exit 127; }
  env HANZO_DEV_ZAP_SOCKET=` + Socket + ` HANZO_DEV_ZAP_PRINCIPAL="$1" ${2:+"HANZO_DEV_IAM_ISSUER=$2"} ${3:+"HANZO_DEV_IAM_CLIENT_ID=$3"} \
    nohup hanzo-dev serve --host 127.0.0.1 --port ` + Port + ` --no-browser >/tmp/dev.log 2>&1 &
}
for i in $(seq 1 60); do [ -S ` + Socket + ` ] && exit 0; sleep 1; done
tail -n 20 /tmp/dev.log >&2; exit 1`

// Execution routes where a session runs and which credential backs its model calls
// (HIP-1350).
type Execution struct {
	// Via selects the route: "cell" (default, cloud cell on Zen), "key" (cloud cell
	// backed by a sealed provider account key) or "desktop" (the caller's linked
	// desktop resolved by source dispatch).
	Via string `json:"via"`
	// Account is the provider account id when Via is "key".
	Account string `json:"account,omitempty"`
	// Desktop is the linked source id (src_...) when Via is "desktop".
	Desktop string `json:"desktop,omitempty"`
	// Provider is the provider catalog name (for example "claude") or login instance
	// id when Via is "desktop".
	Provider string `json:"provider,omitempty"`
	// Instance is the login instance id on Desktop (for example "claude-1") when Via
	// is "desktop". Optional when Provider already names the instance id.
	Instance string `json:"instance,omitempty"`
}

// Session is the runtime a Dev front end resolves for a cell: DevRuntime
// (apps/web/src/embed/runtime.ts) without the token, which the front end holds.
type Session struct {
	// ID is the sandbox id (or linked source id when Execution.Via is "desktop"); it
	// keys the thread list and local caches, and names the session in later calls.
	ID string `json:"id"`
	// Label is shown beside the thread list.
	Label string `json:"label"`
	// Zap is the https endpoint that accepts WebTransport sessions for this cell or
	// linked desktop. A session is one bidirectional stream of ZAP envelopes,
	// authenticated by the caller's IAM bearer as the first envelope's cap.
	Zap string `json:"zap"`
	// Sandbox is the sandbox backing the terminal and browser dock. It equals ID for
	// a sandbox session and is absent for a wasm or linked-desktop session that holds
	// no cloud cell.
	Sandbox string `json:"sandbox,omitempty"`
	// Lease is the dispatch hold on the linked desktop's login instance when
	// Execution.Via is "desktop". Release it when the run ends.
	Lease string `json:"lease,omitempty"`
	// Expires is when Lease lapses by itself, RFC 3339 UTC. Set when Lease is set.
	Expires string `json:"expires,omitempty"`
	// Execution is the resolved execution route when the caller requested one.
	Execution *Execution `json:"execution,omitempty"`
}

// Runtimes a session may run on (HIP-1330 / HIP-1350).
const (
	runtimeSandbox = "sandbox"
	runtimeWasm    = "wasm"
	runtimeSource  = "source"
	runtimeAuto    = "auto"
)

// Execution routes (HIP-1350).
const (
	executionCell    = "cell"
	executionKey     = "key"
	executionDesktop = "desktop"
)

// sessionIn names which cell or linked desktop to take.
type sessionIn struct {
	// Runtime is who answers the session's frames: "sandbox" (the real Dev server in a
	// cell), "wasm", "source" (a linked desktop via Execution) or "auto". Empty is
	// "sandbox".
	Runtime string `json:"runtime,omitempty"`
	// Thread keeps a separate cell for this thread. Empty is the caller's own cell.
	Thread string `json:"thread,omitempty"`
	// Execution selects where the session runs and which credential backs its model
	// calls: {via:"cell"} (default), {via:"key", account:"..."} or
	// {via:"desktop", desktop:"src_...", provider:"...", instance:"..."} (HIP-1350).
	Execution *Execution `json:"execution,omitempty"`
}

// endSessionIn names the session to end.
type endSessionIn struct {
	// ID is the session, from POST /v1/dev/sessions.
	ID string `json:"id"`
	// Lease is the dispatch lease to release when ending a linked-desktop session.
	Lease string `query:"lease" json:"lease,omitempty"`
}

// endIn names the session to act on.
type endIn struct {
	// ID is the session, from POST /v1/dev/sessions.
	ID string `json:"id"`
}

// healthOut is liveness.
type healthOut struct {
	// Status is "ok".
	Status string `json:"status"`
}

// Health answers that the service is mounted.
func health(context.Context, *struct{}) (*healthOut, error) { return &healthOut{Status: "ok"}, nil }

// zapURL is where a session's ZAP stream is opened.
func zapURL(id string) string { return "https://" + brand.ZapHost(cloud.Brand()) + Path + id }

// project names the caller's disk: one per person and thread, and nothing a
// caller chose, so a name cannot reach another person's cell.
func project(subject, thread string) string {
	h := sha256.Sum256([]byte(subject + "/" + strings.TrimSpace(thread)))
	return "dev-" + hex.EncodeToString(h[:8])
}

// acting is the sandbox plane acting for a person in an org: the org and the
// subject the sandbox checks ownership against. It names nobody who pays, so it is
// for the calls that charge nothing (a stream, a stop, an end); a lease is taken
// actingAs the person.
func acting(org, subject string) context.Context {
	return actingAs(org, zip.Caller{User: subject})
}

// actingAs is acting, carrying who pays: the username, home org, platform authority
// and signed billing account the person was validated with. The sandbox resolves a
// lease's wallet from them (meter.PayerFrom), and the model gateway a call's. From
// the subject alone it resolved "<org>/<subject>", a wallet nobody funds, so in the
// signup org every lease was refused 402 while the wallet the same token pays from
// over HTTP held money (#907).
func actingAs(org string, who zip.Caller) context.Context {
	return cloud.For(zip.WithCaller(context.Background(), zip.Caller{
		User:    strings.Clone(strings.TrimSpace(who.User)),
		Name:    strings.Clone(strings.TrimSpace(who.Name)),
		Owner:   strings.Clone(strings.TrimSpace(who.Owner)),
		Admin:   who.Admin,
		Account: strings.Clone(strings.TrimSpace(who.Account)),
	}), org)
}

// on is actingAs the caller of a request, bound to its lifetime.
func on(ctx context.Context) (context.Context, func(), error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, nil, err
	}
	sub, ok := principal.SubjectFrom(ctx)
	if !ok || strings.TrimSpace(sub) == "" {
		return nil, nil, zip.ErrForbidden("dev: a cell belongs to a person, and this credential names none")
	}
	who := zip.CallerOf(ctx)
	who.User = sub
	out, cancel := context.WithCancel(actingAs(org, who))
	stop := context.AfterFunc(ctx, cancel)
	return out, func() { stop(); cancel() }, nil
}

// dispatchSource resolves a linked desktop login and takes a lease through
// apps/source on the internal plane; a test replaces it.
var dispatchSource = func(ctx context.Context, org, subject, id, provider, instance string) (*client.SourceRouteOut, error) {
	return cloud.Ask[client.SourceDispatchIn, client.SourceRouteOut](
		zip.WithCaller(ctx, zip.Caller{Org: org, User: subject}), "sources", client.SourcesDispatch,
		&client.SourceDispatchIn{ID: id, Subject: subject, Provider: provider, Instance: instance})
}

// releaseSource releases a dispatch lease through apps/source on the internal
// plane; a test replaces it.
var releaseSource = func(ctx context.Context, org, subject, id, lease string) error {
	_, err := cloud.Ask[client.SourceLeaseIn, client.SourceReleasedOut](
		zip.WithCaller(ctx, zip.Caller{Org: org, User: subject}), "sources", client.SourcesRelease,
		&client.SourceLeaseIn{ID: id, Subject: subject, Lease: lease})
	return err
}

// openDesktop resolves a linked desktop login via apps/source Dispatch and returns
// its ZAP endpoint and lease. A refusal from Dispatch is returned untouched and
// never silently falls back to a cloud cell or another credential.
func openDesktop(ctx context.Context, exec *Execution) (*Session, error) {
	if exec == nil {
		return nil, zip.ErrBadRequest(`dev: runtime=source requires execution {"via":"desktop","desktop":"...","provider":"..."}`)
	}
	desktop := strings.TrimSpace(exec.Desktop)
	provider := strings.TrimSpace(exec.Provider)
	instance := strings.TrimSpace(exec.Instance)
	if desktop == "" || (provider == "" && instance == "") {
		return nil, zip.ErrBadRequest("dev: execution.desktop and execution.provider are required for via=desktop")
	}
	org, sub, err := person(ctx)
	if err != nil {
		return nil, err
	}
	route, err := dispatchSource(ctx, org, sub, desktop, provider, instance)
	if err != nil {
		return nil, err
	}
	return &Session{
		ID:      desktop,
		Label:   "Linked desktop",
		Zap:     route.Zap,
		Lease:   route.Lease,
		Expires: route.Expires,
		Execution: &Execution{
			Via:      executionDesktop,
			Desktop:  desktop,
			Provider: route.Provider,
			Instance: route.Instance,
		},
	}, nil
}

// create leases the caller's cell (or resolves a linked desktop when execution.via
// is "desktop"), or resumes it, and starts Dev in it.
//
// The cell the project already holds is found by asking the sandbox who has it;
// leasing over it would be refused, because a project's disk attaches to one cell.
// A lease the sandbox refuses — quota, no node — is the answer, untouched. A desktop
// dispatch refusal is likewise returned untouched with no fallback.
func create(ctx context.Context, in *sessionIn) (*Session, error) {
	var resolvedExec *Execution
	via := ""
	if in.Execution != nil {
		via = strings.TrimSpace(in.Execution.Via)
	}
	switch via {
	case "", executionCell:
		if in.Execution != nil {
			resolvedExec = &Execution{Via: executionCell}
		}
	case executionKey:
		account := strings.TrimSpace(in.Execution.Account)
		if account == "" {
			return nil, zip.ErrBadRequest("dev: execution.account is required for via=key")
		}
		resolvedExec = &Execution{Via: executionKey, Account: account}
	case executionDesktop:
		if in.Runtime != "" && in.Runtime != runtimeAuto && in.Runtime != runtimeSource {
			return nil, zip.ErrBadRequest("dev: execution.via=desktop cannot be combined with runtime=" + in.Runtime)
		}
		return openDesktop(ctx, in.Execution)
	default:
		return nil, zip.ErrBadRequest("dev: execution.via must be cell, key or desktop")
	}

	switch in.Runtime {
	case "", runtimeSandbox, runtimeAuto:
	case runtimeWasm:
		s, err := openWasm(ctx)
		if err != nil {
			return nil, err
		}
		s.Execution = resolvedExec
		return s, nil
	case runtimeSource:
		if via != "" && via != executionDesktop {
			return nil, zip.ErrBadRequest("dev: runtime=source requires execution.via=desktop")
		}
		return openDesktop(ctx, in.Execution)
	default:
		return nil, zip.ErrBadRequest("runtime must be sandbox, wasm, source or auto")
	}
	account, issuer, err := callerAccountIssuer(ctx)
	if err != nil {
		return nil, err
	}
	cctx, done, err := on(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	sub, _ := principal.SubjectFrom(ctx)
	cell, err := takeCell(cctx, account, issuer, callerBearer(ctx), sub, in.Thread)
	if err != nil {
		return nil, err
	}
	return &Session{ID: cell.ID, Label: "Hanzo cloud",
		Zap: zapURL(cell.ID), Sandbox: cell.ID, Execution: resolvedExec}, nil
}

// callerAccountIssuer resolves "<owner>/<name>" and the bearer's issuer from an HTTP
// request context, refusing credentials that carry no IAM account.
func callerAccountIssuer(ctx context.Context) (account, issuer string, err error) {
	who := zip.CallerOf(ctx)
	if strings.TrimSpace(who.Owner) == "" || strings.TrimSpace(who.Name) == "" {
		return "", "", zip.ErrForbidden("dev: a cell is pinned to an IAM account, and this credential names none")
	}
	if c, ok := cloud.Request(ctx); ok {
		issuer = cloud.CallerIssuer(c)
	}
	return who.Owner + "/" + who.Name, issuer, nil
}

// callerBearer is the person's own JWT bearer from an HTTP request context, as the
// identity boundary validated it (cloud.CallerBearer), relayed into the lease so the
// cell signs in as them (client.LeaseIn.Bearer). Empty for an API key or a request
// with no bearer, and the cell then holds no identity, as before.
func callerBearer(ctx context.Context) string {
	if c, ok := cloud.Request(ctx); ok {
		return cloud.CallerBearer(c)
	}
	return ""
}

// startCellDaemon ensures hanzo-dev serve is listening on Socket inside cellID,
// pinned to account ("<owner>/<name>") and issuer. Invoked both when a cell is
// first taken and when a parked cell is resumed, since SandboxPark terminates the
// pod (and ephemeral /tmp) while SandboxLease restores only its disk.
func startCellDaemon(cctx context.Context, cellID, account, issuer, clientID string) error {
	ran, err := sandboxpeer.SandboxRun(cctx, &client.RunIn{
		ID:         cellID,
		Argv:       []string{"sh", "-c", start, "sh", account, issuer, clientID},
		TimeoutSec: int(ready / time.Second),
	})
	if err != nil {
		return err
	}
	if ran.ExitCode != 0 {
		return zip.Errorf(http.StatusServiceUnavailable, "dev: the server did not start in the cell: %s", strings.TrimSpace(ran.Stderr))
	}
	return nil
}

// takeCell leases a person's cell, or finds the one they hold, and starts Dev in it
// pinned to their IAM account ("<owner>/<name>"). bearer is the person's own validated
// token, carried on the lease so the cell is signed in as them before Dev starts: the
// agent each Dev turn runs has no other credential (#907). The cell the project already holds is
// found by asking the sandbox who has it; leasing over it would be refused, because a
// project's disk attaches to one cell. A lease the sandbox refuses — quota, no node —
// is the answer, untouched. ctx acts for the person.
//
// A cell this call leased is ended again when Dev does not start in it: the caller is
// answered with no session id, so nothing could end it, and it would be billed to the
// person until the reaper found it idle. Its disk is kept, as on any end. A cell they
// already held is left as it was.
//
// Dev not starting is a 503, never a 502: Cloudflare replaces every origin 502 on
// api.hanzo.ai with its own page, and the reason below is the one thing the caller
// needs to read.
func takeCell(ctx context.Context, account, issuer, bearer, subject, thread string) (*client.Leased, error) {
	proj := project(subject, thread)
	held, err := sandboxpeer.SandboxAttach(ctx, &client.AttachIn{Project: proj})
	if err != nil {
		return nil, err
	}
	cell, err := sandboxpeer.SandboxLease(ctx, &client.LeaseIn{ID: held.ID, Class: class, Project: proj, Bearer: bearer})
	if err != nil {
		return nil, err
	}
	if err := startCellDaemon(ctx, cell.ID, account, issuer, cloud.BearerAudience(bearer)); err != nil {
		if held.ID == "" {
			_, _ = sandboxpeer.SandboxEnd(context.WithoutCancel(ctx), &client.EndIn{ID: cell.ID})
		}
		return nil, err
	}
	return cell, nil
}

// end gives the cell back (or releases a linked-desktop dispatch lease). The disk is
// kept; a lease ends, a person's work does not. A cell or lease already gone is already
// ended.
func end(ctx context.Context, in *endSessionIn) (*struct{}, error) {
	id := strings.TrimSpace(in.ID)
	if strings.HasPrefix(id, wasmPrefix) {
		return &struct{}{}, endWasm(ctx, id)
	}
	if strings.HasPrefix(id, sourcePrefix) {
		lease := strings.TrimSpace(in.Lease)
		if lease == "" {
			return &struct{}{}, nil
		}
		org, sub, err := person(ctx)
		if err != nil {
			return nil, err
		}
		if err := releaseSource(ctx, org, sub, id, lease); err != nil && !gone(err) {
			return nil, err
		}
		return &struct{}{}, nil
	}
	cctx, done, err := on(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	if _, err := sandboxpeer.SandboxEnd(cctx, &client.EndIn{ID: id}); err != nil && !gone(err) {
		return nil, err
	}
	return &struct{}{}, nil
}

// person is the org and subject a request acts for.
func person(ctx context.Context) (org, subject string, err error) {
	if org, err = principal.Acting(ctx); err != nil {
		return "", "", err
	}
	subject, ok := principal.SubjectFrom(ctx)
	if !ok || strings.TrimSpace(subject) == "" {
		return "", "", zip.ErrForbidden("dev: a session belongs to a person, and this credential names none")
	}
	return org, strings.TrimSpace(subject), nil
}

// suspend puts a session away: its cell is given back, its disk and its work stay, and
// it takes no turn until resumed.
func suspend(ctx context.Context, in *endIn) (*struct{}, error) {
	org, sub, err := person(ctx)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(in.ID, wasmPrefix) {
		rt := running.Load()
		if rt == nil {
			return nil, zip.ErrNotFound("dev: no such session")
		}
		return &struct{}{}, mapped(rt.Suspend(ctx, org, sub, in.ID))
	}
	cctx, done, err := on(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	_, err = sandboxpeer.SandboxPark(cctx, &client.ParkIn{ID: strings.TrimSpace(in.ID)})
	return &struct{}{}, err
}

// resume brings a suspended session back.
func resume(ctx context.Context, in *endIn) (*Session, error) {
	org, sub, err := person(ctx)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(in.ID)
	if strings.HasPrefix(id, wasmPrefix) {
		rt := running.Load()
		if rt == nil {
			return nil, zip.ErrNotFound("dev: no such session")
		}
		if err := mapped(rt.Resume(ctx, org, sub, id)); err != nil {
			return nil, err
		}
		return &Session{ID: id, Label: "Hanzo cloud (chat)", Zap: zapURL(id)}, nil
	}
	account, issuer, err := callerAccountIssuer(ctx)
	if err != nil {
		return nil, err
	}
	cctx, done, err := on(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	bearer := callerBearer(ctx)
	cell, err := sandboxpeer.SandboxLease(cctx, &client.LeaseIn{ID: id, Bearer: bearer})
	if err != nil {
		return nil, err
	}
	if err := startCellDaemon(cctx, cell.ID, account, issuer, cloud.BearerAudience(bearer)); err != nil {
		return nil, err
	}
	return &Session{ID: cell.ID, Label: "Hanzo cloud", Zap: zapURL(cell.ID), Sandbox: cell.ID}, nil
}

// interrupt stops the commands running in a session's cell and keeps the cell and
// the session.
func interrupt(ctx context.Context, in *endIn) (*client.Stopped, error) {
	org, sub, err := person(ctx)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(in.ID, wasmPrefix) {
		rt := running.Load()
		if rt == nil {
			return nil, zip.ErrNotFound("dev: no such session")
		}
		n, err := rt.Interrupt(org, sub, in.ID)
		return &client.Stopped{Stopped: n}, mapped(err)
	}
	cctx, done, err := on(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	out, err := sandboxpeer.SandboxStop(cctx, &client.StopIn{ID: strings.TrimSpace(in.ID)})
	if err != nil {
		return nil, err
	}
	return &client.Stopped{Stopped: out.Stopped}, nil
}

// mapped answers a loop's refusal as the status it deserves.
func mapped(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, loop.ErrNoSession):
		return zip.ErrNotFound("dev: no such session")
	case errors.Is(err, loop.ErrBusy):
		return zip.Errorf(http.StatusConflict, "%v", err)
	case errors.Is(err, loop.ErrSuspended):
		return zip.Errorf(http.StatusConflict, "%v", err)
	}
	return err
}

func gone(err error) bool {
	var he *zip.HTTPError
	return errors.As(err, &he) && he.Status == http.StatusNotFound
}

// Routes mounts /v1/dev on the sandbox's app. It is not an app of its own: a cell
// is a sandbox, the stream into it is the sandbox's, and a second subsystem would
// be a second process asking the first for everything.
func Routes(app cloud.Router) error {
	if app == nil {
		return errors.New("dev: nil app")
	}
	g := app.Group("/v1/dev")
	g.Get("/health", health, zip.WithSummary("Dev service liveness"))
	g.Post("/sessions", create, zip.WithStatus(http.StatusCreated),
		zip.WithSummary("Lease the caller's cloud cell and start Dev in it"))
	g.Delete("/sessions/:id", end, zip.WithSummary("End a cloud cell"))
	g.Post("/sessions/:id/suspend", suspend, zip.WithSummary("Put a session away and give its cell back"))
	g.Post("/sessions/:id/resume", resume, zip.WithSummary("Bring a suspended session back"))
	g.Post("/sessions/:id/interrupt", interrupt, zip.WithSummary("Stop the commands a session is running"))
	luxlog.Default().New("subsystem", "dev").Info("dev cells mounted over sandboxes", "peer", sandboxpeer.App)
	return nil
}
