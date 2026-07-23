package ads

// provider.go is the ad-network EXECUTION edge: the ONE place /v1/ads consumes the
// connector plane. An ad campaign runs on a provider (Meta/Google/…) using the
// ORG'S OWN connector token — resolved at call time from KMS through the
// integrations.TokenFor custody seam, never held in this process, never in a
// manifest. This closes the gap the ads store left open: a stored Campaign is now
// LAUNCHABLE against the real provider, and it is what the /v1/campaign paid
// channel fans out to (apps/wire_seams.go adapts LaunchPaid/PaidSpend/PausePaid
// onto campaign.Channel).
//
// FAIL-CLOSED CUSTODY. Every operation resolves the org's token FIRST; any reason
// it cannot be produced — the org never connected the ad account, the integrations
// plane is unmounted, KMS is down — is errNotConnected, and NO provider call is
// made. The token rides the Authorization header only (never the URL, never a log,
// never argv), exactly the meta.go connector discipline.
//
// LEAST SURPRISE ON SPEND. A launch creates the campaign OBJECT on the provider;
// delivery (and therefore spend) does not begin until the ad-set/ad legs are wired
// (the documented ads follow-up), so a launch cannot silently burn budget. Spend is
// READ back from the provider's insights (PaidSpend) — the connector's reported
// number the /v1/campaign metrics plane joins with the analytics funnel.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/clients/integrations"
)

const (
	// accessTokenSecret is the KMS secret name every ad connector custodies its
	// long-lived token under (meta.go/google_marketing.go: "access_token").
	accessTokenSecret = "access_token"
	// launchTimeout bounds a single provider call so a slow ad edge never wedges a
	// launch or a metrics read.
	launchTimeout = 20 * time.Second
)

// platformConnector maps an ads Platform to its integrations connector id. The
// paid channel consumes the connector via TokenFor(org, <id>, "access_token").
// A platform with no entry has no connector wired → errUnsupportedPlatform (the
// token is never even sought), so the map is the ONE source of "which ad networks
// this deployment can run".
var platformConnector = map[string]string{
	"meta":      "meta_ads",
	"google":    "google_ads",
	"tiktok":    "tiktok_ads",
	"reddit":    "reddit_ads",
	"linkedin":  "linkedin_ads",
	"microsoft": "microsoft_ads",
}

var (
	// errNotConnected — the org has no usable connection for the platform (no
	// token, or the provider rejected it). Fail-closed: no spend, no fabrication.
	errNotConnected = errors.New("ads: ad account not connected for org")
	// errUnsupportedPlatform — a valid platform whose provider execution is not yet
	// wired (the connector may be connected; only the create/read impl is missing).
	errUnsupportedPlatform = errors.New("ads: paid execution not wired for platform")
	// errUpstream — the ad platform edge failed transiently (network / non-auth non-2xx).
	errUpstream = errors.New("ads: ad platform edge unavailable")
)

// tokenFor is the connector-custody seam. It defaults to integrations.TokenFor
// (the ONE KMS-backed token door) and is a package var ONLY so a test can exercise
// the provider path — the same reason meta.go's endpoints are package vars. It is
// never reassigned in production.
var tokenFor = integrations.TokenFor

// metaAdsBase is the Meta Graph API base. A package var so a test points create /
// insights / pause at an httptest server; never mutated in production.
var metaAdsBase = "https://graph.facebook.com/v21.0"

// adHTTP is the ONE bounded client for provider calls.
var adHTTP = &http.Client{Timeout: launchTimeout}

// PaidPlan is the standalone contract for launching one ad campaign — the campaign
// paid channel adapts campaign.Plan onto it (apps/wire_seams.go). BudgetCents is
// the org's own (connector-paid) budget; ScheduleAt an optional start time.
type PaidPlan struct {
	Platform    string
	Account     string // provider ad-account ref (Meta: act_<id> or <id>)
	Name        string
	Objective   string // provider objective enum; defaulted per-provider when empty
	BudgetCents int64
	ScheduleAt  int64
}

// PaidRef is a launched ad campaign's durable handle: the provider-side id plus the
// platform/account needed to read spend or pause it.
type PaidRef struct {
	Platform   string
	Account    string
	ExternalID string
	Status     string
	Detail     string
}

// adToken resolves (connectorID, token) for a platform, fail-closed. An unmapped
// platform never reaches KMS. An empty/absent token is errNotConnected.
func adToken(ctx context.Context, org, platform string) (string, string, error) {
	connectorID, ok := platformConnector[strings.ToLower(strings.TrimSpace(platform))]
	if !ok {
		return "", "", fmt.Errorf("%w: %s", errUnsupportedPlatform, platform)
	}
	tok, err := tokenFor(ctx, org, connectorID, accessTokenSecret)
	if err != nil || len(strings.TrimSpace(string(tok))) == 0 {
		return connectorID, "", errNotConnected
	}
	return connectorID, strings.TrimSpace(string(tok)), nil
}

// LaunchPaid creates the ad campaign on its provider using the org's connector
// token. Fail-closed: it resolves the token BEFORE any provider call. Meta is
// executed for real; other connected platforms return errUnsupportedPlatform
// (the connector is verified, only the provider impl is the remaining gap).
func LaunchPaid(ctx context.Context, org string, p PaidPlan) (PaidRef, error) {
	platform := strings.ToLower(strings.TrimSpace(p.Platform))
	connectorID, token, err := adToken(ctx, org, platform)
	if err != nil {
		return PaidRef{}, err
	}
	switch platform {
	case "meta":
		return metaCreateCampaign(ctx, token, p)
	default:
		return PaidRef{}, fmt.Errorf("%w: %s (connector %s connected)", errUnsupportedPlatform, platform, connectorID)
	}
}

// PaidSpend reads the provider-reported spend (minor units) for a launched ad
// campaign. Fail-closed on the token; honest 0 when the platform is unsupported.
func PaidSpend(ctx context.Context, org string, ref PaidRef) (int64, error) {
	platform := strings.ToLower(strings.TrimSpace(ref.Platform))
	_, token, err := adToken(ctx, org, platform)
	if err != nil {
		return 0, err
	}
	switch platform {
	case "meta":
		return metaCampaignSpend(ctx, token, ref.ExternalID)
	default:
		return 0, fmt.Errorf("%w: %s", errUnsupportedPlatform, platform)
	}
}

// PausePaid pauses a launched ad campaign on its provider. Fail-closed on the token.
func PausePaid(ctx context.Context, org string, ref PaidRef) error {
	platform := strings.ToLower(strings.TrimSpace(ref.Platform))
	_, token, err := adToken(ctx, org, platform)
	if err != nil {
		return err
	}
	switch platform {
	case "meta":
		return metaPauseCampaign(ctx, token, ref.ExternalID)
	default:
		return fmt.Errorf("%w: %s", errUnsupportedPlatform, platform)
	}
}

// ── Meta (Facebook/Instagram) ad campaign execution ─────────────────────────

// metaErr is Meta's Graph API error envelope.
type metaErr struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    int    `json:"code"`
}

// metaAcct normalizes a Meta ad-account ref to the act_<id> form the campaigns
// edge expects. Empty stays empty (caller rejects).
func metaAcct(account string) string {
	a := strings.TrimSpace(account)
	if a == "" {
		return ""
	}
	if strings.HasPrefix(a, "act_") {
		return a
	}
	return "act_" + a
}

// metaCreateCampaign POSTs a campaign to /act_<id>/campaigns. Objective defaults to
// OUTCOME_TRAFFIC; special_ad_categories is the required empty set; the object is
// created ACTIVE but does not deliver (no ad sets) so no spend starts on launch.
func metaCreateCampaign(ctx context.Context, token string, p PaidPlan) (PaidRef, error) {
	account := metaAcct(p.Account)
	if account == "" {
		return PaidRef{}, fmt.Errorf("meta: ad account (account) is required")
	}
	objective := strings.TrimSpace(p.Objective)
	if objective == "" {
		objective = "OUTCOME_TRAFFIC"
	}
	form := url.Values{
		"name":                  {strings.TrimSpace(p.Name)},
		"objective":             {objective},
		"status":                {"ACTIVE"},
		"special_ad_categories": {"[]"},
	}
	var out struct {
		ID    string   `json:"id"`
		Error *metaErr `json:"error"`
	}
	if err := metaPost(ctx, metaAdsBase+"/"+account+"/campaigns", token, form, &out); err != nil {
		return PaidRef{}, err
	}
	if out.Error != nil {
		return PaidRef{}, fmt.Errorf("meta create campaign: %s", out.Error.Message)
	}
	if strings.TrimSpace(out.ID) == "" {
		return PaidRef{}, fmt.Errorf("meta create campaign returned no id")
	}
	return PaidRef{Platform: "meta", Account: account, ExternalID: out.ID, Status: "live"}, nil
}

// metaCampaignSpend reads spend from /{campaignID}/insights?fields=spend. Meta
// reports spend in the account currency's major unit as a string; it is converted
// to minor units (cents). No insights row (campaign hasn't spent) → 0.
func metaCampaignSpend(ctx context.Context, token, campaignID string) (int64, error) {
	if strings.TrimSpace(campaignID) == "" {
		return 0, nil
	}
	var out struct {
		Data []struct {
			Spend string `json:"spend"`
		} `json:"data"`
		Error *metaErr `json:"error"`
	}
	if err := metaGet(ctx, metaAdsBase+"/"+campaignID+"/insights?fields=spend", token, &out); err != nil {
		return 0, err
	}
	if out.Error != nil {
		return 0, fmt.Errorf("meta insights: %s", out.Error.Message)
	}
	if len(out.Data) == 0 {
		return 0, nil
	}
	dollars, err := strconv.ParseFloat(strings.TrimSpace(out.Data[0].Spend), 64)
	if err != nil || dollars < 0 {
		return 0, nil
	}
	return int64(dollars*100 + 0.5), nil
}

// metaPauseCampaign POSTs status=PAUSED to /{campaignID}.
func metaPauseCampaign(ctx context.Context, token, campaignID string) error {
	if strings.TrimSpace(campaignID) == "" {
		return fmt.Errorf("meta: campaign id required")
	}
	var out struct {
		Success bool     `json:"success"`
		Error   *metaErr `json:"error"`
	}
	if err := metaPost(ctx, metaAdsBase+"/"+campaignID, token, url.Values{"status": {"PAUSED"}}, &out); err != nil {
		return err
	}
	if out.Error != nil {
		return fmt.Errorf("meta pause: %s", out.Error.Message)
	}
	return nil
}

// ── bounded HTTP (token on the Authorization header only) ────────────────────

func metaPost(ctx context.Context, endpoint, token string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("%w: %v", errUpstream, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return adDo(req, out)
}

func metaGet(ctx context.Context, endpoint, token string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("%w: %v", errUpstream, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return adDo(req, out)
}

// adDo runs a bounded provider request and decodes the JSON body. A 401/403 is
// fail-closed as errNotConnected (the org's token is invalid/revoked — the
// connection is not usable); any other non-2xx or transport error is errUpstream.
// The body is bounded so a hostile edge cannot amplify memory.
func adDo(req *http.Request, out any) error {
	resp, err := adHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", errUpstream, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return errNotConnected
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%w: %d", errUpstream, resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%w: decode: %v", errUpstream, err)
	}
	return nil
}
