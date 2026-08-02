package credz

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/credz/launch"
)

// helperEnv makes this test binary run the CLIENT half of the protocol when set.
// A grant cannot be proven in-process: the broker takes the peer's uid from the
// kernel and refuses a connection it cannot place, so proving the boundary needs
// a real child process on a real socket. The child is symlinked to <app> as
// well — not because that decides anything any more, but because a test that
// stopped producing forgeable argv could no longer prove argv is ignored.
const helperEnv = "CREDZ_TEST_PULL_FROM"

// resolveEnv makes this test binary resolve a posture and print it. cek's master
// key is process-global and has no reset, so any test that installs one poisons
// every later posture check in the same process. A fresh process is the only
// honest way to ask "what does a cloud binary do at boot", which is the question.
const resolveEnv = "CREDZ_TEST_RESOLVE_IN"

func TestMain(m *testing.M) {
	if sock := os.Getenv(helperEnv); sock != "" {
		// token() is the real read-and-scrub the boot path uses, so what this
		// helper presents is what a plugin child presents.
		b, err := pull(sock, token())
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(3)
		}
		_ = json.NewEncoder(os.Stdout).Encode(b)
		os.Exit(0)
	}
	if dir := os.Getenv(resolveEnv); dir != "" {
		p := resolve(dir)
		fmt.Printf("%s\t%s\t%v\n", p, os.Getenv(RootEnv), Err())
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// resolveIn boots a FRESH process against dir and returns (posture, whatever
// RootEnv still holds afterwards).
func resolveIn(t *testing.T, dir string, env ...string) (Posture, string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self)
	cmd.Env = append(append(os.Environ(), resolveEnv+"="+dir), env...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("resolve helper: %v", err)
	}
	f := strings.SplitN(strings.TrimRight(string(out), "\n"), "\t", 3)
	t.Logf("resolve(%s) → posture=%s %s=%q err=%s", dir, f[0], RootEnv, f[1], f[2])
	return Posture(f[0]), f[1]
}

// fakeSource is the store, spelled as a map keyed "path/name".
type fakeSource map[string]string

func (f fakeSource) Names(path, _ string) ([]string, error) {
	var out []string
	for k := range f {
		if p, n, ok := split(k); ok && p == path {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (f fakeSource) Get(path, name, _ string) ([]byte, error) {
	v, ok := f[path+"/"+name]
	if !ok {
		return nil, fmt.Errorf("no such secret %s/%s", path, name)
	}
	return []byte(v), nil
}

func split(k string) (path, name string, ok bool) {
	i := strings.LastIndex(k, "/")
	if i < 0 {
		return "", "", false
	}
	return k[:i], k[i+1:], true
}

type testLog struct{ t *testing.T }

func (l testLog) Info(m string, kv ...interface{})  { l.t.Logf("INFO  %s %v", m, kv) }
func (l testLog) Warn(m string, kv ...interface{})  { l.t.Logf("WARN  %s %v", m, kv) }
func (l testLog) Error(m string, kv ...interface{}) { l.t.Logf("ERROR %s %v", m, kv) }

// store holds one secret for two different apps plus one for everybody, which is
// the only shape that can distinguish "scoped" from "handed the whole store".
func store() fakeSource {
	return fakeSource{
		"/orgs/admin/svc/_shared/IAM_URL":      "http://iam.hanzo.svc",
		"/orgs/admin/svc/ai/CLOUD_AI_API_KEY":  "sk-ai-secret",
		"/orgs/admin/svc/billing/STRIPE_KEY":   "sk-billing-secret",
		"/orgs/admin/svc/ai/CLOUD_AI_BASE_URL": "http://ai.hanzo.svc",
	}
}

// serveTest starts a broker for a test, forcing the Root posture that Publish
// requires. Boot's sync.Once is bypassed deliberately: the postures are resolved
// from the real environment, and this is testing the broker, not the resolution.
func serveTest(t *testing.T, src Source) string {
	t.Helper()
	root = make([]byte, 32)
	t.Cleanup(func() { root = nil })

	dir := t.TempDir()
	c, err := Publish(Root, src, dir, "admin", testLog{t})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if c == nil {
		t.Fatal("Publish returned no broker for a Root process with a Source")
	}
	t.Cleanup(func() { _ = c.Close() })
	return filepath.Join(dir, SockName)
}

// child runs the client half in a real process whose argv[0] is argv and whose
// environment carries env, and returns the bundle it received. The two things a
// peer can control — what it is called and what it presents — are separate
// parameters here, which is the only way to ask which of them the broker acts on.
func child(t *testing.T, argv, sock string, env ...string) (bundle, error) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), argv)
	if err := os.Symlink(self, bin); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin)
	cmd.Env = append(append(os.Environ(), helperEnv+"="+sock), env...)
	out, err := cmd.Output()
	if err != nil {
		return bundle{}, err
	}
	var b bundle
	if err := json.Unmarshal(out, &b); err != nil {
		t.Fatalf("helper output %q: %v", out, err)
	}
	return b, nil
}

// launchAs is the legitimate shape: a child the launcher started as `app` and
// stamped accordingly. LaunchSecret() is the same process-global secret Publish
// gave the broker, which is exactly the fused topology — the launcher and the
// broker are one process.
func launchAs(t *testing.T, app, sock string) (bundle, error) {
	t.Helper()
	return child(t, app, sock, launch.Env(LaunchSecret(), app))
}

// forgeAs is the attack: a child that presents whatever it likes, spelled
// longhand because an attacker does not call our helper. Every call must be
// REFUSED — the helper exits non-zero and the error is the pass condition.
func forgeAs(t *testing.T, argv, tok, sock string) (bundle, error) {
	t.Helper()
	return child(t, argv, sock, launch.TokenEnv+"="+tok)
}

// TestScopeIsTheCallersOwnAndNothingElse is the whole point of the package: the
// app that owns a secret gets it, and the app that does not is not merely
// unauthorized — it is never offered the path, because the path is built from
// the app the LAUNCHER stamped and from nothing the peer said.
func TestScopeIsTheCallersOwnAndNothingElse(t *testing.T) {
	sock := serveTest(t, store())

	ai, err := launchAs(t, "ai", sock)
	if err != nil {
		t.Fatalf("ai pull: %v", err)
	}
	want(t, "ai", ai.Env, map[string]string{
		"IAM_URL":           "http://iam.hanzo.svc",
		"CLOUD_AI_API_KEY":  "sk-ai-secret",
		"CLOUD_AI_BASE_URL": "http://ai.hanzo.svc",
	})

	billing, err := launchAs(t, "billing", sock)
	if err != nil {
		t.Fatalf("billing pull: %v", err)
	}
	want(t, "billing", billing.Env, map[string]string{
		"IAM_URL":    "http://iam.hanzo.svc",
		"STRIPE_KEY": "sk-billing-secret",
	})

	// The named regression: billing must not be able to reach the AI provider key.
	if v, ok := billing.Env["CLOUD_AI_API_KEY"]; ok {
		t.Fatalf("billing was handed the AI provider key: %q", v)
	}
	if _, ok := ai.Env["STRIPE_KEY"]; ok {
		t.Fatal("ai was handed billing's key")
	}
}

// TestEveryAppGetsTheDataPlaneKey — an app with no secrets of its own still has
// stores to open, and a bundle that carries no key is a child that cannot boot.
// That was bug #1.
func TestEveryAppGetsTheDataPlaneKey(t *testing.T) {
	sock := serveTest(t, store())
	b, err := launchAs(t, "dns", sock) // a manifest app with nothing filed for it
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(b.Env) != 1 { // only the shared IAM_URL
		t.Fatalf("dns got %d secrets, want only the shared one: %v", len(b.Env), b.Env)
	}
	k, err := base64.StdEncoding.DecodeString(b.Key)
	if err != nil || len(k) != 32 {
		t.Fatalf("bundle key is not a 32-byte key: %q (%v)", b.Key, err)
	}
}

// TestPeerThatIsNotAnAppGetsNothing — the broker answers apps, and "app" is a
// closed set the manifest defines. This is the defence-in-depth gate, so the
// token here is VALID: the launcher itself is made to stamp a name the manifest
// does not list, and the grant is still refused rather than becoming a store
// path under a scope nobody provisioned.
func TestPeerThatIsNotAnAppGetsNothing(t *testing.T) {
	sock := serveTest(t, store())
	if b, err := launchAs(t, "definitely-not-an-app", sock); err == nil {
		t.Fatalf("an unknown peer was served a bundle: %+v", b)
	}
}

// TestArgvDoesNotDecideScope is the fix for #51, stated as the one experiment
// that can tell the old broker from the new one.
//
// The child is named `billing` — the exact spoof that used to work, and the same
// argv shape manifest.App.Plugin produces for a dedicated binary — while the
// token it presents is the one the launcher stamped for `ai`. If argv were still
// consulted the two would disagree and the answer would be billing's scope (or a
// refusal). It comes back as ai's, whole: argv is not read, and the launcher's
// stamp is the only thing that names an app.
func TestArgvDoesNotDecideScope(t *testing.T) {
	sock := serveTest(t, store())

	b, err := child(t, "billing", sock, launch.Env(LaunchSecret(), "ai"))
	if err != nil {
		t.Fatalf("a child with a valid ai token was refused: %v", err)
	}
	want(t, "ai(argv=billing)", b.Env, map[string]string{
		"IAM_URL":           "http://iam.hanzo.svc",
		"CLOUD_AI_API_KEY":  "sk-ai-secret",
		"CLOUD_AI_BASE_URL": "http://ai.hanzo.svc",
	})
	if v, ok := b.Env["STRIPE_KEY"]; ok {
		t.Fatalf("argv still decides the scope: a process named `billing` got billing's key %q", v)
	}
	t.Logf("argv=billing token=ai → %v (argv ignored)", keysOf(b.Env))
}

// TestForgedIdentityIsRefused is the other half: with argv no longer consulted,
// everything a peer CAN still control has to be worthless. Each case is a
// different way to claim an app without the launcher having said so, and every
// one of them must come back with no bundle at all — not an empty one, and not
// one carrying the data-plane key.
func TestForgedIdentityIsRefused(t *testing.T) {
	sock := serveTest(t, store())
	stolen := launch.Env(LaunchSecret(), "ai")          // the shape a real stamp has
	mac := stolen[len(launch.TokenEnv+"=ai:"):]         // ai's proof, on its own
	elsewhere := launch.Env(launch.Secret(), "billing") // a valid token from another launcher

	for _, tc := range []struct{ name, argv, tok string }{
		// The pre-fix attack: be named after the app, say nothing else.
		{"argv alone, no token", "billing", ""},
		// The other pre-fix attack: the multi-call binary claiming an app.
		{"multi-call argv, no token", "cloud", ""},
		{"a bare claim with no proof", "billing", "billing"},
		{"a hand-written proof", "billing", "billing:" + strings.Repeat("00", 32)},
		{"a proof that is not hex", "billing", "billing:not-hex"},
		// Claim and proof are ONE variable precisely so this cannot be assembled.
		{"ai's proof under billing's name", "billing", "billing:" + mac},
		// A secret this broker never minted signs nothing this broker accepts.
		{"a token from another launcher", "billing", elsewhere[len(launch.TokenEnv+"="):]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, err := forgeAs(t, tc.argv, tc.tok, sock)
			if err == nil {
				t.Fatalf("FORGERY SUCCEEDED: argv=%q token=%q was served %v (key=%q)",
					tc.argv, tc.tok, keysOf(b.Env), b.Key)
			}
			t.Logf("refused: argv=%q token=%q", tc.argv, tc.tok)
		})
	}
}

// TestScopePaths pins the layout an operator provisions against. Shared first so
// an app-specific name overrides the fleet-wide default.
func TestScopePaths(t *testing.T) {
	got := Scope("admin", "ai")
	wantPaths := []string{"/orgs/admin/svc/_shared", "/orgs/admin/svc/ai"}
	if len(got) != 2 || got[0] != wantPaths[0] || got[1] != wantPaths[1] {
		t.Fatalf("Scope = %v, want %v", got, wantPaths)
	}
}

// TestAppSecretOverridesShared — the ordering in Scope has to actually bind.
func TestAppSecretOverridesShared(t *testing.T) {
	sock := serveTest(t, fakeSource{
		"/orgs/admin/svc/_shared/IAM_URL": "shared",
		"/orgs/admin/svc/ai/IAM_URL":      "ai-specific",
	})
	b, err := launchAs(t, "ai", sock)
	if err != nil {
		t.Fatal(err)
	}
	if b.Env["IAM_URL"] != "ai-specific" {
		t.Fatalf("IAM_URL = %q, want the app-specific value", b.Env["IAM_URL"])
	}
}

// TestRootScrubsTheEnvironment is acceptance criterion #5 at the unit level: a
// process that read the root key from its environment must not leave it there,
// because zip hands os.Environ() to every child it spawns.
func TestRootScrubsTheEnvironment(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	p, left := resolveIn(t, t.TempDir(), RootEnv+"="+key)
	if p != Root {
		t.Fatalf("posture = %q, want %q", p, Root)
	}
	if left != "" {
		t.Fatalf("%s survived Boot: %q — every spawned child would inherit it", RootEnv, left)
	}
}

// TestNoKeyNoBrokerIsTheDevPath — `make host` with nothing provisioned has to
// work, and it has to work through the SAME encrypted path as production rather
// than a divergent plaintext one.
func TestNoKeyNoBrokerIsTheDevPath(t *testing.T) {
	p, _ := resolveIn(t, filepath.Join(t.TempDir(), "no-broker-here"), RootEnv+"=")
	if p != Dev {
		t.Fatalf("posture = %q, want %q (this build has no live codec linked)", p, Dev)
	}
}

// TestMalformedKeyIsNotAKey — a wrong-length or unparseable key must not be
// installed. It would encrypt against a store no other key can open.
func TestMalformedKeyIsNotAKey(t *testing.T) {
	for _, bad := range []string{"not-base64!!", base64.StdEncoding.EncodeToString(make([]byte, 16))} {
		if _, ok := decode(bad); ok {
			t.Fatalf("decode(%q) accepted a bad key", bad)
		}
	}
}

func want(t *testing.T, app string, got, exp map[string]string) {
	t.Helper()
	if len(got) != len(exp) {
		t.Fatalf("%s got %d secrets %v, want %d %v", app, len(got), keysOf(got), len(exp), keysOf(exp))
	}
	for k, v := range exp {
		if got[k] != v {
			t.Fatalf("%s[%s] = %q, want %q", app, k, got[k], v)
		}
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// A random master is honest over an EMPTY data directory and catastrophic over a
// populated one: every file was encrypted under a key the new master replaces, so
// each opens as "file is not a database" while the data sits intact and
// unreadable. This is the same mistake the launched-with-a-token branch already
// refuses, and worse for the same reason — it succeeds.
func TestDevMasterIsRefusedOverExistingDatabases(t *testing.T) {
	dir := t.TempDir()
	if had, err := hasDatabases(dir); err != nil || had {
		t.Fatalf("an empty dir holds no databases: had=%v err=%v", had, err)
	}

	// One database, nested the way namespace.Path lays them out (<dir>/<kind>/<name>/<subsystem>.db).
	nested := filepath.Join(dir, "org", "hanzo")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "agents.db"), []byte("not really sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	had, err := hasDatabases(dir)
	if err != nil {
		t.Fatalf("hasDatabases: %v", err)
	}
	if !had {
		t.Fatal("a directory holding agents.db must report that it does — minting a new master over it makes every file unreadable")
	}
}

// A directory that never existed is the clearest possible "nothing preceded this
// process", and must not be mistaken for one we failed to read.
func TestMissingDataDirIsNotAnError(t *testing.T) {
	had, err := hasDatabases(filepath.Join(t.TempDir(), "never-created"))
	if err != nil {
		t.Fatalf("a missing dir is not an error: %v", err)
	}
	if had {
		t.Fatal("a missing dir holds no databases")
	}
	// And neither is an unset one.
	if had, err := hasDatabases(""); err != nil || had {
		t.Fatalf("an unset dir holds no databases: had=%v err=%v", had, err)
	}
}
