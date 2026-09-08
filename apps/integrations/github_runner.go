package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// A queued job on GitHub becomes a runner the same way a forge job does. The
// App is told through its webhook, this mints a just-in-time configuration for
// exactly that job — a runner that registers with it takes one job and is gone
// server-side when it exits — and platform launches it. Nothing asks GitHub
// whether work is waiting.

// githubJob is the subset of GitHub's workflow_job delivery this acts on.
type githubJob struct {
	Action string `json:"action"`
	Job    struct {
		ID     int64    `json:"id"`
		Labels []string `json:"labels"`
	} `json:"workflow_job"`
	Repository struct {
		Name  string `json:"name"`
		Owner struct {
			Login string `json:"login"`
		} `json:"owner"`
	} `json:"repository"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
}

// jitConfig mints a one-job runner registration in owner's organization with
// the installation's own token. Organization scope, because that is the
// permission the App carries: organization_self_hosted_runners. The
// repository endpoint wants a repository's administration, which the App does
// not hold, and answered 403 to every job.
func jitConfig(ctx context.Context, token, owner, name string, labels []string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"name": name, "runner_group_id": 1, "labels": labels, "work_folder": "_work",
	})
	u := githubAPIBase + "/orgs/" + url.PathEscape(owner) + "/actions/runners/generate-jitconfig"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("github jitconfig %s: %d %s", owner, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var out struct {
		JIT string `json:"encoded_jit_config"`
	}
	if err := json.Unmarshal(b, &out); err != nil || out.JIT == "" {
		return "", fmt.Errorf("github jitconfig %s: no configuration in the answer", owner)
	}
	return out.JIT, nil
}

// ours reports whether a queued GitHub job asked for this fleet.
//
// GITHUB HAS RUNNERS OF ITS OWN AND THE FORGE DOES NOT. Every forge job is
// ours by construction; a GitHub job naming ubuntu-latest is answered by
// GitHub, and minting for it would put a pod on the cluster that nothing ever
// claims. So a runner is minted only for a job that named the fleet: the
// platform-and-architecture labels the hosts advertise, or the self-hosted
// label that means the same thing.
//
// Only Linux is served here, because this mints a Kubernetes pod. macOS and
// Windows are real hosts registered with the forge, not pods, so a job asking
// for them is not something this path can answer.
func ours(labels []string) bool {
	for _, l := range labels {
		l = strings.ToLower(l)
		if l == "self-hosted" ||
			strings.HasSuffix(l, "linux-amd64") || strings.HasSuffix(l, "linux-arm64") {
			return true
		}
	}
	return false
}

// handleGitHubJobEvent turns a queued job into a runner; every other action
// is answered 200 and ignored, so GitHub does not retry.
func handleGitHubJobEvent(s *cloud.Service[state], c *zip.Ctx, body []byte) error {
	var ev githubJob
	if err := json.Unmarshal(body, &ev); err != nil {
		return zip.ErrBadRequest("invalid workflow_job payload")
	}
	if ev.Action != "queued" {
		return c.JSON(http.StatusOK, map[string]any{"ignored": ev.Action})
	}
	if ev.Installation.ID == 0 {
		return c.JSON(http.StatusOK, map[string]any{"ignored": "no installation"})
	}
	if !ours(ev.Job.Labels) {
		return c.JSON(http.StatusOK, map[string]any{"ignored": "not this fleet", "labels": ev.Job.Labels})
	}
	org, ok := OrgForExternalID("github", strconv.FormatInt(ev.Installation.ID, 10))
	if !ok {
		return c.JSON(http.StatusOK, map[string]any{"ignored": "unknown installation"})
	}
	owner, repo := ev.Repository.Owner.Login, ev.Repository.Name
	token, err := ghApp.installationToken(c.Context(), ev.Installation.ID)
	if err != nil {
		s.Log.Error("github job: installation token", "org", org, "repo", repo, "err", err)
		return zip.Errorf(http.StatusBadGateway, "no installation token; redeliver")
	}
	name := fmt.Sprintf("runner-github-%s-%d", strings.ToLower(repo), ev.Job.ID)
	jit, err := jitConfig(c.Context(), token, owner, name, ev.Job.Labels)
	if err != nil {
		s.Log.Error("github job: jit config", "org", org, "repo", repo, "job", ev.Job.ID, "err", err)
		return zip.Errorf(http.StatusBadGateway, "GitHub refused a runner registration; redeliver")
	}
	launched, err := cloud.OnWorkflowJob(c.Context(), cloud.JobEvent{
		Org: org, Provider: "github", Repo: owner + "/" + repo, ID: ev.Job.ID,
		Labels: ev.Job.Labels, Token: jit,
	})
	if err != nil {
		s.Log.Error("github job: runner not launched", "org", org, "repo", repo, "job", ev.Job.ID, "err", err)
		return zip.Errorf(http.StatusInternalServerError, "the job was verified but no runner could be launched; redeliver")
	}
	s.Log.Info("github job queued", "org", org, "repo", repo, "job", ev.Job.ID, "labels", ev.Job.Labels, "runner", launched)
	return c.JSON(http.StatusOK, map[string]any{"org": org, "repo": repo, "job": ev.Job.ID, "runner": launched})
}
