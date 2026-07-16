package git

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// Gitea push-webhook ingest. The external Hanzo Git server (a Gitea fork,
// service hanzo-git.hanzo.svc, host git.hanzo.ai) POSTs here on every push so a
// push that lands on it drives the SAME push-to-deploy core the embedded
// smart-HTTP receive-pack path drives: fireBranchBuild → cloud.OnGitPush (deploy
// trigger) + EmitLifecycle (mirror-out / Slack). One deploy trigger, one
// lifecycle stream, regardless of which git server the push landed on — no
// second code path.
//
// Auth is Gitea's HMAC: X-Gitea-Signature is the hex HMAC-SHA256 of the raw
// request body under a shared secret. The secret is KMS-synced
// (hanzo/prod:/git/webhook-secret) into the cloud CR as env GIT_WEBHOOK_SECRET;
// it is NEVER hardcoded. Fail-closed: an unset secret or a mismatched signature
// is 401, so a misconfigured deployment refuses webhooks rather than trusting
// them.

const (
	webhookSecretEnv = "GIT_WEBHOOK_SECRET"
	giteaEventHeader = "X-Gitea-Event"
	giteaSigHeader   = "X-Gitea-Signature"
	// zeroSHA is git's all-zero object id — the `after` of a branch delete and
	// the `before` of a branch create.
	zeroSHA = "0000000000000000000000000000000000000000"
)

// giteaPush is the subset of Gitea's push payload the deploy core needs. Gitea
// varies the actor field name across versions (login vs username) for both the
// repo owner and the pusher, so each accepts both; the first non-empty wins.
type giteaPush struct {
	Ref        string `json:"ref"`
	Before     string `json:"before"`
	After      string `json:"after"`
	Repository struct {
		Name  string `json:"name"`
		Owner struct {
			Username string `json:"username"`
			Login    string `json:"login"`
		} `json:"owner"`
	} `json:"repository"`
	Pusher struct {
		Login    string `json:"login"`
		Username string `json:"username"`
	} `json:"pusher"`
}

// webhook ingests a Gitea push webhook and funnels it through the shared
// push-to-deploy core (fireBranchBuild). Non-push events and no-op pushes
// (branch delete, non-branch ref) are acknowledged 204 so Gitea does not retry.
func webhook(s *cloud.Service[state], c *zip.Ctx) error {
	// Only push drives a build; every other Gitea event is an acknowledged no-op.
	if c.Header(giteaEventHeader) != "push" {
		return c.NoContent(http.StatusNoContent)
	}

	// Verify BEFORE parse so an unauthenticated body is never decoded. An unset
	// secret is a 401 (fail-closed), not an open door.
	body := c.Body()
	if !validSignature(os.Getenv(webhookSecretEnv), c.Header(giteaSigHeader), body) {
		return zip.ErrUnauthorized("invalid webhook signature")
	}

	var ev giteaPush
	if err := json.Unmarshal(body, &ev); err != nil {
		return zip.ErrBadRequest("invalid push payload")
	}

	// Branch pushes only: refs/heads/<branch>. Tags and other refs are ignored.
	branch, ok := strings.CutPrefix(ev.Ref, "refs/heads/")
	if !ok || branch == "" {
		return c.NoContent(http.StatusNoContent)
	}
	// A zero (or empty) `after` is a branch delete / empty push — no build.
	if ev.After == "" || ev.After == zeroSHA {
		return c.NoContent(http.StatusNoContent)
	}

	owner := firstNonEmptyStr(ev.Repository.Owner.Username, ev.Repository.Owner.Login)
	name := ev.Repository.Name
	if owner == "" || name == "" {
		return zip.ErrBadRequest("missing repository owner or name")
	}
	pusher := firstNonEmptyStr(ev.Pusher.Login, ev.Pusher.Username)

	// The SAME funnel receive-pack drives (fireBranchBuilds → fireBranchBuild):
	// cloud.OnGitPush deploy trigger + EmitLifecycle. project is "" — Gitea repos
	// are org-level, the scope the smart-HTTP pack handlers resolve for a native
	// push. Detached from the request (WithoutCancel) so the build outlives the
	// 204, matching receivePack.
	fireBranchBuild(s, context.WithoutCancel(c.Context()), owner, "", name, branch, ev.Before, ev.After, pusher)
	return c.NoContent(http.StatusNoContent)
}

// validSignature reports whether sigHex is the hex HMAC-SHA256 of body under
// secret, compared in constant time. An empty secret or malformed hex is false
// (fail-closed) — never a bypass.
func validSignature(secret, sigHex string, body []byte) bool {
	if secret == "" || sigHex == "" {
		return false
	}
	sig, err := hex.DecodeString(sigHex)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(sig, mac.Sum(nil))
}
