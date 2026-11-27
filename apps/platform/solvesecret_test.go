package platform

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// solvesecret_test.go pins which fabric credentials a build Job carries. A
// BuildKit solve hands every declared secret to any RUN step that mounts it, so
// the estate's credentials reach an estate build and a customer's Dockerfile is
// handed none of them: not the `--secret` declaration, and not the env var or
// Secret reference behind it.

// fabricSecrets are the Secrets in the build namespace that hold the estate's
// forge credentials, keyed by the build-secret id each would be declared as.
var fabricSecrets = map[string]string{
	"GIT_AUTH_TOKEN": "forge-token",
	"REGISTRY_TOKEN": "forge-registry-token",
}

// handed reads one build Job's solve — the container that runs buildctl, an
// init container on a customer Job: the build-secret ids its argv declares, and,
// for each env var sourced from a Secret, the Secret it names.
func handed(t *testing.T, job *unstructured.Unstructured) (declared map[string]bool, env map[string]string) {
	t.Helper()
	c, _ := solverOf(t, job)
	declared = map[string]bool{}
	argv := c["command"].([]any)
	for i, a := range argv {
		if a != "--secret" || i+1 >= len(argv) {
			continue
		}
		id, _, _ := strings.Cut(strings.TrimPrefix(argv[i+1].(string), "id="), ",")
		declared[id] = true
	}
	env = map[string]string{}
	for _, e := range c["env"].([]any) {
		m := e.(map[string]any)
		vf, _ := m["valueFrom"].(map[string]any)
		ref, _ := vf["secretKeyRef"].(map[string]any)
		if ref == nil {
			continue
		}
		env[m["name"].(string)], _ = ref["name"].(string)
	}
	return declared, env
}

// jobs is every build Job in the fake cluster's build namespace.
func jobs(t *testing.T, k *k8sClient) []unstructured.Unstructured {
	t.Helper()
	list, err := k.dyn.Resource(jobsGVR).Namespace(k.buildNS).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list build Jobs: %v", err)
	}
	return list.Items
}

// job is the one build Job named name.
func job(t *testing.T, k *k8sClient, name string) *unstructured.Unstructured {
	t.Helper()
	j, err := k.dyn.Resource(jobsGVR).Namespace(k.buildNS).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Job %s: %v", name, err)
	}
	return j
}

// noFabricSecret fails when the Job declares, or holds in its env, any of the
// estate's forge credentials.
func noFabricSecret(t *testing.T, what string, j *unstructured.Unstructured) {
	t.Helper()
	declared, env := handed(t, j)
	for id, secret := range fabricSecrets {
		if declared[id] {
			t.Errorf("%s: the solve declares %s, so a RUN --mount=type=secret,id=%s step reads it", what, id, id)
		}
		if _, ok := env[id]; ok {
			t.Errorf("%s: the Job's env carries %s (from Secret %q)", what, id, env[id])
		}
		for name, from := range env {
			if from == secret {
				t.Errorf("%s: env %s reads Secret %q", what, name, secret)
			}
		}
	}
}

// hasSecret fails unless the Job both declares id to the solve and sources it
// from secret.
func hasSecret(t *testing.T, what string, j *unstructured.Unstructured, id string) {
	t.Helper()
	declared, env := handed(t, j)
	if !declared[id] {
		t.Errorf("%s: the solve does not declare %s", what, id)
	}
	if env[id] != fabricSecrets[id] {
		t.Errorf("%s: env %s comes from %q, want Secret %q", what, id, env[id], fabricSecrets[id])
	}
}

// A customer org's Dockerfile build, reached the way any signed-up org reaches
// it — POST /v1/platform/.../apps with a git source and buildType dockerfile,
// then deploy — is handed no fabric credential.
func TestCustomerDockerfileBuildIsHandedNoFabricCredential(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		k := fakeK8s()
		app := mountAppK8s(t, k)
		seedProject(t, app, "acme", "web")
		code, body := do(t, app, http.MethodPost, "/v1/platform/projects/web/apps", "acme", map[string]any{
			"name": "api", "source": "git", "buildType": "dockerfile",
			"repo": map[string]any{"url": "https://github.com/acme/app", "branch": "main"},
		})
		if code != http.StatusCreated {
			t.Fatalf("create git app: want 201, got %d (%s)", code, body)
		}
		if code, body = do(t, app, http.MethodPost, "/v1/platform/projects/web/apps/api/deploy", "acme", map[string]any{}); code != http.StatusAccepted {
			t.Fatalf("deploy: want 202, got %d (%s)", code, body)
		}
		all := jobs(t, k)
		if len(all) != 1 {
			t.Fatalf("want one build Job, got %d", len(all))
		}
		noFabricSecret(t, "acme's dockerfile build", &all[0])
	})
}

// A customer app whose repository is on the forge is handed no forge credential
// either, so its Dockerfile cannot read the fabric's git token and its context
// cannot clone the estate's repositories.
func TestCustomerForgeBuildIsHandedNoForgeCredential(t *testing.T) {
	self, fh := selfGitHost, forgeHost
	selfGitHost, forgeHost = "hanzo.ai", "git.hanzo.ai"
	t.Cleanup(func() { selfGitHost, forgeHost = self, fh })

	k := fakeK8s()
	for _, repo := range []string{
		"https://git.hanzo.ai/acme-ws/app.git",
		"https://git.hanzo.ai/hanzoai/cloud.git",
	} {
		a := Application{Slug: "app", RepoURL: repo, RepoBranch: "main", Dockerfile: "Dockerfile"}
		name, err := k.launchBuildJob(context.Background(), customer, "acme", a, k.buildImageRef("acme", "app", "main"), "main", genID("bld"))
		if err != nil {
			t.Fatalf("%s: launch: %v", repo, err)
		}
		noFabricSecret(t, "acme building "+repo, job(t, k, name))
	}
}

// The declare lane (launchDirectBuild) is the other way a customer's repository
// reaches BuildKit. It hands a customer build nothing, on either architecture.
func TestCustomerDeclareBuildIsHandedNoFabricCredential(t *testing.T) {
	k := fakeK8s()
	for _, p := range []string{"linux/amd64", "linux/arm64"} {
		id := genID("bld")
		if _, err := k.launchDirectBuild(context.Background(), customer, "acme", "", "https://github.com/acme/app", "main",
			k.buildImageRef("acme", "app", id), "Dockerfile", "", "", id, nil, []string{p}); err != nil {
			t.Fatalf("launch %s: %v", p, err)
		}
	}
	all := jobs(t, k)
	if len(all) != 2 {
		t.Fatalf("want a Job per build, got %d", len(all))
	}
	for i := range all {
		noFabricSecret(t, "acme's declared build "+all[i].GetName(), &all[i])
	}
}

// A customer build is one platform at one ref. A fan-out's index Job resolves
// each half through the dockerfile frontend with the push credential in its own
// container, where a customer image's ONBUILD triggers would run; extra tags
// ride an export the customer solve does not make. Both are refused before a
// Job exists, and both stay the estate's.
func TestACustomerBuildPublishesOneImage(t *testing.T) {
	k := fakeK8s()
	ctx := context.Background()
	image := k.buildImageRef("acme", "app", "v1")
	for name, launch := range map[string]func() error{
		"two platforms": func() error {
			_, err := k.launchDirectBuild(ctx, customer, "acme", "", "https://github.com/acme/app", "main",
				image, "Dockerfile", "", "", genID("bld"), nil, []string{"linux/amd64", "linux/arm64"})
			return err
		},
		"an extra tag": func() error {
			_, err := k.launchDirectBuild(ctx, customer, "acme", "", "https://github.com/acme/app", "main",
				image, "Dockerfile", "", "", genID("bld"), nil, nil, "latest")
			return err
		},
	} {
		err := launch()
		if err == nil || deployErrStatus(err) != http.StatusBadRequest {
			t.Errorf("a customer build asking for %s: want a 400 refusal, got %v", name, err)
		}
	}
	if n := len(jobs(t, k)); n != 0 {
		t.Fatalf("a refused customer build left %d Jobs", n)
	}
	if _, err := k.launchDirectBuild(ctx, estate, "hanzo", "", "https://github.com/hanzoai/app", "main",
		"ghcr.io/hanzoai/app:v1", "Dockerfile", "", "", genID("bld"), nil, []string{"linux/amd64", "linux/arm64"}, "latest"); err != nil {
		t.Fatalf("an estate fan-out with a tag: %v", err)
	}
}

// The estate lane needs both halves: an org the forge's table names, spelled
// byte-exact, and a caller who is its staff.
func TestTheEstateLaneNeedsStaffOfAnEstateOrg(t *testing.T) {
	for _, c := range []struct {
		org   string
		staff bool
		want  lane
	}{
		{"hanzo", true, estate},
		{"lux", true, estate},
		{"hanzo", false, customer},
		{"acme", true, customer},
		{"Hanzo", true, customer},
		{"", true, customer},
	} {
		if got := laneOf(c.org, c.staff); got != c.want {
			t.Errorf("laneOf(%q, staff=%v) = %v, want %v", c.org, c.staff, got, c.want)
		}
	}
	var zero lane
	if zero != customer {
		t.Errorf("a lane nobody decided is %v; it must be customer", zero)
	}
}

// IAM orgs are byte-exact, so an org spelled like an estate org in another case
// is a customer. The forge's table folds case; the credentials must not.
func TestACaseVariantOfAnEstateOrgIsACustomer(t *testing.T) {
	k := fakeK8s()
	a := Application{Slug: "app", RepoURL: "https://github.com/acme/app", RepoBranch: "main", Dockerfile: "Dockerfile"}
	for _, org := range []string{"Hanzo", "LUX", " zoo"} {
		name, err := k.launchBuildJob(context.Background(), laneOf(org, true), org, a, "ghcr.io/hanzoai/tenant-x/app:main", "main", genID("bld"))
		if err != nil {
			t.Fatalf("%q: launch: %v", org, err)
		}
		noFabricSecret(t, "org "+org, job(t, k, name))
	}
}

// An estate build keeps what it had: the package-registry credential on every
// build, and the forge's git credential when its context is the forge.
func TestEstateBuildIsHandedTheFabricCredentials(t *testing.T) {
	self, fh := selfGitHost, forgeHost
	selfGitHost, forgeHost = "hanzo.ai", "git.hanzo.ai"
	t.Cleanup(func() { selfGitHost, forgeHost = self, fh })

	k := fakeK8s()
	forged := Application{Slug: "app", RepoURL: "https://git.hanzo.ai/hanzoai/app.git", RepoBranch: "main", Dockerfile: "Dockerfile"}
	name, err := k.launchBuildJob(context.Background(), estate, "hanzo", forged, "ghcr.io/hanzoai/app:main", "main", genID("bld"))
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	j := job(t, k, name)
	hasSecret(t, "hanzo building from the forge", j, "REGISTRY_TOKEN")
	hasSecret(t, "hanzo building from the forge", j, "GIT_AUTH_TOKEN")

	hub := Application{Slug: "app", RepoURL: "https://github.com/hanzoai/app", RepoBranch: "main", Dockerfile: "Dockerfile"}
	name, err = k.launchBuildJob(context.Background(), estate, "hanzo", hub, "ghcr.io/hanzoai/app:main", "main", genID("bld"))
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	j = job(t, k, name)
	hasSecret(t, "hanzo building from github", j, "REGISTRY_TOKEN")
	if declared, env := handed(t, j); declared["GIT_AUTH_TOKEN"] || env["GIT_AUTH_TOKEN"] != "" {
		t.Errorf("hanzo building from github: the forge credential is declared=%v env=%q; github.com is never offered it",
			declared["GIT_AUTH_TOKEN"], env["GIT_AUTH_TOKEN"])
	}

	id := genID("bld")
	if _, err := k.launchDirectBuild(context.Background(), estate, "lux", "", "https://github.com/luxfi/app", "main",
		"ghcr.io/luxfi/app:v1", "Dockerfile", "", "", id, nil, nil); err != nil {
		t.Fatalf("launch: %v", err)
	}
	hasSecret(t, "lux's direct build", job(t, k, buildJobName(id, "")), "REGISTRY_TOKEN")
}

// The index Job executes nothing — a FROM resolved per platform — so it is
// handed no credential, estate or not.
func TestTheIndexJobIsHandedNoFabricCredential(t *testing.T) {
	k := fakeK8s()
	id := genID("bld")
	name, err := k.launchIndexBuild(context.Background(), "hanzo", "ghcr.io/hanzoai/app:v1", id, []string{"linux/amd64", "linux/arm64"})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	noFabricSecret(t, "the index Job", job(t, k, name))
}

// The object-store cache credential (artifact-s3) is the object store's ADMIN
// key, and buildkitd holds it in its environment on a lane whose steps share the
// daemon's /proc. A customer's build Job carries neither AWS key; an estate build
// keeps them, for the layer cache.
func TestTheObjectStoreAdminKeyIsEstateOnly(t *testing.T) {
	k := fakeK8s()

	name, err := k.launchBuildJob(context.Background(), customer, "acme",
		Application{Slug: "app", RepoURL: "https://github.com/acme/app", RepoBranch: "main", Dockerfile: "Dockerfile"},
		k.buildImageRef("acme", "app", "main"), "main", genID("bld"))
	if err != nil {
		t.Fatalf("customer launch: %v", err)
	}
	if _, env := handed(t, job(t, k, name)); env["AWS_ACCESS_KEY_ID"] != "" || env["AWS_SECRET_ACCESS_KEY"] != "" {
		t.Errorf("acme's build carries the object-store admin key: AWS_ACCESS_KEY_ID<-%q AWS_SECRET_ACCESS_KEY<-%q",
			env["AWS_ACCESS_KEY_ID"], env["AWS_SECRET_ACCESS_KEY"])
	}

	name, err = k.launchBuildJob(context.Background(), estate, "hanzo",
		Application{Slug: "app", RepoURL: "https://github.com/hanzoai/app", RepoBranch: "main", Dockerfile: "Dockerfile"},
		"ghcr.io/hanzoai/app:main", "main", genID("bld"))
	if err != nil {
		t.Fatalf("estate launch: %v", err)
	}
	if _, env := handed(t, job(t, k, name)); env["AWS_ACCESS_KEY_ID"] != artifactS3Secret || env["AWS_SECRET_ACCESS_KEY"] != artifactS3Secret {
		t.Errorf("hanzo's build lost the layer-cache credential: AWS_ACCESS_KEY_ID<-%q AWS_SECRET_ACCESS_KEY<-%q",
			env["AWS_ACCESS_KEY_ID"], env["AWS_SECRET_ACCESS_KEY"])
	}
}
