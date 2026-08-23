package cloud

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/plane"
	zapmcp "github.com/zap-proto/mcp"
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

// Door publishes app's AGENT DOOR on the internal plane, at [manifest.MCPPath].
//
// It is the same move [fleet.Door.Serve] makes one level up, for the same
// reason: the door a subsystem serves on the edge is reachable only through the
// identity boundary, and the fleet's own callers are not on the edge.
//
// # Why the edge door cannot answer an internal caller
//
// The boundary (SanitizeIdentity) deletes every authority header on ingress and
// re-mints one only from a credential it verified. That is exactly right for a
// stranger and exactly wrong for a sibling: an agent run holds a principal it
// resolved SERVER-SIDE — the org a Slack workspace is installed in, the person
// the run is attributed to — and no bearer to replay for it. Reached through the
// edge, its statement was deleted and every org-scoped op refused it, while the
// request log still showed the identity that had arrived (zip reports the caller
// as the request BEGINS, before any middleware). The op was not seeing a
// different fact from the log; it was seeing a LATER one.
//
// So the internal caller is not sent through the edge. It reaches this door,
// where [zip.CallerOf] reads the caller off the request it is actually serving
// and [principal.OrgFrom] decides on it with the SAME rule a routed request gets
// — an org with no validated user is still refused, here as there.
//
// # What makes that safe is the address, not a check
//
// This is registered on [Plane], which listens on the app's canonical socket and
// is never mounted on the edge router; there is no path from the public internet
// to it, in the same way there is no path to a route nobody registered. A
// sibling's statement is worth what that socket is worth — the same worth
// [zip.WithCaller] already gives one, and the same authority a plane op has
// granted since the plane existed. The edge door is untouched: a forged
// X-Org-Id / X-User-Id arriving at the front door is still hopped into the
// subsystem's EDGE door, and still dies at the boundary there.
//
// It is at manifest.MCPPath and not zip's own /mcp because zip already serves the
// PLANE's ops at /mcp — a different registry, and one route per address.
func Door(app *zip.App) {
	Plane().Post(manifest.MCPPath, func(c *zip.Ctx) error {
		var f zapmcp.Frame
		if err := json.Unmarshal(c.Body(), &f); err != nil {
			return c.JSON(http.StatusOK, &zapmcp.Frame{Kind: zapmcp.Response,
				Err: &zapmcp.Error{Code: zapmcp.CodeParse, Message: "parse error"}})
		}
		// c.Forward() binds THIS request to the context the op is handed, which is
		// what zip's own HTTP adapter does and the whole of how the caller reaches
		// the handler (zip mcp.go, callerContext).
		ans := app.MCP(c.Forward(), &f)
		if ans == nil {
			return c.Status(http.StatusAccepted).JSON(http.StatusAccepted, map[string]any{})
		}
		return c.JSON(http.StatusOK, ans)
	})
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
//
// It answers a DIFFERENT question depending on the context it is handed: off a
// request it is the stated caller, on one it is the headers. A handler that turns
// a TENANT on it must not read it directly — [principal.Acting] is the one rule
// that tells those apart.
func Who(ctx context.Context) zip.Caller { return zip.CallerOf(ctx) }

// Tenant is the org a PLANE OP acts for.
//
// It is not a second copy of [principal.Acting] — it is Acting plus the one case
// Acting cannot admit, and it is deliberately narrow. Acting composes
// validated-ness AND an org, so it refuses a background call by construction: a
// reconcile loop or a peer has no user to be validated. [For] states the tenant
// such a call acts for, in-process, and zip reads a stated caller only on a
// context with no request behind it.
//
// SO IT ASKS WHETHER A REQUEST IS BEHIND THIS CONTEXT, and that is the whole of
// the distinction. With one, the org is a header and only Acting may decide it —
// the identity boundary restores an unvalidated caller's own org header for the
// data path, so a header alone is the client's own claim. Without one, there is
// no header to have been restored.
//
// IT IS FOR OPS REGISTERED ON [Plane], AND ITS PRECONDITION IS [Bridge]. Bridge
// is what makes "a request is behind this" answerable, and an app that serves
// HTTP without installing it answers no to a question whose true answer is yes.
// Identify installs it for every app cloud mounts, which is why the ops that use
// this are safe; an app that stands one up itself and reaches for this without it
// would be reading a header as though a peer had said it. Ops on the edge use
// Acting, which needs no such precondition and refuses either way.
func Tenant(ctx context.Context) (string, bool) {
	if org, err := principal.Acting(ctx); err == nil {
		return org, true
	}
	if _, onRequest := Request(ctx); onRequest {
		return "", false
	}
	org := Who(ctx).Org
	if org == "" || principal.OrgHasUnsafeRune(org) || len(org) > principal.MaxOrgLen {
		return "", false
	}
	return org, true
}

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
	// BOTH READERS OF THE TENANT MOVE TOGETHER, OR NEITHER DOES.
	//
	// principal.OrgFrom asks the parked slot FIRST and the caller second, and
	// calls them one fact. The parked slot holds the org the boundary minted for
	// the ORIGINAL request, so re-pointing only the caller left them disagreeing —
	// and which one a callee saw turned on whether it happened to be co-resident.
	// A socket hop reads the caller off headers and answers the new tenant;
	// zip.Here hands this very context to the handler and answers the old one.
	// Same call, two tenants, decided by a deployment shape plane.Ask exists to
	// keep from being the caller's business.
	//
	// principal.WithActing is the one that can refuse — an org bearing a
	// whitespace or format rune grants no scoping, because trimming it would fold
	// two distinct orgs onto one namespace. So it decides, and the caller follows
	// it: an org it would not park is one this does not state either, and the
	// delegation falls back to the caller's own tenant rather than half-moving.
	ctx := principal.WithActing(c.Context(), org)
	if acting, ok := principal.OrgFrom(ctx); ok && acting != c.Org() {
		who.Org = acting
	}
	return zip.WithCaller(ctx, who)
}
