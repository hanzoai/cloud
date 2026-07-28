package git

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v2"
)

// build_on_push_test.go proves the PURE core of the native CI/CD orchestrator: the
// hanzo.yml image parse, the deterministic enqueue body/tag (which must match the ci
// mode:delegate shape byte-for-byte), and the brand→GitHub-owner map. The reactor's
// IO (tree read + HTTP enqueue) is exercised end-to-end when armed; these lock the
// contract that makes the two build front doors converge instead of fork.

func TestPipelineParse(t *testing.T) {
	const cfg = `
images:
  - name: api
    repo: ghcr.io/hanzoai/api
    context: ./api
  - name: web
    repo: ghcr.io/hanzoai/web
    tag-suffix: frontend
deploy:
  on: [main]
`
	var p pipeline
	if err := yaml.Unmarshal([]byte(cfg), &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(p.Images) != 2 {
		t.Fatalf("want 2 images, got %d", len(p.Images))
	}
	if p.Images[0].Name != "api" || p.Images[0].Repo != "ghcr.io/hanzoai/api" || p.Images[0].Context != "./api" {
		t.Fatalf("image[0] parsed wrong: %+v", p.Images[0])
	}
	if p.Images[1].TagSuffix != "frontend" {
		t.Fatalf("image[1] tag-suffix want frontend, got %q", p.Images[1].TagSuffix)
	}
}

func TestEnqueueBody(t *testing.T) {
	sha := "abcdef1234567890abcdef1234567890abcdef12"
	// tag-suffix defaults to name; context defaults to "."; dockerfile to <ctx>/Dockerfile.
	b := enqueueBody(pipelineImage{Name: "api", Repo: "ghcr.io/hanzoai/api"}, "hanzoai/api", "main", sha)
	if b == nil {
		t.Fatal("nil body for a valid image")
	}
	// The tag MUST match the ci mode:delegate shape sha-<short7>-amd64[-suffix].
	if b.Image != "ghcr.io/hanzoai/api:sha-abcdef1-amd64-api" {
		t.Fatalf("image ref: %q", b.Image)
	}
	if b.Repo != "hanzoai/api" || b.SHA != sha || b.Branch != "main" || b.Ref != "refs/heads/main" {
		t.Fatalf("body coords wrong: %+v", b)
	}
	if b.Context != "." || b.Dockerfile != "./Dockerfile" || b.OS != "linux" || b.Arch != "amd64" {
		t.Fatalf("body defaults wrong: %+v", b)
	}

	// Explicit context + dockerfile + tag-suffix are honored.
	b2 := enqueueBody(pipelineImage{Name: "web", Repo: "ghcr.io/hanzoai/web", Context: "svc", Dockerfile: "svc/Prod.Dockerfile", TagSuffix: "frontend"}, "hanzoai/web", "main", sha)
	if b2.Image != "ghcr.io/hanzoai/web:sha-abcdef1-amd64-frontend" {
		t.Fatalf("explicit-suffix image ref: %q", b2.Image)
	}
	if b2.Context != "svc" || b2.Dockerfile != "svc/Prod.Dockerfile" {
		t.Fatalf("explicit ctx/dockerfile not honored: %+v", b2)
	}

	// An image with no repo yields nil (nothing to push to) — the reactor counts it
	// failed and moves on, never panicking.
	if enqueueBody(pipelineImage{Name: "x"}, "hanzoai/x", "main", sha) != nil {
		t.Fatal("expected nil body for an image with no repo")
	}
}

func TestGithubOwnerFor(t *testing.T) {
	cases := map[string]string{
		"hanzo": "hanzoai", "HANZO": "hanzoai", " lux ": "luxfi", "zoo": "zooai",
		"acme": "acme", // an unmapped org is its own owner — forward-safe default
	}
	for in, want := range cases {
		if got := githubOwnerFor(in); got != want {
			t.Fatalf("githubOwnerFor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNativeCICDEnabled(t *testing.T) {
	t.Setenv(enqueueTokenEnv, "tok")
	for _, v := range []string{"", "0", "off", "false", "no"} {
		t.Setenv(nativeCICDEnabledEnv, v)
		if nativeCICDEnabled() {
			t.Fatalf("enable=%q should be dormant", v)
		}
	}
	t.Setenv(nativeCICDEnabledEnv, "true")
	if !nativeCICDEnabled() {
		t.Fatal("enable=true + token present should be armed")
	}
	// Armed flag but NO token ⇒ still dormant (never an unauthenticated POST).
	t.Setenv(enqueueTokenEnv, "")
	if nativeCICDEnabled() {
		t.Fatal("no token should be dormant even with enable=true")
	}
}

// TestPipelineMergeShape proves the .hanzo/workflows/ multi-file merge semantics at
// the parse level: two workflow files' images concatenate into one pipeline.
func TestPipelineMergeShape(t *testing.T) {
	build := "images:\n  - { name: api, repo: ghcr.io/hanzoai/api }\n"
	extra := "images:\n  - { name: worker, repo: ghcr.io/hanzoai/worker }\n"
	merged := &pipeline{}
	for _, txt := range []string{build, extra} {
		var p pipeline
		if err := yaml.Unmarshal([]byte(txt), &p); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		merged.Images = append(merged.Images, p.Images...)
	}
	if len(merged.Images) != 2 || merged.Images[0].Name != "api" || merged.Images[1].Name != "worker" {
		t.Fatalf("merge shape wrong: %+v", merged.Images)
	}
	names := []string{}
	for _, i := range merged.Images {
		names = append(names, i.Name)
	}
	if strings.Join(names, ",") != "api,worker" {
		t.Fatalf("merge order wrong: %v", names)
	}
}
