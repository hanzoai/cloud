package integrations

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/forge"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// The forge delivers a queued workflow job here, and it becomes a runner.
//
// A push never arrives this way: the native git server sees its own pushes and
// a GitHub-canonical repository's reach us through the App. A queued job is
// different in kind — Actions run on the forge and nowhere else, and the only
// alternative to being told is asking, which is the two-second poll every
// runner used to run. So this is the one thing the forge reports to us, and a
// delivery carrying anything else is answered 200 and ignored.
//
// The forge is configured with a SYSTEM webhook, so every repository on it is
// covered at once:
//
//	Target URL   https://api.hanzo.ai/v1/integration/forge/webhook
//	Content type application/json
//	Secret       the value at KMS forge.WebhookRef
//	Trigger      Workflow job
const forgeWebhookPath = "/v1/integration/forge/webhook"

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
	openapi.Register(forgeWebhookPath, "POST", forgeJob{}, forgeLaunched{})
	openapi.Describe(forgeWebhookPath, "POST",
		"Forge workflow_job webhook",
		"Receives the forge's workflow_job delivery. The signature over the raw body is "+
			"checked against the secret at KMS forge.WebhookRef before anything is decoded; a "+
			"queued job becomes one ephemeral runner Job on the cluster, minted a registration "+
			"token for exactly that job. Every other action is answered 200 and ignored.")
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
	var ev forgeJob
	if err := json.Unmarshal(body, &ev); err != nil {
		return zip.ErrBadRequest("invalid payload")
	}
	owner := cmp.Or(ev.Repository.Owner.Login, ev.Repository.Owner.Username)
	if ev.Job.ID == 0 || owner == "" || ev.Repository.Name == "" {
		return c.JSON(http.StatusOK, map[string]any{"ignored": "not a workflow job"})
	}
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
	if err != nil {
		s.Log.Error("forge webhook: runner not launched", "org", org, "repo", ev.Repository.Name, "job", ev.Job.ID, "err", err)
		return zip.Errorf(http.StatusInternalServerError, "the job was verified but no runner could be launched; redeliver")
	}
	s.Log.Info("forge job queued", "org", org, "repo", ev.Repository.Name, "job", ev.Job.ID, "labels", ev.Job.Labels, "runner", name)
	return c.JSON(http.StatusOK, forgeLaunched{Org: org, Repo: ev.Repository.Name, Job: ev.Job.ID, Runner: name})
}
