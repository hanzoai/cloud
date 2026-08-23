package platform

import (
	"context"
	"fmt"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
)

// pinFixture mirrors the real charts/app/values/hanzo/cloud.yaml shape: a top-level
// image block whose tag scalar sits BELOW an indented comment block (the reasoning
// for every pin that ever moved), a blank line inside the block, and other
// top-level keys either side. Those are exactly the features a YAML round-trip
// would reflow away.
const pinFixture = `chart: app
replicas: 1
env:
- name: GOMEMLIMIT
  value: 9GiB
image:
  repository: ghcr.io/hanzoai/cloud
  # v1.801.334 = cloud main 87a9441d5. Repairs the release pipeline itself,
  # which could not publish: the smoke gate grepped the boot log for
  #   "message":"listening"

  # while the zip transport logs "zip listening".
  tag: v1.801.335
livenessProbe:
  httpGet:
    path: /health
`

// ── the file surgery is byte-exact ───────────────────────────────────────────

// The one thing a pin may change is the tag scalar. These files carry the
// reasoning for every value in them, so anything else moving — a reflowed comment,
// a dropped blank line, a requoted scalar — is a regression.
func TestPinRewritesOnlyTheTagScalar(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cloud.yaml")
	if err := os.WriteFile(path, []byte(pinFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := readPinFile(path)
	if err != nil {
		t.Fatalf("readPinFile: %v", err)
	}
	if f.repository != "ghcr.io/hanzoai/cloud" {
		t.Errorf("repository = %q", f.repository)
	}
	if f.current != "v1.801.335" {
		t.Errorf("current tag = %q", f.current)
	}
	f.setTag("v1.801.336")
	if err := f.write("v1.801.336"); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(pinFixture, "tag: v1.801.335", "tag: v1.801.336", 1)
	if string(got) != want {
		t.Errorf("file is not byte-identical apart from the tag scalar:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// A trailing comment on the tag line is part of the file's reasoning and survives,
// as does the indentation — the same span pin.sh's `sub` leaves alone.
func TestPinPreservesIndentAndTrailingComment(t *testing.T) {
	src := "image:\n  repository: ghcr.io/hanzoai/cloud\n    tag:   v1.0.0   # pinned by hand during the incident\n"
	dir := t.TempDir()
	path := filepath.Join(dir, "svc.yaml")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := readPinFile(path)
	if err != nil {
		t.Fatalf("readPinFile: %v", err)
	}
	f.setTag("v1.0.1")
	if err := f.write("v1.0.1"); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, _ := os.ReadFile(path)
	want := "image:\n  repository: ghcr.io/hanzoai/cloud\n    tag: v1.0.1   # pinned by hand during the incident\n"
	if string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// The pin is the TOP-LEVEL image block's tag. A `tag:` nested under some other key
// is a different value and must never be mistaken for the pin — that would deploy
// a sidecar's version as the service.
func TestPinReadsOnlyTheTopLevelImageBlock(t *testing.T) {
	src := `image:
  repository: ghcr.io/hanzoai/cloud

  # a blank line and an indented comment do not close the block
  tag: v1.0.0
sidecar:
  image:
    repository: ghcr.io/hanzoai/other
    tag: v9.9.9
`
	lines := strings.Split(src, "\n")
	if got := imageBlockValue(lines, "tag"); got != "v1.0.0" {
		t.Errorf("tag = %q, want v1.0.0 (the nested sidecar tag must not win)", got)
	}
	if got := imageBlockValue(lines, "repository"); got != "ghcr.io/hanzoai/cloud" {
		t.Errorf("repository = %q", got)
	}
}

// A file that declares no image block is refused, not repaired: this moves ONE
// scalar and must never be the thing that invents a service's image.
func TestPinRefusesAFileWithNoImagePin(t *testing.T) {
	for name, src := range map[string]string{
		"no image block": "chart: app\nreplicas: 1\n",
		"no tag":         "image:\n  repository: ghcr.io/hanzoai/cloud\n",
		"no repository":  "image:\n  tag: v1.0.0\n",
		"empty tag":      "image:\n  repository: ghcr.io/hanzoai/cloud\n  tag:\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "svc.yaml")
			if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := readPinFile(path); err == nil {
				t.Fatal("want a refusal, got nil")
			}
		})
	}
}

// ── the refusals ─────────────────────────────────────────────────────────────

func TestPinRefusals(t *testing.T) {
	file := func(repo, tag string) *pinFile {
		return &pinFile{path: "cloud.yaml", repository: repo, current: tag}
	}
	cases := []struct {
		name        string
		f           *pinFile
		repo, tag   string
		wantChanged bool
		wantErr     string
	}{
		{
			name: "moves forward",
			f:    file("ghcr.io/hanzoai/cloud", "v1.801.335"), repo: "ghcr.io/hanzoai/cloud", tag: "v1.801.336",
			wantChanged: true,
		},
		{
			// A rebuild of an unchanged tree is a no-op, not an empty commit.
			name: "already pinned is a no-op success",
			f:    file("ghcr.io/hanzoai/cloud", "v1.801.335"), repo: "ghcr.io/hanzoai/cloud", tag: "v1.801.335",
			wantChanged: false,
		},
		{
			// The whole point of taking the repository from the FILE: a build cannot
			// repoint a service at an image of its choosing.
			name: "a tag of another repository",
			f:    file("ghcr.io/hanzoai/cloud", "v1.801.335"), repo: "ghcr.io/attacker/evil", tag: "v9.0.0",
			wantErr: "refusing to pin a tag of one repository into another",
		},
		{
			name: "backward",
			f:    file("ghcr.io/hanzoai/cloud", "v1.801.335"), repo: "ghcr.io/hanzoai/cloud", tag: "v1.801.334",
			wantErr: "refusing to roll production back",
		},
		{
			name: "backward across minor",
			f:    file("ghcr.io/hanzoai/cloud", "v1.801.0"), repo: "ghcr.io/hanzoai/cloud", tag: "v1.800.999",
			wantErr: "refusing to roll production back",
		},
		{
			name: "an unorderable current pin",
			f:    file("ghcr.io/hanzoai/cloud", "sha-a1b2c3d"), repo: "ghcr.io/hanzoai/cloud", tag: "v1.0.0",
			wantErr: "cannot be ordered",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			changed, err := checkPin(tc.f, tc.repo, tc.tag)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("want a refusal containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected refusal: %v", err)
			}
			if changed != tc.wantChanged {
				t.Fatalf("changed = %v, want %v", changed, tc.wantChanged)
			}
		})
	}
}

// splitReleaseImage is the semver gate the pin inherits. Every mutable form is
// refused BEFORE anything is cloned, so a non-deterministic tag can never be pinned.
func TestPinRefusesEveryNonSemverTag(t *testing.T) {
	for _, image := range []string{
		"ghcr.io/hanzoai/cloud:latest",
		"ghcr.io/hanzoai/cloud:main",
		"ghcr.io/hanzoai/cloud:sha-a1b2c3d",
		"ghcr.io/hanzoai/cloud:v1.801.335-dirty",
		"ghcr.io/hanzoai/cloud:1.801.335", // the bare form: not what this registry holds
	} {
		if _, _, err := splitReleaseImage(image); err == nil {
			t.Errorf("%s: want a refusal, got nil", image)
		}
	}
	if _, _, err := splitReleaseImage("ghcr.io/hanzoai/cloud:v1.801.335"); err != nil {
		t.Errorf("clean semver refused: %v", err)
	}
}

// A service must already be in the inventory. Zero matches means a release would be
// enrolling one as a side effect; more than one means two namespaces hold the name
// and nothing here may guess which production was meant.
func TestPinResolvesExactlyOneValuesFile(t *testing.T) {
	root := t.TempDir()
	mk := func(ns, svc string) {
		dir := filepath.Join(root, "charts", "app", "values", ns)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, svc+".yaml"), []byte("image:\n  tag: v1.0.0\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("hanzo", "cloud")
	got, err := resolvePinFile(root, "cloud")
	if err != nil {
		t.Fatalf("resolvePinFile: %v", err)
	}
	if filepath.Base(filepath.Dir(got)) != "hanzo" {
		t.Errorf("resolved %s, want the hanzo namespace", got)
	}
	if _, err := resolvePinFile(root, "not-a-service"); err == nil ||
		!strings.Contains(err.Error(), "added deliberately") {
		t.Errorf("unknown service: want the not-in-inventory refusal, got %v", err)
	}
	mk("lux", "cloud")
	if _, err := resolvePinFile(root, "cloud"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("two namespaces: want the ambiguity refusal, got %v", err)
	}
}

// ── the credential ───────────────────────────────────────────────────────────

// Fail-closed. An unmounted KMS, a KMS that cannot answer, and an empty secret each
// stop the release: an anonymous push would fail deep in the git client, long after
// the pipeline reported the tag minted.
func TestPinTokenFailsClosed(t *testing.T) {
	ctx := context.Background()
	if _, err := pinToken(testService(), ctx); err == nil {
		t.Error("no KMS mounted: want a refusal, got nil")
	}
	empty := newFakeKMS()
	if err := empty.PutSecret(ctx, pinTokenRef, []byte("   ")); err != nil {
		t.Fatal(err)
	}
	if _, err := pinToken(serviceWithKMS(empty), ctx); err == nil {
		t.Error("empty secret: want a refusal, got nil")
	}
	// A KMS that has never been sealed answers not-found, which is also a refusal.
	if _, err := pinToken(serviceWithKMS(newFakeKMS()), ctx); err == nil {
		t.Error("absent secret: want a refusal, got nil")
	}
	sealed := newFakeKMS()
	if err := sealed.PutSecret(ctx, pinTokenRef, []byte(" sk-real-token \n")); err != nil {
		t.Fatal(err)
	}
	got, err := pinToken(serviceWithKMS(sealed), ctx)
	if err != nil {
		t.Fatalf("sealed token: %v", err)
	}
	if got != "sk-real-token" {
		t.Errorf("token = %q, want it trimmed", got)
	}
}

// The token reaches git as an http.extraHeader in the child ENVIRONMENT and never
// as an argument or as URL userinfo: /proc/<pid>/cmdline is readable inside the
// container, and a URL credential is echoed back by git in its own error messages.
// It is presented over https ONLY — never on a plaintext wire.
func TestPinCredentialIsEnvOnlyAndHTTPSOnly(t *testing.T) {
	const token = "sk-secret-value"
	restore := swapUniverseRemote("https://git.hanzo.ai/hanzo/universe")
	env := pinGitEnv(token)
	restore()

	var header string
	for _, kv := range env {
		if strings.Contains(kv, token) {
			header = kv
		}
		if strings.HasPrefix(kv, "GIT_CONFIG_VALUE_") && strings.Contains(kv, "Authorization: Basic ") {
			header = kv
		}
	}
	if header == "" {
		t.Fatal("no Authorization header in the git environment over https")
	}
	if strings.Contains(header, token) {
		t.Errorf("the raw token appears verbatim in the environment; it must be base64 basic-auth: %q", header)
	}
	if !strings.Contains(strings.Join(env, "\n"), "credential.helper") {
		t.Error("credential.helper is not disabled: git may consult an ambient helper")
	}
	if !strings.Contains(strings.Join(env, "\n"), "http.followRedirects") {
		t.Error("followRedirects is not disabled: the credential could ride a redirect off the forge")
	}
	// Nothing inherited: the child gets PATH and the pin's own config, so no other
	// secret in cloud's environment — and no operator-set GIT_TRACE — reaches it.
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "PATH", "HOME", "LC_ALL", "GIT_CONFIG_NOSYSTEM", "GIT_CONFIG_GLOBAL",
			"GIT_TERMINAL_PROMPT", "GIT_ALLOW_PROTOCOL", "GIT_CONFIG_COUNT":
		default:
			if !strings.HasPrefix(k, "GIT_CONFIG_KEY_") && !strings.HasPrefix(k, "GIT_CONFIG_VALUE_") {
				t.Errorf("unexpected inherited variable in the git environment: %q", k)
			}
		}
	}

	// A remote that is not https gets no credential at all.
	restore = swapUniverseRemote("http://127.0.0.1:1/universe.git")
	plain := strings.Join(pinGitEnv(token), "\n")
	restore()
	if strings.Contains(plain, "Authorization") {
		t.Error("a credential was prepared for a plaintext remote")
	}
}

// ── the registry gate ────────────────────────────────────────────────────────

// Build first, pin second, never the reverse: a tag the registry cannot serve is an
// ImagePullBackOff with no rollback path.
func TestPinRefusesAnImageTheRegistryDoesNotHave(t *testing.T) {
	srv := fakeRegistry(t, map[string]bool{"v1.0.1": true})
	defer swapRegistryBase(srv.URL)()

	if err := imagePullable(context.Background(), "ghcr.io/hanzoai/cloud", "v1.0.1"); err != nil {
		t.Errorf("a published tag was refused: %v", err)
	}
	err := imagePullable(context.Background(), "ghcr.io/hanzoai/cloud", "v1.0.2")
	if err == nil || !strings.Contains(err.Error(), "not pullable") {
		t.Errorf("a phantom tag was accepted: %v", err)
	}
}

// ── end to end, against a real git remote ────────────────────────────────────

// The whole pin, exercised for real: a shallow clone over HTTP from a bare
// repository served by git-http-backend, the rule set, the one-scalar rewrite, a
// commit and a fast-forward push — then the remote is read back to prove production
// would now pull the new image.
func TestPinMovesTheRealPin(t *testing.T) {
	remote, bare := gitRemote(t, pinFixture)
	defer swapUniverseRemote(remote)()
	defer swapRegistryBase(fakeRegistry(t, map[string]bool{"v1.801.336": true}).URL)()

	s := serviceWithKMS(kmsWithPinToken(t))
	if err := pinUniverse(s, context.Background(), "cloud", "ghcr.io/hanzoai/cloud:v1.801.336", "87a9441d5"); err != nil {
		t.Fatalf("pinUniverse: %v", err)
	}

	got := showFile(t, bare, "charts/app/values/hanzo/cloud.yaml")
	want := strings.Replace(pinFixture, "tag: v1.801.335", "tag: v1.801.336", 1)
	if got != want {
		t.Errorf("the pinned file is not byte-identical apart from the tag:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

	log := mustGit(t, bare, "log", "-1", "--format=%an <%ae>%n%B")
	for _, want := range []string{
		"hanzo-ci <ci@hanzo.ai>",
		"cloud v1.801.335 -> v1.801.336",
		"87a9441d5",
		"ghcr.io/hanzoai/cloud:v1.801.336",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("commit does not record %q:\n%s", want, log)
		}
	}
	// The credential must not have been written into the clone's remote URL.
	if strings.Contains(log, "sk-") {
		t.Error("the commit records something token-shaped")
	}
}

// Re-running a release whose pin already landed is a no-op: no empty commit, no
// second deploy record.
func TestPinIsIdempotent(t *testing.T) {
	remote, bare := gitRemote(t, pinFixture)
	defer swapUniverseRemote(remote)()
	defer swapRegistryBase(fakeRegistry(t, map[string]bool{"v1.801.335": true}).URL)()

	before := mustGit(t, bare, "rev-parse", "main")
	s := serviceWithKMS(kmsWithPinToken(t))
	if err := pinUniverse(s, context.Background(), "cloud", "ghcr.io/hanzoai/cloud:v1.801.335", "sha"); err != nil {
		t.Fatalf("pinUniverse: %v", err)
	}
	if after := mustGit(t, bare, "rev-parse", "main"); after != before {
		t.Errorf("an already-live pin committed anyway: %s -> %s", before, after)
	}
}

// THE RACE. universe is the repository the whole fleet deploys through, so another
// service lands its own pin between our read and our push. The push is a plain
// fast-forward, so that is REJECTED rather than silently overwriting the other
// deploy — and the answer is to re-read the new tip and re-apply, not to merge.
//
// The competing commit is landed from inside the registry probe, which runs on
// every attempt just before the write, so the race is deterministic rather than
// timing-dependent.
func TestPinRetriesWhenAnotherPinLandsFirst(t *testing.T) {
	remote, bare := gitRemote(t, pinFixture)
	defer swapUniverseRemote(remote)()

	var probes atomic.Int32
	reg := fakeRegistryFunc(t, func(tag string) bool {
		// Only the first attempt races; the second must find a clean fast-forward.
		if probes.Add(1) == 1 {
			landCompetingPin(t, remote, "insights", "1.52.29")
		}
		return tag == "v1.801.336"
	})
	defer swapRegistryBase(reg.URL)()

	s := serviceWithKMS(kmsWithPinToken(t))
	if err := pinUniverse(s, context.Background(), "cloud", "ghcr.io/hanzoai/cloud:v1.801.336", "sha"); err != nil {
		t.Fatalf("pinUniverse did not survive a concurrent pin: %v", err)
	}
	if n := probes.Load(); n < 2 {
		t.Fatalf("the race did not happen (%d probes) — this test is not proving the retry", n)
	}
	if got := showFile(t, bare, "charts/app/values/hanzo/cloud.yaml"); !strings.Contains(got, "tag: v1.801.336") {
		t.Errorf("our pin did not land after the retry:\n%s", got)
	}
	// The other service's pin must still be there: we re-applied onto the new tip,
	// we did not clobber it.
	if got := showFile(t, bare, "charts/app/values/hanzo/insights.yaml"); !strings.Contains(got, "tag: 1.52.29") {
		t.Errorf("the concurrent pin was clobbered:\n%s", got)
	}
}

// A concurrent pin that already moved this service PAST our version is refused by
// the monotonic rule on re-read, rather than rolling production back onto our
// older image.
func TestPinRefusesToClobberANewerConcurrentPin(t *testing.T) {
	remote, bare := gitRemote(t, pinFixture)
	defer swapUniverseRemote(remote)()

	var probes atomic.Int32
	reg := fakeRegistryFunc(t, func(string) bool {
		if probes.Add(1) == 1 {
			landCompetingPin(t, remote, "cloud", "v1.801.400")
		}
		return true
	})
	defer swapRegistryBase(reg.URL)()

	s := serviceWithKMS(kmsWithPinToken(t))
	err := pinUniverse(s, context.Background(), "cloud", "ghcr.io/hanzoai/cloud:v1.801.336", "sha")
	if err == nil || !strings.Contains(err.Error(), "refusing to roll production back") {
		t.Fatalf("want the monotonic refusal after re-reading a newer tip, got %v", err)
	}
	if got := showFile(t, bare, "charts/app/values/hanzo/cloud.yaml"); !strings.Contains(got, "tag: v1.801.400") {
		t.Errorf("the newer pin was overwritten:\n%s", got)
	}
}

// ── harness ──────────────────────────────────────────────────────────────────

func swapUniverseRemote(url string) func() {
	prev := universeRemote
	universeRemote = url
	return func() { universeRemote = prev }
}

// swapRegistryBase points the registry reads at url and returns a restore func.
func swapRegistryBase(url string) func() {
	prev := registryBase
	registryBase = url
	return func() { registryBase = prev }
}

func testService() *cloud.Service[state] {
	return &cloud.Service[state]{Base: cloud.Base{Log: luxlog.New("test")}}
}

func serviceWithKMS(k cloud.KMSClient) *cloud.Service[state] {
	return &cloud.Service[state]{Base: cloud.Base{Log: luxlog.New("test"), KMS: k}}
}

func kmsWithPinToken(t *testing.T) *fakeKMS {
	t.Helper()
	k := newFakeKMS()
	if err := k.PutSecret(context.Background(), pinTokenRef, []byte("sk-test-token")); err != nil {
		t.Fatal(err)
	}
	return k
}

// fakeRegistry answers the anonymous ghcr token flow and a manifest probe, serving
// exactly the tags in have.
func fakeRegistry(t *testing.T, have map[string]bool) *httptest.Server {
	t.Helper()
	return fakeRegistryFunc(t, func(tag string) bool { return have[tag] })
}

func fakeRegistryFunc(t *testing.T, have func(tag string) bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"anon"}`))
			return
		}
		if i := strings.Index(r.URL.Path, "/manifests/"); i >= 0 {
			if have(r.URL.Path[i+len("/manifests/"):]) {
				w.Header().Set("Content-Type", "application/vnd.oci.image.index.v1+json")
				_, _ = w.Write([]byte(`{"schemaVersion":2}`))
				return
			}
			http.Error(w, `{"errors":[{"code":"MANIFEST_UNKNOWN"}]}`, http.StatusNotFound)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// gitRemote stands up a real bare repository seeded with a fleet values tree and
// serves it over HTTP with git-http-backend — the same smart-HTTP transport the
// forge speaks, so the clone, the fast-forward push and the rejection on a race are
// all the real thing. Returns the clone URL and the bare repository's path.
// fleetSetFixture is the ApplicationSet with the reservation-based fence — what
// universe must carry for an org's bare name to be safe as a directory. The
// discriminator checkFence looks for is the ABSENCE of the prefix rule.
const fleetSetFixture = `apiVersion: apps.hanzo.ai/v1
kind: ApplicationSet
metadata:
  name: fleet
  namespace: hanzo-cd
spec:
  generators:
    - git:
        repoURL: https://git.hanzo.ai/hanzo/universe
        revision: main
        files:
          - path: charts/app/values/*/*.yaml
  template:
    spec:
      # The fence is derived from the path, and the question the path is asked is
      # whether the directory is RESERVED — the platform's own namespace family —
      # not whether it carries a prefix. hanzo, lux, zoo, admin and the kube-*
      # family are ours; everything else is an org and is fenced under its own name.
      project: '{{ if has .path.basename (list "hanzo" "lux" "zoo" "admin" "default" "hanzo-cd" "hanzo-build") }}hanzo-platform{{ else }}{{ .path.basename }}{{ end }}'
`

func gitRemote(t *testing.T, cloudValues string) (url, bare string) {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git is not installed: %v", err)
	}
	root := t.TempDir()
	seed := filepath.Join(root, "seed")
	values := filepath.Join(seed, "charts", "app", "values", "hanzo")
	if err := os.MkdirAll(values, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(values, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("cloud.yaml", cloudValues)
	write("insights.yaml", "image:\n  repository: ghcr.io/hanzoai/insights\n  tag: 1.52.28\n")

	// The delivery plane's own rule travels with the inventory it fences, and
	// declare.go's checkFence reads it out of the clone before writing main. The
	// fixture carries the RESERVATION form — the one an org's bare name needs —
	// so a commit here exercises the post-companion-change world.
	// declare_test.go's TestCommitRefusesAStaleFence covers the other form.
	setDir := filepath.Join(seed, "infra", "k8s", "hanzo-cd")
	if err := os.MkdirAll(setDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(setDir, "applicationset-fleet.yaml"), []byte(fleetSetFixture), 0o644); err != nil {
		t.Fatal(err)
	}

	mustGit(t, "", "init", "-q", "-b", universeBranch, seed)
	mustGit(t, seed, "add", "-A")
	mustGit(t, seed, "-c", "user.name=seed", "-c", "user.email=seed@test", "commit", "-qm", "seed the fleet inventory")

	bare = filepath.Join(root, "universe.git")
	mustGit(t, "", "clone", "-q", "--bare", seed, bare)
	mustGit(t, bare, "config", "http.receivepack", "true")

	srv := httptest.NewServer(&cgi.Handler{
		Path: gitBin,
		Args: []string{"http-backend"},
		Env: []string{
			"GIT_PROJECT_ROOT=" + root,
			"GIT_HTTP_EXPORT_ALL=1",
		},
	})
	t.Cleanup(srv.Close)
	return srv.URL + "/universe.git", bare
}

// landCompetingPin is another service's release landing its own pin while ours is
// in flight — a clone, a one-line change, a push, exactly as a concurrent pin does.
func landCompetingPin(t *testing.T, remote, service, tag string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "competitor")
	mustGit(t, "", "clone", "-q", "--branch", universeBranch, remote, dir)
	path := filepath.Join(dir, "charts", "app", "values", "hanzo", service+".yaml")
	f, err := readPinFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f.setTag(tag)
	if err := f.write(tag); err != nil {
		t.Fatal(err)
	}
	mustGit(t, dir, "add", "-A")
	mustGit(t, dir, "-c", "user.name=other", "-c", "user.email=other@test", "commit", "-qm",
		fmt.Sprintf("%s -> %s", service, tag))
	mustGit(t, dir, "push", "-q", "origin", "HEAD:refs/heads/"+universeBranch)
}

func showFile(t *testing.T, bare, path string) string {
	t.Helper()
	return mustGit(t, bare, "show", universeBranch+":"+path)
}

// mustGit runs a git command for TEST SETUP with a hermetic environment — the
// developer's own git config must never change what these tests prove.
func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"LC_ALL=C",
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
