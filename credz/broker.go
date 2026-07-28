package credz

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hanzoai/cloud/credz/launch"
)

// Source is the secret store the broker reads. It is deliberately two methods:
// enumerate a scope, read one name. Nothing here knows what a secret means, so
// adding a credential is a write to the store and not a change to this package.
//
// The embedded KMS client satisfies it. An RPC or disabled KMS client does not,
// and that is the point: only the process that OWNS the sealed store can broker
// it, so which process is the broker is decided by the deployment topology
// rather than by a flag.
type Source interface {
	Names(path, env string) ([]string, error)
	Get(path, name, env string) ([]byte, error)
}

// Logger is the slice of the platform logger this package uses. Declared here
// rather than imported so credz stays a leaf: it is linked into every child, and
// a credential path should not drag a logging tree behind it.
type Logger interface {
	Info(msg string, ctx ...interface{})
	Warn(msg string, ctx ...interface{})
	Error(msg string, ctx ...interface{})
}

// Publish starts the broker, and is a no-op unless this process is both the root
// of the credential tree (p) and the owner of the secret store (src). Both halves
// matter:
//
//   - Not Root — a Leaf holds the same data-plane key it was handed, but it was
//     handed ONE app's scope. Letting it broker would let any app answer for any
//     other, which is the whole boundary.
//   - No Source — a process reaching KMS over RPC has no store to read and no
//     authority to delegate. Exactly one process in a deployment owns the sealed
//     store, so exactly one can be the broker, and it is not a choice.
//
// The posture is a parameter rather than a read of this package's own state
// because it is the caller's fact: Boot resolved it, and passing it makes "who
// may broker" a value at the call site instead of an invisible precondition.
//
// The returned Closer stops accepting and removes the socket. A nil Closer means
// this process does not broker, which is the normal case for 107 of 108 apps.
func Publish(p Posture, src Source, dataDir, adminOrg string, log Logger) (io.Closer, error) {
	if p != Root || src == nil || adminOrg == "" {
		return nil, nil
	}
	sock := filepath.Join(dataDir, SockName)
	ln, err := listen(sock)
	if err != nil {
		return nil, err
	}
	b := &broker{ln: ln, src: src, org: adminOrg, log: log, secret: LaunchSecret()}
	go b.serve()
	log.Info("credz broker LISTENING (this process holds the root key and the sealed store)",
		"sock", sock, "admin_org", adminOrg)
	return b, nil
}

// listen binds the socket at 0600. A leftover socket from a process that died
// without unlinking is reclaimed only after a dial proves nobody is behind it —
// unlinking a LIVE broker's socket would silently orphan every child that has not
// connected yet, which is a worse failure than refusing to start.
func listen(sock string) (*net.UnixListener, error) {
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		if c, derr := net.DialTimeout("unix", sock, dialTimeout); derr == nil {
			_ = c.Close()
			return nil, err // someone is actually serving here
		}
		if rerr := os.Remove(sock); rerr != nil {
			return nil, err
		}
		if ln, err = net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"}); err != nil {
			return nil, err
		}
	}
	// The data directory is already the deployment's private state; this makes the
	// socket's own mode say so too, so a broker is never reachable by another uid
	// on a shared host.
	if err := os.Chmod(sock, 0o600); err != nil {
		_ = ln.Close()
		return nil, err
	}
	return ln, nil
}

type broker struct {
	ln  *net.UnixListener
	src Source
	org string
	log Logger
	// secret verifies the tokens this deployment's launcher stamped. Taken once
	// at Publish rather than read per request so the broker cannot start
	// answering under one secret and finish under another.
	secret string
}

// maxRequest bounds what a peer may send before it has proven anything: the
// greeting plus one token line. Generous by an order of magnitude and still
// small enough that an unauthenticated peer cannot make the broker hold a buffer
// on its behalf.
const maxRequest = 512

func (b *broker) Close() error {
	err := b.ln.Close() // unlinks the socket: Go's UnixListener owns the file it bound
	return err
}

func (b *broker) serve() {
	for {
		c, err := b.ln.AcceptUnix()
		if err != nil {
			return // listener closed; shutdown
		}
		go b.handle(c)
	}
}

// handle answers one child. The connection is one request, one bundle, close —
// a child boots once, and a long-lived session would only create a thing to
// hijack.
func (b *broker) handle(c *net.UnixConn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(dialTimeout))

	// The kernel first, for the one thing the kernel can actually answer: is this
	// peer one of MY OWN processes. peerPID fails on any other uid. The pid it
	// returns is for the audit line and nothing else — see peer_linux.go for why
	// nothing about the process itself may name the app.
	pid, err := peerPID(c)
	if err != nil {
		b.log.Warn("credz REFUSED: no peer identity", "err", err)
		return
	}

	r := bufio.NewReader(io.LimitReader(c, maxRequest))
	line, err := r.ReadString('\n')
	if err != nil || line != hello {
		b.log.Warn("credz REFUSED: bad protocol", "pid", pid)
		return
	}
	tok, err := r.ReadString('\n')
	if err != nil {
		b.log.Warn("credz REFUSED: no launch token", "pid", pid, "err", err)
		return
	}

	// THE identity check. The token opens only under the secret this process
	// minted (or, in the host topology, was handed as the broker), so what comes
	// back is the name the LAUNCHER stamped — never a name the peer chose. A
	// child that was started without a stamp, or that wrote its own, gets ""
	// here, and "" is not an app.
	app := launch.Open(b.secret, strings.TrimSuffix(tok, "\n"))
	if app == "" {
		b.log.Warn("credz REFUSED: launch token does not verify", "pid", pid)
		return
	}
	// Defence in depth, and it costs one map lookup. launch.Open already proves
	// the launcher issued this name, so reaching here means the launcher stamped
	// something the manifest does not list — a real disagreement between the two,
	// worth refusing loudly rather than turning into a store path.
	if !appNames[app] {
		b.log.Warn("credz REFUSED: launcher stamped an app the manifest does not list", "app", app, "pid", pid)
		return
	}

	env, err := b.collect(app)
	if err != nil {
		b.log.Error("credz FAILED: reading scope", "app", app, "err", err)
		return
	}
	if err := json.NewEncoder(c).Encode(bundle{Key: KeyB64(), Env: env}); err != nil {
		b.log.Warn("credz FAILED: writing bundle", "app", app, "err", err)
		return
	}
	// Names, never values: this line is the audit trail for who holds what, and it
	// has to be safe to leave on in production.
	b.log.Info("credz GRANTED", "app", app, "pid", pid, "secrets", len(env), "names", names(env))
}

// collect reads the app's scope, shared first so a name filed under the app
// itself wins. A path with nothing in it is not an error — most apps need no
// credential at all, and the empty bundle they get still carries the data-plane
// key, which is what makes them boot.
func (b *broker) collect(app string) (map[string]string, error) {
	env := map[string]string{}
	for _, p := range Scope(b.org, app) {
		names, err := b.src.Names(p, secretEnv)
		if err != nil {
			return nil, err
		}
		for _, n := range names {
			v, err := b.src.Get(p, n, secretEnv)
			if err != nil {
				// One unreadable secret must not deny the whole bundle: the app
				// would then fail to boot for a credential it may not even use.
				b.log.Warn("credz: skipping unreadable secret", "app", app, "path", p, "name", n, "err", err)
				continue
			}
			env[n] = string(v)
		}
	}
	return env, nil
}

func names(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k := range env {
		out = append(out, k)
	}
	return out
}

// errNoPeerCreds is returned where the kernel cannot name the peer. It is fatal
// to a grant by design — an unidentified peer gets nothing.
var errNoPeerCreds = errors.New("credz: peer credentials unavailable on this platform")
