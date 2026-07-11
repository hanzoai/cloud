package content

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/hanzoai/cloud/clients/integrations"
)

// channels.go is the REAL Distributor: the edge that fans a published/queued content
// item OUT to a brand's social channels via hanzoai/social's Public API. publish.go
// owns the stable orchestration (read the CMS doc, ask the Distributor, record the
// returned ids); this file owns the ONE place a social provider/API is touched.
//
// The social Public API surface used (Authorization: <per-brand API key>, raw — the
// social auth middleware reads the header value directly, no "Bearer" prefix):
//
//	GET  {social}/public/v1/integrations   → [{id,name,identifier,disabled,...}]
//	     a brand's connected channels; `identifier` is the provider (instagram/x/…),
//	     `id` is the integration id a post must target.
//	POST {social}/public/v1/posts  {type:"now"|"schedule", date, shortLink, tags:[],
//	     posts:[{integration:{id}, value:[{content,image:[{id,path,alt}]}], settings:{}}]}
//	     → [{postId, integration}]   (one entry per post; settings.__type is filled
//	     server-side from the integration's provider).
//
// ONE POST per channel: the social API 400s the WHOLE batch if any one channel's
// settings fail validation, so posting per-channel is what makes a partial fan-out
// honest (channel A can succeed while channel B fails) instead of an all-or-nothing.
//
// PER-BRAND CREDENTIALS. The social API key is per-org and lives ONLY in KMS, reached
// through clients/integrations (integrations.TokenFor). It is resolved at call time —
// never held in this process's config, never in a manifest. Any reason the key cannot
// be produced (integrations not mounted, org never connected social, KMS down) is the
// honest fail-closed: errNotConfigured → 503 / distribution "not_configured", never a
// crash, exactly the scaffold contract.

const (
	// socialProvider is the clients/integrations registry slug under which a brand's
	// hanzoai/social API key is custodied; socialKeyName is the KMS secret name.
	socialProvider = "social"
	socialKeyName  = "api_key"

	// socialURLEnv overrides the hanzoai/social base URL (self-hosted deployments);
	// defaultSocialURL is the managed default. One deployment-wide value — the base
	// URL is NOT per-org (the per-org part is the API key).
	socialURLEnv     = "CLOUD_SOCIAL_URL"
	defaultSocialURL = "https://api.social.hanzo.ai"

	// distributeTimeout bounds a single social API call so a slow edge never wedges a
	// publish/transition.
	distributeTimeout = 20 * time.Second
)

// errUpstream is the honest "the social edge is temporarily unavailable" signal
// (network error, or a non-2xx that is not an auth failure). It maps to 503 (Service
// Unavailable — the dependency, not this service, is down) and, on a transition,
// records distribution "failed" (retryable) — never a 5xx from a bug, never a wedge.
var errUpstream = errors.New("content: distribution edge unavailable")

// socialDistributor is the real Distributor over hanzoai/social's Public API. It opens
// no store; the CMS state is the framework's. baseURL is deployment config; apiKey
// resolves the per-brand key at call time (fail-closed); http bounds every call.
type socialDistributor struct {
	baseURL string
	http    *http.Client
	apiKey  func(ctx context.Context, org string) (string, error)
}

// newDistributor builds the real hanzoai/social Distributor. Wired at Mount as the ONE
// place the edge is selected. No secret is captured here — the per-brand key is fetched
// from KMS (via clients/integrations) on every call.
func newDistributor() Distributor {
	return socialDistributor{
		baseURL: socialBaseURL(),
		http:    &http.Client{Timeout: distributeTimeout},
		apiKey:  socialAPIKey,
	}
}

// socialBaseURL resolves the hanzoai/social base URL from env, trimming a trailing
// slash so path joins are clean.
func socialBaseURL() string {
	if v := strings.TrimSpace(os.Getenv(socialURLEnv)); v != "" {
		return strings.TrimRight(v, "/")
	}
	return defaultSocialURL
}

// socialAPIKey resolves a brand's hanzoai/social API key from KMS via clients/
// integrations. ANY reason it cannot be produced — the integrations plane is not
// mounted, the org never connected social, or KMS is down — collapses to the ONE
// honest fail-closed signal (errNotConfigured). Distribution then reports
// "not_configured" without ever crashing.
func socialAPIKey(ctx context.Context, org string) (string, error) {
	tok, err := integrations.TokenFor(ctx, org, socialProvider, socialKeyName)
	if err != nil || len(bytes.TrimSpace(tok)) == 0 {
		return "", errNotConfigured
	}
	return string(bytes.TrimSpace(tok)), nil
}

// Channels lists a brand's connected social channels. A brand that has not connected
// social (no key) fails closed (errNotConfigured → 503); a connected brand with no
// channels yet honestly returns an empty list (200), not an error.
func (d socialDistributor) Channels(ctx context.Context, org string) ([]Channel, error) {
	key, err := d.apiKey(ctx, org)
	if err != nil {
		return nil, err
	}
	raw, err := d.listIntegrations(ctx, key)
	if err != nil {
		return nil, err
	}
	out := make([]Channel, 0, len(raw))
	for _, r := range raw {
		out = append(out, Channel{ID: r.ID, Provider: r.Identifier, Name: r.Name, Disabled: r.Disabled})
	}
	return out, nil
}

// Publish fans a content item OUT to its channels. It resolves the requested channel
// tokens against the brand's connected integrations (by integration id OR provider
// identifier; an empty request targets every connected, enabled channel), then posts
// to each ONE at a time so a per-channel failure is reported honestly rather than
// failing the whole fan-out. A brand with no social key, or connected but with zero
// channels, fails closed as "not_configured".
func (d socialDistributor) Publish(ctx context.Context, org string, req DistributeRequest) (DistributeResult, error) {
	key, err := d.apiKey(ctx, org)
	if err != nil {
		return DistributeResult{}, err
	}
	connected, err := d.listIntegrations(ctx, key)
	if err != nil {
		return DistributeResult{}, err
	}
	if len(connected) == 0 {
		return DistributeResult{}, errNotConfigured // brand connected social but has no channels
	}

	targets, unknown := resolveTargets(connected, req.Channels)
	scheduled := strings.TrimSpace(req.ScheduleAt) != ""

	results := make([]ChannelResult, 0, len(targets)+len(unknown))
	external := make(map[string]string, len(targets))
	for _, t := range targets {
		id, perr := d.createPost(ctx, key, t, req, scheduled)
		if perr != nil {
			results = append(results, ChannelResult{
				Channel: t.ID, Provider: t.Identifier, Status: "failed", Error: shortErr(perr),
			})
			continue
		}
		status := "distributed"
		if scheduled {
			status = "scheduled"
		}
		results = append(results, ChannelResult{
			Channel: t.ID, Provider: t.Identifier, Status: status, ExternalID: id,
		})
		external[t.ID] = id
	}
	// A requested channel that matches no connected integration is an honest per-channel
	// failure — the caller named something the brand has not connected.
	for _, u := range unknown {
		results = append(results, ChannelResult{Channel: u, Status: "failed", Error: "channel not connected"})
	}

	return DistributeResult{Scheduled: scheduled, ExternalIDs: external, Channels: results}, nil
}

// resolveTargets maps the requested channel tokens onto the brand's connected
// integrations. Matching is by exact integration id OR by provider identifier (which
// targets EVERY connected account of that provider). An empty request targets ALL
// connected, ENABLED channels. Disabled channels are never targeted. Tokens that match
// nothing come back as `unknown`, so the caller reports them as honest per-channel
// failures rather than silently dropping them.
func resolveTargets(connected []socialIntegration, want []string) (targets []socialIntegration, unknown []string) {
	enabled := make([]socialIntegration, 0, len(connected))
	byID := make(map[string]socialIntegration, len(connected))
	byProvider := make(map[string][]socialIntegration, len(connected))
	for _, c := range connected {
		if c.Disabled {
			continue
		}
		enabled = append(enabled, c)
		byID[c.ID] = c
		byProvider[c.Identifier] = append(byProvider[c.Identifier], c)
	}

	if len(want) == 0 {
		return enabled, nil
	}

	seen := make(map[string]bool, len(want))
	add := func(c socialIntegration) {
		if !seen[c.ID] {
			seen[c.ID] = true
			targets = append(targets, c)
		}
	}
	for _, w := range want {
		matched := false
		if c, ok := byID[w]; ok {
			add(c)
			matched = true
		}
		for _, c := range byProvider[w] {
			add(c)
			matched = true
		}
		if !matched {
			unknown = append(unknown, w)
		}
	}
	return targets, unknown
}

// ── hanzoai/social Public API wire shapes + calls ───────────────────────────────

// socialIntegration is one row of GET /public/v1/integrations (only the fields the
// Distributor needs: the id to target, the provider identifier, and enabled state).
type socialIntegration struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Identifier string `json:"identifier"`
	Disabled   bool   `json:"disabled"`
}

// listIntegrations calls GET /public/v1/integrations for the brand. An auth failure
// (bad/expired key) is fail-closed as not_configured; any other non-2xx or a transport
// error is errUpstream (503 / retryable), never a 5xx from a bug.
func (d socialDistributor) listIntegrations(ctx context.Context, key string) ([]socialIntegration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.baseURL+"/public/v1/integrations", nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errUpstream, err)
	}
	req.Header.Set("Authorization", key)

	resp, err := d.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errUpstream, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, errNotConfigured // invalid/expired social key ⇒ not configured for this brand
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: integrations %d", errUpstream, resp.StatusCode)
	}
	var out []socialIntegration
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("%w: decode integrations: %v", errUpstream, err)
	}
	return out, nil
}

// socialCreatePost is the POST /public/v1/posts body (the fields the CreatePostDto
// requires). tags is present-but-empty (the DTO demands an array); settings is left
// empty — social fills settings.__type from the integration's provider server-side.
type socialCreatePost struct {
	Type      string           `json:"type"` // "now" | "schedule"
	ShortLink bool             `json:"shortLink"`
	Date      string           `json:"date"` // ISO-8601; required even for "now"
	Tags      []any            `json:"tags"`
	Posts     []socialPostItem `json:"posts"`
}

type socialPostItem struct {
	Integration socialRef      `json:"integration"`
	Value       []socialValue  `json:"value"`
	Settings    map[string]any `json:"settings"`
}

type socialRef struct {
	ID string `json:"id"`
}

type socialValue struct {
	Content string        `json:"content"`
	Image   []socialMedia `json:"image"`
}

type socialMedia struct {
	ID   string `json:"id"`
	Path string `json:"path"`
	Alt  string `json:"alt,omitempty"`
}

// socialPostResult is one row of the POST /public/v1/posts response.
type socialPostResult struct {
	PostID      string `json:"postId"`
	Integration string `json:"integration"`
}

// createPost posts ONE content item to ONE channel and returns its social post id.
// type=now posts immediately (date is the current instant); type=schedule queues it
// for req.ScheduleAt. The returned id is what publish.go records into external_ids for
// reconciliation.
func (d socialDistributor) createPost(ctx context.Context, key string, ch socialIntegration, req DistributeRequest, scheduled bool) (string, error) {
	postType, date := "now", time.Now().UTC().Format(time.RFC3339)
	if scheduled {
		postType, date = "schedule", strings.TrimSpace(req.ScheduleAt)
	}
	payload := socialCreatePost{
		Type:      postType,
		ShortLink: false,
		Date:      date,
		Tags:      []any{},
		Posts: []socialPostItem{{
			Integration: socialRef{ID: ch.ID},
			Value:       []socialValue{{Content: req.Content, Image: socialMediaFrom(req.Media)}},
			Settings:    map[string]any{},
		}},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode post: %v", err)
	}

	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, d.baseURL+"/public/v1/posts", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	hreq.Header.Set("Authorization", key)
	hreq.Header.Set("Content-Type", "application/json")

	resp, err := d.http.Do(hreq)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("social %d: %s", resp.StatusCode, socialErrMsg(body))
	}
	var out []socialPostResult
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("decode post: %v", err)
	}
	for _, r := range out {
		if strings.TrimSpace(r.PostID) != "" {
			return r.PostID, nil
		}
	}
	return "", fmt.Errorf("social returned no post id")
}

// socialMediaFrom maps the provider-agnostic media refs onto the social MediaDto
// shape. The social DTO requires both an id and a path; for an externally-hosted asset
// (a studio render URL) the URL is both — social validates the path and, when
// RESTRICT_UPLOAD_DOMAINS is set, an unaccepted URL surfaces as an honest per-channel
// 400 rather than a silent drop.
func socialMediaFrom(media []MediaRef) []socialMedia {
	out := make([]socialMedia, 0, len(media))
	for _, m := range media {
		u := strings.TrimSpace(m.URL)
		if u == "" {
			continue
		}
		out = append(out, socialMedia{ID: u, Path: u, Alt: m.Alt})
	}
	return out
}

// socialErrMsg pulls social's `{msg}` error field out of a non-2xx body for a short,
// honest per-channel reason (falling back to a trimmed raw body).
func socialErrMsg(body []byte) string {
	var e struct {
		Msg   string `json:"msg"`
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil {
		if e.Msg != "" {
			return e.Msg
		}
		if e.Error != "" {
			return e.Error
		}
	}
	s := strings.TrimSpace(string(body))
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// shortErr renders a per-channel failure reason for a ChannelResult — one trimmed
// line, never a stack, never leaking the API key (which is only ever a header).
func shortErr(err error) string {
	s := strings.TrimSpace(err.Error())
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
