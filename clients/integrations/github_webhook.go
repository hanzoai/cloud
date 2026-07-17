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
	default:
		return c.JSON(http.StatusOK, map[string]any{"ignored": "event"})
	}

	var ev githubPushEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		return zip.ErrBadRequest("invalid push payload")
	}
	// Only branch pushes drive the mutable canonical sync. Tags/others → ack no-op.
	if !strings.HasPrefix(ev.Ref, "refs/heads/") {
		return c.JSON(http.StatusOK, map[string]any{"ignored": "non-branch ref"})
	}
	// A branch delete on GitHub is NEVER propagated to native (native is canonical;
	// we never delete a native ref from an inbound event).
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
	branch := strings.TrimPrefix(ev.Ref, "refs/heads/")

	// Mint the installation token HERE (the App plane owns token custody) and pass
	// it THROUGH the event, so the engine's git provider fetches without re-minting.
	tok, err := InstallationToken(c.Context(), org)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "mint github installation token: %v", err)
	}
	clone := strings.TrimSpace(ev.Repository.CloneURL)
	if clone == "" && ev.Repository.FullName != "" {
		clone = "https://github.com/" + ev.Repository.FullName + ".git"
	}
	// The universal sync engine is the ONE place a sync happens: it resolves the
	// SyncLink(s) whose source is this GitHub repo and applies each (the git
	// provider fast-forward-advances native, honoring the link's direction + loop
	// guard + cursor). Fail-closed when the engine is unmounted — never a fake OK.
	res, err := cloud.Sync(c.Context(), cloud.SyncEvent{
		Kind: "git", Provider: "github", Org: org,
		Locator: clone, Repo: ev.Repository.Name, Branch: branch,
		Before: ev.Before, After: ev.After, Actor: actorOf(ev), Token: tok,
	})
	if err != nil {
		if errors.Is(err, cloud.ErrSyncUnavailable) {
			return zip.Errorf(http.StatusServiceUnavailable, "sync engine not available")
		}
		return zip.Errorf(http.StatusBadGateway, "sync: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{"ran": res.Ran, "skipped": res.Skipped})
}

// actorOf returns the login that pushed on GitHub — the App/bot for a push our own
// outbound mirror made (the loop-guard fast-path; cursor idempotency is the real
// guarantee).
func actorOf(ev githubPushEvent) string {
	if ev.Sender.Login != "" {
		return ev.Sender.Login
	}
	return ev.Pusher.Name
}
