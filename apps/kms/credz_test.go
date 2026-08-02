package kms

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/credz"
	"github.com/hanzoai/cloud/credz/launch"
	luxlog "github.com/luxfi/log"
)

// This is the contract between the sealed store and the credential broker, and
// it is deliberately end-to-end: a REAL master key, a REAL AES-256-GCM seal on
// disk, a REAL unix socket, and REAL child processes whose identity comes from
// the kernel. The unit tests in credz prove the protocol against a fake store;
// this proves the store the fleet actually runs is the thing behind it.

// bootInEnv makes this test binary boot as a plugin child against a data dir.
const bootInEnv = "KMS_CREDZ_TEST_BOOT_IN"

// testMaster is the deployment's one credential, for this test's deployment. The
// same 32 bytes seal the store and encrypt the data plane, which is the point:
// one key, and the broker is what turns it into per-app scopes.
var testMaster = func() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i + 1)
	}
	return k
}()

// childView is what a booted child reports about itself: the posture it
// resolved, the environment its subsystems will read through os.Getenv, and the
// kernel's own record of the environment it was execve'd with. The two
// environments differing is the security property — see TestBrokerServesTheSealedStoreScoped.
type childView struct {
	Posture string            `json:"posture"`
	Getenv  map[string]string `json:"getenv"`
	Proc    string            `json:"proc"`
}

// TestMain gives this package the plugin-child half. The child runs the REAL
// boot path — credz.Boot, exactly as cloud.Listen calls it — rather than a
// test-only accessor, so what this proves is what production does. A child is
// identified by the token its LAUNCHER stamped on it, which is why it has to be
// a real process this test really started (see bootAs).
func TestMain(m *testing.M) {
	// The PARENT is the broker, so it boots the way the live broker does: the root
	// key in its own environment, taken by credz.Boot into cek before any store
	// opens. Here rather than in the test body because other tests in this package
	// open stores too, and a store opened before the master is installed fails —
	// TestMain is the only point guaranteed to be first.
	if os.Getenv(bootInEnv) == "" {
		_ = os.Setenv(credz.RootEnv, base64.StdEncoding.EncodeToString(testMaster))
		credz.Boot(os.TempDir())
	}
	if dir := os.Getenv(bootInEnv); dir != "" {
		p := credz.Boot(dir)
		v := childView{Posture: string(p), Getenv: map[string]string{}}
		for _, kv := range os.Environ() {
			if k, val, ok := strings.Cut(kv, "="); ok {
				v.Getenv[k] = val
			}
		}
		if b, err := os.ReadFile("/proc/self/environ"); err == nil {
			v.Proc = string(b)
		}
		_ = json.NewEncoder(os.Stdout).Encode(v)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// TestEmbeddedClientIsACredzSource pins the interface. It is a compile-time fact
// worth asserting at run time too: Publish silently declines a Source it does not
// recognise, so a signature drift here would disable the broker rather than break
// the build.
func TestEmbeddedClientIsACredzSource(t *testing.T) {
	var c *Client
	if _, ok := any(c).(credz.Source); !ok {
		t.Fatal("*kms.Client no longer satisfies credz.Source — the broker would silently stop starting")
	}
}

// TestBrokerServesTheSealedStoreScoped is the acceptance test for the whole
// mechanism: the app that owns a secret reads it out of the sealed store through
// the broker, and the app that does not is refused — not by an ACL it could argue
// with, but because the store path is built from the app its launcher stamped on
// it, which it cannot choose.
func TestBrokerServesTheSealedStoreScoped(t *testing.T) {
	dir := t.TempDir()
	c, err := New(Config{DataDir: dir, MasterKeyB64: base64.StdEncoding.EncodeToString(testMaster)}, luxlog.New("test"))
	if err != nil {
		t.Fatalf("kms.New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if !c.Ready() {
		t.Fatal("kms client not ready with a valid master key")
	}

	// Provision exactly as an operator would through
	// POST /v1/kms/orgs/admin/secrets — the path IS the scope, the name IS the
	// environment variable the app already reads.
	for _, s := range []struct{ path, name, value string }{
		{"/orgs/admin/svc/_shared", "IAM_URL", "http://iam.hanzo.svc"},
		{"/orgs/admin/svc/ai", "CLOUD_AI_API_KEY", "sk-live-ai-provider-key"},
		{"/orgs/admin/svc/ai", "driverName", "sqlite3"},
		{"/orgs/admin/svc/billing", "COMMERCE_SERVICE_TOKEN", "svc-billing-token"},
	} {
		if err := c.Put(s.path, s.name, "default", []byte(s.value)); err != nil {
			t.Fatalf("put %s/%s: %v", s.path, s.name, err)
		}
	}

	// Nothing readable is on disk: the store is sealed, which is why the broker
	// (which holds the key) has to be the one that reads it.
	assertNoPlaintextOnDisk(t, dir, "sk-live-ai-provider-key")

	publishFor(t, c, dir)

	ai := bootAs(t, "ai", dir)
	if ai.Posture != string(credz.Leaf) {
		t.Fatalf("ai posture = %q, want leaf", ai.Posture)
	}
	if ai.Getenv["CLOUD_AI_API_KEY"] != "sk-live-ai-provider-key" {
		t.Fatal("ai did not receive its provider key")
	}
	if ai.Getenv["driverName"] != "sqlite3" {
		t.Fatal("ai did not receive driverName — the exact gate that made /v1/chat/completions 503")
	}
	if ai.Getenv["IAM_URL"] != "http://iam.hanzo.svc" {
		t.Fatal("ai did not receive the shared scope")
	}
	if v, ok := ai.Getenv["COMMERCE_SERVICE_TOKEN"]; ok {
		t.Fatalf("ai was handed billing's service token: %q", v)
	}

	// The child that owns nothing here still must not be holding the root key in
	// the environment it was started with — that is the sprawl this ends.
	if strings.Contains(ai.Proc, credz.RootEnv+"=") {
		t.Fatalf("%s is in the child's /proc/self/environ", credz.RootEnv)
	}
	// ...and the credentials it DID receive are not there either, because they
	// arrived after execve. Same interface (os.Getenv), none of the exposure.
	if strings.Contains(ai.Proc, "sk-live-ai-provider-key") {
		t.Fatal("the provider key is readable in the child's /proc/self/environ")
	}
	// The launch token is single-use: Boot reads it and takes it out, so anything
	// this child execs inherits an environment that cannot answer for it. It is
	// still in the child's /proc/self/environ — that is the honest limit stated in
	// credz/launch, and it is why this asserts the scrub and not secrecy.
	if v, ok := ai.Getenv[launch.TokenEnv]; ok {
		t.Fatalf("%s survived Boot as %q — every process this child starts could present it", launch.TokenEnv, v)
	}

	billing := bootAs(t, "billing", dir)
	if billing.Getenv["COMMERCE_SERVICE_TOKEN"] != "svc-billing-token" {
		t.Fatal("billing did not receive its own token")
	}
	if v, ok := billing.Getenv["CLOUD_AI_API_KEY"]; ok {
		t.Fatalf("REFUSAL FAILED: billing read the AI provider key: %q", v)
	}

	// An app that is not in the manifest is refused outright: no bundle, and
	// therefore no data-plane key either.
	stranger := bootAs(t, "definitely-not-an-app", dir)
	if stranger.Posture == string(credz.Leaf) {
		t.Fatal("a peer that is not a manifest app was served a bundle")
	}

	t.Logf("ai       posture=%s CLOUD_AI_API_KEY=%q driverName=%q COMMERCE_SERVICE_TOKEN=%q",
		ai.Posture, ai.Getenv["CLOUD_AI_API_KEY"], ai.Getenv["driverName"], ai.Getenv["COMMERCE_SERVICE_TOKEN"])
	t.Logf("billing  posture=%s COMMERCE_SERVICE_TOKEN=%q CLOUD_AI_API_KEY=%q",
		billing.Posture, billing.Getenv["COMMERCE_SERVICE_TOKEN"], billing.Getenv["CLOUD_AI_API_KEY"])
	t.Logf("stranger posture=%s (refused)", stranger.Posture)
}

// publishFor starts the broker over the real sealed store. Posture is a
// parameter to Publish precisely so this does not have to fake process-global
// state: the fact under test is "a Root process owning the store brokers it".
func publishFor(t *testing.T, c *Client, dir string) {
	t.Helper()
	closer, err := credz.Publish(credz.Root, c, dir, "admin", luxlog.New("test"))
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if closer == nil {
		t.Fatal("no broker started for a Root process owning the store")
	}
	t.Cleanup(func() { _ = closer.Close() })
}

// bootAs boots a child THIS PROCESS LAUNCHED as `app`, through the real
// credz.Boot, and returns what that child can see.
//
// The stamp is what makes it that app — credz.LaunchSecret() is the same secret
// this process handed its broker in publishFor, so this test binary is playing
// the launcher exactly as cloud.PluginSpec and cmd/cloud do. Without it every
// child is refused, which is the correct failure and the reason this line is not
// optional. The binary is still symlinked to <app> so a regression that started
// reading argv again would be visible rather than harmless.
func bootAs(t *testing.T, app, dir string) childView {
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
	// No root key and no credentials in the child's environment: everything it
	// ends up with, it got from the broker. This is the spawn shape zip produces
	// minus the one variable a launcher must no longer be carrying, plus the one
	// a launcher must now be stamping.
	cmd.Env = append(scrubbed(os.Environ()), bootInEnv+"="+dir, launch.Env(credz.LaunchSecret(), app))
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s boot: %v", app, err)
	}
	var v childView
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("%s output %q: %v", app, out, err)
	}
	return v
}

func scrubbed(env []string) []string {
	out := env[:0:0]
	for _, kv := range env {
		if strings.HasPrefix(kv, credz.RootEnv+"=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// assertNoPlaintextOnDisk is the reason the broker exists at all: the store is
// sealed, so "read the file" is not a way around the scope check.
func assertNoPlaintextOnDisk(t *testing.T, dir, secret string) {
	t.Helper()
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr == nil && strings.Contains(string(b), secret) {
			t.Fatalf("secret plaintext found at rest in %s", p)
		}
		return nil
	})
}
