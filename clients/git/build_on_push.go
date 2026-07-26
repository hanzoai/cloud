package git

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/hanzoai/cloud"
	"gopkg.in/yaml.v2"
)

// build_on_push.go is the NATIVE CI/CD orchestrator: a git-lifecycle reactor that
// turns a push landed on native Hanzo Git into a build (+ deploy) on OUR executor,
// with NO GitHub Actions in the loop. It reads the repo's pipeline from
// `.hanzo/workflows/*.yml` (native-first), or the root `hanzo.yml` (the SAME schema,
// GitHub-Actions-compatible location) as a fallback, at the pushed commit — then
// enqueues each declared image to platform.hanzo.ai's in-cluster BuildKit via the
// ONE direct-build front door (`/v1/arcd/enqueue`). That is the SAME build muscle
// the platform GitHub-App webhook and the `hanzoai/ci mode:delegate` path drive:
// platform builds, pushes to the registry, and patches the operator Service CR, and
// the operator deploys. One build path — now with a THIRD front door: a native push.
//
// It is the native twin of `.github/workflows/cicd.yml`: the exact same `hanzo.yml`
// (images/test/deploy) config, sourced from native git and triggered by a native
// push instead of a GitHub event. Eventually it REPLACES the GitHub caller; during
// the transition both work (a repo may carry `.github/workflows/cicd.yml` AND
// `.hanzo/workflows/`, and the two enqueue identical image tags so they converge,
// never diverge).
//
// SHIPS DORMANT — the exact env-gated, fail-safe-default idiom the sync reconcile
// scheduler uses. buildOnPush no-ops unless CLOUD_NATIVE_CICD_ENABLED is truthy AND
// the enqueue token is present, so LINKING it can never perturb the cloud writer.
// Flip the env on the git App CR to activate, once validated end-to-end.
//
// Best-effort + detached: it rides the lifecycle fan-out (EmitLifecycle dispatches
// each subscriber in its own cancel-immune goroutine with a panic-recover), so it
// can neither block nor fail the git/deploy path. Durability (enqueue as a durable
// task on the embedded engine, retried on failure — the twin of index_on_push.go)
// is the documented follow-up; the MVP enqueues inline, and the platform build row
// is itself the durable record of record once accepted.

const (
	// nativeCICDEnabledEnv arms the orchestrator. Empty/"0"/"off"/"false" DISABLES
	// it (the fail-safe default) — the binary ships dormant and native CI/CD is
	// turned on by setting this on the git App CR env, exactly like the sync
	// scheduler's CLOUD_SYNC_RECONCILE_INTERVAL gate.
	nativeCICDEnabledEnv = "CLOUD_NATIVE_CICD_ENABLED"

	// enqueueTokenEnv is the machine-to-machine bearer for platform's direct build
	// webhook — the SAME credential name `/v1/arcd/enqueue` and `/v1/build-callback`
	// check (PLATFORM_BUILD_CALLBACK_TOKEN), KMS-sourced into the CR env, never
	// hardcoded. Absent ⇒ the orchestrator stays dormant (no unauthenticated POST).
	enqueueTokenEnv = "PLATFORM_BUILD_CALLBACK_TOKEN"

	// enqueueURLEnv overrides the platform direct-build endpoint; defaults to the
	// production front door. One URL, one build path.
	enqueueURLEnv     = "CLOUD_NATIVE_CICD_ENQUEUE_URL"
	defaultEnqueueURL = "https://platform.hanzo.ai/v1/arcd/enqueue"
)

// pipeline is the slice of the `hanzo.yml` / `.hanzo/workflows/*.yml` schema the
// orchestrator acts on: the images to build. Deploy is intentionally NOT read here
// — platform's build-completion gates the rollout per the same `hanzo.yml` deploy
// block (one deploy policy, evaluated once, on the executor), so the orchestrator
// only needs to know WHAT to build; WHERE it rolls stays platform's decision.
type pipeline struct {
	Images []pipelineImage `yaml:"images"`
}

// pipelineImage mirrors one `images:` entry (the multi-image hanzo.yml form). The
// field names are the canonical schema the platform TS validator and the ci
// reusable already read — one schema, three readers.
type pipelineImage struct {
	Name       string `yaml:"name"`
	Repo       string `yaml:"repo"`        // bare registry repo, e.g. ghcr.io/hanzoai/foo
	Context    string `yaml:"context"`     // build context dir (default ".")
	Dockerfile string `yaml:"dockerfile"`  // default "<context>/Dockerfile"
	TagSuffix  string `yaml:"tag-suffix"`  // default = name
}

// enqueueReq is platform's /v1/arcd/enqueue body (EnqueueBody). Field-for-field the
// same shape the `hanzoai/ci mode:delegate` step POSTs, so a native-push build and a
// delegated GitHub-Actions build are byte-identical downstream — one build path.
type enqueueReq struct {
	Repo       string `json:"repo"`  // GitHub owner/repo — BuildKit's clone context
	SHA        string `json:"sha"`   // full commit the build pins to
	Image      string `json:"image"` // full pushed ref repo:tag (we own the tag)
	Branch     string `json:"branch,omitempty"`
	Ref        string `json:"ref,omitempty"`
	Dockerfile string `json:"dockerfile,omitempty"`
	Context    string `json:"context,omitempty"`
	OS         string `json:"os,omitempty"`
	Arch       string `json:"arch,omitempty"`
}

// nativeCICDEnabled reports whether the orchestrator is armed: the enable flag is
// truthy AND the enqueue token is present. Both are required — the flag is the
// operator's intent, the token is the capability; without either the reactor is a
// no-op, so the binary is safe to ship with the reactor linked but dormant.
func nativeCICDEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(nativeCICDEnabledEnv))) {
	case "", "0", "off", "false", "no":
		return false
	}
	return strings.TrimSpace(os.Getenv(enqueueTokenEnv)) != ""
}

// buildOnPush is the reactor. On a default-branch push that carries a native
// pipeline, it enqueues one build per declared image. Gated dormant; default branch
// only (a feature-branch/PR pipeline is a documented follow-up — the deploy-relevant
// slice is the canonical branch, matching index_on_push's default-branch gate).
func buildOnPush(s *cloud.Service[state], ctx context.Context, ev cloud.LifecycleEvent) {
	if !nativeCICDEnabled() || ev.Kind != cloud.LifecyclePushLanded || ev.Org == "" || ev.Repo == "" || ev.Branch == "" {
		return
	}
	repo, err := openRepoForIndex(s, ev.Org, ev.Project, ev.Repo)
	if err != nil {
		s.Log.Warn("native ci/cd: open repo", "org", ev.Org, "repo", ev.Repo, "err", err)
		return
	}
	if !isDefaultBranch(repo, ev.Branch) {
		return // only the canonical branch builds+deploys in the MVP
	}
	pl, path, err := readPipeline(repo, ev.After)
	if err != nil {
		s.Log.Warn("native ci/cd: read pipeline", "org", ev.Org, "repo", ev.Repo, "err", err)
		return
	}
	if pl == nil || len(pl.Images) == 0 {
		return // no native pipeline in this repo — nothing for the orchestrator to run
	}
	enqueued, failed := enqueuePipeline(ctx, s, ev, pl)
	s.Log.Info("native ci/cd: enqueued", "org", ev.Org, "repo", ev.Repo, "config", path,
		"images", len(pl.Images), "enqueued", enqueued, "failed", failed, "commit", shortSHA(ev.After))
}

// readPipeline resolves the repo's native pipeline at the pushed commit: it merges
// the images of every `.hanzo/workflows/*.yml|*.yaml` file (the native-first
// location), and falls back to the root `hanzo.yml` when that directory is absent or
// declares no image. Returns the parsed pipeline + the config path it came from
// (for the log), or (nil,"",nil) when the repo carries no native pipeline at all.
func readPipeline(repo *gogit.Repository, after string) (*pipeline, string, error) {
	commit, err := resolveIndexCommit(repo, after)
	if err != nil {
		return nil, "", err
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil, "", err
	}
	// Native-first: every workflow file under .hanzo/workflows/, images merged.
	if sub, err := tree.Tree(".hanzo/workflows"); err == nil {
		merged := &pipeline{}
		var from []string
		for _, e := range sub.Entries {
			name := strings.ToLower(e.Name)
			if !strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml") {
				continue
			}
			f, ferr := sub.TreeEntryFile(&e)
			if ferr != nil {
				continue
			}
			txt, cerr := f.Contents()
			if cerr != nil {
				continue
			}
			var p pipeline
			if yaml.Unmarshal([]byte(txt), &p) == nil && len(p.Images) > 0 {
				merged.Images = append(merged.Images, p.Images...)
				from = append(from, e.Name)
			}
		}
		if len(merged.Images) > 0 {
			return merged, ".hanzo/workflows/{" + strings.Join(from, ",") + "}", nil
		}
	}
	// Fallback: the root hanzo.yml (GitHub-Actions-compatible location).
	if pl := pipelineFromTreeFile(tree, "hanzo.yml"); pl != nil {
		return pl, "hanzo.yml", nil
	}
	return nil, "", nil
}

// pipelineFromTreeFile parses one tree file as a pipeline, returning nil when the
// file is absent, unreadable, binary, or declares no image.
func pipelineFromTreeFile(tree *object.Tree, path string) *pipeline {
	f, err := tree.File(path)
	if err != nil {
		return nil
	}
	if bin, _ := f.IsBinary(); bin {
		return nil
	}
	txt, err := f.Contents()
	if err != nil {
		return nil
	}
	var p pipeline
	if yaml.Unmarshal([]byte(txt), &p) != nil || len(p.Images) == 0 {
		return nil
	}
	return &p
}

// enqueuePipeline POSTs one direct-build request per image and returns
// (enqueued, failed). Best-effort: each image is independent, a failure is logged
// (never the token) and never aborts the others. The bearer + URL come from env
// (KMS-sourced token); the client carries a hard timeout so a slow platform can
// never wedge the reactor goroutine.
func enqueuePipeline(ctx context.Context, s *cloud.Service[state], ev cloud.LifecycleEvent, pl *pipeline) (int, int) {
	token := strings.TrimSpace(os.Getenv(enqueueTokenEnv))
	url := strings.TrimSpace(os.Getenv(enqueueURLEnv))
	if url == "" {
		url = defaultEnqueueURL
	}
	ghRepo := githubOwnerFor(ev.Org) + "/" + normalizeName(ev.Repo)
	client := &http.Client{Timeout: 20 * time.Second}

	enqueued, failed := 0, 0
	for _, img := range pl.Images {
		body := enqueueBody(img, ghRepo, ev.Branch, ev.After)
		if body == nil {
			failed++ // an image with no repo can't be tagged/pushed — skip, count it
			s.Log.Warn("native ci/cd: image missing repo", "org", ev.Org, "repo", ev.Repo, "image", img.Name)
			continue
		}
		if err := postEnqueue(ctx, client, url, token, body); err != nil {
			failed++
			s.Log.Warn("native ci/cd: enqueue failed", "org", ev.Org, "repo", ev.Repo, "image", body.Image, "err", err)
			continue
		}
		enqueued++
		s.Log.Info("native ci/cd: build queued", "org", ev.Org, "repo", ev.Repo, "image", body.Image)
	}
	return enqueued, failed
}

// enqueueBody builds the /v1/arcd/enqueue request for one image. The image tag
// (`sha-<short7>-amd64[-<suffix>]`) is the SAME deterministic shape the ci
// mode:delegate path emits, so a native-push build and a delegated build produce the
// identical ref — the two front doors converge, never fork a tag. Returns nil for an
// image with no `repo` (nothing to push to).
func enqueueBody(img pipelineImage, ghRepo, branch, sha string) *enqueueReq {
	repo := strings.TrimSpace(img.Repo)
	if repo == "" {
		return nil
	}
	ctxDir := strings.TrimSpace(img.Context)
	if ctxDir == "" {
		ctxDir = "."
	}
	dockerfile := strings.TrimSpace(img.Dockerfile)
	if dockerfile == "" {
		dockerfile = ctxDir + "/Dockerfile"
	}
	suffix := strings.TrimSpace(img.TagSuffix)
	if suffix == "" {
		suffix = strings.TrimSpace(img.Name)
	}
	tag := "sha-" + shortSHA(sha) + "-amd64"
	if suffix != "" {
		tag += "-" + suffix
	}
	return &enqueueReq{
		Repo:       ghRepo,
		SHA:        sha,
		Image:      repo + ":" + tag,
		Branch:     branch,
		Ref:        "refs/heads/" + branch,
		Dockerfile: dockerfile,
		Context:    ctxDir,
		OS:         "linux",
		Arch:       "amd64",
	}
}

// postEnqueue POSTs one build request and treats only HTTP 202 (Accepted = queued)
// as success — matching the ci delegate contract. A 409 (no live runner) or any
// other code is an error the caller logs. The token is sent as a bearer, never
// logged.
func postEnqueue(ctx context.Context, client *http.Client, url, token string, body *enqueueReq) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("enqueue %s: HTTP %d", body.Image, resp.StatusCode)
	}
	return nil
}

// brandGitHubOwner maps a native brand org to its GitHub owner — the INVERSE of the
// platform ci's ORG_TO_BRAND (hanzoai→hanzo), used because platform's BuildKit
// clones the build context from github.com/<owner>/<repo>. A native repo is a mirror
// of that GitHub repo (the sync engine keeps them at the same SHA), so building from
// github.com at the pushed commit is equivalent — and NO GitHub Actions runner is
// involved (the build is in-cluster BuildKit). Pointing the context at native git
// (git.hanzo.ai) instead is the documented follow-up (needs platform to accept a
// non-github context URL + the native clone credential). An org that IS its own
// GitHub owner maps to itself — the forward-safe default.
var brandGitHubOwner = map[string]string{
	"hanzo": "hanzoai",
	"lux":   "luxfi",
	"zoo":   "zooai",
}

func githubOwnerFor(org string) string {
	if o, ok := brandGitHubOwner[strings.ToLower(strings.TrimSpace(org))]; ok {
		return o
	}
	return org
}
