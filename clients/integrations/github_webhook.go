package integrations

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// github_webhook.go is the GitHub trigger of the universal sync engine: the org's
// GitHub App POSTs a push event to /v1/connector/github/webhook, we HMAC-verify it, resolve
// the org from the (signed) installation id, mint the installation token, and hand
// the push to cloud.Sync. The engine resolves the SyncLink(s) for the repo and the
// git provider fast-forward-only advances native. PUBLIC at the JWT layer (GitHub
// has no Hanzo session) — auth is the signature, verified fail-closed here, exactly
// like the Slack events webhook.
//
// SPLIT-BRAIN GUARD lives in the git object plane (the fast-forward-only fetch): a
// divergence never overwrites native. LOOP PREVENTION is the engine's cursor
// (identical SHAs are a no-op) + actor guard (a push our own mirror made is
// skipped), so no ping-pong occurs. This handler stays thin: verify, resolve, mint,
// enqueue — the engine is the one place the sync happens.

// githubMaxWebhookBody bounds the payload we read + sign over. GitHub caps webhook
// bodies well under this; a hostile/oversized body can neither exhaust memory nor
// slip past the HMAC (we verify exactly the bytes we act on).
const githubMaxWebhookBody = 8 << 20 // 8 MiB

// verifyGitHubSignature reports whether sigHeader is a valid GitHub webhook
// signature over body under secret. GitHub sends
// "X-Hub-Signature-256: sha256=<hex(HMAC_SHA256(secret, body))>". Constant-time
// compare; fail-closed on an empty secret / header / malformed input (never a panic).
func verifyGitHubSignature(secret, sigHeader string, body []byte) bool {
	if secret == "" || sigHeader == "" {
		return false
	}
	const prefix = "sha256="
	if !strings.HasPrefix(sigHeader, prefix) {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := prefix + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(sigHeader), []byte(want))
}

// githubPushEvent is the subset of GitHub's push payload we act on.
type githubPushEvent struct {
	Ref        string `json:"ref"`
	Before     string `json:"before"`
	After      string `json:"after"`
	Deleted    bool   `json:"deleted"`
	Repository struct {
		Name     string `json:"name"`
		FullName string `json:"full_name"`
		CloneURL string `json:"clone_url"`
	} `json:"repository"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
	// Sender/Pusher identify who pushed — the App/bot login for a push our own
	// outbound mirror made, which the engine's loop guard fast-skips.
	Sender struct {
		Login string `json:"login"`
	} `json:"sender"`
	Pusher struct {
		Name string `json:"name"`
	} `json:"pusher"`
}

// githubWebhook verifies + processes an inbound GitHub webhook. It ALWAYS answers a
// benign 200 for events it does not act on (ping, non-push, tag/non-branch ref,
// branch delete, unknown installation) so GitHub does not retry-storm — only a bad
// signature (401) and an internal sync failure (502) are non-200.
func githubWebhook(s *cloud.Service[state], c *zip.Ctx) error {
	body := c.Body()
	if len(body) > githubMaxWebhookBody {
		return zip.Errorf(http.StatusRequestEntityTooLarge, "payload too large")
	}
	secret := strings.TrimSpace(os.Getenv(githubWebhookSecretEnv))
	if !verifyGitHubSignature(secret, c.Header("X-Hub-Signature-256"), body) {
		return zip.Errorf(http.StatusUnauthorized, "invalid signature")
	}
	switch c.Header("X-GitHub-Event") {
	case "ping":
		return c.JSON(http.StatusOK, map[string]any{"ok": true})
	case "push":
		// handled below
	case "issues", "issue_comment":
		// Issue lifecycle → native tracker mirror (github_issues.go). Same signed
		// installation → org resolution as push; the tracker sink is idempotent by
		// ExtRef, so opened/edited/closed/reopened + comment all re-sync one row.
		return handleGitHubIssueEvent(c, body)
	default:
		return c.JSON(http.StatusOK, map[string]any{"ignored": "event"})
	}

	var ev githubPushEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		return zip.ErrBadRequest("invalid push payload")
	}
	// EVERY ref syncs — branches and tags alike. Releases are cut by tag, so a
	// filter here silently stops publishing with nothing failing to say so.
	if !strings.HasPrefix(ev.Ref, "refs/") {
		return c.JSON(http.StatusOK, map[string]any{"ignored": "not a ref"})
	}
	// A delete on GitHub is NEVER propagated to native (native is canonical; we
	// never delete a native ref from an inbound event).
	if ev.Deleted {
		return c.JSON(http.StatusOK, map[string]any{"ignored": "branch delete not propagated"})
	}
	if ev.Installation.ID == 0 {
		return c.JSON(http.StatusOK, map[string]any{"ignored": "no installation"})
	}
	// Tenant resolution: the org comes ONLY from the installation id in the
	// HMAC-verified body — never a header, never a payload field a client controls.
	// An org can therefore never receive another org's inbound push.
	org, ok := OrgForExternalID("github", strconv.FormatInt(ev.Installation.ID, 10))
	if !ok {
		return c.JSON(http.StatusOK, map[string]any{"ignored": "unknown installation"})
	}

	clone := strings.TrimSpace(ev.Repository.CloneURL)
	if clone == "" && ev.Repository.FullName != "" {
		clone = "https://github.com/" + ev.Repository.FullName + ".git"
	}

	// Hand the push to the SAME deploy trigger the embedded git server fires, so an
	// upstream merge and a native push travel one seam rather than two CIs. This
	// runs BEFORE the token mint and the mirror on purpose: a build reads from
	// GitHub, so it must not be lost because native mirroring was unavailable —
	// otherwise a sync outage silently stops the fleet from releasing. Best-effort
	// by the seam's contract. Bot-authored pushes are excluded by the same guard
	// automations use: our outbound mirror pushes AS the App, and a release must
	// never rebuild itself.
	if !isBotActor(actorOf(ev)) {
		if err := cloud.OnGitPush(c.Context(), cloud.GitPushEvent{
			Org: org, Repo: ev.Repository.Name, Ref: ev.Ref,
			Commit: ev.After, CloneURL: clone,
		}); err != nil {
			s.Log.Warn("github push: build trigger failed",
				"org", org, "repo", ev.Repository.Name, "ref", ev.Ref, "err", err)
		}
	}

	// Mint the installation token HERE (the App plane owns token custody) and pass
	// it THROUGH the event, so the engine's git provider fetches without re-minting.
	tok, err := InstallationToken(c.Context(), org)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "mint github installation token: %v", err)
	}
	// The universal sync engine is the ONE place a sync happens: it resolves the
	// SyncLink(s) whose source is this GitHub repo and applies each (the git
	// provider fast-forward-advances native, honoring the link's direction + loop
	// guard + cursor). Fail-closed when the engine is unmounted — never a fake OK.
	res, err := cloud.Sync(c.Context(), cloud.SyncEvent{
		Kind: "git", Provider: "github", Org: org,
		Locator: clone, Repo: ev.Repository.Name, Ref: ev.Ref,
		Before: ev.Before, After: ev.After, Actor: actorOf(ev), Token: tok,
	})
	if err != nil {
		if errors.Is(err, cloud.ErrSyncUnavailable) {
			return zip.Errorf(http.StatusServiceUnavailable, "sync engine not available")
		}
		return zip.Errorf(http.StatusBadGateway, "sync: %v", err)
	}
	// Fire any automation flows subscribed to github/push for THIS org (best-effort — a
	// dispatch failure never fails the webhook ack). org is the signature-verified
	// installation owner resolved above, never a client field; ev.After (the head SHA) is
	// the idempotency key so a redelivered push fires each flow at most once. depth 0 =
	// external origin. MED-1: a bot/App-authored push (our own outbound mirror pushes AS the
	// GitHub App bot) does NOT fire automations — else "on push → our action pushes → push"
	// loops. A human push always fires. (The sync plane has its own cursor loop-guard.)
	if !isBotActor(actorOf(ev)) {
		fireTrigger(c.Context(), org, "github", "push", ev.After, 0, map[string]any{
			"repo": ev.Repository.Name, "ref": ev.Ref, "branch": refShort(ev.Ref),
			"before": ev.Before, "after": ev.After, "pusher": actorOf(ev),
		})
	}
	return c.JSON(http.StatusOK, map[string]any{"ran": res.Ran, "skipped": res.Skipped})
}

// isBotActor is cloud.IsBotActor — the ONE bot predicate every push transport
// shares. Our own outbound mirror pushes AS the App bot, so this is the
// automation-plane loop-guard: a bot-authored push never re-fires automations.
var isBotActor = cloud.IsBotActor

// actorOf returns the login that pushed on GitHub — the App/bot for a push our own
// outbound mirror made (the loop-guard fast-path; cursor idempotency is the real
// guarantee).
func actorOf(ev githubPushEvent) string {
	if ev.Sender.Login != "" {
		return ev.Sender.Login
	}
	return ev.Pusher.Name
}

// refShort is the ref without its namespace: refs/heads/main -> main,
// refs/tags/v1.2.3 -> v1.2.3. Only for display and for flow payloads that
// predate full refs; the sync itself always carries the whole ref.
func refShort(ref string) string {
	for _, p := range []string{"refs/heads/", "refs/tags/"} {
		if strings.HasPrefix(ref, p) {
			return strings.TrimPrefix(ref, p)
		}
	}
	return ref
}
