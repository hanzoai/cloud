package provider

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/forge"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/spec"
	"github.com/zap-proto/zip"
)

// The forge delivers a queued workflow job here, and it becomes a runner; a job
// of a pull request, of any action, writes the verdict of its runs to GitHub; and a
// push, which is told to git, so a repository git links to the forge (apps/git
// link.go) reads it and indexes a push to its default branch, and which has the
// forge hold a pull request GitHub proposes from that branch (gate.go). Anything
// else is answered 200 and ignored.
//
// The forge is configured with a SYSTEM webhook, so every repository on it is
// covered at once:
//
//	Target URL   https://api.hanzo.ai/v1/provider/forge/webhook
//	Content type application/json
//	Secret       the value at KMS forge.WebhookRef
//	Trigger      Workflow job, Push
const (
	// forgeWebhookLeaf is the leaf the receiver registers on the app's group.
	forgeWebhookLeaf = "/forge/webhook"
	// forgeWebhookPath is the served address, which the projections key on.
	forgeWebhookPath = "/v1/provider" + forgeWebhookLeaf
)

const (
	// forgeMaxBody bounds what is hashed and decoded; a job delivery is a few KB.
	forgeMaxBody = 1 << 20
	// forgeFresh bounds how long the verifying secret is held: a rotation is
	// live within it with no restart, and a flood costs one KMS read per window.
	forgeFresh = 5 * time.Minute
)

// forgeSigHeaders is every header this forge signs with, in the order it owns
// them. All carry the same hex HMAC-SHA256 of the body; the GitHub one prefixes
// it with "sha256=".
var forgeSigHeaders = []string{"X-Git-Signature", "X-Gitea-Signature", "X-Hub-Signature-256"}

// forgeJob is the subset of the forge's workflow_job delivery this receiver
// acts on.
type forgeJob struct {
	// Action is what happened to the job: queued, in_progress, completed, waiting.
	Action string `json:"action"`
	// Job is the workflow job the action happened to.
	Job struct {
		// ID is the job's id on the forge.
		ID int64 `json:"id"`
		// RunID is the workflow run the job belongs to.
		RunID int64 `json:"run_id"`
		// Name is the job's name in its workflow.
		Name string `json:"name"`
		// Labels is what the job's runs-on asked for; the runner registers with exactly these.
		Labels []string `json:"labels"`
	} `json:"workflow_job"`
	// Repository is the repository the workflow lives in.
	Repository struct {
		// Name is the repository's name.
		Name string `json:"name"`
		// Owner is the namespace the repository lives under.
		Owner struct {
			// Login is the owner's login.
			Login string `json:"login"`
			// Username is the owner's login under the forge's older spelling.
			Username string `json:"username"`
		} `json:"owner"`
	} `json:"repository"`
}

// forgeLaunched is what a delivery is answered with, so the forge's delivery
// page says which runner took the job.
type forgeLaunched struct {
	// Org is the tenant whose compute the runner spends.
	Org string `json:"org"`
	// Repo is the repository the job belongs to.
	Repo string `json:"repo"`
	// Job is the job's id on the forge.
	Job int64 `json:"job"`
	// Runner is the name of the Job launched for it.
	Runner string `json:"runner"`
}

func init() {
	spec.Register(forgeWebhookPath, "POST", forgeJob{}, forgeLaunched{})
	spec.Describe(forgeWebhookPath, "POST",
		"Forge workflow_job webhook",
		"Receives the forge's workflow_job delivery. The signature over the raw body is "+
			"checked against the secret at KMS forge.WebhookRef before anything is decoded; a "+
			"queued job becomes one ephemeral runner Job on the cluster, minted a registration "+
			"token for exactly that job. A job of a pull request, of any action, also writes what "+
			"its commit's runs say to GitHub as the commit status hanzo/ci. Every other action is "+
			"answered 200 and ignored.")
}

// forgeKey holds the verifying secret for a window, so a delivery does not
// cost a KMS read and a rotation still lands without a restart.
type forgeKey struct {
	mu   sync.Mutex
	v    string
	when time.Time
}

func (k *forgeKey) read(s *cloud.Service[state], ctx context.Context) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.v != "" && time.Since(k.when) < forgeFresh {
		return k.v, nil
	}
	if s.State.kms == nil {
		return "", fmt.Errorf("no KMS client in use: cannot read %s", forge.WebhookRef)
	}
	b, err := s.State.kms.GetSecret(ctx, forge.WebhookRef)
	if err != nil {
		if k.v != "" {
			return k.v, nil
		}
		return "", fmt.Errorf("read %s: %w", forge.WebhookRef, err)
	}
	v := strings.TrimSpace(string(b))
	if v == "" {
		return "", fmt.Errorf("%s is empty", forge.WebhookRef)
	}
	k.v, k.when = v, time.Now()
	return v, nil
}

// forgeWebhook verifies a delivery and turns a queued job into a runner.
func forgeWebhook(s *cloud.Service[state], c *zip.Ctx) error {
	if enc := strings.TrimSpace(c.Header("Content-Encoding")); enc != "" {
		return zip.Errorf(http.StatusUnsupportedMediaType, "this receiver reads an uncompressed body")
	}
	body := c.Body()
	if len(body) > forgeMaxBody {
		return zip.Errorf(http.StatusRequestEntityTooLarge, "payload too large")
	}
	key, err := s.State.forgeKey.read(s, c.Context())
	if err != nil {
		s.Log.Error("forge webhook: no secret to verify against", "err", err)
		return zip.Errorf(http.StatusServiceUnavailable, "forge webhook secret unavailable")
	}
	sigs := make([]string, len(forgeSigHeaders))
	for i, h := range forgeSigHeaders {
		sigs[i] = c.Header(h)
	}
	if !signed(key, body, sigs...) {
		return zip.Errorf(http.StatusUnauthorized, "invalid signature")
	}
	if forgeEvent(c) == "push" {
		return forgePush(s, c, body)
	}
	var ev forgeJob
	if err := json.Unmarshal(body, &ev); err != nil {
		return zip.ErrBadRequest("invalid payload")
	}
	owner := cmp.Or(ev.Repository.Owner.Login, ev.Repository.Owner.Username)
	if ev.Job.ID == 0 || owner == "" || ev.Repository.Name == "" {
		return c.JSON(http.StatusOK, map[string]any{"ignored": "not a workflow job"})
	}
	// Every action of a job says something of its run, and a pull request's
	// verdict is written to GitHub (gate.go) whatever the job did.
	gateRun(s, owner, "writing a pull request's verdict", func(ctx context.Context) error {
		return gateJob(ctx, s, owner, ev.Repository.Name, ev.Job.RunID)
	})
	if ev.Action != "queued" {
		return c.JSON(http.StatusOK, map[string]any{"ignored": ev.Action})
	}
	org, err := forge.Org(owner)
	if err != nil {
		s.Log.Warn("forge webhook: job in a namespace that maps to no org", "owner", owner, "repo", ev.Repository.Name)
		return c.JSON(http.StatusOK, map[string]any{"ignored": "forge namespace maps to no org"})
	}
	fc, err := forge.Dial(c.Context(), s.State.kms)
	if err != nil {
		s.Log.Error("forge webhook: no forge credential", "err", err)
		return zip.Errorf(http.StatusServiceUnavailable, "forge credential unavailable")
	}
	token, err := fc.Machine().RunnerToken(c.Context())
	if err != nil {
		s.Log.Error("forge webhook: registration token", "err", err)
		return zip.Errorf(http.StatusBadGateway, "the forge refused a runner registration; redeliver")
	}
	name, err := cloud.OnWorkflowJob(c.Context(), cloud.JobEvent{
		Org: org, Provider: "forge", Repo: owner + "/" + ev.Repository.Name, ID: ev.Job.ID,
		Labels: ev.Job.Labels, URL: "https://" + fc.Host(), Token: token,
	})
	if errors.Is(err, cloud.ErrRunnerCapacity) {
		// A full cluster defers the job; it does not fail the delivery. The forge
		// never redelivers, and nothing needs it to: the job stays queued there
		// and platform's queue loop starts it once a slot frees.
		s.Log.Info("forge job left queued: no runner slot free", "org", org, "repo", ev.Repository.Name, "job", ev.Job.ID)
		return c.JSON(http.StatusAccepted, map[string]any{
			"queued": true, "reason": cloud.ErrRunnerCapacity.Error(),
			"org": org, "repo": ev.Repository.Name, "job": ev.Job.ID,
		})
	}
	if err != nil {
		s.Log.Error("forge webhook: runner not launched", "org", org, "repo", ev.Repository.Name, "job", ev.Job.ID, "err", err)
		return zip.Errorf(http.StatusInternalServerError, "the job was verified but no runner could be launched; redeliver")
	}
	s.Log.Info("forge job queued", "org", org, "repo", ev.Repository.Name, "job", ev.Job.ID, "labels", ev.Job.Labels, "runner", name)
	return c.JSON(http.StatusOK, forgeLaunched{Org: org, Repo: ev.Repository.Name, Job: ev.Job.ID, Runner: name})
}

// forgeEvent is the event a delivery names, under whichever header this forge
// spells it.
func forgeEvent(c *zip.Ctx) string {
	return cmp.Or(c.Header("X-Forgejo-Event"), c.Header("X-Gitea-Event"), c.Header("X-GitHub-Event"))
}

// forgePush tells git what a verified push landed on the forge, for the org its
// namespace maps to. A delete, and a namespace no org owns, is answered 200 and
// ignored.
func forgePush(s *cloud.Service[state], c *zip.Ctx, body []byte) error {
	var ev struct {
		Ref        string `json:"ref"`
		Before     string `json:"before"`
		After      string `json:"after"`
		Repository struct {
			Name     string `json:"name"`
			CloneURL string `json:"clone_url"`
			Owner    struct {
				Login    string `json:"login"`
				Username string `json:"username"`
			} `json:"owner"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(body, &ev); err != nil {
		return zip.ErrBadRequest("invalid push payload")
	}
	owner := cmp.Or(ev.Repository.Owner.Login, ev.Repository.Owner.Username)
	if owner == "" || ev.Repository.CloneURL == "" || !strings.HasPrefix(ev.Ref, "refs/") {
		return c.JSON(http.StatusOK, map[string]any{"ignored": "not a push"})
	}
	if strings.Trim(ev.After, "0") == "" {
		return c.JSON(http.StatusOK, map[string]any{"ignored": "a delete"})
	}
	org, err := forge.Org(owner)
	if err != nil {
		return c.JSON(http.StatusOK, map[string]any{"ignored": "forge namespace maps to no org"})
	}
	tellGit(s, c.Context(), org, ev.Repository.CloneURL, ev.Ref, ev.Before, ev.After)
	// A branch that landed may be one GitHub proposes: the forge then holds the
	// proposal too, so its workflows judge the head (gate.go).
	if branch, ok := strings.CutPrefix(ev.Ref, "refs/heads/"); ok {
		gateRun(s, owner, "opening the forge's pull request", func(ctx context.Context) error {
			return gateLanded(ctx, s, owner, ev.Repository.Name, branch)
		})
	}
	return c.JSON(http.StatusOK, map[string]any{"told": "git"})
}
