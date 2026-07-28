package git

import (
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

// webhook.go is the CANONICAL-FORGE trigger of push-to-deploy: git.hanzo.ai (the
// hanzoai/git server, Service hanzo-git) POSTs every push here, we HMAC-verify it
// and hand the push to cloud.OnGitPush — the SAME single-registrant seam the
// embedded git server (smart_http.go) and the GitHub App (clients/integrations)
// fire. Third transport, one trigger; the build decision stays downstream in
// clients/platform, which is the one place that knows what a push MEANS.
//
// It exists because the canonical forge is a SEPARATE process from this binary:
// its pushes never touch our receive-pack, so without this door a push to the
// host we call canonical builds nothing and only the GitHub mirror releases.
//
// PUBLIC at the JWT layer (the forge has no Hanzo session) — auth is the
// signature, verified fail-closed here, exactly like the GitHub webhook.

const (
	// webhookSecretEnv holds the forge's shared webhook secret (KMS-synced into the
	// cloud CR env as GIT_WEBHOOK_SECRET, same as GITHUB_APP_WEBHOOK_SECRET). This
	// endpoint TRIGGERS BUILDS, so an unset secret refuses every delivery rather
	// than trusting it.
	webhookSecretEnv = "GIT_WEBHOOK_SECRET"
	// The forge's canonical header family. It also emits the X-Gitea-*/X-Gogs-*/
	// X-Hub-* aliases for third-party receivers written against those servers; we
	// read the one spelling that is ours.
	eventHeader = "X-Git-Event"
	sigHeader   = "X-Git-Signature"
	// zeroSHA is git's all-zero object id — the `after` of a ref delete.
	zeroSHA = "0000000000000000000000000000000000000000"
	// gitMaxWebhookBody bounds the payload we read + sign over. A hostile/oversized
	// body can neither exhaust memory nor slip past the HMAC (we verify exactly the
	// bytes we act on).
	gitMaxWebhookBody = 8 << 20 // 8 MiB
)

// pushEvent is the subset of the forge's push payload we act on. Owner and pusher
// each accept both spellings the payload has carried across versions (login vs
// username); first non-empty wins.
type pushEvent struct {
	Ref        string `json:"ref"`
	After      string `json:"after"`
	Repository struct {
		Name     string `json:"name"`
		CloneURL string `json:"clone_url"`
		Owner    struct {
			Login    string `json:"login"`
			Username string `json:"username"`
		} `json:"owner"`
	} `json:"repository"`
	Pusher struct {
		Login    string `json:"login"`
		Username string `json:"username"`
	} `json:"pusher"`
}

// webhook verifies + processes an inbound forge webhook. It ALWAYS answers a benign
// 204 for deliveries it does not act on (non-push event, non-ref, ref delete, bot
// author) so the forge does not retry-storm — only a bad signature (401) and a
// malformed body (400) are non-2xx.
func webhook(s *cloud.Service[state], c *zip.Ctx) error {
	body := c.Body()
	if len(body) > gitMaxWebhookBody {
		return zip.Errorf(http.StatusRequestEntityTooLarge, "payload too large")
	}
	// Verify BEFORE parse so an unauthenticated body is never decoded.
	if !validSignature(strings.TrimSpace(os.Getenv(webhookSecretEnv)), c.Header(sigHeader), body) {
		return zip.Errorf(http.StatusUnauthorized, "invalid signature")
	}
	if c.Header(eventHeader) != "push" {
		return c.NoContent(http.StatusNoContent)
	}

	var ev pushEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		return zip.ErrBadRequest("invalid push payload")
	}
	// EVERY ref reaches the builder — branches and tags alike, matching the GitHub
	// path. Releases are cut by tag, so a filter here silently stops publishing.
	if !strings.HasPrefix(ev.Ref, "refs/") {
		return c.NoContent(http.StatusNoContent)
	}
	// A zero `after` is a ref delete: nothing to build at a commit that is gone.
	if ev.After == "" || ev.After == zeroSHA {
		return c.NoContent(http.StatusNoContent)
	}
	org := firstNonEmptyStr(ev.Repository.Owner.Login, ev.Repository.Owner.Username)
	if org == "" || ev.Repository.Name == "" {
		return zip.ErrBadRequest("missing repository owner or name")
	}
	// Bot-authored pushes are excluded by the same guard the GitHub path uses: our
	// release automation pushes AS the forge's Actions user, and a release must
	// never rebuild itself.
	if cloud.IsBotActor(firstNonEmptyStr(ev.Pusher.Login, ev.Pusher.Username)) {
		return c.NoContent(http.StatusNoContent)
	}

	// Best-effort by the seam's contract: the push already landed on the forge, so a
	// trigger failure is logged, never answered as an error the forge would retry.
	if err := cloud.OnGitPush(c.Context(), cloud.GitPushEvent{
		Org: org, Repo: ev.Repository.Name, Ref: ev.Ref,
		Commit: ev.After, CloneURL: ev.Repository.CloneURL,
	}); err != nil {
		s.Log.Warn("forge push: build trigger failed",
			"org", org, "repo", ev.Repository.Name, "ref", ev.Ref, "err", err)
	}
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
