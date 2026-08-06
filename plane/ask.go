package plane

// ask.go — the CLIENT half of the internal plane, in the leaf both halves
// already import.
//
// It lives here rather than in package cloud for one structural reason: the
// generated per-app clients (plane/<app>, see plane/gen) are the ONE typed
// way to call a peer, and package cloud is itself a caller — the edge
// rate-limiter reads finance_scope_rules, the KMS seam reads a secret, the site
// edge resolves a host. A client that had to import cloud could therefore never
// be imported BY cloud, and the one call that most needs to stop being an HTTP
// URL would be the one call the mechanism could not express. A leaf has no such
// direction.
//
// It is also what keeps a generated client cheap. package cloud drags 576
// packages; this leaf drags 75 plus zip's own. A caller that wanted one typed
// peer call should not link the request tier to get it.
//
// package cloud keeps the SERVER half — Plane, ServePlane, ResetPlane — because
// binding a socket is a deployment act that reports itself to o11y, and o11y is
// cloud's. Registering an op and calling one are opposite duties; only the
// second one has to be reachable from everywhere.

//go:generate go run ./gen

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"reflect"
	"strings"
	"syscall"
	"time"

	"github.com/zap-proto/zip"
)

// RunDirEnv overrides where app sockets live. Default: {CLOUD_DATA_DIR}/run.
const RunDirEnv = "CLOUD_RUN_DIR"

// bound is what Bind last wrote. Without it the first call would PIN the
// directory for the process: having written ZIP_RUNTIME_DIR, every later call
// would see it set and return early, so a cloud var changed afterwards — which is
// how the tests point a run at their own directory — would be read once and then
// ignored. An operator's own ZIP_RUNTIME_DIR still always wins, because it is the
// one value this never wrote.
var bound string

// Bind points zip's runtime dir at cloud's, before anything serves or dials. It
// is cloud's deployment convention expressed in zip's ONE scheme, rather than a
// second scheme that has to agree with it.
func Bind() {
	if cur := strings.TrimSpace(os.Getenv("ZIP_RUNTIME_DIR")); cur != "" && cur != bound {
		return // set by someone other than us: always wins, for both halves alike
	}
	// ONE rule, in the leaf both halves import — a caller that resolved the directory
	// differently from a callee would miss it silently (see BindRuntimeDir).
	bound = BindRuntimeDir()
}

// Unbind forgets what Bind resolved, so the next Bind reads the environment
// again.
//
// It is the test seam, and it exists for the same reason ResetPlane does: a
// process binds once, so nothing in production calls this, but a test that points
// a run at its own directory has to be able to say the previous answer is stale.
func Unbind() { bound = "" }

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
	// wakeTimeout bounds one start request to the router. A cold child pays its
	// whole startup inside this, so it is the host's own plugin-start budget.
	wakeTimeout = 90 * time.Second
	// probeWait bounds the liveness probe Reach makes before every plane call. A
	// unix socket with a listener accepts from the backlog whether or not the app is
	// between requests, so this is a ceiling for a host that has run out of
	// descriptors — not a cost anyone pays in the steady state.
	probeWait = 250 * time.Millisecond
)

// Peer opens a call to another app. The socket is resolved per call, so an app
// that starts later is reached without a restart here.
//
// It DIALS and nothing more. Bringing a lazy app up is Ask's job, because waking
// one costs a child's whole startup and only a caller holding a context can say
// how long it is willing to wait for that.
func Peer(app string) (*zip.Conn, error) {
	Bind()
	c, err := zip.DialApp(app)
	if err != nil {
		return nil, fmt.Errorf("peer %s: %w", app, err)
	}
	return c, nil
}

// Reach makes app's socket resolvable, or says why it cannot be.
//
// Listening already ⇒ nothing to do, which is the steady state.
// Not listening ⇒ ask the router to start it, and let the router's answer decide
// which fact it is: it owns the manifest, so "no plugin named x" is "not deployed
// here" and a failed start is an outage.
//
// The CALLER's deadline governs. wakeTimeout is a ceiling — the host's own
// plugin-start budget — never a floor: a gate that gave itself ten seconds must
// not block for ninety inside a call it thought it had bounded. A caller whose
// budget expires mid-start still fails closed, and the child it asked for keeps
// coming up (the host single-flights the start), so the next call finds it.
func Reach(ctx context.Context, app string) error {
	up, err := Listening(zip.SocketPath(app))
	if err != nil {
		return fmt.Errorf("reach %s: %w", app, err)
	}
	if up {
		return nil
	}
	host, err := Listening(zip.SocketPath(HostApp))
	if err != nil {
		// The router's door is there and unusable. That is an outage, and the same
		// fail-open argument as below applies: it may never read as absence.
		return fmt.Errorf("wake %s: the router's start door is unusable: %w", app, err)
	}
	if !host {
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
	c, err := zip.DialApp(HostApp)
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
	out, err := zip.Call[StartIn, Started](ctx, c, HostStart, &StartIn{App: app})
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
	if up, err := Listening(zip.SocketPath(app)); err != nil || !up {
		// The router started it and its plane socket still answers nobody. ServePlane
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

// Listening reports whether path has a LISTENER behind it.
//
// It CONNECTS, because the FILE does not answer the question. This was a stat, on
// the argument that a connect is too dear for the hot path and that a stale file
// left by a crash is "the app is here and broken" — a fact the failed call would
// then carry. Both halves were wrong for a LAZY app, and the fleet runs 106 of them.
//
// A socket file outlives the process that bound it wherever the run directory is a
// volume, so by stat a pod that died last night is indistinguishable from an app
// that is up. For a lazy app that difference is the entire answer: the file
// suppresses the wake that would have PUT a listener there, so the peer is never
// started, and no number of failed calls changes that — "here and broken" is
// learnable from the call, "here and never coming up" is not. Prod ran that shape
// for three days: /var/lib/cloud/run/commerce.sock left by a previous pod, commerce
// never woken, every prepaid-balance read refused, and the balance gate being
// fail-CLOSED, every paid completion in the fleet answering 503.
//
// It removes nothing. The listener already unlinks a stale path before it binds
// (zaphttp.Server.ListenAndServe), so the wake this returns to repairs the run
// directory as a side effect of doing its job and the directory keeps ONE writer.
//
// ENOENT (never bound here) and ECONNREFUSED (a file with nobody behind it) are the
// same fact — there is no listener — and both are the wake path's business. Any
// OTHER dial error is a socket that is present and unusable: an outage, returned as
// one, never laundered into an absence a caller would be entitled to fall back on.
func Listening(path string) (bool, error) {
	c, err := net.DialTimeout("unix", path, probeWait)
	if err == nil {
		_ = c.Close()
		return true, nil
	}
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
		return false, nil
	}
	return false, err
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
// bound one. Ask asks the router to start it (see Reach); a router that does not
// know the name — or a fleet with no router at all — answers ErrNoPeer, which is the
// ONLY error a caller may read as "fall back". Every other failure is an outage.
//
// Prefer the GENERATED client for the peer (plane/<app>) over calling this with
// three loose strings and two loose type arguments. Ask cannot check that an op
// name belongs to the app it is being sent to, nor that In and Out are the pair
// that op declared; the generated wrapper is exactly that check, made by the
// compiler. This stays exported because the generator emits calls to it, and
// because an op declared outside the apps tree still has to be reachable.
func Ask[In, Out any](ctx context.Context, app, op string, in *In) (*Out, error) {
	Bind()
	// THE PEER IS SOMETIMES THIS PROCESS. A fused binary mounts many apps, and
	// the ops of every one of them are registered on the same plane app; commerce
	// asking commerce for a balance is then a function call wearing an address.
	// Dialing our own socket for it would encode a value we hold, hand it to the
	// kernel, and parse it back into a copy — and there is nothing to wake, so
	// Reach has no work either.
	//
	// This is the ONE place that decision is made. A caller says the op's name
	// and the same op runs; where it runs is not the caller's to know, which is
	// what keeps "co-resident" from becoming a second spelling of every call.
	if a := zip.Serving(app); a != nil {
		return zip.Here[In, Out](ctx, a, op, in)
	}
	if err := Reach(ctx, app); err != nil {
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
