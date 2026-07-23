package integrations

// google_marketing.go adds the two Google MARKETING connectors — Google Ads and
// Google Analytics (GA4) — as siblings of the base google.go productivity provider.
// They REUSE google.go's OAuth plumbing wholesale (googleCreds, googleConfigured,
// googleExchange, googleRevoke, googleAccount, googlePostForm, googleHTTP) and the
// same registered Google Cloud app creds (GOOGLE_CLIENT_ID/SECRET); the ONLY thing
// that differs is the requested SCOPE set — Ads (adwords) vs Analytics
// (analytics.readonly) vs the base Drive/Sheets. Distinct provider ids give each
// its OWN KMS custody namespace (/orgs/{org}/integrations/{google_ads|
// google_analytics}), so an org can hold three independent Google consents, one per
// product, and the ads/analytics readers each pull their own token via
// integrations.TokenFor(org, "google_ads"|"google_analytics", "access_token").
//
// access_type=offline + prompt=consent request a refresh token; both access and
// refresh are sealed. Linking a client's Ads/Analytics account is an org-admin
// action. Google Analytics is read-only; Google Ads requests the single "adwords"
// scope (Google offers no finer-grained read-only Ads scope), and calling the Ads
// API additionally requires a developer token, supplied by the ads reader at call
// time — not part of the connector's custody.

import (
	"net/url"
	"strings"
)

const (
	googleAdsProvider       = "google_ads"
	googleAnalyticsProvider = "google_analytics"
)

// Scope sets. openid+email give googleAccount the connecting user's email for the
// account label; the product scope is the capability.
var (
	googleAdsScopes = []string{
		"openid", "email",
		"https://www.googleapis.com/auth/adwords",
	}
	googleAnalyticsScopes = []string{
		"openid", "email",
		"https://www.googleapis.com/auth/analytics.readonly",
	}
)

func init() {
	register(&Provider{
		ID:           googleAdsProvider,
		Name:         "Google Ads",
		Description:  "Connect Google Ads to read campaigns, keywords, and performance.",
		Category:     "Advertising",
		AdminOnly:    true,
		Scopes:       googleAdsScopes,
		RedirectPath: callbackPath(googleAdsProvider),
		Secrets:      []string{accessSecret, refreshSecret},
		Configured:   googleConfigured, // reuse: GOOGLE_CLIENT_ID present
		Creds:        googleCreds,      // reuse: GOOGLE_CLIENT_ID/SECRET
		Authorize:    googleAuthorizeWith(googleAdsScopes),
		Exchange:     googleExchange, // reuse: scope-agnostic code→tokens+account
		Revoke:       googleRevoke,   // reuse
	})

	register(&Provider{
		ID:           googleAnalyticsProvider,
		Name:         "Google Analytics",
		Description:  "Connect Google Analytics (GA4) to read traffic, conversions, and audiences.",
		Category:     "Analytics",
		AdminOnly:    true,
		Scopes:       googleAnalyticsScopes,
		RedirectPath: callbackPath(googleAnalyticsProvider),
		Secrets:      []string{accessSecret, refreshSecret},
		Configured:   googleConfigured,
		Creds:        googleCreds,
		Authorize:    googleAuthorizeWith(googleAnalyticsScopes),
		Exchange:     googleExchange,
		Revoke:       googleRevoke,
	})
}

// googleAuthorizeWith builds a Google consent-URL Authorize bound to a specific
// scope set — the ONE thing that varies across the Google product family. It is the
// exact param shape google.go's googleAuthorize uses (offline access + consent for
// a refresh token, include_granted_scopes to keep prior grants), parameterized on
// scopes so the base provider and the two marketing providers share one builder
// without duplicating the plumbing.
func googleAuthorizeWith(scopes []string) func(OAuthConfig, string, string) (string, error) {
	joined := strings.Join(scopes, " ")
	return func(creds OAuthConfig, redirectURI, state string) (string, error) {
		q := url.Values{
			"client_id":              {creds.ClientID},
			"redirect_uri":           {redirectURI},
			"response_type":          {"code"},
			"scope":                  {joined},
			"state":                  {state},
			"access_type":            {"offline"},
			"include_granted_scopes": {"true"},
			"prompt":                 {"consent"},
		}
		return googleAuthURL + "?" + q.Encode(), nil
	}
}
