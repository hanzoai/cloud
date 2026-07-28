// Package credz is how a cloud process gets its credentials.
//
// THE PROBLEM. Every subsystem reads its secrets from the process environment —
// CLOUD_AI_API_KEY, IAM_CLIENT_SECRET, DO_API_TOKEN, driverName, ~50 names across
// 108 apps. When those apps ran as one binary that was one environment to fill.
// Now they are child processes: zip spawns each with `os.Environ() + ZIP_ADDR`,
// so whatever the launcher holds, EVERY child holds — all 50 secrets in all 108
// processes, readable in each one's /proc/<pid>/environ and inherited by anything
// any of them execs. The alternative that was actually happening is worse: nothing
// was filled in at all, so a lazily-spawned child booted with no data-plane key
// and no provider credential and failed or served 503.
//
// THE SHAPE OF THE ANSWER. One process holds the root key. Every other process
// asks it, over a unix socket, for the credentials of the app it is — and gets
// only those. Nothing secret is passed at spawn, so nothing secret is at rest in
// any child's environment.
//
// WHY THE ENVIRONMENT IS STILL THE INTERFACE. The bundle is installed with
// os.Setenv, so all 108 apps keep reading os.Getenv and not one of them changes.
// That is the DRY choice AND the secure one: a value set after execve never
// appears in /proc/<pid>/environ, which is the kernel's snapshot of the argument
// page as it was passed. Same interface, none of the exposure.
//
// WHERE IDENTITY COMES FROM. The launcher, and only the launcher. It stamps a
// per-child token into zip.Plugin.Env at spawn; the child presents it here; the
// broker verifies it against the secret it holds (credz/launch). Nothing the
// peer chose about itself decides its scope.
//
// It used to. The broker took SO_PEERCRED's pid and read the peer's argv out of
// /proc — but execve takes argv from the caller, so any same-uid process could
// exec itself as `billing` and be handed billing's scope, and the broker logged
// it as a legitimate grant. SO_PEERCRED remains, doing the two things it can
// actually do: the uid check that says the peer is one of this deployment's own
// processes, and the pid in the audit line.
//
// THE HONEST LIMIT. The token is in the child's environment, which the same uid
// can read at /proc/<pid>/environ, and every plugin in the pod IS that uid. So
// the cost of impersonating an app went from nothing to "first steal a live
// peer's token", and a stolen token buys exactly the one app it was stolen from.
// That is a real boundary against accident and against casual forgery; it is NOT
// a boundary against a peer that reads its neighbours. Making it one means the
// socket becomes the credential — the launcher pre-connects and passes the fd as
// an ExtraFile, which nothing can name or copy — and that is a change to zip's
// spawn contract, not to this package. See credz/launch for the full statement.
//
// THE THREE POSTURES, resolved once at Boot:
//
//	Root   — CLOUD_KMS_MASTER_KEY_REF was in MY environment. I hold the root key,
//	         I scrub it so my children do not inherit it, and (if I also own the
//	         secret store) I serve the broker.
//	Leaf   — no root key; I pulled my bundle from the broker.
//	Dev    — no root key and no broker, on a build that cannot be production
//	         (the live SQLCipher codec is not linked). Deterministic dev key, zero
//	         configuration: `make host` works with nothing provisioned.
//
// A production build with no key and no broker resolves to none of these and
// fails closed at the first store open, exactly as it did before.
//
// ORDERING IS THE WHOLE BUG. cek memoizes the master key on first use, so the key
// must be installed before the first store opens — which is inside BuildDeps, not
// after it. Boot is sync.Once-guarded and called from the two entry points that
// precede every open (Serve, BuildDeps); calling it twice is free and calling it
// late is impossible.
package credz

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/cek"
	"github.com/hanzoai/cloud/credz/launch"
)

// RootEnv is the ONE variable a production deployment provisions. It carries the
// base64 32-byte key that both unseals the KMS secret store and encrypts the cek
// data plane; every other secret lives inside that store. cek names the same
// variable — one key, one name, no second gate. It is defined on the launch leaf
// (credz/launch) because the light host, which cannot import credz, scrubs this
// same name from its children; there is one spelling of it, and it is there.
const RootEnv = launch.RootEnv

// SockName is the broker's socket, inside the data directory. That directory is
// already the deployment's private state (RWO volume, 0700), so the filesystem is
// the ACL — the same rule zip uses for the plugin sockets themselves.
const SockName = "credz.sock"

// secretEnv is the KMS environment slug service credentials are filed under. It
// is the store's own default: separating prod from dev is what separate
// deployments are for, not a second axis inside one store.
const secretEnv = "default"

// dialTimeout bounds the broker handshake. A child blocks on this before it can
// serve anything, and the broker is a local unix socket doing a handful of store
// reads, so a slow answer is a broken broker rather than a busy one — fail fast
// and let the posture fall through rather than hang a lazy spawn past zip's
// 10s start budget.
const dialTimeout = 3 * time.Second

// Posture is how this process resolved its credentials. It exists to be logged:
// the failure this package was written to fix announced a dev key while none was
// installed, so the resolved posture is now a value, not a guess.
type Posture string

const (
	Root    Posture = "root"    // root key from my own environment
	Leaf    Posture = "leaf"    // bundle pulled from the broker
	Dev     Posture = "dev"     // deterministic dev key; no key and no broker
	Unkeyed Posture = "unkeyed" // production build, nothing configured — opens fail closed
)

var (
	once     sync.Once
	posture  Posture
	root     []byte // the 32-byte root key, in memory only
	loaded   int    // how many scoped names the bundle installed
	bootErr  error  // why a broker pull failed, for the boot log; never fatal
	bootFrom string // where the bundle came from, for the boot log

	secretOnce sync.Once
	secret     string // the launcher's token-signing secret; see LaunchSecret
)

// LaunchSecret is the key this process signs its children's identities with, and
// the key its broker verifies them against. One value for both because in the
// fused topology they are one process: /cloud holds the root key, serves the
// broker, AND spawns the plugin children whose tokens it will later be asked to
// open. Two values there would be two ways to spell one fact, and the second one
// would be the bug.
//
// It is adopted from the environment when it is there and minted when it is not,
// which is the difference between the two topologies rather than a mode:
//
//   - Fused (/cloud) — nothing to adopt, so it is minted here and never leaves
//     the process. Children get tokens; nothing gets the secret.
//   - Host (cmd/cloud) — the launcher is the host and the broker is a child, so
//     the secret has to cross that one edge. cmd/cloud mints it and hands it to
//     the broker child ALONE, which adopts it here.
//
// Scrubbed on adoption for the same reason RootEnv is: a process holding this
// can mint any app's identity, and leaving it in the environment hands that
// power to anything the broker ever execs.
func LaunchSecret() string {
	secretOnce.Do(func() {
		if s := os.Getenv(launch.SecretEnv); s != "" {
			secret = s
			_ = os.Unsetenv(launch.SecretEnv)
			return
		}
		secret = launch.Secret()
	})
	return secret
}

// Boot resolves this process's credentials and installs them: the root key into
// cek, the scoped service credentials into the environment. It runs exactly once
// and must run before the first store opens.
//
// It never fails. Every posture that cannot serve is expressed as the posture
// itself plus a reason in Err(), because the honest failure is the first store
// open refusing to run unencrypted — not a boot that dies before the logger is
// up and takes the reason with it.
func Boot(dataDir string) Posture {
	once.Do(func() { posture = resolve(dataDir) })
	return posture
}

// Resolved reports the posture, how many scoped credentials were installed, and
// where they came from. Callers log it; nothing branches on it.
func Resolved() (Posture, int, string) { return posture, loaded, bootFrom }

// Err reports why a broker pull failed, or nil. A Leaf that could not reach the
// broker degrades to Dev or Unkeyed rather than dying, so this is the only place
// the reason survives.
func Err() error { return bootErr }

// Key returns the root key this process holds, or nil. It is the ONE accessor:
// the environment variable is scrubbed at Boot so a child never inherits it, and
// everything downstream that needs the key (the embedded KMS store, cek) reads it
// from here instead of re-reading an environment that is deliberately empty.
func Key() []byte { return root }

// KeyB64 is Key in the base64 form cloud.Config and the embedded KMS client
// carry. Empty when no key is held.
func KeyB64() string {
	if len(root) == 0 {
		return ""
	}
	return base64.StdEncoding.EncodeToString(root)
}

// resolve is Boot's body, split out so it is testable without the sync.Once.
func resolve(dataDir string) Posture {
	// The launcher's stamp, taken before anything branches: whichever posture
	// this process turns out to be, the token has served its purpose the moment
	// it is read, and a token left in the environment is one more thing a child
	// of THIS process could present as its own.
	tok := token()

	// 1. My own environment. This process is the root of the credential tree.
	if k, ok := decode(os.Getenv(RootEnv)); ok {
		root = k
		cek.SetMasterKey(k)
		// Scrub. zip spawns children with os.Environ(), so leaving the root key
		// here hands it to all 108 of them — the exact sprawl this package exists
		// to end. Everything in-process that needs it now reads Key().
		_ = os.Unsetenv(RootEnv)
		bootFrom = RootEnv
		return Root
	}

	// 2. The broker. No key of my own, so I am a child: ask the process that has
	// one for the credentials of the app I am.
	sock := filepath.Join(dataDir, SockName)
	b, err := pull(sock, tok)
	if err == nil {
		if k, ok := decode(b.Key); ok {
			root = k
			cek.SetMasterKey(k)
		} else {
			// A bundle with no data-plane key still installs the service credentials,
			// but the first store open will fail closed — and the reason lives HERE,
			// at the broker, not in the cek error thirty frames later. Saying nothing
			// is how this package's predecessor bug worked.
			bootErr = fmt.Errorf("credz: broker at %s sent no data-plane key; store opens will fail closed", sock)
		}
		loaded = install(b.Env)
		bootFrom = sock
		return Leaf
	}
	bootErr = err

	// 3. Nothing configured. On a build that cannot be production, run the
	// production code path against a well-known key so a developer needs no
	// configuration at all; on a production build, hold nothing and let the first
	// store open refuse to write plaintext.
	if cek.EnsureDevKey() {
		// Hold the dev key like any other: a keyless dev deployment still has ONE
		// data-plane key, and its broker has to be able to hand that same key to a
		// child rather than let each child derive its own.
		root = cek.Master()
		bootFrom = "dev key"
		return Dev
	}
	return Unkeyed
}

// bundle is what the broker hands one app: the data-plane key it needs to open
// any store, and the service credentials filed under its own scope.
type bundle struct {
	Key string            `json:"key"` // base64 32-byte root key
	Env map[string]string `json:"env"` // secret name → value, already scoped
}

// install writes the bundle into the process environment. This is what lets 108
// subsystems keep reading os.Getenv unchanged — and, because setenv after execve
// does not touch the kernel's argument page, what keeps every one of those values
// out of /proc/<pid>/environ.
func install(env map[string]string) int {
	n := 0
	for k, v := range env {
		if k == RootEnv {
			continue // the root key travels in Key and is never an env var here
		}
		if os.Setenv(k, v) == nil {
			n++
		}
	}
	return n
}

// token takes the launcher's stamp out of the environment, and takes it OUT:
// single-use, exactly as RootEnv is scrubbed and for the same reason. This
// process asks the broker once, at boot, so the token has no second use — and
// anything this process execs afterwards inherits an environment that cannot
// answer for it.
func token() string {
	t := os.Getenv(launch.TokenEnv)
	_ = os.Unsetenv(launch.TokenEnv)
	return t
}

// pull performs the client half of the handshake: connect, announce the protocol
// version, present the launcher's stamp, read the bundle.
//
// The stamp is the whole request body. A child says nothing else about itself —
// no name, no path, no flag — because the only identity the broker accepts is
// one it can verify it issued, and giving a child a second field to fill in is
// giving it something to argue with.
func pull(sock, tok string) (bundle, error) {
	var b bundle
	c, err := net.DialTimeout("unix", sock, dialTimeout)
	if err != nil {
		return b, fmt.Errorf("credz: broker at %s: %w", sock, err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(dialTimeout))
	if _, err := c.Write([]byte(hello + tok + "\n")); err != nil {
		return b, fmt.Errorf("credz: hello: %w", err)
	}
	if err := json.NewDecoder(c).Decode(&b); err != nil {
		return b, fmt.Errorf("credz: read bundle: %w", err)
	}
	return b, nil
}

// hello is the protocol version. Bumped to 2 when the request grew its second
// line: a v1 child sends no token and would hang until the broker's deadline, so
// the version says which frame shape is on the wire and a mismatched pair fails
// at the greeting instead of somewhere less obvious.
const hello = "credz/2\n"

// decode parses the base64 root key and enforces its length. A malformed key is
// reported as "no key" so the caller falls through to the next posture rather
// than installing something that would fail obscurely at the first store open.
func decode(s string) ([]byte, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, false
	}
	k, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(k) != 32 {
		return nil, false
	}
	return k, true
}
