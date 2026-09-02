package git

import (
	"github.com/hanzoai/cloud/internal/environ"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/contract"
)

// build_on_push.go is the NATIVE CI/CD orchestrator: a git-lifecycle reactor that
// turns a push landed on native Hanzo Git into a build (+ deploy) on OUR executor,
// with NO GitHub Actions in the loop. It reads the repo's pipeline from
// `.hanzo/workflows/*.yml` (native-first), or the root `hanzo.yml` (the SAME schema,
// GitHub-Actions-compatible location) as a fallback, at the pushed commit — then
// enqueues each declared image to platform.hanzo.ai's in-cluster BuildKit via the
// ONE direct-build endpoint (`/v1/runner`). That is the SAME build muscle
// the platform GitHub-App webhook and the `hanzoai/ci mode:delegate` path drive:
// platform builds, pushes to the registry, and patches the operator Service CR, and
// the operator deploys. One build path — now with a THIRD entry point: a native push.
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
	// webhook — the SAME credential name `/v1/runner` and `/v1/build-callback`
	// check (PLATFORM_BUILD_CALLBACK_TOKEN), KMS-sourced into the CR env, never
	// hardcoded. Absent ⇒ the orchestrator stays dormant (no unauthenticated POST).
	enqueueTokenEnv = "PLATFORM_BUILD_CALLBACK_TOKEN"

	// enqueueURLEnv overrides the direct-build endpoint; defaults to CLOUD'S OWN.
	//
	// It pointed at https://platform.hanzo.ai/v1/runner — a DIFFERENT deployment,
	// and one that names a different credential: cloud's runner refuses with
	// "invalid build token" and that one with "Invalid enqueue token". This holds
	// PLATFORM_BUILD_CALLBACK_TOKEN, which is the token CLOUD checks
	// (apps/platform/runner.go runnerTokenOK) — measured against production, that
	// token is accepted at /v1/platform/runner and rejected at the other address.
	// So the hop crossed a service boundary to reach the SAME build muscle this
	// binary already carries (apps/platform launchDirectBuild), presenting a
	// credential the far side does not use.
	//
	// One capability, one owner: apps/platform builds, and the address is the one
	// it registers. `hanzo.yml`'s deploy block is still evaluated there, so nothing
	// about WHERE a build rolls moves with this.
	//
	// STILL AN HTTP HOP, and that is the remaining defect rather than the fix: a
	// call from this process to its own edge is the re-entry apps/commerce's
	// transport documents. The destination is a plane op beside plane.PlatformPush
	// — same shape, taking the image list instead of the push — at which point the
	// URL and the shared token both go away.
	enqueueURLEnv     = "CLOUD_NATIVE_CICD_ENQUEUE_URL"
	defaultEnqueueURL = "http://cloud.hanzo.svc.cluster.local:8000/v1/platform/runner"
)

// pipeline is the slice of the contract / `.hanzo/workflows/*.yml` schema the
// orchestrator acts on: the images to build. Deploy is intentionally NOT read here
// — platform's build-completion gates the rollout per the same contract's deploy
// block (one deploy policy, evaluated once, on the executor), so the orchestrator
// only needs to know WHAT to build; WHERE it rolls stays platform's decision.
//
// The fields carry json names because a resolved contract's canonical form is
// JSON, whatever spelling the repository wrote it in (contract.Doc.Into).
type pipeline struct {
	Images []pipelineImage `json:"images"`
}

// pipelineImage mirrors one `images:` entry (the multi-image contract form). The
// field names are the canonical schema the platform TS validator and the ci
// reusable already read — one schema, three readers.
type pipelineImage struct {
	Name       string `json:"name"`
	Repo       string `json:"repo"`       // bare registry repo, e.g. ghcr.io/hanzoai/foo
	Context    string `json:"context"`    // build context dir (default ".")
	Dockerfile string `json:"dockerfile"` // default "<context>/Dockerfile"
	TagSuffix  string `json:"tag-suffix"` // default = name
	// Args are `--build-arg` values for THIS image. They are what makes several
	// entries off ONE Dockerfile mean different things: hanzoai/bot declares
	// three sandbox classes as three entries that differ only by
	// `args: {STAGE: exec|dev|desktop}`. Dropped on the floor, the three tags are
	// three copies of whatever stage the Dockerfile defaults to — an `exec` tag
	// carrying a whole desktop, published under a name that says otherwise.
	Args map[string]string `json:"args"`
}

// enqueueReq is platform's /v1/runner body (EnqueueBody). Field-for-field the
// same shape the `hanzoai/ci mode:delegate` step POSTs, so a native-push build and a
// delegated GitHub-Actions build are byte-identical downstream — one build path.
type enqueueReq struct {
	Repo       string            `json:"repo"`  // GitHub owner/repo — BuildKit's clone context
	SHA        string            `json:"sha"`   // full commit the build pins to
	Image      string            `json:"image"` // full pushed ref repo:tag (we own the tag)
	Branch     string            `json:"branch,omitempty"`
	Ref        string            `json:"ref,omitempty"`
	Dockerfile string            `json:"dockerfile,omitempty"`
	Context    string            `json:"context,omitempty"`
	OS         string            `json:"os,omitempty"`
	Arch       string            `json:"arch,omitempty"`
	Args       map[string]string `json:"args,omitempty"`
}

// nativeCICDEnabled reports whether the orchestrator is armed: the enable flag is
// truthy AND the enqueue token is present. Both are required — the flag is the
// operator's intent, the token is the capability; without either the reactor is a
// no-op, so the binary is safe to ship with the reactor linked but dormant.
func nativeCICDEnabled() bool {
	switch strings.ToLower(environ.Or(nativeCICDEnabledEnv, "")) {
	case "", "0", "off", "false", "no":
		return false
	}
	return environ.Or(enqueueTokenEnv, "") != ""
}

// buildOnPush is the reactor. On a default-branch push that carries a native
// pipeline, it enqueues one build per declared image. Gated dormant; default branch
// only (a feature-branch/PR pipeline is a documented follow-up — the deploy-relevant
// slice is the canonical branch, matching index_on_push's default-branch gate).
func buildOnPush(s *cloud.Service[state], ctx context.Context, ev cloud.LifecycleEvent) {
	if !nativeCICDEnabled() || ev.Kind != cloud.LifecyclePushLanded || ev.Org == "" || ev.Repo == "" || ev.Branch == "" {
		return
	}
	repo, err := openRepository(s, Repo{Org: ev.Org, Project: ev.Project, Name: ev.Repo})
	if err != nil {
		s.Log.Warn("native ci/cd: open repo", "org", ev.Org, "repo", ev.Repo, "err", err)
		return
	}
	if !isDefaultBranch(ctx, repo, ev.Branch) {
		return // only the canonical branch builds+deploys in the MVP
	}
	pl, path, err := readPipeline(ctx, repo, ev.After)
	if err != nil {
		level(s, err)("native ci/cd: read pipeline", "org", ev.Org, "repo", ev.Repo, "err", err)
		return
	}
	if pl == nil || len(pl.Images) == 0 {
		return // no native pipeline in this repo — nothing for the orchestrator to run
	}
	enqueued, failed := enqueuePipeline(ctx, s, ev, pl)
	s.Log.Info("native ci/cd: enqueued", "org", ev.Org, "repo", ev.Repo, "config", path,
		"images", len(pl.Images), "enqueued", enqueued, "failed", failed, "commit", shortSHA(ev.After))
}

// level says how loudly a failed pipeline read is reported. Two contracts is a
// MISCONFIGURATION and not a bad moment: it stops this repository building on every
// push until somebody deletes a file, and nothing downstream ever finds out — the
// reactor is best-effort by design, so this line is the only account of it. A read
// that failed once clears on the next push. One line, two levels, because the two
// want two different answers from whoever is reading the log.
func level(s *cloud.Service[state], err error) func(string, ...interface{}) {
	if errors.Is(err, contract.ErrMany) {
		return s.Log.Error
	}
	return s.Log.Warn
}

// readPipeline resolves the repo's native pipeline at the pushed commit: it merges
// the images of every `.hanzo/workflows/*.yml|*.yaml` file (the native-first
// location), and falls back to the repo's own contract — in whichever spelling it
// is written — when that directory is absent or declares no image. Returns the
// parsed pipeline + the file it came from (for the log), or (nil,"",nil) when the
// repo carries no native pipeline at all.
func readPipeline(ctx context.Context, repo Repository, after string) (*pipeline, string, error) {
	rev, _, err := repo.Resolve(ctx, after)
	if err != nil {
		return nil, "", err
	}
	// Native-first: every workflow file under .hanzo/workflows/, images merged.
	if entries, err := repo.Tree(ctx, rev, ".hanzo/workflows"); err == nil {
		merged := &pipeline{}
		var from []string
		for _, e := range entries {
			name := strings.ToLower(e.Name)
			if e.Dir || (!strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml")) {
				continue
			}
			// A link is not a workflow, for the reason blobs() says: its bytes are
			// a path, and a path that happens to parse would declare an image no
			// file in this tree carries.
			b, berr := repo.Blob(ctx, rev, e.Path, contract.Max)
			if berr != nil || b.Link || b.Binary || b.Truncated {
				continue
			}
			doc, derr := contract.Parse(e.Path, b.Content)
			if derr != nil {
				continue
			}
			var p pipeline
			if doc.Into(&p) == nil && len(p.Images) > 0 {
				merged.Images = append(merged.Images, p.Images...)
				from = append(from, e.Name)
			}
		}
		if len(merged.Images) > 0 {
			return merged, ".hanzo/workflows/{" + strings.Join(from, ",") + "}", nil
		}
	}
	// Fallback: the repo's own contract at its root — hanzo.yml, .yaml or .json.
	// A generator spelling is refused by contract.Load and reported: this reactor
	// reads what a repository declares, it does not run it.
	doc, err := contract.Load(blobs(ctx, repo, rev))
	switch {
	case errors.Is(err, contract.ErrNone):
		return nil, "", nil
	case err != nil:
		return nil, "", err
	}
	var p pipeline
	if err := doc.Into(&p); err != nil {
		return nil, "", fmt.Errorf("%s: %w", doc.Name, err)
	}
	if len(p.Images) == 0 {
		return nil, "", nil
	}
	return &p, doc.Name, nil
}

// blobs reads repo-root files at a revision in the shape contract.Load resolves
// through: a path that is not in the tree answers fs.ErrNotExist, and anything else
// is a real failure resolution must stop on rather than read past. A file too big
// or too binary to read is one of those — skipping it would let a repository lose
// its declaration without a word.
//
// A LINK IS ABSENT, which is the same answer a checkout gives. Its bytes are a
// path, so reading them as a declaration would mean this reader and a checkout
// disagreeing about the repository they both hold: one sees the target's name,
// the other the target's document.
func blobs(ctx context.Context, repo Repository, rev Revision) contract.Read {
	return func(name string) ([]byte, error) {
		b, err := repo.Blob(ctx, rev, name, contract.Max)
		switch {
		case errors.Is(err, ErrNoPath):
			return nil, fs.ErrNotExist
		case err != nil:
			return nil, err
		case b.Link:
			return nil, fs.ErrNotExist
		case b.Truncated:
			return nil, fmt.Errorf("%d bytes; a declaration is smaller than %d", b.Size, contract.Max)
		case b.Binary:
			return nil, errors.New("not text")
		}
		return b.Content, nil
	}
}

// enqueuePipeline POSTs one direct-build request per image and returns
// (enqueued, failed). Best-effort: each image is independent, a failure is logged
// (never the token) and never aborts the others. The bearer + URL come from env
// (KMS-sourced token); the client carries a hard timeout so a slow platform can
// never wedge the reactor goroutine.
func enqueuePipeline(ctx context.Context, s *cloud.Service[state], ev cloud.LifecycleEvent, pl *pipeline) (int, int) {
	token := environ.Or(enqueueTokenEnv, "")
	url := environ.Or(enqueueURLEnv, "")
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
// identical ref — the two entry points converge, never fork a tag. Returns nil for an
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
		Args:       img.Args,
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
