package platform

// checkout_test.go is "Add to project" on a PRIVATE GitHub repository (#766): the
// customer's build is handed a token that reads that repository and nothing else,
// minted as the caller, carried in a Secret the build Job owns, and revoked once
// the Job is done. A public repository's build mints nothing.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/zap-proto/zip"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

// testToken is the token the peer mints. It must never appear in a Job.
const testToken = "ghs_checkout_test_token"

// revoker is GitHub's DELETE /installation/token, recording the bearer of each.
type revoker struct {
	mu      sync.Mutex
	bearers []string
}

func (r *revoker) revoked() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.bearers...)
}

// serveRevoker stands GitHub's token revocation up at GITHUB_API_URL.
func serveRevoker(t *testing.T) *revoker {
	t.Helper()
	r := &revoker{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodDelete || req.URL.Path != "/installation/token" {
			http.NotFound(w, req)
			return
		}
		r.mu.Lock()
		r.bearers = append(r.bearers, strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer "))
		r.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("GITHUB_API_URL", srv.URL)
	return r
}

// privateHarness is the deploy harness where acme-gh/secret is private, visible
// to acme's members and admins, and the peer mints a read token for it.
func privateHarness(t *testing.T) (*deployHarness, *revoker) {
	t.Helper()
	r := serveRevoker(t)
	h := newDeployHarness(t)
	h.k.checkoutWait, h.k.checkoutPoll = 50*time.Millisecond, 10*time.Millisecond
	public := h.peer.visible
	h.peer.visible = func(who zip.Caller, owner, repo string) (*client.GitHubRepo, bool) {
		if strings.EqualFold(owner+"/"+repo, "acme-gh/secret") && who.Org == "acme" {
			return &client.GitHubRepo{Owner: "acme-gh", Repo: "secret", Base: "main", Private: true, Installation: 7}, true
		}
		return public(who, owner, repo)
	}
	h.peer.run = func(who zip.Caller, in client.GitHubRunIn) (*client.GitHubRun, error) {
		if !in.Read || !strings.EqualFold(in.Owner+"/"+in.Repo, "acme-gh/secret") {
			return nil, zip.ErrForbidden("github run: not this one")
		}
		return &client.GitHubRun{Token: testToken, Owner: "acme-gh", Repo: "secret", Base: "main"}, nil
	}
	return h, r
}

// secretOf is the Secret named name in the build namespace, or nil.
func secretOf(t *testing.T, k *k8sClient, name string) *unstructured.Unstructured {
	t.Helper()
	s, err := k.dyn.Resource(secretsGVR).Namespace(k.buildNS).Get(context.Background(), name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("Secret %s: %v", name, err)
	}
	return s
}

func TestAPrivateRepoBuildIsHandedItsOwnReadToken(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		for _, admin := range []bool{true, false} {
			t.Run(map[bool]string{true: "admin", false: "member"}[admin], func(t *testing.T) {
				h, rev := privateHarness(t)
				out := h.deploy(t, admin, map[string]any{"repo": "acme-gh/secret", "mode": "branch"})
				if out.Build == nil || out.Build.Repo != "https://github.com/acme-gh/secret.git" {
					t.Fatalf("admin=%v: want a build of the private repository: %+v", admin, out.Build)
				}

				// Minted once, read-only, AS the caller: their org and their person.
				got := h.peer.minted()
				if len(got) != 1 || !got[0].in.Read || got[0].in.Owner != "acme-gh" || got[0].in.Repo != "secret" ||
					got[0].who.Org != "acme" || got[0].who.User != "u-acme" || got[0].who.OrgAdmin != admin {
					t.Fatalf("admin=%v: want one read mint as the caller, got %+v", admin, got)
				}

				j := job(t, h.k, out.Build.Job)
				// The solve declares buildkit's github.com credential, sourced from the
				// Job's own Secret, and nothing of the fabric's.
				declared, env := handed(t, j)
				if !declared[checkoutID] {
					t.Fatalf("admin=%v: the solve does not declare %s", admin, checkoutID)
				}
				want := checkoutSecretName(out.Build.Job)
				if env["GIT_AUTH_TOKEN_github_com"] != want {
					t.Fatalf("admin=%v: the checkout env comes from %q, want %q (%v)", admin, env["GIT_AUTH_TOKEN_github_com"], want, env)
				}
				if len(declared) != 1 || len(env) != 1 {
					t.Fatalf("admin=%v: the solve holds more than its checkout: declared %v env %v", admin, declared, env)
				}
				noFabricSecret(t, "private repo build", j)
				// Only the solve holds it: the push container runs no customer code
				// and needs no source.
				if _, mains := containersOf(j); len(mains) != 1 || strings.Contains(strings.Join(heldBy(j, mains[0]), " "), want) {
					t.Fatalf("admin=%v: the push container holds the checkout: %v", admin, heldBy(j, mains[0]))
				}
				// The token itself is in no Job field: `get job` shows a reference.
				raw, _ := json.Marshal(j.Object)
				if strings.Contains(string(raw), testToken) {
					t.Fatalf("admin=%v: the Job carries the token", admin)
				}

				// The Secret: the token, owned by the Job, so the collector takes it
				// with the Job.
				sec := secretOf(t, h.k, want)
				if sec == nil {
					t.Fatalf("admin=%v: no Secret %s", admin, want)
				}
				if tok, _, _ := unstructured.NestedString(sec.Object, "stringData", "token"); tok != testToken {
					t.Fatalf("admin=%v: the Secret holds %q", admin, tok)
				}
				owners := sec.GetOwnerReferences()
				if len(owners) != 1 || owners[0].Kind != "Job" || owners[0].Name != out.Build.Job {
					t.Fatalf("admin=%v: the Secret must be owned by its Job: %+v", admin, owners)
				}

				// Revoked once the Job is done, or at the wait's end, whichever is first.
				deadline := time.Now().Add(5 * time.Second)
				for len(rev.revoked()) == 0 && time.Now().Before(deadline) {
					time.Sleep(10 * time.Millisecond)
				}
				if b := rev.revoked(); len(b) != 1 || b[0] != testToken {
					t.Fatalf("admin=%v: want the token revoked once, got %v", admin, b)
				}
			})
		}
	})
}

// The builder's spelling of a private GitHub row (this deployment's own git
// address naming the GitHub owner) builds from GitHub with the same token.
func TestTheBuildersAddressForAPrivateRowIsHandedItsToken(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		servesOwnGit(t)
		h, rev := privateHarness(t)
		out := h.deploy(t, false, map[string]any{"repo": "https://api.hanzo.ai/v1/git/acme-gh/secret.git", "mode": "branch"})
		if out.Build == nil || out.Build.Repo != "https://github.com/acme-gh/secret.git" {
			t.Fatalf("want a build of the private repository from GitHub: %+v", out.Build)
		}
		if got := h.peer.minted(); len(got) != 1 || got[0].in.Owner != "acme-gh" || got[0].in.Repo != "secret" {
			t.Fatalf("want one mint for acme-gh/secret, got %+v", got)
		}
		if secretOf(t, h.k, checkoutSecretName(out.Build.Job)) == nil {
			t.Fatal("the build was handed no Secret")
		}
		deadline := time.Now().Add(5 * time.Second)
		for len(rev.revoked()) == 0 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if b := rev.revoked(); len(b) != 1 {
			t.Fatalf("want the token revoked once, got %v", b)
		}
	})
}

func TestAPublicRepoBuildMintsNothing(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h, rev := privateHarness(t)
		out := h.deploy(t, true, map[string]any{"repo": "acme-gh/web", "mode": "branch"})
		if got := h.peer.minted(); len(got) != 0 {
			t.Fatalf("a public repository's build minted: %+v", got)
		}
		declared, env := handed(t, job(t, h.k, out.Build.Job))
		if len(declared) != 0 || len(env) != 0 {
			t.Fatalf("a public repository's build is handed a secret: %v %v", declared, env)
		}
		if secretOf(t, h.k, checkoutSecretName(out.Build.Job)) != nil {
			t.Fatal("a public repository's build wrote a Secret")
		}
		if len(rev.revoked()) != 0 {
			t.Fatal("nothing was minted, so nothing is revoked")
		}
	})
}

// A mint provider refuses — no push access of the caller's own, no admin of the
// org — is that refusal, before any Job exists.
func TestARefusedMintLaunchesNothing(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h, _ := privateHarness(t)
		h.peer.run = nil
		code, body := h.post(t, false, map[string]any{"repo": "acme-gh/secret", "mode": "branch"})
		if code != http.StatusForbidden || !strings.Contains(string(body), "acme-gh/secret") {
			t.Fatalf("a refused mint want 403 naming the repository, got %d %s", code, body)
		}
		if n := h.jobs(t); n != 0 {
			t.Fatalf("a refused mint launched %d build(s)", n)
		}
	})
}

// A deployment that may not write the Secret (the grant in hanzo-build is
// missing) answers 503, leaves no Job behind, and revokes what it minted.
func TestACheckoutThatCannotBeWrittenTakesItsJob(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h, rev := privateHarness(t)
		h.k.dyn.(*dynamicfake.FakeDynamicClient).PrependReactor("create", "secrets",
			func(clienttesting.Action) (bool, runtime.Object, error) {
				return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "", nil)
			})
		code, body := h.post(t, true, map[string]any{"repo": "acme-gh/secret", "mode": "branch"})
		if code != http.StatusServiceUnavailable || !strings.Contains(string(body), "Secret") {
			t.Fatalf("want 503 saying the Secret could not be written, got %d %s", code, body)
		}
		if n := h.jobs(t); n != 0 {
			t.Fatalf("a build with no credential left %d Job(s)", n)
		}
		if b := rev.revoked(); len(b) != 1 || b[0] != testToken {
			t.Fatalf("want the minted token revoked, got %v", b)
		}
	})
}

// The customer lane's Job renders a per-build secret and drops anything else a
// caller hands it, and a checkout is refused on the estate lane and for a
// repository other than its own.
func TestACustomerSolveHoldsOnlyItsOwnBuildsSecret(t *testing.T) {
	k := fakeK8s()
	spec := k.buildJobSpec(customer, "bld-x", "bld-x", "acme", "runner", "push-hanzoai", "ghcr.io/hanzoai/x:1",
		[]any{"buildctl-daemonless.sh", "build"}, defaultArch,
		[]solveSecret{forgeRead, registryRead, githubSource, githubCheckout, checkoutSecret("bld-x")})
	declared, env := handed(t, spec)
	if len(declared) != 1 || !declared[checkoutID] || len(env) != 1 || env["GIT_AUTH_TOKEN_github_com"] != "bld-x-checkout" {
		t.Fatalf("a customer solve must hold its checkout alone: declared %v env %v", declared, env)
	}
	noFabricSecret(t, "customer Job", spec)

	g := &gitCheckout{owner: "acme-gh", repo: "secret", token: testToken}
	for _, tc := range []struct {
		name string
		l    lane
		url  string
	}{
		{"the estate lane", estate, "https://github.com/acme-gh/secret.git"},
		{"another repository", customer, "https://github.com/acme-gh/web.git"},
		{"another host", customer, "https://gitlab.com/acme-gh/secret.git"},
	} {
		id := genID("bld")
		if _, err := k.launchCheckedBuild(context.Background(), tc.l, "acme", "", tc.url, "main", k.buildImageRef("acme", "app", id),
			"Dockerfile", "", "", id, g, nil, nil); err == nil || !strings.Contains(err.Error(), "checkout") {
			t.Errorf("%s: want the checkout refused, got %v", tc.name, err)
		}
	}
	if !g.clones("https://GitHub.com/Acme-GH/Secret") || g.String() != "github.com/acme-gh/secret" {
		t.Fatal("clones folds case and an absent .git; String names the repository alone")
	}
}

// POST /v1/build on an org-owned private GitHub repository (like hanzoai/zrok)
// mints a short-lived one-repository contents:read installation token for that
// build, attaches it via a Job-owned Secret to single-arch and multi-platform
// halves alike, and revokes it once all halves finish.
func TestEstateRunnerBuildMintsPerBuildCheckoutForPrivateRepo(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		rev := serveRevoker(t)
		asked := serveGrant(t, func(owner, repo string) (*client.GitHubGrant, error) {
			return &client.GitHubGrant{Token: testToken, Owner: owner, Repo: repo}, nil
		})
		k := fakeK8s()
		k.checkoutWait, k.checkoutPoll = 100*time.Millisecond, 10*time.Millisecond
		app, _ := runnerAppOn(t, k)

		// Single-platform delegated build (zrok's exact shape: hanzoai/zrok -> ghcr.io/hanzo-inc/zrok).
		code, raw := postRunner(t, app, map[string]any{
			"repo":       "https://github.com/hanzoai/zrok.git",
			"sha":        "bcb4fa0000000000000000000000000000000000",
			"image":      "ghcr.io/hanzo-inc/zrok:sha-bcb4fa0-amd64",
			"tags":       []string{"0.1.7"},
			"dockerfile": "Dockerfile",
			"platforms":  []string{"linux/amd64"},
		})
		if code != http.StatusAccepted {
			t.Fatalf("POST /v1/build = %d %s", code, raw)
		}
		var resp runnerBuildResp
		if err := json.Unmarshal(raw, &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got := asked(); len(got) != 1 || got[0] != "hanzo hanzoai/zrok" {
			t.Fatalf("want one read grant for hanzo hanzoai/zrok, got %v", got)
		}
		jobName := buildJobName(resp.BuildJobID, "")
		j := job(t, k, jobName)
		declared, env := handed(t, j)
		wantSecret := checkoutSecretName(jobName)
		if !declared[checkoutID] || env["GIT_AUTH_TOKEN_github_com"] != wantSecret {
			t.Fatalf("estate build must carry per-build checkout secret %s: declared=%v env=%v", wantSecret, declared, env)
		}
		if sec := secretOf(t, k, wantSecret); sec == nil {
			t.Fatalf("checkout Secret %s not created", wantSecret)
		}
		// Wait for revocation when job finishes (or checkoutWait elapses).
		deadline := time.Now().Add(5 * time.Second)
		for len(rev.revoked()) == 0 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if b := rev.revoked(); len(b) != 1 || b[0] != testToken {
			t.Fatalf("want token revoked once, got %v", b)
		}
	})
}

func TestEstateFanOutCheckoutAttachesToBothHalves(t *testing.T) {
	k := fakeK8s()
	g := &gitCheckout{owner: "hanzoai", repo: "zrok", token: testToken}
	id := genID("bld")
	_, err := k.launchCheckedBuild(context.Background(), estate, "hanzo", "",
		"https://github.com/hanzoai/zrok.git", "main", "ghcr.io/hanzo-inc/zrok:0.1.7",
		"Dockerfile", "", "", id, g, nil, []string{"linux/amd64", "linux/arm64"})
	if err != nil {
		t.Fatalf("launchCheckedBuild fan-out: %v", err)
	}
	for _, arch := range []string{"amd64", "arm64"} {
		jn := buildJobName(id, arch)
		j := job(t, k, jn)
		declared, env := handed(t, j)
		want := checkoutSecretName(jn)
		if !declared[checkoutID] || env["GIT_AUTH_TOKEN_github_com"] != want {
			t.Fatalf("%s half missing checkout secret %s: declared=%v env=%v", arch, want, declared, env)
		}
		if secretOf(t, k, want) == nil {
			t.Fatalf("%s half Secret %s not created", arch, want)
		}
	}
}
