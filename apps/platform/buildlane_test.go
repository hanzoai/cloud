package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/zap-proto/zip"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// buildlane_test.go pins which container of a build Job holds the registry push
// credential. A customer's RUN steps share buildkitd's user and PID namespaces
// and uid, and read whatever its container holds through /proc/<buildkitd>/root
// and /proc/<buildkitd>/environ. The push credential writes every image of its
// registry namespace, push-hanzoai the fleet's own among them, so a customer
// Job's solve holds nothing and a container that runs no customer code pushes.
// An estate Job keeps its one credentialed container.
//
// A member of an estate org is not its staff: an IAM invitation makes one, and
// such a member's build is a customer's.

// containersOf is a build Job's init containers and main containers.
func containersOf(j *unstructured.Unstructured) (inits, mains []map[string]any) {
	pod := podSpec(j.Object)
	for _, c := range asAnyList(pod["initContainers"]) {
		inits = append(inits, c.(map[string]any))
	}
	for _, c := range asAnyList(pod["containers"]) {
		mains = append(mains, c.(map[string]any))
	}
	return inits, mains
}

func asAnyList(v any) []any {
	l, _ := v.([]any)
	return l
}

// argvOf is a container's command followed by its args.
func argvOf(c map[string]any) []string {
	var out []string
	for _, k := range []string{"command", "args"} {
		for _, a := range asAnyList(c[k]) {
			out = append(out, a.(string))
		}
	}
	return out
}

// solverOf is the container that runs buildctl, whose RUN steps are the
// Dockerfile's, and whether it is an init container.
func solverOf(t *testing.T, j *unstructured.Unstructured) (map[string]any, bool) {
	t.Helper()
	inits, mains := containersOf(j)
	runs := func(c map[string]any) bool {
		return slices.ContainsFunc(argvOf(c), func(a string) bool { return strings.Contains(a, "buildctl") })
	}
	for _, c := range inits {
		if runs(c) {
			return c, true
		}
	}
	for _, c := range mains {
		if runs(c) {
			return c, false
		}
	}
	t.Fatalf("%s: no container runs buildctl", j.GetName())
	return nil, false
}

// heldBy lists what one container of j holds of the fabric's: each Secret it
// mounts, DOCKER_CONFIG, each env var read from a Secret, and each build secret
// its argv declares to the solve.
func heldBy(j *unstructured.Unstructured, c map[string]any) []string {
	secretVolume := map[string]string{}
	for _, v := range asAnyList(podSpec(j.Object)["volumes"]) {
		m := v.(map[string]any)
		if s, ok := m["secret"].(map[string]any); ok {
			secretVolume[m["name"].(string)], _ = s["secretName"].(string)
		}
	}
	var held []string
	for _, v := range asAnyList(c["volumeMounts"]) {
		if s, ok := secretVolume[v.(map[string]any)["name"].(string)]; ok {
			held = append(held, "mount of Secret "+s)
		}
	}
	for _, e := range asAnyList(c["env"]) {
		m := e.(map[string]any)
		if m["name"] == "DOCKER_CONFIG" {
			held = append(held, "DOCKER_CONFIG")
		}
		if vf, ok := m["valueFrom"].(map[string]any); ok {
			if ref, ok := vf["secretKeyRef"].(map[string]any); ok {
				held = append(held, "env "+m["name"].(string)+" from Secret "+ref["name"].(string))
			}
		}
	}
	argv := argvOf(c)
	for i, a := range argv {
		if a == "--secret" && i+1 < len(argv) {
			held = append(held, "build secret "+argv[i+1])
		}
	}
	return held
}

// outputOf is the value of the solve's --output flag.
func outputOf(argv []string) string {
	for i, a := range argv {
		if a == "--output" && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	return ""
}

// jobFor is the one build Job the fake cluster holds for app.
func jobFor(t *testing.T, k *k8sClient, app string) *unstructured.Unstructured {
	t.Helper()
	var found []unstructured.Unstructured
	for _, j := range jobs(t, k) {
		if j.GetLabels()["hanzo.ai/application"] == app {
			found = append(found, j)
		}
	}
	if len(found) != 1 {
		t.Fatalf("want one build Job for %s, got %d", app, len(found))
	}
	return &found[0]
}

type caller func(t *testing.T, app *zip.App, method, path, org string, body any) (int, []byte)

// deployAs creates a Dockerfile app named name under org's project web and
// deploys it, both as who, and answers the deployment.
func deployAs(t *testing.T, app *zip.App, who caller, org, name, repo string) deploymentView {
	t.Helper()
	code, body := who(t, app, http.MethodPost, "/v1/platform/projects/web/apps", org, map[string]any{
		"name": name, "slug": name, "source": "git", "buildType": "dockerfile", "dockerfile": "Dockerfile",
		"repo": map[string]any{"url": repo, "branch": "main"},
	})
	if code != http.StatusCreated {
		t.Fatalf("%s creates %s: want 201, got %d (%s)", org, name, code, body)
	}
	code, body = who(t, app, http.MethodPost, "/v1/platform/projects/web/apps/"+name+"/deploy", org, map[string]any{})
	if code != http.StatusAccepted {
		t.Fatalf("%s deploys %s: want 202, got %d (%s)", org, name, code, body)
	}
	var d deploymentView
	if err := json.Unmarshal(body, &d); err != nil {
		t.Fatalf("deploy answer: %v (%s)", err, body)
	}
	return d
}

// customerShape fails unless j is a customer Job: its solve holds nothing and
// runs to completion before anything else starts, publishes nothing itself,
// touches no registry cache, and shares no process namespace.
func customerShape(t *testing.T, what string, j *unstructured.Unstructured) {
	t.Helper()
	c, init := solverOf(t, j)
	if held := heldBy(j, c); len(held) > 0 {
		t.Errorf("%s: the solve's container holds %v, and its RUN steps read all of it", what, held)
	}
	if !init {
		t.Errorf("%s: the solve runs as a main container, alongside the rest of the pod; it must finish before the push starts", what)
	}
	argv := argvOf(c)
	if out := outputOf(argv); !strings.HasPrefix(out, "type=oci,") || strings.Contains(out, "push=") {
		t.Errorf("%s: the solve's output is %q; it exports an OCI layout and pushes nothing", what, out)
	}
	for _, flag := range []string{"--import-cache", "--export-cache"} {
		if slices.Contains(argv, flag) {
			t.Errorf("%s: the solve names %s, a registry it holds no credential for", what, flag)
		}
	}
	if share, _ := podSpec(j.Object)["shareProcessNamespace"].(bool); share {
		t.Errorf("%s: the pod shares one process namespace across its containers", what)
	}
	if l := j.Object["spec"].(map[string]any)["template"].(map[string]any)["metadata"].(map[string]any)["labels"]; l != nil {
		if _, ok := l.(map[string]any)["hanzo.ai/publish"]; ok {
			t.Errorf("%s: the pod is labelled for a path to the object store it holds no credential for", what)
		}
	}
}

// A CUSTOMER'S SOLVE HOLDS NO REGISTRY CREDENTIAL. The org that reaches this is
// any signed-up org, deploying a Dockerfile app the way the console does.
func TestACustomerSolveHoldsNoRegistryCredential(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		k := fakeK8s()
		app := mountAppK8s(t, k)
		seedProject(t, app, "acme", "web")
		deployAs(t, app, do, "acme", "api", "https://github.com/acme/app")
		customerShape(t, "acme's build", jobFor(t, k, "api"))
	})
}

// THE PUSH RUNS NO CUSTOMER CODE. The one container holding the credential runs
// a fixed crane argv, pinned by digest, locked down, reading the layout the
// solve left read-only, and pushing to the ref the server derived.
func TestTheCustomerPushRunsNoCustomerCode(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		k := fakeK8s()
		app := mountAppK8s(t, k)
		seedProject(t, app, "acme", "web")
		repo := "https://github.com/acme/app"
		d := deployAs(t, app, do, "acme", "api", repo)
		j := jobFor(t, k, "api")

		inits, mains := containersOf(j)
		if len(inits) != 1 || len(mains) != 1 {
			t.Fatalf("want the solve as the one init container and the push as the one main container, got %d and %d", len(inits), len(mains))
		}
		push := mains[0]
		for _, c := range inits {
			if held := heldBy(j, c); len(held) > 0 {
				t.Errorf("init container %v holds %v", c["name"], held)
			}
		}
		if held := heldBy(j, push); !slices.Contains(held, "mount of Secret push-hanzoai") {
			t.Errorf("the push container holds %v, want the push credential", held)
		}

		want := []string{"/ko-app/crane", "push", "/out/image", d.Image}
		if got := argvOf(push); !slices.Equal(got, want) {
			t.Errorf("push argv = %q, want %q", got, want)
		}
		if d.Image != k.buildImageRef("acme", "api", "main") {
			t.Errorf("the push publishes %q, want the server-derived %q", d.Image, k.buildImageRef("acme", "api", "main"))
		}
		for _, a := range argvOf(push) {
			if strings.Contains(a, repo) || strings.Contains(a, "Dockerfile") || strings.Contains(a, "buildctl") {
				t.Errorf("push argv carries %q, which the build chose", a)
			}
		}
		image, _ := push["image"].(string)
		if !strings.HasPrefix(image, "gcr.io/go-containerregistry/crane:") || !strings.Contains(image, "@sha256:") {
			t.Errorf("push image %q is not crane pinned by digest", image)
		}
		for _, m := range asAnyList(push["volumeMounts"]) {
			if ro, _ := m.(map[string]any)["readOnly"].(bool); !ro {
				t.Errorf("the push container mounts %v writable", m.(map[string]any)["name"])
			}
		}
		sc, _ := push["securityContext"].(map[string]any)
		if sc["runAsNonRoot"] != true || sc["allowPrivilegeEscalation"] != false || sc["readOnlyRootFilesystem"] != true {
			t.Errorf("push securityContext = %v; want non-root, no escalation, read-only root", sc)
		}
		if caps, _ := sc["capabilities"].(map[string]any); !slices.Equal(asAnyList(caps["drop"]), []any{"ALL"}) {
			t.Errorf("push capabilities = %v, want every one dropped", sc["capabilities"])
		}
		if sp, _ := sc["seccompProfile"].(map[string]any); sp["type"] != "RuntimeDefault" {
			t.Errorf("push seccomp = %v, want RuntimeDefault", sc["seccompProfile"])
		}
	})
}

// AN ESTATE BUILD KEEPS ITS ONE CREDENTIALED CONTAINER: the push, the layer
// cache and the estate's build credentials, as it had them.
func TestAnEstateBuildKeepsItsOneContainer(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		k := fakeK8s()
		app := mountAppK8s(t, k)
		seedProject(t, app, "hanzo", "web")
		deployAs(t, app, doAdmin, "hanzo", "admin", "https://github.com/hanzoai/app")
		estateShape(t, "an admin of hanzo", jobFor(t, k, "admin"))
	})
}

// A MEMBER OF AN ESTATE ORG BUILDS AS A CUSTOMER. Acting as `hanzo` is what an
// invitation grants; administering it is what the estate's credentials need.
func TestAMemberOfAnEstateOrgBuildsAsACustomer(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		k := fakeK8s()
		app := mountAppK8s(t, k)
		seedProject(t, app, "hanzo", "web")
		deployAs(t, app, do, "hanzo", "member", "https://github.com/hanzoai/app")
		customerShape(t, "a member of hanzo", jobFor(t, k, "member"))
	})
}

// estateShape fails unless j is the estate Job: one container, solving and
// pushing with the registry credential and the estate's build credentials.
func estateShape(t *testing.T, what string, j *unstructured.Unstructured) {
	t.Helper()
	inits, mains := containersOf(j)
	if len(inits) != 0 || len(mains) != 1 {
		t.Fatalf("%s: want one container and no init container, got %d and %d", what, len(mains), len(inits))
	}
	c, _ := solverOf(t, j)
	held := heldBy(j, c)
	for _, want := range []string{
		"mount of Secret push-hanzoai", "DOCKER_CONFIG",
		"env REGISTRY_TOKEN from Secret forge-registry-token", "build secret id=REGISTRY_TOKEN,env=REGISTRY_TOKEN",
		"env AWS_ACCESS_KEY_ID from Secret " + artifactS3Secret,
	} {
		if !slices.Contains(held, want) {
			t.Errorf("%s: the estate solve lost %q (holds %v)", what, want, held)
		}
	}
	argv := argvOf(c)
	if out := outputOf(argv); !strings.HasPrefix(out, "type=image,") || !strings.Contains(out, "push=true") {
		t.Errorf("%s: the estate solve's output is %q, want its own push", what, out)
	}
	if !slices.Contains(argv, "--import-cache") || !slices.Contains(argv, "--export-cache") {
		t.Errorf("%s: the estate solve lost its layer cache", what)
	}
}

// A PUSH OF A REPOSITORY THAT IS NOT THE ESTATE'S BUILDS AS A CUSTOMER, in an
// estate org as anywhere. A push names an org and a commit and nobody who may
// speak for the org: any member can create the app it builds and have it track
// a repository the member writes — a repository on the embedded git server, or
// a GitHub repository of their own that the org's installation delivers.
func TestAPushOfACustomerRepositoryBuildsAsACustomerInAnEstateOrg(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		s := pushService(t)
		for _, c := range []struct{ slug, repo, clone string }{
			{"site", "https://git.hanzo.ai/v1/git/hanzo/site.git", "https://git.hanzo.ai/v1/git/hanzo/site.git"},
			{"mine", "https://github.com/someone/mine", "https://github.com/someone/mine.git"},
		} {
			seedGitApp(t, s, "hanzo", c.slug, c.repo, "main")
			n, err := buildFromPush(s, ctx, mkPushEvent("hanzo", c.slug, "main",
				"deadbeefcafe0123456789abcdef0123456789ab", c.clone))
			if err != nil || n != 1 {
				t.Fatalf("%s: buildFromPush launched %d, err %v; want 1", c.repo, n, err)
			}
			customerShape(t, "a push of "+c.repo+" to hanzo", jobFor(t, s.State.k8s, c.slug))
		}
	})
}

// A PUSH OF THE ESTATE'S OWN REPOSITORY BUILDS THE ESTATE JOB, the one an
// estate build of that app has always been. hanzo/console tracks
// github.com/hanzoai/console: whoever landed the push was given write access to
// that repository by GitHub, or by the forge that mirrors it, and that is the
// staff signal a push carries. The Job must be byte for byte the estate lane's,
// build id aside.
func TestAPushOfAnEstateRepositoryBuildsTheEstateJob(t *testing.T) {
	const commit = "deadbeefcafe0123456789abcdef0123456789ab"
	for _, clone := range []string{
		"https://github.com/hanzoai/console.git",   // the GitHub App's delivery
		"https://git.hanzo.ai/hanzoai/console.git", // the forge's
	} {
		// Each delivery is its own database, as each was its own temp dir: on hanzo-sql
		// the second would otherwise find the first's app.
		sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
			ctx := context.Background()
			s := pushService(t)
			a := seedGitApp(t, s, "hanzo", "console", "https://github.com/hanzoai/console", "main")
			n, err := buildFromPush(s, ctx, mkPushEvent("hanzo", "console", "main", commit, clone))
			if err != nil || n != 1 {
				t.Fatalf("%s: buildFromPush launched %d, err %v; want 1", clone, n, err)
			}
			pushed := jobFor(t, s.State.k8s, "console")
			estateShape(t, "a push of "+clone, pushed)

			k := fakeK8s()
			if _, err := k.launchBuildJob(ctx, estate, "hanzo", a, k.buildImageRef("hanzo", "console", shortTag(commit)), commit, "bld_estate"); err != nil {
				t.Fatalf("estate launch: %v", err)
			}
			if got, want := jobJSON(t, pushed), jobJSON(t, jobFor(t, k, "console")); got != want {
				t.Errorf("a push of %s built a Job that is not the estate Job:\n got %s\nwant %s", clone, got, want)
			}
		})
	}
}

// estateSource admits exactly an owner/name repository on the estate's hosts
// under a namespace the forge's tables name or keep.
func TestEstateSourceIsTheEstatesOwnRepository(t *testing.T) {
	self, fh := selfGitHost, forgeHost
	selfGitHost, forgeHost = "hanzo.ai", "git.hanzo.ai"
	t.Cleanup(func() { selfGitHost, forgeHost = self, fh })
	for repo, want := range map[string]bool{
		"https://github.com/hanzoai/console":          true,
		"https://github.com/hanzoai/console.git":      true,
		"https://GitHub.com/HanzoAI/console":          true,
		"https://git.hanzo.ai/hanzoai/console.git":    true,
		"https://github.com/hanzoai/cloud":          true,
		"https://github.com/luxfi/node":               true,
		"https://github.com/zooai/node":               true,
		"https://github.com/someone/console":          false,
		"https://github.com/hanzoai-evil/console":     false,
		"https://gitlab.com/hanzoai/console":          false,
		"https://git.hanzo.ai/v1/git/hanzo/site.git":  false,
		"https://api.hanzo.ai/v1/git/hanzo/site.git":  false,
		"https://github.com/hanzoai/console/extra":    false,
		"https://github.com/hanzoai":                  false,
		"https://x@github.com/hanzoai/console":        false,
		"https://github.com:8443/hanzoai/console":     false,
		"http://github.com/hanzoai/console":           false,
		"https://github.com/hanzoai/console?x=y":      false,
		"https://github.com.evil.example/hanzoai/app": false,
		"": false,
	} {
		if got := estateSource(repo); got != want {
			t.Errorf("estateSource(%q) = %v, want %v", repo, got, want)
		}
	}
}

// jobJSON is a build Job's JSON with its build id — the Job name and the
// hanzo.ai/build-id label, the one value each attempt mints — blanked.
func jobJSON(t *testing.T, j *unstructured.Unstructured) string {
	t.Helper()
	c := j.DeepCopy()
	c.SetName("")
	labels := c.GetLabels()
	delete(labels, "hanzo.ai/build-id")
	c.SetLabels(labels)
	b, err := json.Marshal(c.Object)
	if err != nil {
		t.Fatalf("marshal %s: %v", j.GetName(), err)
	}
	return string(b)
}

// A build pod's log is every container's, in the order the pod runs them: the
// solve of a customer build is an init container, and its push the main one.
func TestBuildLogs_ReadsEveryContainerInOrder(t *testing.T) {
	p := pod("pf-build-acme-api-abc-0", "hanzo",
		map[string]string{"job-name": "pf-build-acme-api-abc"}, time.Unix(1000, 0))
	p.Spec.InitContainers = []corev1.Container{{Name: "buildkit"}}
	p.Spec.Containers = []corev1.Container{{Name: "push"}}
	logs, ok := clientsetK8s(p).buildLogs(context.Background(), "pf-build-acme-api-abc")
	build, push := strings.Index(logs, "── buildkit ──"), strings.Index(logs, "── push ──")
	if !ok || build < 0 || push < build {
		t.Fatalf("buildLogs: want the solve's log then the push's, got ok=%v %q", ok, logs)
	}
}
