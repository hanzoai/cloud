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
)

// helperEnv makes this test binary run the CLIENT half of the protocol when set.
// A grant cannot be proven in-process: the broker identifies its peer from the
// kernel's record of that peer's argv, so proving the scope boundary needs a real
// process really named after an app. Symlinking the test binary to <app> gives
// exactly that, and it is the same argv shape manifest.App.Plugin produces.
const helperEnv = "CREDZ_TEST_PULL_FROM"

// resolveEnv makes this test binary resolve a posture and print it. cek's master
// key is process-global and has no reset, so any test that installs one poisons
// every later posture check in the same process. A fresh process is the only
// honest way to ask "what does a cloud binary do at boot", which is the question.
const resolveEnv = "CREDZ_TEST_RESOLVE_IN"

func TestMain(m *testing.M) {
	if sock := os.Getenv(helperEnv); sock != "" {
		b, err := pull(sock)
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

// pullAs runs the client half from a process the kernel will report as `app`,
// and returns the bundle it received.
func pullAs(t *testing.T, app, sock string) (bundle, error) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), app)
	if err := os.Symlink(self, bin); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), helperEnv+"="+sock)
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

// TestScopeIsTheCallersOwnAndNothingElse is the whole point of the package: the
// app that owns a secret gets it, and the app that does not is not merely
// unauthorized — it is never offered the path, because the path is built from
// who the kernel says it is.
func TestScopeIsTheCallersOwnAndNothingElse(t *testing.T) {
	sock := serveTest(t, store())

	ai, err := pullAs(t, "ai", sock)
	if err != nil {
		t.Fatalf("ai pull: %v", err)
	}
	want(t, "ai", ai.Env, map[string]string{
		"IAM_URL":           "http://iam.hanzo.svc",
		"CLOUD_AI_API_KEY":  "sk-ai-secret",
		"CLOUD_AI_BASE_URL": "http://ai.hanzo.svc",
	})

	billing, err := pullAs(t, "billing", sock)
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
	b, err := pullAs(t, "dns", sock) // a manifest app with nothing filed for it
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
// closed set the manifest defines. A process that is not one gets no bundle at
// all, not an empty one.
func TestPeerThatIsNotAnAppGetsNothing(t *testing.T) {
	sock := serveTest(t, store())
	if b, err := pullAs(t, "definitely-not-an-app", sock); err == nil {
		t.Fatalf("an unknown peer was served a bundle: %+v", b)
	}
}

// TestMultiCallPeerIsTheAppItEnables covers the other spawn shape: one binary
// named `cloud`, told which app to be.
func TestAppOf(t *testing.T) {
	for _, tc := range []struct {
		name string
		argv []string
		want string
		err  bool
	}{
		{"dedicated binary", []string{"/opt/hanzo/ai"}, "ai", false},
		{"multi-call", []string{"/opt/hanzo/cloud", "--enable=billing"}, "billing", false},
		{"multi-call single dash", []string{"/opt/hanzo/cloud", "-enable=dns"}, "dns", false},
		{"multi-call with other flags", []string{"/opt/hanzo/cloud", "-addr=:8080", "--enable=kms"}, "kms", false},
		// Two apps in one process would need two scopes, and merging them is how
		// one plugin quietly acquires another's credentials.
		{"multi-call two apps", []string{"/opt/hanzo/cloud", "--enable=ai,billing"}, "", true},
		{"multi-call no app", []string{"/opt/hanzo/cloud"}, "", true},
		{"not an app", []string{"/usr/bin/curl"}, "", true},
		{"unknown enable", []string{"/opt/hanzo/cloud", "--enable=nope"}, "", true},
		{"empty", nil, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := appOf(tc.argv)
			if tc.err {
				if err == nil {
					t.Fatalf("appOf(%v) = %q, want an error", tc.argv, got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("appOf(%v) = (%q, %v), want %q", tc.argv, got, err, tc.want)
			}
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
	b, err := pullAs(t, "ai", sock)
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
