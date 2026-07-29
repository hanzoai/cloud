package cloud

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	luxlog "github.com/luxfi/log"
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

// runDirEnv overrides where app sockets live. Default: {CLOUD_DATA_DIR}/run.
const runDirEnv = "CLOUD_RUN_DIR"

// boundRuntimeDir is what bindRuntimeDir last wrote. Without it the first call
// would PIN the directory for the process: having written ZIP_RUNTIME_DIR, every
// later call would see it set and return early, so a cloud var changed afterwards
// — which is how the tests point a run at their own directory — would be read
// once and then ignored. An operator's own ZIP_RUNTIME_DIR still always wins,
// because it is the one value this never wrote.
var boundRuntimeDir string

// bindRuntimeDir points zip's runtime dir at cloud's, before anything serves or
// dials. It is cloud's deployment convention expressed in zip's ONE scheme,
// rather than a second scheme that has to agree with it.
func bindRuntimeDir() {
	if cur := strings.TrimSpace(os.Getenv("ZIP_RUNTIME_DIR")); cur != "" && cur != boundRuntimeDir {
		return // set by someone other than us: always wins, for both halves alike
	}
	dir := "/run/hanzo"
	if v := strings.TrimSpace(os.Getenv(runDirEnv)); v != "" {
		dir = v
	} else if v := strings.TrimSpace(os.Getenv("CLOUD_DATA_DIR")); v != "" {
		dir = filepath.Join(v, "run")
	}
	boundRuntimeDir = dir
	_ = os.Setenv("ZIP_RUNTIME_DIR", dir)
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
func ServePlane(name string, log luxlog.Logger) (func() error, error) {
	bindRuntimeDir()
	path := zip.SocketPath(name)
	app := Plane()
	errs := make(chan error, 1)
	go func() { errs <- app.Listen(path) }()
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

// Peer opens a call to another app. The socket is resolved per call, so an app
// that starts later is reached without a restart here.
//
// A dial failure is the answer "that app is not running here" — the legitimate
// split-deploy or no-such-plane shape — and callers distinguish it from a bad
// ANSWER, which is a real failure. Peer absent means fall back or stay inert;
// peer answered badly means error.
func Peer(app string) (*zip.Conn, error) {
	bindRuntimeDir()
	c, err := zip.DialApp(app)
	if err != nil {
		return nil, fmt.Errorf("peer %s: %w", app, err)
	}
	return c, nil
}

// Ask is the whole client half: dial the app, invoke the op, close.
//
// The org a call acts for rides the CALLER — forwarded from the gateway's
// assertion when ctx carries a request, or stated by a background job with
// zip.WithCaller. It is never an argument, because a caller that can name the
// org can bill or read another tenant.
func Ask[In, Out any](ctx context.Context, app, op string, in *In) (*Out, error) {
	c, err := Peer(app)
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()
	return zip.Call[In, Out](ctx, c, op, in)
}

// For states the tenant a BACKGROUND call acts for — a reconcile loop, a grant
// issued when an org opens, a meter that debits after the response has gone
// out. Each acts for a tenant with no request to forward, and the callee has to
// know which one to write to the right books.
//
// An inbound request always wins over this (zip prefers the gateway's
// assertion), so it supplies an identity where there is none and can never
// launder one.
func For(ctx context.Context, org string) context.Context {
	return zip.WithCaller(ctx, zip.Caller{Org: strings.TrimSpace(org)})
}

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
