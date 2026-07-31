package cloud

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
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

// ErrNoPeer reports that an app is NOT PART OF THIS DEPLOYMENT: its socket is
// unbound and the router either is not here or does not know the name.
//
// It is the answer Peer always claimed to give and never could. zip dials lazily,
// so a missing socket produced no error at all until the first Call, and then
// arrived as a 502 indistinguishable from a peer that answered badly — so every
// caller had to choose one meaning for both, and the fleet has shipped that
// mistake in both directions (a 403 read as "split deploy", an outage read as
// "nothing is priced"). Now the two facts are two errors:
//
//	ErrNoPeer          the app is not here — fall back, or stay inert
//	anything else      the app is here and this call failed — that is an outage
//
// The distinction is DECIDED, not guessed: an unbound socket is asked of the
// router, which owns the manifest, and only its answer settles which fact it is.
var ErrNoPeer = errors.New("cloud: app is not deployed here")

const (
	// planeBindWait bounds how long ServePlane waits for its own socket.
	planeBindWait = 5 * time.Second
	// wakeTimeout bounds one start request to the router. A cold child pays its
	// whole startup inside this, so it is the host's own plugin-start budget.
	wakeTimeout = 90 * time.Second
)

// Peer opens a call to another app. The socket is resolved per call, so an app
// that starts later is reached without a restart here.
//
// It DIALS and nothing more. Bringing a lazy app up is Ask's job, because waking
// one costs a child's whole startup and only a caller holding a context can say
// how long it is willing to wait for that.
func Peer(app string) (*zip.Conn, error) {
	bindRuntimeDir()
	c, err := zip.DialApp(app)
	if err != nil {
		return nil, fmt.Errorf("peer %s: %w", app, err)
	}
	return c, nil
}

// reach makes app's socket resolvable, or says why it cannot be.
//
// Bound already ⇒ nothing to do, which is the steady state and costs one stat.
// Unbound ⇒ ask the router to start it, and let the router's answer decide which
// fact it is: it owns the manifest, so "no plugin named x" is "not deployed here"
// and a failed start is an outage.
//
// The CALLER's deadline governs. wakeTimeout is a ceiling — the host's own
// plugin-start budget — never a floor: a gate that gave itself ten seconds must
// not block for ninety inside a call it thought it had bounded. A caller whose
// budget expires mid-start still fails closed, and the child it asked for keeps
// coming up (the host single-flights the start), so the next call finds it.
func reach(ctx context.Context, app string) error {
	if exists(zip.SocketPath(app)) {
		return nil
	}
	if !exists(zip.SocketPath(plane.HostApp)) {
		if underRouter() {
			// We were SPAWNED by a router and its door is gone. That is an outage,
			// and reading it as "not deployed here" would be the fail-open this
			// whole change exists to close — a payment rail that concluded nothing
			// is priced because the router's socket was missing for a moment.
			return fmt.Errorf("wake %s: this process runs under a router whose start door is not there", app)
		}
		// No router in this process tree and none expected: nothing can start an
		// app, and nothing is going to. A single-app binary run directly and a
		// developer's test are exactly this shape, and for them "not deployed
		// here" is simply true.
		return fmt.Errorf("%w: %s (no socket, no router)", ErrNoPeer, app)
	}
	ctx, cancel := context.WithTimeout(ctx, wakeTimeout)
	defer cancel()
	c, err := zip.DialApp(plane.HostApp)
	if err != nil {
		return fmt.Errorf("wake %s: %w", app, err)
	}
	defer func() { _ = c.Close() }()
	// EVERY error here is an outage, and only an ANSWER can say the app is absent.
	// This used to read a 404 as absence, and three different things answer 404 on
	// this wire — the router's own "no such app", zip's "unknown op" when the router
	// predates this op, and any framework 404 for the path. They rebuild into the
	// same *HTTPError with an empty Code, so nothing but the message text separated
	// them. On a rolling deploy the middle one is live: an older host answers
	// "unknown op: host_start", the rail would have read version skew as "this fleet
	// prices nothing", and every priced tool goes free until the last pod turns over.
	out, err := zip.Call[plane.StartIn, plane.Started](ctx, c, plane.HostStart,
		&plane.StartIn{App: app})
	switch {
	case err != nil:
		return fmt.Errorf("wake %s: %w", app, err)
	case out == nil:
		// A void reply from the only thing that knows the manifest is not an answer.
		return fmt.Errorf("wake %s: the router answered nothing", app)
	case !out.Known:
		// The router looked in its own plugin table and there is no such app here.
		return fmt.Errorf("%w: %s (the router runs no such app)", ErrNoPeer, app)
	}
	if !exists(zip.SocketPath(app)) {
		// The router started it and its plane socket is still not there. ServePlane
		// binds before the app's own listener and the router waits on that listener,
		// so this cannot be a race — it is an app that serves no plane.
		return fmt.Errorf("wake %s: started, but it binds no plane socket", app)
	}
	return nil
}

// underRouter reports whether this process was started BY the fleet router.
//
// zip hands every child it spawns the private socket it must serve on, so a set
// ZIP_ADDR is proof of a parent that owns a plugin table — the one thing that can
// start a sibling. It is what separates "this deployment does not run that app"
// from "the thing that would have told me is down", and only the first of those may
// ever be read as free.
func underRouter() bool { return strings.TrimSpace(os.Getenv("ZIP_ADDR")) != "" }

// exists is the HOT-PATH question — "is there a socket to dial" — and it is a
// stat, because it runs before every plane call and a connect does not. A stale
// file left by a crash passes it; that is correct, because "the app is here and
// broken" is an outage, and the caller must learn it from the failed CALL rather
// than from an absence it would be entitled to fall back on.
func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

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

// Ask is the whole client half: dial the app, invoke the op, close.
//
// The org a call acts for rides the CALLER — forwarded from the gateway's
// assertion when ctx carries a request, or stated by a background job with
// zip.WithCaller. It is never an argument, because a caller that can name the
// org can bill or read another tenant.
//
// It also WAKES the app when the fleet runs it lazily. A plane call never touches
// the router, so nothing else would: 106 of 112 apps start on a request reaching
// their prefix, and an app reached only over its socket was never started and never
// bound one. Ask asks the router to start it (see reach); a router that does not
// know the name — or a fleet with no router at all — answers ErrNoPeer, which is the
// ONLY error a caller may read as "fall back". Every other failure is an outage.
func Ask[In, Out any](ctx context.Context, app, op string, in *In) (*Out, error) {
	bindRuntimeDir()
	if err := reach(ctx, app); err != nil {
		return nil, err
	}
	c, err := Peer(app)
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()
	out, err := zip.Call[In, Out](ctx, c, op, in)
	if out != nil {
		detach(reflect.ValueOf(out).Elem())
	}
	return out, err
}

// detach copies every string in a reply OUT of the transport's read buffer.
//
// ZAP decodes a string zero-copy — unsafe.String over the frame (zap-proto/go
// Object.Text) — so a decoded string is a VIEW of a buffer the next call on that
// connection reuses. A reply that outlives the call therefore mutates under its
// owner, silently and much later, which is the worst shape a bug can have.
//
// It is not hypothetical: an x402 settlement recorded a payee address that had
// already become the bytes of a later message's amount ("0.0025USD" written over the
// middle of an 0x… address), so the row could never match itself and every retry of
// a paid authorization read as a replayed nonce. The decoder already copies byte
// slices; strings are the hole.
//
// It runs HERE because Ask is the one client half. A rule every caller must remember
// is a rule some caller forgets — and this one costs money three hops away from the
// line that forgot it.
//
// The kinds it walks are exactly the kinds ZAP carries: scalars, strings, byte
// slices, structs, slices of those, and pointers to them. A map cannot cross the
// plane at all (zapenc layoutOf refuses it), so there is no map case to write and no
// silent hole where one should be.
func detach(v reflect.Value) {
	switch v.Kind() {
	case reflect.String:
		if v.CanSet() && v.Len() > 0 {
			v.SetString(strings.Clone(v.String()))
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			detach(v.Field(i))
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			detach(v.Index(i))
		}
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			detach(v.Elem())
		}
	}
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
