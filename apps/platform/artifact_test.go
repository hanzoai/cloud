package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// The artifact lane accepts a hanzo.yml `binaries:` recipe verbatim and answers
// with the index URL a host will read — the ci lane's layout, byte for byte.
func TestRunnerArtifact_LaunchesAndIndexes(t *testing.T) {
	t.Setenv("PLATFORM_BUILD_CALLBACK_TOKEN", testBuildTok)
	app := runnerApp(t)
	code, body := postRunner(t, app, testBuildTok, map[string]any{
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
}

// THE RECIPE THE RELEASE SENDS, checked here rather than by a red release.
//
// .hanzo/workflows/cicd.yml's `plugins` job POSTs exactly this to publish the
// plugin set for a release. Every field in it is one this endpoint VALIDATES — the
// git host, the flat tag segment, the recipe's own shape — so a typo there is a
// 400 nobody sees until a tag build, and the artifacts for that release simply
// never exist. Keeping the body here makes it a compile-and-test-time fact.
//
// It also pins the two things a host depends on: the publish layout is keyed by
// the TAG (so `CLOUD_PLUGINS` for v1.801.533 names that release and not the
// commit), and the forge — not the mirror — is an accepted git host, which
// matters because the tag a release claims exists only on the forge and the
// build Job clones with no credential.
func TestRunnerArtifact_TheReleaseRecipeIsAccepted(t *testing.T) {
	t.Setenv("PLATFORM_BUILD_CALLBACK_TOKEN", testBuildTok)

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
	code, body := postRunner(t, app, testBuildTok, map[string]any{
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
		t.Fatalf("index = %q, want %q — this is the URL CLOUD_PLUGINS is pointed at", resp.Index, want)
	}
}

// The other half, so the deployment requirement above is a fact and not a
// comment: with no domain and no CLOUD_PLATFORM_GIT_HOSTS, the release recipe is
// REFUSED. This is the production configuration as measured, and it is why the
// `plugins` job carries the env var it does. The day the forge is trusted by
// some other derivation, this test goes red and says so.
func TestRunnerArtifact_ForgeIsRefusedUntilTheDeploymentTrustsIt(t *testing.T) {
	t.Setenv("PLATFORM_BUILD_CALLBACK_TOKEN", testBuildTok)
	prev := selfGitHost
	selfGitHost = ""
	t.Cleanup(func() { selfGitHost = prev })

	app := runnerApp(t)
	code, body := postRunner(t, app, testBuildTok, map[string]any{
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
	code, body = postRunner(t, app, testBuildTok, map[string]any{
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
	job := k.artifactJobSpec("pf-artifact-x", "https://github.com/hanzoai/cloud", "v1.2.3", "v1.2.3", "https://s3.hanzo.ai/plugins/hanzoai/cloud/v1.2.3", "http://s3.hanzo.svc:9000/plugins/hanzoai/cloud/v1.2.3", bins)
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

// Bad recipes and lane confusion are 400s, not builds.
func TestRunnerArtifact_Rejects(t *testing.T) {
	t.Setenv("PLATFORM_BUILD_CALLBACK_TOKEN", testBuildTok)
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
		if code, body := postRunner(t, app, testBuildTok, tc.body); code != tc.want {
			t.Errorf("%s: want %d, got %d (%s)", tc.name, tc.want, code, body)
		}
	}
}

// An IAM org-admin publishes artifacts only for a forge owner its own org owns —
// the artifact-lane twin of the registry-namespace binding (H1).
func TestRunnerArtifact_IAMAdminBoundToItsOwnOwner(t *testing.T) {
	t.Setenv("PLATFORM_BUILD_CALLBACK_TOKEN", "")
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
}

// The recipe INTERPRETER is a shell script, so it is tested by running it — both
// lanes, against a real git repo, producing real files. This is what proves the
// declaration actually builds something rather than merely parsing.
func TestArtifactBuildScript_BuildsBothLanes(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain")
	}
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

// launchArtifactBuild is bounded by the SAME per-org build ceiling as the image
// lane — an artifact build cannot outrun a container build.
func TestLaunchArtifactBuild_SharesTheBuildCeiling(t *testing.T) {
	k := fakeK8s()
	bins := []binarySpec{{Name: "x", Main: ".", Platforms: []string{"linux/amd64"}, Image: defaultToolchainImage}}
	for i := 0; i < k.limits.maxConcurrentBuilds(); i++ {
		if _, err := k.launchArtifactBuild(context.Background(), "https://github.com/hanzoai/cloud", "v1", "v1", "https://s3/x", "http://s3/x", bins, "bld_"+string(rune('a'+i))); err != nil {
			t.Fatalf("launch %d: %v", i, err)
		}
	}
	if _, err := k.launchArtifactBuild(context.Background(), "https://github.com/hanzoai/cloud", "v1", "v1", "https://s3/x", "http://s3/x", bins, "bld_over"); err != errTooManyBuilds {
		t.Fatalf("over the ceiling: want errTooManyBuilds, got %v", err)
	}
	list, _ := k.dyn.Resource(jobsGVR).Namespace(k.buildNS).List(context.Background(), metav1.ListOptions{})
	if len(list.Items) != k.limits.maxConcurrentBuilds() {
		t.Fatalf("launched %d Jobs, want %d", len(list.Items), k.limits.maxConcurrentBuilds())
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
		if _, err := k.launchArtifactBuild(context.Background(), tc.repo, tc.ref, "v1", "https://s3/x", "http://s3/x", bins, "bld_x"); err == nil {
			t.Fatalf("%s: launched a build from %q@%q", tc.name, tc.repo, tc.ref)
		}
		list, _ := k.dyn.Resource(jobsGVR).Namespace(k.buildNS).List(context.Background(), metav1.ListOptions{})
		if len(list.Items) != 0 {
			t.Fatalf("%s: %d Jobs created for a source it refused", tc.name, len(list.Items))
		}
	}
	k := fakeK8s()
	if _, err := k.launchArtifactBuild(context.Background(), "https://github.com/hanzoai/cloud", "v1.2.3", "v1.2.3", "https://s3/x", "http://s3/x", bins, "bld_ok"); err != nil {
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
	if _, err := exec.LookPath("sha256sum"); err != nil {
		t.Skip("no sha256sum")
	}
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
	if _, err := exec.LookPath("sha256sum"); err != nil {
		t.Skip("no sha256sum")
	}
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
// rather than something asserted about. `put` is stubbed and the readback
// satisfied: what is being read is the index it composes, not curl.
func runArtifactScripts(t *testing.T, src, name, run, out string) artifactIndex {
	t.Helper()
	w := t.TempDir()
	build := exec.Command("/bin/sh", "-c", strings.ReplaceAll(artifactBuildScript, "/w/", w+"/"))
	build.Env = append(os.Environ(), "NAME="+name, "MAIN=", "RUN="+run, "OUT="+out,
		"LDFLAGS=", "PLATFORMS=", "REPO_URL="+src, "REF=HEAD", "HOME="+w)
	if o, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, o)
	}
	script := strings.ReplaceAll(artifactPublishScript, "/w/", w+"/")
	script = strings.Replace(script, "put() {", "put() { :; }\nunused() {", 1)
	script = strings.Replace(script, `code="$(curl -s -o /dev/null -w '%{http_code}' "$PUT_BASE/binaries.json")"`, `code=200`, 1)
	pub := exec.Command("/bin/sh", "-c", script)
	pub.Env = append(os.Environ(), "BASE=https://s3.hanzo.ai/plugins/hanzoai/demo/v1",
		"PUT_BASE=http://s3.hanzo.svc:9000/plugins/hanzoai/demo/v1",
		"REPO=hanzoai/demo", "TAG=v1", "S3_REGION=us-east-1",
		"S3_ADMIN_ACCESS_KEY=k", "S3_ADMIN_SECRET_KEY=s")
	o, err := pub.CombinedOutput()
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
	for _, k := range []string{"HOME", "GOPATH", "npm_config_cache", "REPO_URL", "REF"} {
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
