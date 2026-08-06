package cloud

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// plane.go — the internal plane: one app calling another, over ZAP on a unix
// socket, as ordinary typed ops.
//
// # It is a SECOND app, and that is the point
//
// A typed op rides every transport its app listens on. The product app listens
// on the edge — HTTP for browsers, ZAP over TCP across hosts — and a host that
// composed this binary as a plugin also proxies edge traffic to it over a
// private unix socket. So neither "is this HTTP?" nor "did this arrive on a
// socket?" separates an internal call from a public request: the proxied leg
// looks exactly like a peer.
//
// The separation is therefore structural rather than a check. Internal ops are
// registered on THIS app, which listens on exactly one address — the app's
// canonical socket — and is never mounted on the edge router. There is no path
// from the public internet to a plane op, in the same way there is no path to a
// route that was never registered.
//
// It keeps every projection. The plane app has its own OpenAPI document, MCP
// tool list and CLI — so the gate, the meter and the secret reads are as
// self-describing as the product surface, without publishing themselves into
// the public one.
//
// # One socket path scheme, and it is zip's
//
// zip.SocketPath(name) resolves {ZIP_RUNTIME_DIR}/<name>.sock, and both halves
// use it: an app serves at that path, a caller reaches it with
// zip.DialApp(name). Cloud points ZIP_RUNTIME_DIR at its own data root (see
// bindRuntimeDir) rather than keeping a second rule about where sockets live —
// the deployment convention still decides the directory, zip decides the shape,
// and a server and its callers cannot disagree.
//
// # The CLIENT half lives in the leaf
//
// Ask and everything under it moved to package plane (plane/ask.go). This file
// keeps the SERVER half — registering ops and binding the socket — because
// binding reports itself to o11y, and o11y is cloud's.
//
// The split is what makes a generated peer client possible at all. package cloud
// is itself a caller (the edge rate-limiter below reads finance_scope_rules), so
// a client that imported cloud could never be imported by cloud — the one call
// that most needed to stop being an HTTP URL would be the one call the mechanism
// could not express. The names below stay so the callers that already say
// cloud.Ask keep saying it; there is still exactly one implementation.

// runDirEnv overrides where app sockets live. Default: {CLOUD_DATA_DIR}/run.
const runDirEnv = plane.RunDirEnv

// ErrNoPeer reports that an app is NOT PART OF THIS DEPLOYMENT. See
// [plane.ErrNoPeer] — this is that error, not a second one, so errors.Is holds
// across both spellings.
var ErrNoPeer = plane.ErrNoPeer

// bindRuntimeDir points zip's runtime dir at cloud's, before anything serves or
// dials.
func bindRuntimeDir() { plane.Bind() }

// reach makes app's socket resolvable, or says why it cannot be.
func reach(ctx context.Context, app string) error { return plane.Reach(ctx, app) }

// listening reports whether path has a LISTENER behind it.
func listening(path string) (bool, error) { return plane.Listening(path) }

// Peer opens a call to another app.
func Peer(app string) (*zip.Conn, error) { return plane.Peer(app) }

// Ask is the whole client half: dial the app, invoke the op, close.
//
// Prefer the GENERATED client for the peer (plane/<app>) — it is this call with
// the app name, the op name and the In/Out pair already fixed to each other, so
// the compiler checks what only a running fleet could check here.
func Ask[In, Out any](ctx context.Context, app, op string, in *In) (*Out, error) {
	return plane.Ask[In, Out](ctx, app, op, in)
}

var planeApp struct {
	sync.Mutex
	app *zip.App
}

// Plane is the app internal ops register on. Every app in this process shares
// it, because a process serves ONE canonical socket per app name and the ops
// behind it are that app's whole internal surface.
//
// Declare an op on it exactly as on any app:
//
//	zip.Post[plane.SecretIn, plane.Secret](cloud.Plane(), "/kms/get", read,
//	    zip.WithOperationID(plane.KMSGet))
func Plane() *zip.App {
	planeApp.Lock()
	defer planeApp.Unlock()
	if planeApp.app == nil {
		bindRuntimeDir()
		planeApp.app = zip.New(zip.Config{AppName: "plane"})
	}
	return planeApp.app
}

// ResetPlane drops the process's plane app and every op declared on it.
//
// Mount runs again in tests, and zip's op registry APPENDS: a second
// registration of the same op sits BEHIND the first, so a test would be
// answered by a previous test's handler — with that test's state, silently, and
// looking like a pass. The hand-written registry this replaced allowed
// re-exposing a name because of exactly that, and this is where the property
// went.
//
// A process mounts once, so nothing in production calls this.
func ResetPlane() {
	planeApp.Lock()
	defer planeApp.Unlock()
	if planeApp.app != nil {
		_ = planeApp.app.Shutdown()
	}
	planeApp.app = nil
}

// ServePlane binds the canonical socket for one app name and serves every op
// registered on the plane.
//
// Serve calls it for each app this process mounts, AFTER Mount has run, so a
// resolvable socket always means "the app is up, with its ops live". An app
// that is up and answers 404 for an op is version skew — a different fact, and
// separately diagnosable.
//
// It does not return until the socket ACCEPTS. Listening happens in a goroutine,
// so returning early made "the socket is bound" a race the caller could not see:
// Serve binds the plane before the app's own listener, and the host waits on that
// listener to call a child up — so a peer woken through the router could dial a
// socket that existed as a promise and not yet as a listener. Waiting here is
// what makes the ordering a guarantee instead of a coincidence.
func ServePlane(name string, log luxlog.Logger) (func() error, error) {
	bindRuntimeDir()
	path := zip.SocketPath(name)
	app := Plane()
	errs := make(chan error, 1)
	go func() { errs <- app.Listen(path) }()
	if err := awaitSocket(path, errs, planeBindWait); err != nil {
		return nil, fmt.Errorf("plane %s: %w", name, err)
	}
	// This process was asked to serve this plane, so from here on its socket
	// going quiet is a FAULT rather than an absence. Recording it at the moment
	// of binding is what lets hanzo_plane_peer_bound tell those two apart —
	// the distinction o11y's own disappearance turned on.
	planeServed(name)
	if log != nil {
		log.Info("plane listening", "app", name, "sock", path)
	}
	return func() error {
		if err := app.Shutdown(); err != nil {
			return err
		}
		select {
		case err := <-errs:
			return err
		default:
			return nil
		}
	}, nil
}

const (
	// planeBindWait bounds how long ServePlane waits for its own socket. The
	// client half's own budgets (the wake ceiling, the liveness probe) live with
	// it in plane/ask.go.
	planeBindWait = 5 * time.Second
)

// awaitSocket blocks until path ACCEPTS, or the listener gives up, or the deadline
// passes. This one really does connect: it runs once, at bind, and the whole point
// is to know a listener is there before anyone is told the app is up.
//
// It watches the listener WHILE it waits. A stale socket left by a crash fails Listen
// immediately with "address already in use", and a poll that only looked at the path
// would spend its entire budget waiting for a listener that had already returned —
// turning an instant, nameable failure into a timeout with the wrong reason on it.
func awaitSocket(path string, errs <-chan error, within time.Duration) error {
	deadline := time.Now().Add(within)
	for {
		select {
		case err := <-errs:
			return fmt.Errorf("listener on %s gave up: %w", path, err)
		default:
		}
		c, err := net.DialTimeout("unix", path, time.Second)
		if err == nil {
			_ = c.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("socket %s did not accept within %s: %v", path, within, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// For states the tenant a BACKGROUND call acts for — a reconcile loop, a grant
// issued when an org opens, a meter that debits after the response has gone
// out. See [plane.For] — this is that function, not a second one, so a caller
// below the cloud import edge states the tenant exactly the same way.
func For(ctx context.Context, org string) context.Context { return plane.For(ctx, org) }

// Who reads the principal a plane op is acting for. A handler that needs
// authority refuses an empty org rather than treating it as permission.
func Who(ctx context.Context) zip.Caller { return zip.CallerOf(ctx) }

// As delegates THIS request's principal to a call, pointed at a named tenant.
//
// It is for the operator acting on someone else's books: a SuperAdmin migrating
// org X carries their own identity — which is what the callee re-checks — while
// the tenant being read is X, not the admin's own org. c.Forward() alone cannot
// express that, deliberately: it propagates the gateway's assertion unchanged,
// and the assertion says the admin's org.
//
// So the principal is carried WHOLE and only the tenant is re-pointed, on a
// context with no request behind it, which is the one place zip reads a stated
// caller. The authority still comes from the gateway — every other field is the
// one it minted — and the callee applies its own rules to it. A caller that was
// not admitted as an admin gains nothing by naming another org, because naming
// the org was never what granted anything.
//
// org empty keeps the caller's own tenant, so this is also the plain "delegate
// me" form for a call made from a handler that has no other tenant in mind.
func As(c *zip.Ctx, org string) context.Context {
	if c == nil {
		return For(context.Background(), org)
	}
	who := zip.Caller{
		Org:       c.Org(),
		Project:   c.Project(),
		User:      c.User(),
		Name:      c.UserName(),
		Email:     c.UserEmail(),
		Owner:     c.UserOwner(),
		Admin:     c.IsAdmin(),
		OrgAdmin:  c.IsOrgAdmin(),
		RequestID: c.RequestID(),
	}
	if org = strings.TrimSpace(org); org != "" {
		who.Org = org
	}
	return zip.WithCaller(c.Context(), who)
}
