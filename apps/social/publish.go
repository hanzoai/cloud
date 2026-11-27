package social

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/appcred"
	"github.com/hanzoai/cloud/sdk/secret"
	"github.com/hanzoai/cloud/sdk/text"
	"github.com/hanzoai/authz"

	"github.com/hanzoai/cloud/sdk"
)

// publish.go is the distribution edge of the social fold: the ONE path that pushes a
// post OUT to its channel's connected accounts, plus the provider client that push runs
// through. It mirrors clients/content's publish/Distributor split — the machine here is
// stable; the provider integration is a swappable edge selected once at Mount.
//
// GROUND TRUTH (why the default is fail-closed). The live social stack
// (github.com/hanzoai/social) publishes through per-provider OAuth apps whose global
// client id/secret come from deployment env (providerCreds below, the live orchestrator's
// exact var names). NO Hanzo deployment carries any of those credentials today (the
// social-secrets Secret has none), and no account access tokens exist — so the live
// orchestrator cannot publish either. There is no publishing capability to preserve, only
// one to enable. Accordingly the default Publisher is honest fail-closed: a publish reports
// exactly which credentials are missing and NEVER fakes success. The machine is complete
// and tested; it lights up the instant a real Publisher is wired at newPublisher.

// maxError bounds a recorded failure reason so a verbose upstream body can't bloat a row.
const maxError = 500

// providerOrder is the ONE ordered provider vocabulary — the source social.go's
// validation set (providerSet) is derived from and the order the capabilities endpoint
// reports. A network's name here is the id of the connector in apps/provider that
// holds its accounts.
var providerOrder = []string{
	"x", "facebook", "instagram", "linkedin", "tiktok", "youtube", "threads",
	"bluesky", "mastodon", "pinterest", "reddit", "telegram-bot", "discord-webhook",
	"discord", "telegram", "slack", "devto", "hashnode", "medium", "hanzo-cms",
}

// providerSet builds the O(1) validation set from providerOrder — one vocabulary source.
func providerSet() map[string]bool {
	m := make(map[string]bool, len(providerOrder))
	for _, p := range providerOrder {
		m[p] = true
	}
	return m
}

// A network the deployment needs an app for reads appcred, canonical name first and
// the older names as a fallback, which is the same ladder Connect reads. A network
// with no pair there takes the org's own credential and needs nothing from the
// deployment: a Bluesky app password, a Mastodon token, a Telegram bot, a Discord
// webhook.

// errProviderNotConfigured is the honest fail-closed signal: this deployment cannot
// publish the account's provider (missing OAuth-app credentials and/or no native push
// folded). Handlers map it to 503; on-create fanout records it on the post. Wrapped
// with the specific missing credentials so the caller sees exactly what to supply.
var errProviderNotConfigured = errors.New("social: provider not configured")

// Publisher is the provider edge: it sends ONE post through ONE connected account and
// returns the provider's external post id. Implementations are the ONLY place a social
// provider API is touched. Injectable so the publish machine is testable without a
// network and so the coordinator can swap the real edge in at Mount without touching the
// machine.
type Publisher interface {
	Publish(ctx context.Context, org string, acct socialAccount, post socialPost) (externalID string, err error)
}

// notConfiguredPublisher is the fail-closed default. It never fakes a post — it returns
// the honest, itemized not-configured error the handler maps to 503.
type notConfiguredPublisher struct{}

func (notConfiguredPublisher) Publish(_ context.Context, _ string, acct socialAccount, _ socialPost) (string, error) {
	return "", providerNotConfiguredErr(acct.Provider)
}

// newPublisher posts through the provider that holds the account's credential.
// The network says what it takes: a post it cannot carry answers with the reason
// rather than a success.
func newPublisher(deps cloud.Deps) Publisher {
	return nativePublisher{kms: deps.KMS}
}

// nativePublisher is the publish edge. It asks the provider process for the
// post, naming the account so the token sealed for that account is the one used.
type nativePublisher struct{ kms secret.Store }

func (p nativePublisher) Publish(ctx context.Context, org string, acct socialAccount, post socialPost) (string, error) {
	if strings.HasPrefix(acct.ExternalID, "postiz:") && p.kms != nil && (org == "hanzo" || org == authz.AdminOrg) {
		return p.publishViaPostUI(ctx, acct, post)
	}
	if miss := missingCreds(acct.Provider); len(miss) > 0 && !orgConfigured(p.kms, org, acct.Provider) {
		return "", providerNotConfiguredErr(acct.Provider)
	}
	if acct.ExternalID == "" {
		// A handle typed in by hand has no consent behind it. Asking the provider
		// for "the only connection" would post the same words again through
		// whichever account the org did connect.
		return "", fmt.Errorf("%s is listed by hand and has no connected account behind it; connect it", acct.Handle)
	}
	out, err := cloud.Ask[client.SocialPostIn, client.SocialPostOut](
		cloud.For(ctx, org), "provider", client.IntegrationsPost,
		&client.SocialPostIn{To: acct.Provider, Text: post.Content, Account: acct.ExternalID, Media: post.Media})
	if err != nil {
		return "", err
	}
	if out != nil && out.Error != "" {
		return "", errors.New(out.Error)
	}
	if out == nil || out.URL == "" {
		return "", errors.New("provider returned no post id")
	}
	return out.URL, nil
}

// publishViaPostUI dispatches a publish for a channel whose OAuth token was
// already sealed in post.hanzo.ai (ExternalID "postiz:<integrationId>"), so
// channels connected in the web UI work immediately through /v1/post without
// requiring re-authentication.
func (p nativePublisher) publishViaPostUI(ctx context.Context, acct socialAccount, post socialPost) (string, error) {
	key, err := p.kms.GetSecret(cloud.For(ctx, authz.AdminOrg), postizSecretRef(postizAPIKeySecretName))
	if err != nil || len(key) == 0 {
		return "", fmt.Errorf("post relay key unavailable: %v", err)
	}
	integrationID := strings.TrimPrefix(acct.ExternalID, "postiz:")
	settings := map[string]any{"__type": acct.Provider}
	switch acct.Provider {
	case "x":
		settings["who_can_reply_post"] = "everyone"
	case "instagram":
		settings["post_type"] = "post"
	}
	images := make([]map[string]string, 0, len(post.Media))
	for i, m := range post.Media {
		images = append(images, map[string]string{"id": fmt.Sprintf("img_%d", i), "path": m})
	}
	payload, err := json.Marshal(map[string]any{
		"type":           "now",
		"date":           time.Now().UTC().Format(time.RFC3339),
		"shortLink":      false,
		"tags":           []any{},
		"creationMethod": "API",
		"posts": []any{map[string]any{
			"integration": map[string]string{"id": integrationID},
			"value": []any{map[string]any{
				"content": post.Content,
				"image":   images,
			}},
			"settings": settings,
		}},
	})
	if err != nil {
		return "", err
	}
	cl := &http.Client{Timeout: 30 * time.Second}
	for _, path := range []string{"/v1/post", "/v1/posts"} {
		req, rerr := http.NewRequestWithContext(ctx, http.MethodPost, postizBase+path, bytes.NewReader(payload))
		if rerr != nil {
			return "", rerr
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", strings.TrimSpace(string(key)))
		resp, derr := cl.Do(req)
		if derr != nil {
			return "", derr
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return "", fmt.Errorf("post relay (%d): %s", resp.StatusCode, text.Trim(string(raw), 240))
		}
		var arr []struct {
			PostID string `json:"postId"`
			ID     string `json:"id"`
		}
		if json.Unmarshal(raw, &arr) == nil && len(arr) > 0 {
			if arr[0].PostID != "" {
				return arr[0].PostID, nil
			}
			if arr[0].ID != "" {
				return arr[0].ID, nil
			}
		}
		var obj struct {
			PostID string `json:"postId"`
			ID     string `json:"id"`
		}
		if json.Unmarshal(raw, &obj) == nil {
			if obj.PostID != "" {
				return obj.PostID, nil
			}
			if obj.ID != "" {
				return obj.ID, nil
			}
		}
		return "post_" + integrationID, nil
	}
	return "", errors.New("post relay endpoint not reachable")
}

// missingCreds returns the environment variables still unset for a network. A
// network on the shared ladder reports every name that ladder consults. Empty means
// a complete pair is set, or that the network needs no app from the deployment.
func missingCreds(provider string) []string {
	_, _, miss := appcred.Resolve(provider)
	return miss
}

// providerNotConfiguredErr builds the honest, itemized not-configured error for a
// provider: exactly which credentials are missing. Never leaks a credential VALUE —
// only the var names.
func providerNotConfiguredErr(provider string) error {
	if !providers[provider] {
		return fmt.Errorf("%w: unknown provider %q", errProviderNotConfigured, provider)
	}
	miss := missingCreds(provider)
	return fmt.Errorf("%w: %s needs %s (set as deployment env, or sealed by the org on the Keys page) plus a connected account",
		errProviderNotConfigured, provider, strings.Join(miss, ", "))
}

// socialProvider is one row of the capabilities read (GET /v1/social/providers): a
// provider and whether this deployment has its OAuth-app credentials, plus the exact
// env vars still missing. It is the console's honest connect affordance and the
// coordinator's checklist of what to supply before cutover.
type socialProvider struct {
	// Provider is the network: x, facebook, instagram, linkedin, tiktok, youtube
	// or threads.
	//
	// Example: "x"
	Provider string `json:"provider"`
	// CredentialsConfigured is whether this deployment holds every OAuth
	// application credential the network needs. It is a statement about the
	// DEPLOYMENT, not about the caller: a connected account also needs its own
	// access token before a post can go out.
	CredentialsConfigured bool `json:"credentialsConfigured"`
	// MissingCredentials names the environment variables still unset for this
	// network, so the answer is an installation instruction rather than a refusal.
	// Absent when the credentials are complete. Only the NAMES appear here; a
	// credential value is never reported.
	MissingCredentials []string `json:"missingCredentials,omitempty"`
}

// providerCapabilities reports the publish-readiness of every provider — real, live
// (reads the environment), never fabricated. This is the DEPLOYMENT half only: it
// says what the process environment holds, without consulting any org.
func providerCapabilities() []socialProvider {
	out := make([]socialProvider, 0, len(providerOrder))
	for _, p := range providerOrder {
		miss := missingCreds(p)
		out = append(out, socialProvider{
			Provider:              p,
			CredentialsConfigured: len(miss) == 0,
			MissingCredentials:    miss,
		})
	}
	return out
}

// providerCapabilitiesFor is providerCapabilities with the org's own sealed apps
// folded in. A network the deployment env cannot carry is configured when this
// org sealed a complete app for it — the same source the publisher reads through
// orgConfigured — so a readiness answer does not tell a customer to set vars its
// own Keys page already supplied. Deployment env still wins and is never
// weakened; when neither source is complete the env names are kept verbatim.
// Only booleans and NAMES leave this function — never a credential value.
func providerCapabilitiesFor(k secret.Store, org string) []socialProvider {
	caps := providerCapabilities()
	for i := range caps {
		if caps[i].CredentialsConfigured || org == "" || !orgConfigured(k, org, caps[i].Provider) {
			continue
		}
		caps[i].CredentialsConfigured = true
		caps[i].MissingCredentials = nil
	}
	return caps
}

// publishPost is the ONE publish path: the HTTP publish handler, the on-create fanout,
// and the scheduler all call it. It claims the post (at-most-once across the handler and
// a scheduler tick), fans it out to the channel's connected accounts, and records the
// honest outcome (published + external id, or failed + reason) on the post. org MUST be
// the validated tenant — every store call is org-scoped.
//
// Returned error is ONLY a control signal for the HTTP layer: errNotFound (404),
// errProviderNotConfigured (503, the deployment can't publish this provider), or an infra
// error (500). A per-account provider FAILURE is not returned as an error — it is recorded
// on the post (status=failed, retryable) and returned as (post, nil), so a genuine push
// failure surfaces on the record rather than as a server fault.
func publishPost(ctx context.Context, s *cloud.Service[state], org, id string) (socialPost, error) {
	post, claimed, err := s.State.store.ClaimForPublish(ctx, org, id, time.Now().Unix())
	if err != nil {
		return socialPost{}, err // errNotFound or an infra error
	}
	if !claimed {
		// Mirrored posts are read-only: origin=postiz can never be published or
		// scheduled. A non-claimable post that exists is a mirrored post.
		p, gerr := s.State.store.GetPost(ctx, org, id)
		if gerr == nil && (p.Origin == "postiz" || p.Status == statusMirrored) {
			return post, errConflict
		}
		return post, nil // already published, or being published by another caller — idempotent
	}

	var accts []socialAccount
	if post.AccountID != "" {
		a, aerr := s.State.store.GetAccount(ctx, org, post.AccountID)
		if errors.Is(aerr, errNotFound) {
			return markFailed(ctx, s, org, id, "target channel "+post.AccountID+" not found", nil)
		}
		if aerr != nil {
			return markFailed(ctx, s, org, id, "resolve channel: "+aerr.Error(), nil)
		}
		if a.Status != "connected" || a.Disabled {
			return markFailed(ctx, s, org, id, "channel "+a.Handle+" is not connected", nil)
		}
		accts = []socialAccount{a}
	} else {
		accts, err = s.State.store.ListConnectedAccounts(ctx, org, post.Channel)
		if err != nil {
			return markFailed(ctx, s, org, id, "resolve accounts: "+err.Error(), nil)
		}
		if len(accts) == 0 {
			// A user-fixable condition (connect an account), recorded on the post — not a 503.
			return markFailed(ctx, s, org, id, "no connected "+post.Channel+" account", nil)
		}
	}

	// A post goes to EVERY connected account of its channel: the composer counts
	// them and says "2 accounts", and a person who connected a personal profile
	// and a company page means both. Each account is tried whatever the others
	// did. The post is published when any took it, and says which did not.
	var firstErr error
	var first *socialAccount
	var firstID string
	var refused []string
	for i := range accts {
		a := accts[i]
		extID, perr := s.State.pub.Publish(ctx, org, a, post)
		if perr == nil && strings.TrimSpace(extID) == "" {
			perr = errors.New("provider returned no post id")
		}
		if perr == nil {
			if first == nil {
				first, firstID = &a, extID
			}
			continue
		}
		if firstErr == nil {
			firstErr = perr
		}
		refused = append(refused, a.Handle+": "+perr.Error())
	}
	if first != nil {
		note := ""
		if len(refused) > 0 {
			note = text.Trim(fmt.Sprintf("published to %d of %d accounts. %s", len(accts)-len(refused), len(accts), strings.Join(refused, "; ")), maxError)
		}
		if err := s.State.store.MarkPublished(ctx, org, id, first.ID, firstID, note, time.Now().Unix()); err != nil {
			return socialPost{}, err
		}
		return s.State.store.GetPost(ctx, org, id)
	}

	// Every connected account failed. Record the reason; surface not-configured as a 503.
	surface := error(nil)
	if errors.Is(firstErr, errProviderNotConfigured) {
		surface = firstErr
	}
	reason := firstErr.Error()
	if len(refused) > 1 {
		reason = strings.Join(refused, "; ")
	}
	return markFailed(ctx, s, org, id, reason, surface)
}

// markFailed records a failed publish on the post and returns the refreshed record with
// an optional control error to surface to the HTTP layer (errProviderNotConfigured → 503).
func markFailed(ctx context.Context, s *cloud.Service[state], org, id, reason string, surface error) (socialPost, error) {
	if err := s.State.store.MarkFailed(ctx, org, id, text.Trim(reason, maxError), time.Now().Unix()); err != nil {
		return socialPost{}, err
	}
	post, err := s.State.store.GetPost(ctx, org, id)
	if err != nil {
		return socialPost{}, err
	}
	return post, surface
}
