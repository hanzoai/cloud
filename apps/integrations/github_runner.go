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

// jitConfig mints a one-job runner registration for owner/repo with the
// installation's own token. Repository scope, because the App's permission
// for it is the repository's administration, which every installation carries.
func jitConfig(ctx context.Context, token, owner, repo, name string, labels []string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"name": name, "runner_group_id": 1, "labels": labels, "work_folder": "_work",
	})
	u := githubAPIBase + "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/actions/runners/generate-jitconfig"
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
		return "", fmt.Errorf("github jitconfig %s/%s: %d %s", owner, repo, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var out struct {
		JIT string `json:"encoded_jit_config"`
	}
	if err := json.Unmarshal(b, &out); err != nil || out.JIT == "" {
		return "", fmt.Errorf("github jitconfig %s/%s: no configuration in the answer", owner, repo)
	}
	return out.JIT, nil
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
	jit, err := jitConfig(c.Context(), token, owner, repo, name, ev.Job.Labels)
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
