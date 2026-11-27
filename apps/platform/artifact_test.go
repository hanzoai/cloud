package platform

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk/kit"

	"github.com/hanzoai/cloud/internal/sqltest"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// The artifact lane accepts a hanzo.yml `binaries:` recipe verbatim and answers
// with the index URL a host will read — the ci lane's layout, byte for byte.
func TestRunnerArtifact_LaunchesAndIndexes(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := runnerApp(t)
		code, body := postRunner(t, app, map[string]any{
			"repo": "https://github.com/hanzoai/cloud", "sha": "0abcdef1234567890a1b2c3d4e5f60718293a4bc",
			"bucket": "plugins",
			"binaries": []any{
				map[string]any{"name": "cloud", "main": "./cmd/cloud", "platforms": []string{"linux/amd64", "linux/arm64"}},
				map[string]any{"name": "sdk", "run": "npm pack", "out": "*.tgz", "image": "node:22-bookworm"},
			},
		})
		if code != http.StatusAccepted {
			t.Fatalf("artifact build: want 202, got %d (%s)", code, body)
		}
		var resp runnerBuildResp
		if err := json.Unmarshal(body, &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		want := "https://s3.hanzo.ai/plugins/hanzoai/cloud/0abcdef1234567890a1b2c3d4e5f60718293a4bc/binaries.json"
		if resp.Index != want {
			t.Fatalf("index = %q, want %q", resp.Index, want)
		}
		if resp.Image != "" {
			t.Fatalf("artifact build must claim no image, got %q", resp.Image)
		}
	})
}

// THE RECIPE THE RELEASE SENDS, checked here rather than by a red release.
//
// This is the body that publishes the plugin set for a release. Every field in it is one this endpoint VALIDATES — the
// git host, the flat tag segment, the recipe's own shape — so a typo there is a
// 400 nobody sees until a tag build, and the artifacts for that release simply
// never exist. Keeping the body here makes it a compile-and-test-time fact.
//
// It also pins the two things a host depends on: the publish layout is keyed by
// the TAG (so a plugins.lock stanza for v1.801.533 names that release and not the
// commit), and the forge — not the mirror — is an accepted git host, which
// matters because the tag a release claims exists only on the forge and the
// build Job clones with no credential.
func TestRunnerArtifact_TheReleaseRecipeIsAccepted(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {

		// THE FORGE MUST BE A TRUSTED BUILD SOURCE, AND IT IS NOT TRUSTED BY DEFAULT.
		// hostAllowed trusts `selfGitHost` — brand.Apex(deps.Domain), set at Mount —
		// plus the four public providers. Measured: brand.Apex("") is "", CLOUD_DOMAIN
		// is set NOWHERE in the cloud deployment, and neither GitHub mirror carries a
		// release tag. So on a deployment that names neither its domain nor
		// CLOUD_PLATFORM_GIT_HOSTS, this recipe is refused 400 and the plugin set for
		// every release silently does not exist.
		//
		// Setting it here is what Mount does on a deployment that names its domain;
		// the sibling below pins the refusal, so the requirement cannot be forgotten
		// by anyone reading only the happy path.
		prev := selfGitHost
		selfGitHost = "hanzo.ai"
		t.Cleanup(func() { selfGitHost = prev })

		app := runnerApp(t)
		code, body := postRunner(t, app, map[string]any{
			"repo":   "https://git.hanzo.ai/hanzoai/cloud",
			"sha":    "0abcdef1234567890a1b2c3d4e5f60718293a4bc",
			"tag":    "v1.801.533",
			"bucket": "plugins",
			"binaries": []any{
				map[string]any{"name": "plugins", "run": "make -f mk/fleet.mk dist", "out": "dist/*"},
			},
		})
		if code != http.StatusAccepted {
			t.Fatalf("the release recipe was refused: %d (%s)", code, body)
		}
		var resp runnerBuildResp
		if err := json.Unmarshal(body, &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		want := "https://s3.hanzo.ai/plugins/hanzoai/cloud/v1.801.533/binaries.json"
		if resp.Index != want {
			t.Fatalf("index = %q, want %q — this is the URL plugins.lock stanzas are built from", resp.Index, want)
		}
	})
}

// The other half, so the deployment requirement above is a fact and not a
// comment: with no domain and no CLOUD_PLATFORM_GIT_HOSTS, the release recipe is
// REFUSED. This is the production configuration as measured, and it is why the
// `plugins` job carries the env var it does. The day the forge is trusted by
// some other derivation, this test goes red and says so.
func TestRunnerArtifact_ForgeIsRefusedUntilTheDeploymentTrustsIt(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		prev := selfGitHost
		selfGitHost = ""
		t.Cleanup(func() { selfGitHost = prev })

		app := runnerApp(t)
		code, body := postRunner(t, app, map[string]any{
			"repo": "https://git.hanzo.ai/hanzoai/cloud",
			"sha":  "0abcdef1234567890a1b2c3d4e5f60718293a4bc",
			"tag":  "v1.801.533", "bucket": "plugins",
			"binaries": []any{
				map[string]any{"name": "plugins", "run": "make -f mk/fleet.mk dist", "out": "dist/*"},
			},
		})
		if code != http.StatusBadRequest {
			t.Fatalf("want 400 for an untrusted forge, got %d (%s)", code, body)
		}
		if !strings.Contains(string(body), "not an allowed git provider") {
			t.Fatalf("refusal should name the reason, got %s", body)
		}
		// github.com stays trusted with no configuration at all, which is what makes
		// this a MISSING TRUST rather than a broken endpoint.
		code, body = postRunner(t, app, map[string]any{
			"repo": "https://github.com/hanzoai/cloud",
			"sha":  "0abcdef1234567890a1b2c3d4e5f60718293a4bc",
			"tag":  "v1.801.533", "bucket": "plugins",
			"binaries": []any{
				map[string]any{"name": "plugins", "run": "make -f mk/fleet.mk dist", "out": "dist/*"},
			},
		})
		if code != http.StatusAccepted {
			t.Fatalf("github.com should need no configuration, got %d (%s)", code, body)
		}
	})
}

// One initContainer per recipe entry, each in ITS OWN toolchain image, and the
// publisher — which is the only container that sees the object-store credential.
func TestArtifactJobSpec_ToolchainPerEntryAndCredentialOnlyInPublisher(t *testing.T) {
	k := fakeK8s()
	bins := []binarySpec{
		{Name: "cloud", Main: "./cmd/cloud", Platforms: []string{"linux/amd64"}},
		{Name: "sdk", Run: "npm pack", Out: "*.tgz", Image: "node:22-bookworm"},
	}
	for i := range bins {
		if err := bins[i].validate(); err != nil {
			t.Fatalf("validate %d: %v", i, err)
		}
	}
	job := k.artifactJobSpec("pf-artifact-x", "hanzo", "https://github.com/hanzoai/cloud", "v1.2.3", "v1.2.3", "https://s3.hanzo.ai/plugins/hanzoai/cloud/v1.2.3", "http://s3.hanzo.svc:9000/plugins/hanzoai/cloud/v1.2.3", bins)
	pod, _, err := unstructured.NestedMap(job.Object, "spec", "template", "spec")
	if err != nil {
		t.Fatalf("pod spec: %v", err)
	}
	inits, _ := pod["initContainers"].([]any)
	if len(inits) != 3 {
		t.Fatalf("initContainers = %d, want 3 (prepare + one per recipe entry)", len(inits))
	}

	// [0] PREPARE is the ONE credentialed container: a constant image running a
	// constant script, holding the only token in the Job. Everything the recipe
	// needs from a credentialed place is fetched here so the recipe needs none.
	prep := inits[0].(map[string]any)
	if prep["name"] != "prepare" || prep["image"] != defaultToolchainImage {
		t.Fatalf("initContainer[0] = %v/%v, want prepare on the constant toolchain", prep["name"], prep["image"])
	}
	if got := prep["command"].([]any)[2]; got != artifactPrepareScript {
		t.Error("prepare must run the constant script, nothing recipe-supplied")
	}
	var secrets []string
	for _, e := range prep["env"].([]any) {
		if vf, ok := e.(map[string]any)["valueFrom"].(map[string]any); ok {
			secrets = append(secrets, vf["secretKeyRef"].(map[string]any)["name"].(string))
		}
	}
	if !slices.Equal(secrets, []string{forgeTokenSecret}) {
		t.Errorf("prepare secrets = %v, want exactly [%s]", secrets, forgeTokenSecret)
	}
	// The token stays in the ENVIRONMENT. Written to a .gitconfig it would land
	// on /w, which every recipe container reads.
	if !strings.Contains(artifactPrepareScript, "GIT_CONFIG_COUNT") ||
		strings.Contains(artifactPrepareScript, "git config --global") {
		t.Error("prepare must carry the credential in the environment, not a .gitconfig")
	}
	// Both halves, one mechanism: the clone is authenticated AND module fetches
	// are pointed at the forge.
	for _, want := range []string{".extraheader", "insteadOf", "go mod download"} {
		if !strings.Contains(artifactPrepareScript, want) {
			t.Errorf("prepare must do %q — it is the only credentialed step", want)
		}
	}

	// [1..] run the RECIPE, and carry no secret at all.
	for i, want := range []string{defaultToolchainImage, "docker.io/library/node:22-bookworm"} {
		c := inits[i+1].(map[string]any)
		if c["image"] != want {
			t.Errorf("initContainer[%d] image = %v, want %s", i+1, c["image"], want)
		}
		for _, e := range c["env"].([]any) {
			if _, secret := e.(map[string]any)["valueFrom"]; secret {
				t.Errorf("initContainer[%d] (runs the recipe) must carry NO secret env: %v", i+1, e)
			}
		}
	}
	pub := pod["containers"].([]any)[0].(map[string]any)
	if pub["image"] != publishImage {
		t.Errorf("publisher image = %v, want %s", pub["image"], publishImage)
	}
	if got := pub["command"].([]any)[2]; got != artifactPublishScript {
		t.Error("publisher must run the constant publish script, nothing recipe-supplied")
	}
	if pod["automountServiceAccountToken"] != false {
		t.Error("artifact build must mount no service-account token")
	}
}

// A recipe's shell, in an image the recipe names, runs on the CI pool beside
// visor sandboxes and runners. The launched Job holds every container to the
// runtime's default seccomp profile, no setuid transition and no capability.
func TestAnArtifactBuildRunsConfined(t *testing.T) {
	k := fakeK8s()
	bins := []binarySpec{
		{Name: "cloud", Main: "./cmd/cloud", Platforms: []string{"linux/amd64"}},
		{Name: "sdk", Run: "npm pack", Out: "*.tgz", Image: "node:22-bookworm"},
	}
	for i := range bins {
		if err := bins[i].validate(); err != nil {
			t.Fatalf("validate %d: %v", i, err)
		}
	}
	name, err := k.launchArtifactBuild(context.Background(), "hanzo", "https://github.com/hanzoai/cloud",
		"v1", "v1", "https://s3/x", "http://s3/x", bins, "bld_confined")
	if err != nil {
		t.Fatalf("artifact build: %v", err)
	}
	job := getJob(t, k, name)
	if got, _, _ := unstructured.NestedString(job.Object, "spec", "template", "spec", "securityContext", "seccompProfile", "type"); got != "RuntimeDefault" {
		t.Errorf("pod seccompProfile = %q, want RuntimeDefault", got)
	}
	inits, _, _ := unstructured.NestedSlice(job.Object, "spec", "template", "spec", "initContainers")
	mains, _, _ := unstructured.NestedSlice(job.Object, "spec", "template", "spec", "containers")
	all := slices.Concat(inits, mains)
	if len(all) != len(bins)+2 {
		t.Fatalf("containers = %d, want prepare + %d recipes + publish", len(all), len(bins))
	}
	for _, x := range all {
		c := x.(map[string]any)
		if esc, found, _ := unstructured.NestedBool(c, "securityContext", "allowPrivilegeEscalation"); !found || esc {
			t.Errorf("%s: allowPrivilegeEscalation = %v (set %v), want false", c["name"], esc, found)
		}
		if drop, _, _ := unstructured.NestedStringSlice(c, "securityContext", "capabilities", "drop"); !slices.Equal(drop, []string{"ALL"}) {
			t.Errorf("%s: capabilities.drop = %v, want [ALL]", c["name"], drop)
		}
	}
}

// Bad recipes and lane confusion are 400s, not builds.
func TestRunnerArtifact_Rejects(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := runnerApp(t)
		for _, tc := range []struct {
			name string
			body map[string]any
			want int
		}{
			{"image and binaries", map[string]any{"repo": "https://github.com/hanzoai/cloud", "image": "ghcr.io/hanzoai/cloud:v1",
				"binaries": []any{map[string]any{"name": "x"}}}, http.StatusBadRequest},
			{"run without out", map[string]any{"repo": "https://github.com/hanzoai/cloud",
				"binaries": []any{map[string]any{"name": "x", "run": "make"}}}, http.StatusBadRequest},
			{"main and run", map[string]any{"repo": "https://github.com/hanzoai/cloud",
				"binaries": []any{map[string]any{"name": "x", "main": "./cmd/x", "run": "make", "out": "x"}}}, http.StatusBadRequest},
			{"branch ref needs an explicit tag", map[string]any{"repo": "https://github.com/hanzoai/cloud", "branch": "feat/x",
				"binaries": []any{map[string]any{"name": "x"}}}, http.StatusBadRequest},
			{"unallowlisted forge", map[string]any{"repo": "https://evil.example.com/hanzoai/cloud",
				"binaries": []any{map[string]any{"name": "x"}}}, http.StatusBadRequest},
		} {
			if code, body := postRunner(t, app, tc.body); code != tc.want {
				t.Errorf("%s: want %d, got %d (%s)", tc.name, tc.want, code, body)
			}
		}
	})
}

// An IAM org-admin publishes artifacts only for a forge owner its own org owns —
// the artifact-lane twin of the registry-namespace binding (H1).
func TestRunnerArtifact_IAMAdminBoundToItsOwnOwner(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := runnerApp(t)
		recipe := []any{map[string]any{"name": "x", "main": "./cmd/x"}}
		if code, body := postRunnerAs(t, app, "u", "hanzo", true, false, map[string]any{
			"repo": "https://github.com/hanzoai/cloud", "sha": "0abcdef1234567890a1b2c3d4e5f60718293a4bc", "binaries": recipe}); code != http.StatusAccepted {
			t.Fatalf("own-owner build: want 202, got %d (%s)", code, body)
		}
		if code, _ := postRunnerAs(t, app, "u", "hanzo", true, false, map[string]any{
			"repo": "https://github.com/luxfi/node", "sha": "0abcdef1234567890a1b2c3d4e5f60718293a4bc", "binaries": recipe}); code != http.StatusForbidden {
			t.Fatalf("cross-owner build: want 403, got %d", code)
		}
	})
}

// The recipe INTERPRETER is a shell script, so it is tested by running it — both
// lanes, against a real git repo, producing real files. This is what proves the
// declaration actually builds something rather than merely parsing.
func TestArtifactBuildScript_BuildsBothLanes(t *testing.T) {
	kit.Tool(t, "go")
	src := t.TempDir()
	write(t, filepath.Join(src, "go.mod"), "module demo\n\ngo 1.21\n")
	write(t, filepath.Join(src, "cmd", "demo", "main.go"), "package main\n\nfunc main() { println(\"demo\") }\n")
	write(t, filepath.Join(src, "pack.sh"), "#!/bin/sh\necho payload > demo-pkg.tgz\n")
	git(t, src, "init", "-q")
	git(t, src, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "x", "--allow-empty")
	git(t, src, "add", "-A")
	git(t, src, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "src")

	for _, tc := range []struct {
		name string
		env  []string
		want string
	}{
		{"go lane", []string{"NAME=demo", "MAIN=./cmd/demo", "RUN=", "OUT=", "LDFLAGS=-s -w", "PLATFORMS=linux/amd64"}, "demo-linux-amd64"},
		{"run lane", []string{"NAME=pkg", "MAIN=", "RUN=sh pack.sh", "OUT=*.tgz", "LDFLAGS=", "PLATFORMS="}, "demo-pkg.tgz"},
	} {
		w := t.TempDir()
		cmd := exec.Command("/bin/sh", "-c", strings.ReplaceAll(artifactBuildScript, "/w/", w+"/"))
		cmd.Env = append(append(os.Environ(), tc.env...),
			"REPO_URL="+src, "REF=HEAD", "HOME="+w, "GOFLAGS=-mod=mod", "GOTOOLCHAIN=local")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", tc.name, err, out)
		}
		if _, err := os.Stat(filepath.Join(w, "dist", tc.want)); err != nil {
			t.Fatalf("%s: artifact %s not produced\n%s", tc.name, tc.want, out)
		}
		meta, _ := os.ReadFile(filepath.Join(w, "meta.txt"))
		if !strings.Contains(string(meta), tc.want) {
			t.Fatalf("%s: meta.txt does not name %s: %q", tc.name, tc.want, meta)
		}
		t.Logf("%s produced %s\n%s", tc.name, tc.want, out)
	}
}

// launchArtifactBuild joins the SAME line as the image lane — an artifact build cannot
// outrun a container build: past the org's concurrency it waits, suspended, and is not refused.
func TestLaunchArtifactBuild_JoinsTheBuildLine(t *testing.T) {
	k := fakeK8s()
	bins := []binarySpec{{Name: "x", Main: ".", Platforms: []string{"linux/amd64"}, Image: defaultToolchainImage}}
	for i := 0; i <= client.PlatformBuildsPerOrgSeed; i++ {
		if _, err := k.launchArtifactBuild(context.Background(), "hanzo", "https://github.com/hanzoai/cloud", "v1", "v1", "https://s3/x", "http://s3/x", bins, "bld_"+itoa(i)); err != nil {
			t.Fatalf("launch %d: %v", i, err)
		}
	}
	if running, waiting := heldAndWaiting(t, k); running != client.PlatformBuildsPerOrgSeed || waiting != 1 {
		t.Fatalf("%d running and %d waiting, want %d running and the one past them waiting", running, waiting, client.PlatformBuildsPerOrgSeed)
	}
}

// launchArtifactBuild clones from an allowlisted git host over https, the same
// rule launchBuildJob and launchDirectBuild state. It holds at the constructor,
// so it does not depend on which caller reached it.
func TestLaunchArtifactBuild_ClonesAnAllowedSource(t *testing.T) {
	bins := []binarySpec{{Name: "x", Main: ".", Platforms: []string{"linux/amd64"}, Image: defaultToolchainImage}}
	refused := []struct{ name, repo, ref string }{
		{"another host", "https://evil.example.com/hanzoai/cloud", "v1"},
		{"a host the allowlist only suffixes", "https://evil-github.com/hanzoai/cloud", "v1"},
		{"a host the allowlist only prefixes", "https://github.com.evil.tld/hanzoai/cloud", "v1"},
		{"plaintext", "http://github.com/hanzoai/cloud", "v1"},
		{"embedded credentials", "https://u:p@github.com/hanzoai/cloud", "v1"},
		{"the loopback", "https://127.0.0.1/hanzoai/cloud", "v1"},
		{"instance metadata", "https://169.254.169.254/latest/meta-data", "v1"},
		{"a shell metacharacter", "https://github.com/hanzoai/cloud;id", "v1"},
		{"an abbreviated commit", "https://github.com/hanzoai/cloud", "abc1234"},
		{"a traversing ref", "https://github.com/hanzoai/cloud", "../../etc"},
	}
	for _, tc := range refused {
		k := fakeK8s()
		if _, err := k.launchArtifactBuild(context.Background(), "hanzo", tc.repo, tc.ref, "v1", "https://s3/x", "http://s3/x", bins, "bld_x"); err == nil {
			t.Fatalf("%s: launched a build from %q@%q", tc.name, tc.repo, tc.ref)
		}
		list, _ := k.dyn.Resource(jobsGVR).Namespace(k.buildNS).List(context.Background(), metav1.ListOptions{})
		if len(list.Items) != 0 {
			t.Fatalf("%s: %d Jobs created for a source it refused", tc.name, len(list.Items))
		}
	}
	k := fakeK8s()
	if _, err := k.launchArtifactBuild(context.Background(), "hanzo", "https://github.com/hanzoai/cloud", "v1.2.3", "v1.2.3", "https://s3/x", "http://s3/x", bins, "bld_ok"); err != nil {
		t.Fatalf("an ordinary source: %v", err)
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// A `run:` recipe that emits <name>-<os>-<arch> is indexed BY THAT TRIPLE, so
// one recipe entry can publish a whole plugin set and a host resolving
// (name, os, arch) finds each one. Recording the recipe's own name and "any"
// instead — which is what this lane did — puts every file under one name and
// resolves nothing, which is the whole reason cloud's plugin lane could not
// publish through this endpoint. The sibling test below pins the other half: a file
// with no platform in its name still takes the recipe's name and "any".
//
// Both scripts run for real, so the meta.txt hand-off is exercised rather than
// asserted.
func TestArtifactScripts_IndexPerFileWhenTheNameCarriesThePlatform(t *testing.T) {
	kit.Tool(t, "sha256sum")
	src := t.TempDir()
	write(t, filepath.Join(src, "pack.sh"), "#!/bin/sh\nmkdir -p dist\n"+
		"for f in o11y-linux-amd64 o11y-linux-arm64 iam-darwin-arm64 pkg-1.2.3.whl\n"+
		"do echo payload > \"dist/$f\"; done\n")
	git(t, src, "init", "-q")
	git(t, src, "add", "-A")
	git(t, src, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "src")

	idx := runArtifactScripts(t, src, "plugins", "sh pack.sh", "dist/*")

	got := map[string][2]string{}
	for _, b := range idx.Binaries {
		got[b.Name] = [2]string{b.OS, b.Arch}
		if b.SHA256 == "" {
			t.Errorf("%s: no digest — release.go DROPS an entry without one", b.Name)
		}
	}
	// o11y appears TWICE, once per platform, under its own name — the property
	// a single "plugins/any/any" entry cannot express.
	var o11y []string
	for _, b := range idx.Binaries {
		if b.Name == "o11y" {
			o11y = append(o11y, b.OS+"/"+b.Arch)
		}
	}
	sort.Strings(o11y)
	if want := []string{"linux/amd64", "linux/arm64"}; !slices.Equal(o11y, want) {
		t.Errorf("o11y = %v, want %v", o11y, want)
	}
	if got["iam"] != [2]string{"darwin", "arm64"} {
		t.Errorf("iam = %v, want darwin/arm64", got["iam"])
	}
	// The wheel is not per-platform, so it keeps the recipe's name and "any" —
	// naming a platform it does not have would be the same lie in reverse.
	if got["plugins"] != [2]string{"any", "any"} {
		t.Errorf("pkg-1.2.3.whl = %v under name plugins, want any/any", got["plugins"])
	}
	if len(idx.Binaries) != 4 {
		t.Errorf("index carries %d entries, want 4", len(idx.Binaries))
	}
}

// The published index is the ci lane's schema, field for field: `name` is the
// RECIPE entry's name (what a host asks for), not the file, which the url
// already carries. Proven by running the two scripts back to back over a fake
// object store, so the meta.txt hand-off between them is exercised for real.
func TestArtifactScripts_IndexNamesTheRecipeEntry(t *testing.T) {
	kit.Tool(t, "sha256sum")
	src := t.TempDir()
	write(t, filepath.Join(src, "pack.sh"), "#!/bin/sh\necho payload > sdk-1.0.0.tgz\n")
	git(t, src, "init", "-q")
	git(t, src, "add", "-A")
	git(t, src, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "src")

	idx := runArtifactScripts(t, src, "hanzo-sdk", "sh pack.sh", "*.tgz")
	if len(idx.Binaries) != 1 {
		t.Fatalf("index = %+v", idx.Binaries)
	}
	b := idx.Binaries[0]
	if b.Name != "hanzo-sdk" {
		t.Errorf("name = %q, want the recipe entry name hanzo-sdk", b.Name)
	}
	if b.URL != "https://s3.hanzo.ai/plugins/hanzoai/demo/v1/sdk-1.0.0.tgz" {
		t.Errorf("url = %q — the FILE belongs in the url, not in name", b.URL)
	}
	if b.SHA256 == "" || idx.Repo != "hanzoai/demo" || idx.Tag != "v1" {
		t.Errorf("index = %+v", idx)
	}
}

type artifactIndex struct {
	Repo, Tag string
	Binaries  []struct{ Name, OS, Arch, URL, SHA256 string }
}

// runArtifactScripts runs the build half and then the publish half over one
// workspace, so the meta.txt hand-off between them is the thing under test
// rather than something asserted about. The publisher runs unmodified, curl and
// all, against an in-process object store.
func runArtifactScripts(t *testing.T, src, name, run, out string) artifactIndex {
	t.Helper()
	w := buildArtifacts(t, src, name, run, out)
	o, err := publishArtifacts(t, w, newObjectStore(t))
	if err != nil {
		t.Fatalf("publish: %v\n%s", err, o)
	}
	raw, rerr := os.ReadFile(filepath.Join(w, "dist", "binaries.json"))
	if rerr != nil {
		t.Fatalf("no index written: %v\n%s", rerr, o)
	}
	var idx artifactIndex
	if err := json.Unmarshal(raw, &idx); err != nil {
		t.Fatalf("index is not JSON: %v\n%s", err, raw)
	}
	t.Logf("index: %s", raw)
	return idx
}

func buildArtifacts(t *testing.T, src, name, run, out string) string {
	t.Helper()
	w := t.TempDir()
	build := exec.Command("/bin/sh", "-c", strings.ReplaceAll(artifactBuildScript, "/w/", w+"/"))
	build.Env = append(os.Environ(), "NAME="+name, "MAIN=", "RUN="+run, "OUT="+out,
		"LDFLAGS=", "PLATFORMS=", "REPO_URL="+src, "REF=HEAD", "HOME="+w)
	if o, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, o)
	}
	return w
}

func publishArtifacts(t *testing.T, w string, s *objectStore) ([]byte, error) {
	t.Helper()
	pub := exec.Command("/bin/sh", "-c", strings.ReplaceAll(artifactPublishScript, "/w/", w+"/"))
	pub.Env = append(os.Environ(), "BASE=https://s3.hanzo.ai/plugins/hanzoai/demo/v1",
		"PUT_BASE="+s.url+"/plugins/hanzoai/demo/v1",
		"REPO=hanzoai/demo", "TAG=v1", "S3_REGION=us-east-1",
		"S3_ADMIN_ACCESS_KEY=k", "S3_ADMIN_SECRET_KEY=s")
	return pub.CombinedOutput()
}

// objectStore is S3 as the publisher uses it: a signed PUT whose body must hash
// to its x-amz-checksum-sha256 (400 BadDigest otherwise, as s3.hanzo.ai answers),
// GET of the bytes, and HEAD that returns the stored sum under
// x-amz-checksum-mode: ENABLED. A test can mark one object missing (404), denied
// (403), corrupt (other bytes and another sum) or unsummed (no sum kept).
type objectStore struct {
	url     string
	mu      sync.Mutex
	objects map[string][]byte
	sums    map[string]string
	faults  map[string]string // object → missing | denied | corrupt | unsummed
}

func newObjectStore(t *testing.T) *objectStore {
	s := &objectStore{objects: map[string][]byte{}, sums: map[string]string{}, faults: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
			http.Error(w, "unsigned", http.StatusForbidden)
			return
		}
		f := path.Base(r.URL.Path)
		s.mu.Lock()
		defer s.mu.Unlock()
		if r.Method == http.MethodPut {
			b, _ := io.ReadAll(r.Body)
			d := sha256.Sum256(b)
			if sum := base64.StdEncoding.EncodeToString(d[:]); r.Header.Get("x-amz-checksum-sha256") != sum {
				http.Error(w, "<Error><Code>BadDigest</Code></Error>", http.StatusBadRequest)
				return
			}
			s.objects[f], s.sums[f] = b, r.Header.Get("x-amz-checksum-sha256")
			return
		}
		b, ok := s.objects[f]
		sum := s.sums[f]
		switch s.faults[f] {
		case "missing":
			ok = false
		case "denied":
			http.Error(w, "AccessDenied", http.StatusForbidden)
			return
		case "corrupt":
			b = append(append([]byte{}, b...), 'x')
			d := sha256.Sum256(b)
			sum = base64.StdEncoding.EncodeToString(d[:])
		case "unsummed":
			sum = ""
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		if sum != "" && r.Header.Get("x-amz-checksum-mode") == "ENABLED" {
			w.Header().Set("X-Amz-Checksum-Sha256", sum)
		}
		switch r.Method {
		case http.MethodHead:
			w.Header().Set("Content-Length", fmt.Sprint(len(b)))
		case http.MethodGet:
			w.Write(b)
		default:
			http.Error(w, r.Method, http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(srv.Close)
	s.url = srv.URL
	return s
}

// The publisher asks the store, with its own credential, for each artifact's
// stored sha256 and for the index's bytes, and fails on an object that is
// missing, unreadable, unsummed or not what the index says.
func TestArtifactPublish_ReadsBackEveryObjectItWrote(t *testing.T) {
	kit.Tool(t, "sha256sum")
	src := t.TempDir()
	write(t, filepath.Join(src, "pack.sh"), "#!/bin/sh\nmkdir -p dist\n"+
		"for f in o11y-linux-amd64 iam-linux-amd64; do echo \"$f\" > \"dist/$f\"; done\n")
	git(t, src, "init", "-q")
	git(t, src, "add", "-A")
	git(t, src, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "src")

	o, err := publishArtifacts(t, buildArtifacts(t, src, "plugins", "sh pack.sh", "dist/*"), newObjectStore(t))
	if err != nil || !strings.Contains(string(o), "the store's sha256 for 2 artifacts: every one matches the index") {
		t.Fatalf("clean store: err=%v\n%s", err, o)
	}
	for _, c := range []struct{ fault, object, want string }{
		{"missing", "iam-linux-amd64", "read-back: iam-linux-amd64 is missing or unreadable"},
		{"denied", "o11y-linux-amd64", "read-back: o11y-linux-amd64 is missing or unreadable"},
		{"corrupt", "iam-linux-amd64", "read-back: iam-linux-amd64 is stored with sha256 "},
		{"unsummed", "o11y-linux-amd64", "read-back: o11y-linux-amd64 is stored with sha256 none"},
		{"missing", "binaries.json", "read-back: binaries.json is missing or unreadable"},
		{"corrupt", "binaries.json", "read-back: binaries.json holds other bytes"},
	} {
		s := newObjectStore(t)
		s.faults[c.object] = c.fault
		o, err := publishArtifacts(t, buildArtifacts(t, src, "plugins", "sh pack.sh", "dist/*"), s)
		if err == nil || !strings.Contains(string(o), c.want) {
			t.Errorf("%s %s: err=%v, want a failure saying %q\n%s", c.fault, c.object, err, c.want, o)
		}
	}
}

// An UNDECLARED recipe field is OMITTED, never sent empty.
//
// This is the invariant behind three separate outages, each of which reported
// success: an empty PLATFORMS defeated a makefile's conditional default ("0
// binaries for 0 platforms"), and the index recorded a triple it did not have.
// An unset variable lets the recipe's own default stand; an empty one overrides
// it with nothing, which is the one answer that is never true.
//
// The script reads every optional field as ${VAR:-}, so absence is legal under
// `set -u` — that pairing is what makes omission safe, and both halves are
// asserted here so neither can be undone alone.
func TestRecipeEnv_OmitsWhatTheRecipeDidNotDeclare(t *testing.T) {
	names := func(b binarySpec) map[string]string {
		got := map[string]string{}
		for _, e := range recipeEnv("https://git.hanzo.ai/hanzoai/cloud", "v1", b) {
			m := e.(map[string]any)
			got[m["name"].(string)] = m["value"].(string)
		}
		return got
	}

	// A run: recipe declares no platforms, no main, no ldflags.
	run := names(binarySpec{Name: "plugins", Run: "make dist", Out: "dist/*"})
	for _, absent := range []string{"PLATFORMS", "MAIN", "LDFLAGS"} {
		if v, ok := run[absent]; ok {
			t.Errorf("%s must be OMITTED for a run: recipe, got %q — an empty value defeats the recipe's own default", absent, v)
		}
	}
	for k, want := range map[string]string{"RUN": "make dist", "OUT": "dist/*", "NAME": "plugins"} {
		if run[k] != want {
			t.Errorf("%s = %q, want %q", k, run[k], want)
		}
	}

	// The Go lane declares them, so it gets them.
	gol := names(binarySpec{Name: "cloud", Main: "./cmd/cloud", Ldflags: "-s -w", Platforms: []string{"linux/amd64", "linux/arm64"}})
	if gol["PLATFORMS"] != "linux/amd64 linux/arm64" || gol["MAIN"] != "./cmd/cloud" || gol["LDFLAGS"] != "-s -w" {
		t.Errorf("the Go lane must receive what it declared, got %v", gol)
	}
	if _, ok := gol["RUN"]; ok {
		t.Error("RUN must be omitted for the Go lane")
	}

	// The workspace is the JOB's own knowledge, always stated.
	for _, k := range []string{"HOME", "GOPATH", "npm_config_cache", "REPO_URL", "REF", "GOTOOLCHAIN"} {
		if run[k] == "" {
			t.Errorf("%s must always be set", k)
		}
	}

	// Absence must be LEGAL: the script is `set -eu`, so every optional read has
	// to tolerate an unset variable or omitting it turns into a fatal error.
	for _, v := range []string{"MAIN", "RUN", "OUT", "LDFLAGS", "PLATFORMS"} {
		if !strings.Contains(artifactBuildScript, "${"+v+":-}") {
			t.Errorf("the script must read $%s as ${%s:-} — it runs under set -u and the field may be absent", v, v)
		}
	}
}

// A hanzoai module is fetched from the forge only when the forge holds its
// repository and everything go.sum pins from it: each release tag (a module
// below the root is released by `<dir>/vX`) and each commit a pseudo-version
// names. Every other module comes from github.com. Routing the whole prefix to
// the forge failed a build on the first module nobody had mirrored
// (github.com/hanzoai/agent), a mirror that lags a tag fails it the same way,
// and so does a copy missing the commit a pseudo-version pins.
func TestModuleRoutesSendOnlyWhatTheForgeHolds(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		for _, bin := range []string{"sh", "git", "sed", "sort", "grep", "awk", "cut", "curl"} {
			if _, err := exec.LookPath(bin); err != nil {
				t.Fatalf("%s is not on PATH; the prepare step runs these", bin)
			}
		}
		forge := t.TempDir()
		repo := func(name string, tags ...string) {
			dir := filepath.Join(forge, "hanzoai", name)
			for _, args := range [][]string{
				{"init", "-q", dir},
				{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "x"},
			} {
				if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, out)
				}
			}
			for _, tag := range tags {
				if out, err := exec.Command("git", "-C", dir, "tag", tag).CombinedOutput(); err != nil {
					t.Fatalf("git tag %s: %v\n%s", tag, err, out)
				}
			}
		}
		repo("account", "v1.0.1")
		repo("agent", "v1.0.10")
		repo("iam", "v1.34.116")
		repo("sdk", "v1.0.10")
		repo("kit", "sub/v0.3.0")
		repo("dbx")
		repo("git")

		// The forge API answers for the commits it holds: dbx's, and not git's.
		var asked []string
		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			asked = append(asked, r.URL.Path)
			if r.URL.Path == "/repos/hanzoai/dbx/git/commits/abcdef123456" {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{}`))
				return
			}
			http.NotFound(w, r)
		}))
		defer api.Close()

		work := t.TempDir()
		gosum := strings.Join([]string{
			"github.com/hanzoai/account v1.0.1 h1:x=",
			"github.com/hanzoai/account v1.0.1/go.mod h1:x=",
			"github.com/hanzoai/agent v1.0.10 h1:x=",
			"github.com/hanzoai/agents v0.2.0 h1:x=",
			"github.com/hanzoai/iam v1.34.117 h1:x=",
			"github.com/hanzoai/sdk v1.0.1 h1:x=",
			"github.com/hanzoai/kit/sub v0.3.0 h1:x=",
			"github.com/hanzoai/dbx v0.0.0-20260101010101-abcdef123456 h1:x=",
			"github.com/" + "hanzoai/git/modules/actions/runner v0.0.0-20260911002917-df2a572e7f53 h1:x=",
			"github.com/hanzoai/vfs v1.0.0/go.mod h1:x=",
			"github.com/luxfi/log v1.0.0 h1:x=",
		}, "\n") + "\n"
		if err := os.WriteFile(filepath.Join(work, "go.sum"), []byte(gosum), 0o644); err != nil {
			t.Fatal(err)
		}

		script := "set -eu\nexport GIT_CONFIG_COUNT=0\n" + artifactModuleRoutes + `
for r in account agent agents iam sdk kit dbx git vfs; do
  echo "$r $(git ls-remote --get-url https://github.com/hanzoai/$r)"
done
`
		cmd := exec.Command("sh", "-c", script)
		cmd.Dir = work
		cmd.Env = append(os.Environ(), "FORGE_URL=file://"+forge, "FORGE_API="+api.URL,
			"GIT_CONFIG_NOSYSTEM=1", "HOME="+t.TempDir(), "GIT_TOKEN=")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("module routes: %v\n%s", err, out)
		}
		got := map[string]string{}
		for _, line := range strings.Split(string(out), "\n") {
			if f := strings.Fields(line); len(f) == 2 {
				got[f[0]] = f[1]
			}
		}
		onForge := func(r string) string { return "file://" + forge + "/hanzoai/" + r }
		onGitHub := func(r string) string { return "https://github.com/hanzoai/" + r }
		for r, want := range map[string]string{
			"account": onForge("account"), // held, with its tag
			"agent":   onForge("agent"),   // held, with its tag
			"agents":  onGitHub("agents"), // not held; the entry for agent must not rewrite it
			"iam":     onGitHub("iam"),    // held, but the pinned tag has not reached the mirror
			"sdk":     onGitHub("sdk"),    // v1.0.1 is not v1.0.10: a tag matches whole
			"kit":     onForge("kit"),     // a module below the root is released by sub/v0.3.0
			"dbx":     onForge("dbx"),     // held, and the forge has the pseudo-version's commit
			"git":     onGitHub("git"),    // held, but the pseudo-version's commit is not in the copy
			"vfs":     onGitHub("vfs"),    // never mirrored
		} {
			if got[r] != want {
				t.Errorf("github.com/hanzoai/%s is fetched from %q, want %q\n%s", r, got[r], want, out)
			}
		}
		if !slices.Contains(asked, "/repos/hanzoai/git/git/commits/df2a572e7f53") {
			t.Errorf("the pseudo-version's commit was never asked of the forge API: %v", asked)
		}
		if strings.Contains(string(out), "luxfi") {
			t.Errorf("a module outside github.com/hanzoai was routed:\n%s", out)
		}
	})
}

// The artifact build resolves modules exactly as the image build does. Two
// module environments meant a pseudo-version the proxy serves built into the
// image and failed pf-artifact with `unknown revision`, because this one had
// sent every hanzoai module straight to git.
func TestPrepareResolvesModulesAsTheImageBuildDoes(t *testing.T) {
	b, err := os.ReadFile("../../Dockerfile")
	if err != nil {
		t.Fatalf("read the Dockerfile: %v", err)
	}
	image := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), "\\"))
		line = strings.TrimPrefix(line, "ENV ")
		for _, kv := range moduleEnv {
			k := kv[:strings.Index(kv, "=")+1]
			if strings.HasPrefix(line, k) {
				image[k] = strings.TrimSpace(line)
			}
		}
	}
	for _, kv := range moduleEnv {
		k := kv[:strings.Index(kv, "=")+1]
		if image[k] != kv {
			t.Errorf("the image build states %q, the artifact build %q", image[k], kv)
		}
	}
	for _, kv := range moduleEnv {
		if !strings.Contains(artifactPrepareScript, "'"+kv+"'") {
			t.Errorf("the prepare step does not export %s", kv)
		}
	}
}
