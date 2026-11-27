package ad

// provider.go is the ad-network EXECUTION edge: the ONE place /v1/ad consumes the
// connector plane. An ad campaign runs on a provider (Meta/Google/…) using the
// ORG'S OWN connector token — resolved at call time from KMS through the
// provider.TokenFor custody client, never held in this process, never in a
// manifest. This closes the gap the ads store left open: a stored AdCampaign is now
// LAUNCHABLE against the real provider, and it is what the /v1/campaign paid
// channel fans out to (plugin/campaign/clients.go adapts LaunchPaid/PaidSpend/PausePaid
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
	"bytes"
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

	"github.com/hanzoai/cloud/apps/provider"
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

// tokenFor is the connector-custody client. It defaults to provider.TokenFor
// (the ONE KMS-backed token source) and is a package var ONLY so a test can exercise
// the provider path — the same reason meta.go's endpoints are package vars. It is
// never reassigned in production.
var tokenFor = provider.TokenFor

// metaAdsBase is the Meta Graph API base. A package var so a test points create /
// insights / pause at an httptest server; never mutated in production.
var metaAdsBase = "https://graph.facebook.com/v21.0"

// linkedinAdsBase is the LinkedIn Marketing REST API base (versioned via
// LinkedIn-Version header). A package var for httptest injection only.
var (
	linkedinAdsBase    = "https://api.linkedin.com/rest"
	linkedinAdsVersion = "202611"
)

// adHTTP is the ONE bounded client for provider calls.
var adHTTP = &http.Client{Timeout: launchTimeout}

// PaidPlan is the standalone contract for launching one ad campaign — the campaign
// paid channel adapts campaign.Plan onto it (plugin/campaign/clients.go). BudgetCents is
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
	case "linkedin":
		return linkedinCreateCampaign(ctx, token, p)
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
	case "linkedin":
		return linkedinCampaignSpend(ctx, token, ref.ExternalID)
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
	case "linkedin":
		return linkedinPauseCampaign(ctx, token, ref)
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

// ── LinkedIn Marketing REST API (v202611) campaign execution ─────────────────

// linkedinAcctID normalizes a LinkedIn sponsored account ref ("560774110" or
// "urn:li:sponsoredAccount:560774110") to its bare numeric id.
func linkedinAcctID(account string) string {
	a := strings.TrimSpace(account)
	a = strings.TrimPrefix(a, "urn:li:sponsoredAccount:")
	return strings.TrimSpace(a)
}

func linkedinObjective(objective string) string {
	switch strings.ToUpper(strings.TrimSpace(objective)) {
	case "WEBSITE_CONVERSIONS", "CONVERSIONS", "OUTCOME_SALES":
		return "WEBSITE_CONVERSIONS"
	case "LEAD_GENERATION", "LEADS", "OUTCOME_LEADS":
		return "LEAD_GENERATION"
	case "BRAND_AWARENESS", "AWARENESS", "OUTCOME_AWARENESS":
		return "BRAND_AWARENESS"
	case "ENGAGEMENT", "OUTCOME_ENGAGEMENT":
		return "ENGAGEMENT"
	case "VIDEO_VIEWS":
		return "VIDEO_VIEWS"
	default:
		return "WEBSITE_VISITS"
	}
}

func linkedinCampaignGroupURN(ctx context.Context, token, acctID string) (string, error) {
	var groups struct {
		Elements []struct {
			ID int64 `json:"id"`
		} `json:"elements"`
	}
	u := fmt.Sprintf("%s/adAccounts/%s/adCampaignGroups?q=search&count=1",
		strings.TrimRight(linkedinAdsBase, "/"), url.PathEscape(acctID))
	if _, err := linkedinReq(ctx, http.MethodGet, u, token, nil, nil, &groups); err != nil {
		return "", err
	}
	if len(groups.Elements) > 0 && groups.Elements[0].ID > 0 {
		return fmt.Sprintf("urn:li:sponsoredCampaignGroup:%d", groups.Elements[0].ID), nil
	}
	createBody := map[string]any{
		"account": "urn:li:sponsoredAccount:" + acctID,
		"name":    "Hanzo Campaigns",
		"status":  "ACTIVE",
	}
	var created struct {
		ID int64 `json:"id"`
	}
	hdr, err := linkedinReq(ctx, http.MethodPost,
		fmt.Sprintf("%s/adAccounts/%s/adCampaignGroups", strings.TrimRight(linkedinAdsBase, "/"), url.PathEscape(acctID)),
		token, nil, createBody, &created)
	if err != nil {
		return "", err
	}
	if id := strings.TrimSpace(hdr.Get("X-Restli-Id")); id != "" {
		return "urn:li:sponsoredCampaignGroup:" + id, nil
	}
	if created.ID > 0 {
		return fmt.Sprintf("urn:li:sponsoredCampaignGroup:%d", created.ID), nil
	}
	return "", fmt.Errorf("linkedin: campaign group creation returned no id")
}

// linkedinCreateCampaign creates a PAUSED Sponsored Campaign under the org's
// LinkedIn ad account so spend never starts silently before creatives are attached.
func linkedinCreateCampaign(ctx context.Context, token string, p PaidPlan) (PaidRef, error) {
	acctID := linkedinAcctID(p.Account)
	if acctID == "" {
		return PaidRef{}, fmt.Errorf("linkedin: ad account (account) is required")
	}
	groupURN, err := linkedinCampaignGroupURN(ctx, token, acctID)
	if err != nil {
		return PaidRef{}, err
	}
	budgetCents := p.BudgetCents
	if budgetCents < 1000 {
		budgetCents = 1000 // LinkedIn minimum daily budget ($10.00)
	}
	body := map[string]any{
		"account":       "urn:li:sponsoredAccount:" + acctID,
		"campaignGroup": groupURN,
		"name":          strings.TrimSpace(p.Name),
		"status":        "PAUSED",
		"type":          "SPONSORED_UPDATES",
		"costType":      "CPM",
		"objectiveType": linkedinObjective(p.Objective),
		"dailyBudget": map[string]string{
			"currencyCode": "USD",
			"amount":       fmt.Sprintf("%.2f", float64(budgetCents)/100.0),
		},
		"locale": map[string]string{"country": "US", "language": "en"},
	}
	var out struct {
		ID json.Number `json:"id"`
	}
	u := fmt.Sprintf("%s/adAccounts/%s/adCampaigns", strings.TrimRight(linkedinAdsBase, "/"), url.PathEscape(acctID))
	hdr, err := linkedinReq(ctx, http.MethodPost, u, token, nil, body, &out)
	if err != nil {
		return PaidRef{}, err
	}
	extID := strings.TrimSpace(hdr.Get("X-Restli-Id"))
	if extID == "" {
		extID = strings.TrimSpace(out.ID.String())
	}
	if extID == "" {
		return PaidRef{}, fmt.Errorf("linkedin create campaign returned no id")
	}
	return PaidRef{Platform: "linkedin", Account: acctID, ExternalID: extID, Status: "paused"}, nil
}

// linkedinCampaignSpend reads lifetime spend from /rest/adAnalytics for one
// sponsored campaign and converts costInLocalCurrency into minor units (cents).
func linkedinCampaignSpend(ctx context.Context, token, campaignID string) (int64, error) {
	id := strings.TrimPrefix(strings.TrimSpace(campaignID), "urn:li:sponsoredCampaign:")
	if id == "" {
		return 0, nil
	}
	urn := url.QueryEscape("urn:li:sponsoredCampaign:" + id)
	u := fmt.Sprintf("%s/adAnalytics?q=analytics&pivot=CAMPAIGN&dateRange=(start:(year:2020,month:1,day:1))&timeGranularity=ALL&campaigns=List(%s)&fields=costInLocalCurrency",
		strings.TrimRight(linkedinAdsBase, "/"), urn)
	var out struct {
		Elements []struct {
			CostInLocalCurrency string `json:"costInLocalCurrency"`
		} `json:"elements"`
	}
	if _, err := linkedinReq(ctx, http.MethodGet, u, token, nil, nil, &out); err != nil {
		return 0, err
	}
	var total int64
	for _, el := range out.Elements {
		dollars, err := strconv.ParseFloat(strings.TrimSpace(el.CostInLocalCurrency), 64)
		if err == nil && dollars > 0 {
			total += int64(dollars*100 + 0.5)
		}
	}
	return total, nil
}

// linkedinPauseCampaign issues a Rest.li PARTIAL_UPDATE setting status=PAUSED.
func linkedinPauseCampaign(ctx context.Context, token string, ref PaidRef) error {
	acctID := linkedinAcctID(ref.Account)
	campID := strings.TrimPrefix(strings.TrimSpace(ref.ExternalID), "urn:li:sponsoredCampaign:")
	if acctID == "" || campID == "" {
		return fmt.Errorf("linkedin: account and campaign id required")
	}
	u := fmt.Sprintf("%s/adAccounts/%s/adCampaigns/%s",
		strings.TrimRight(linkedinAdsBase, "/"), url.PathEscape(acctID), url.PathEscape(campID))
	patch := map[string]any{"patch": map[string]any{"$set": map[string]string{"status": "PAUSED"}}}
	_, err := linkedinReq(ctx, http.MethodPost, u, token, map[string]string{"X-RestLi-Method": "PARTIAL_UPDATE"}, patch, nil)
	return err
}

func linkedinReq(ctx context.Context, method, endpoint, token string, extraHeaders map[string]string, payload any, out any) (http.Header, error) {
	var bodyReader io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("%w: encode: %v", errUpstream, err)
		}
		bodyReader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errUpstream, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("LinkedIn-Version", linkedinAdsVersion)
	req.Header.Set("X-Restli-Protocol-Version", "2.0.0")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	resp, err := adHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errUpstream, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, errNotConnected
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: %d", errUpstream, resp.StatusCode)
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return nil, fmt.Errorf("%w: decode: %v", errUpstream, err)
		}
	}
	return resp.Header, nil
}
