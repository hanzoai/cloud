package platform

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/kit"
	"github.com/hanzoai/cloud/sdk/secret"

	"github.com/hanzoai/cloud/client"
	providerpeer "github.com/hanzoai/cloud/client/provider"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/plane"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
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

// serveGrant stands provider's grant op up on a real socket, answering with
// answer, and returns what each mint was asked for: "<org> <owner>/<repo>".
func serveGrant(t *testing.T, answer func(owner, repo string) (*client.GitHubGrant, error)) func() []string {
	t.Helper()
	t.Setenv("ZIP_RUNTIME_DIR", kit.Sockets(t))
	plane.Unbind()
	var mu sync.Mutex
	var asked []string
	cloud.ResetPlane()
	cloud.Plane().Post("/integrations/github/grant",
		func(ctx context.Context, in *client.GitHubGrantIn) (*client.GitHubGrant, error) {
			mu.Lock()
			asked = append(asked, cloud.Who(ctx).Org+" "+in.Owner+"/"+in.Repo)
			mu.Unlock()
			return answer(in.Owner, in.Repo)
		},
		zip.WithOperationID(client.IntegrationsGitHubGrant))
	stop, err := cloud.ServePlane(providerpeer.App, nil)
	if err != nil {
		t.Fatalf("ServePlane(provider): %v", err)
	}
	t.Cleanup(func() { _ = stop(); cloud.ResetPlane() })
	for range 200 {
		if c, derr := net.Dial("unix", zip.SocketPath(providerpeer.App)); derr == nil {
			_ = c.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), asked...)
	}
}

// refuseGrant is provider's answer for an owner the org connected no
// installation on.
func refuseGrant(owner, _ string) (*client.GitHubGrant, error) {
	return nil, zip.ErrForbidden(fmt.Sprintf("integrations: github account %q is not connected for this org", owner))
}

// universe is read with a GitHub App token for hanzo-inc/universe, minted from the
// installation the PLATFORM org connected — whoever's request is behind the read.
func TestUniverseTokenIsMintedForThePlatformOrg(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		defer swapUniverseRemote("https://github.com/hanzo-inc/universe")()
		asked := serveGrant(t, func(owner, repo string) (*client.GitHubGrant, error) {
			return &client.GitHubGrant{Token: "ghs_minted", Owner: owner, Repo: repo}, nil
		})
		got, err := universeGrant(cloud.For(context.Background(), "acme"))
		if err != nil || got.token != "ghs_minted" || got.remote != "https://github.com/hanzo-inc/universe" {
			t.Fatalf("universeGrant = %+v, %v; want the minted token at hanzo-inc/universe", got, err)
		}
		if a := asked(); len(a) != 1 || a[0] != "hanzo hanzo-inc/universe" {
			t.Fatalf("minted %q; want one token for hanzo-inc/universe asked as org hanzo", a)
		}
	})
}

// A transferred universe is read and written where it lives now. Provider answers
// the repository's current full name and mints for it, and a token for the new
// owner reaches nothing at the old URL, so the remote is provider's answer, not
// the name asked.
func TestUniverseFollowsATransfer(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		defer swapUniverseRemote("https://github.com/hanzoai/universe")()
		serveGrant(t, func(owner, repo string) (*client.GitHubGrant, error) {
			return &client.GitHubGrant{Token: "ghs_moved", Owner: "hanzo-inc", Repo: repo}, nil
		})
		got, err := universeGrant(context.Background())
		if err != nil || got.token != "ghs_moved" || got.remote != "https://github.com/hanzo-inc/universe" {
			t.Fatalf("universeGrant = %+v, %v; want the minted token at hanzo-inc/universe", got, err)
		}
		if r := reviewURL(got.remote, "deploy/acme/web"); r != "https://github.com/hanzo-inc/universe/compare/main...deploy/acme/web" {
			t.Errorf("reviewURL = %q; want the compare view where the repository lives now", r)
		}

		// An answer that names no repository is no answer: nothing is cloned from it.
		serveGrant(t, func(owner, repo string) (*client.GitHubGrant, error) {
			return &client.GitHubGrant{Token: "ghs_bad", Owner: "../x", Repo: repo}, nil
		})
		var ng *noGrant
		if got, err := universeGrant(context.Background()); !errors.As(err, &ng) || got.remote != "" {
			t.Fatalf("universeGrant = %+v, %v; want a *noGrant for an owner GitHub cannot have", got, err)
		}
	})
}

// No installation is a typed refusal naming it, never an anonymous clone and
// never a person's token read from the environment instead.
func TestUniverseTokenWithoutAnInstallationIsTyped(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		defer swapUniverseRemote("https://github.com/hanzo-inc/universe")()
		t.Setenv("GIT_MIRROR_TOKEN_GITHUB_COM", "gho_personal")
		serveGrant(t, refuseGrant)
		got, err := universeGrant(context.Background())
		var ng *noGrant
		if got.token != "" || !errors.As(err, &ng) {
			t.Fatalf("universeGrant = %+v, %v; want no token and a *noGrant", got, err)
		}
		for _, want := range []string{"hanzo-inc/universe", "installation on hanzo-inc", "org hanzo", "not connected"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal does not say %q: %v", want, err)
			}
		}
		if universeStatus(fmt.Errorf("universe credential: %w", err)) != http.StatusServiceUnavailable {
			t.Error("a token that could not be minted must answer 503")
		}

		// A provider that is not running at all is the same refusal.
		cloud.ResetPlane()
		t.Setenv("ZIP_RUNTIME_DIR", kit.Sockets(t))
		plane.Unbind()
		if _, err := universeGrant(context.Background()); !errors.As(err, &ng) {
			t.Fatalf("no provider: want a *noGrant, got %v", err)
		}

		// A remote off GitHub has no installation to mint on.
		defer swapUniverseRemote("https://git.hanzo.ai/hanzoai/universe")()
		if _, err := universeGrant(context.Background()); err == nil || errors.As(err, &ng) {
			t.Fatalf("a remote off GitHub: want a plain refusal, got %v", err)
		}
	})
}

// Production reads and writes the repository CD reads. The forge carries a
// read-only mirror of it that trails main.
func TestUniverseIsTheRepositoryCDReads(t *testing.T) {
	if universeRemote != "https://github.com/hanzo-inc/universe" {
		t.Fatalf("universeRemote = %q; the fleet ApplicationSet reads https://github.com/hanzo-inc/universe", universeRemote)
	}
}

// The token reaches git as an http.extraHeader in the child ENVIRONMENT and never
// as an argument or as URL userinfo: /proc/<pid>/cmdline is readable inside the
// container, and a URL credential is echoed back by git in its own error messages.
// It is presented over https ONLY — never on a plaintext wire.
func TestPinCredentialIsEnvOnlyAndHTTPSOnly(t *testing.T) {
	const token = "sk-secret-value"
	env := pinGitEnv(universe{remote: "https://github.com/hanzo-inc/universe", token: token})

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
	plain := strings.Join(pinGitEnv(universe{remote: "http://127.0.0.1:1/universe.git", token: token}), "\n")
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
	return &cloud.Service[state]{Log: luxlog.New("test")}
}

func serviceWithKMS(k secret.Store) *cloud.Service[state] {
	return &cloud.Service[state]{Log: luxlog.New("test"), KMS: k}
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
        repoURL: https://github.com/hanzo-inc/universe
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
	gitBin := kit.Tool(t, "git")[0]
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
	servable(t, bare)

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

// servable configures a bare repository the way the forge serves universe: it
// takes pushes, and it serves the blob-less partial clone universeClone asks for
// — without allowFilter git would ignore the filter and clone everything, and the
// partial path would never run under test.
func servable(t *testing.T, bare string) {
	t.Helper()
	mustGit(t, bare, "config", "http.receivepack", "true")
	mustGit(t, bare, "config", "uploadpack.allowFilter", "true")
	mustGit(t, bare, "config", "uploadpack.allowAnySHA1InWant", "true")
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
