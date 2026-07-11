package social

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
)

// publish.go is the distribution edge of the social fold: the ONE path that pushes a
// post OUT to its channel's connected accounts, plus the provider seam that push runs
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
// reports.
var providerOrder = []string{"x", "facebook", "instagram", "linkedin", "tiktok", "youtube", "threads"}

// providerSet builds the O(1) validation set from providerOrder — one vocabulary source.
func providerSet() map[string]bool {
	m := make(map[string]bool, len(providerOrder))
	for _, p := range providerOrder {
		m[p] = true
	}
	return m
}

// providerCreds is the global OAuth-app credential each provider requires to publish,
// mirroring the live orchestrator's env var names (the hanzoai/social provider modules,
// e.g. x.provider.ts reads X_API_KEY/X_API_SECRET). A provider with all vars present is
// "credentials-configured"; a real publish ALSO needs a per-account access token from the
// connect/OAuth flow. This map is the authoritative "what must be supplied" list — the
// capabilities endpoint and the not-configured error both read it, so the answer is never
// hardcoded in two places.
var providerCreds = map[string][]string{
	"x":         {"X_API_KEY", "X_API_SECRET"},
	"facebook":  {"FACEBOOK_APP_ID", "FACEBOOK_APP_SECRET"},
	"instagram": {"FACEBOOK_APP_ID", "FACEBOOK_APP_SECRET"}, // Instagram posts via the Facebook Graph app
	"linkedin":  {"LINKEDIN_CLIENT_ID", "LINKEDIN_CLIENT_SECRET"},
	"tiktok":    {"TIKTOK_CLIENT_ID", "TIKTOK_CLIENT_SECRET"},
	"youtube":   {"YOUTUBE_CLIENT_ID", "YOUTUBE_CLIENT_SECRET"},
	"threads":   {"THREADS_APP_ID", "THREADS_APP_SECRET"},
}

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
	Publish(ctx context.Context, org string, acct Account, post Post) (externalID string, err error)
}

// notConfiguredPublisher is the fail-closed default. It never fakes a post — it returns
// the honest, itemized not-configured error the handler maps to 503.
type notConfiguredPublisher struct{}

func (notConfiguredPublisher) Publish(_ context.Context, _ string, acct Account, _ Post) (string, error) {
	return "", providerNotConfiguredErr(acct.Provider)
}

// newPublisher selects the publish edge at Mount — the ONE place a real provider
// integration is wired (the twin of content.newDistributor). Native per-provider push is
// NOT folded into the binary yet and no deployment carries the provider OAuth-app
// credentials, so the honest fail-closed default is returned. When the coordinator folds
// a real Publisher (native push, or the hanzoai/social Public API edge that
// clients/content already speaks), it is swapped in HERE and the machine — publishPost,
// the scheduler, on-create fanout — is unchanged.
func newPublisher(_ cloud.Deps) Publisher { return notConfiguredPublisher{} }

// missingCreds returns the provider's required env vars that are unset on this
// deployment (empty slice ⇒ credentials fully configured).
func missingCreds(provider string) []string {
	need := providerCreds[provider]
	miss := make([]string, 0, len(need))
	for _, k := range need {
		if strings.TrimSpace(os.Getenv(k)) == "" {
			miss = append(miss, k)
		}
	}
	return miss
}

// providerNotConfiguredErr builds the honest, itemized not-configured error for a
// provider: exactly which credentials are missing (or, if creds are present, that native
// push is not yet folded). Never leaks a credential VALUE — only the var names.
func providerNotConfiguredErr(provider string) error {
	if _, known := providerCreds[provider]; !known {
		return fmt.Errorf("%w: unknown provider %q", errProviderNotConfigured, provider)
	}
	if miss := missingCreds(provider); len(miss) > 0 {
		return fmt.Errorf("%w: %s needs %s (set as deployment env) plus a connected account access token",
			errProviderNotConfigured, provider, strings.Join(miss, ", "))
	}
	return fmt.Errorf("%w: %s credentials are present but native push is not folded into this binary yet",
		errProviderNotConfigured, provider)
}

// ProviderCapability is one row of the capabilities read (GET /v1/social/providers): a
// provider and whether this deployment has its OAuth-app credentials, plus the exact
// env vars still missing. It is the console's honest connect affordance and the
// coordinator's checklist of what to supply before cutover.
type ProviderCapability struct {
	Provider              string   `json:"provider"`
	CredentialsConfigured bool     `json:"credentialsConfigured"`
	MissingCredentials    []string `json:"missingCredentials,omitempty"`
}

// providerCapabilities reports the publish-readiness of every provider — real, live
// (reads the environment), never fabricated.
func providerCapabilities() []ProviderCapability {
	out := make([]ProviderCapability, 0, len(providerOrder))
	for _, p := range providerOrder {
		miss := missingCreds(p)
		out = append(out, ProviderCapability{
			Provider:              p,
			CredentialsConfigured: len(miss) == 0,
			MissingCredentials:    miss,
		})
	}
	return out
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
func publishPost(ctx context.Context, s *cloud.Service[state], org, id string) (Post, error) {
	post, claimed, err := s.State.store.ClaimForPublish(ctx, org, id, time.Now().Unix())
	if err != nil {
		return Post{}, err // errNotFound or an infra error
	}
	if !claimed {
		return post, nil // already published, or being published by another caller — idempotent
	}

	accts, err := s.State.store.ListConnectedAccounts(ctx, org, post.Channel)
	if err != nil {
		return markFailed(ctx, s, org, id, "resolve accounts: "+err.Error(), nil)
	}
	if len(accts) == 0 {
		// A user-fixable condition (connect an account), recorded on the post — not a 503.
		return markFailed(ctx, s, org, id, "no connected "+post.Channel+" account", nil)
	}

	var firstErr error
	for _, a := range accts {
		extID, perr := s.State.pub.Publish(ctx, org, a, post)
		if perr == nil && strings.TrimSpace(extID) == "" {
			perr = errors.New("provider returned no post id")
		}
		if perr == nil {
			if err := s.State.store.MarkPublished(ctx, org, id, a.ID, extID, time.Now().Unix()); err != nil {
				return Post{}, err
			}
			return s.State.store.GetPost(ctx, org, id)
		}
		if firstErr == nil {
			firstErr = perr
		}
	}

	// Every connected account failed. Record the reason; surface not-configured as a 503.
	surface := error(nil)
	if errors.Is(firstErr, errProviderNotConfigured) {
		surface = firstErr
	}
	return markFailed(ctx, s, org, id, firstErr.Error(), surface)
}

// markFailed records a failed publish on the post and returns the refreshed record with
// an optional control error to surface to the HTTP layer (errProviderNotConfigured → 503).
func markFailed(ctx context.Context, s *cloud.Service[state], org, id, reason string, surface error) (Post, error) {
	if err := s.State.store.MarkFailed(ctx, org, id, clipN(reason, maxError), time.Now().Unix()); err != nil {
		return Post{}, err
	}
	post, err := s.State.store.GetPost(ctx, org, id)
	if err != nil {
		return Post{}, err
	}
	return post, surface
}
