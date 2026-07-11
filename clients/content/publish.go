package content

import (
	"context"
	"strings"

	"github.com/hanzoai/cloud/clients/framework"
)

// publish.go is the distribution seam: fan a published/queued content item OUT to
// social channels. The site half of "publish" needs no push — the site PULLS live
// content (karma.style reads GET /v1/framework/Post?filters=[["status","in",
// ["published"]]]), so becoming `published` IS site-publish. This file handles only
// the channel PUSH.
//
// Decomplected: Distributor is the edge (hanzoai/social, "the distribution edge; cloud
// orchestrates"); Publish is the stable orchestration that reads the CMS document,
// asks the Distributor to post, and records the returned channel post ids back onto the
// document for reconciliation (queued → published). The real Distributor (wired at
// Mount) calls the social Public API:
//
//	GET  {social}/public/v1/integrations?group=<brand>   → a brand's channels
//	POST {social}/public/v1/posts  {type:"now"|"schedule", date, posts:[{integration:{id},
//	     value:[{content,image}], settings:{__type:<provider>}}]}   (Authorization: <org API key>)
//
// with the per-brand key custodied in KMS (via clients/integrations), never in a manifest.
// The scaffold ships the notConfigured Distributor (honest fail-closed), so a transition
// into distribution records "not_configured" and NEVER fails the status change.

// Channel is one connected distribution channel for a brand (a social integration).
type Channel struct {
	ID       string `json:"id"`       // the social integration id to target in a post
	Provider string `json:"provider"` // "x" | "instagram" | "tiktok" | ...
	Name     string `json:"name"`
	Disabled bool   `json:"disabled"`
}

// MediaRef is one media attachment on a post.
type MediaRef struct {
	URL  string `json:"url"`
	Alt  string `json:"alt,omitempty"`
	Mime string `json:"mime,omitempty"`
}

// DistributeRequest is the provider-agnostic post the Distributor sends. Content is the
// caption/copy; Channels are provider ids (or integration ids) to target; ScheduleAt ""
// means publish now, otherwise an ISO-8601 future time.
type DistributeRequest struct {
	Content    string
	Media      []MediaRef
	Channels   []string
	ScheduleAt string
}

// DistributeResult is what the edge returns: whether it was scheduled (vs posted
// now), the per-channel external post ids to record for reconciliation, and the
// honest per-channel outcome. ExternalIDs holds ONLY the channels that succeeded
// (channel id → external post id); Channels reports every attempted channel,
// including failures, so partial success is never flattened into a blanket error.
type DistributeResult struct {
	Scheduled   bool
	ExternalIDs map[string]string // channel id → external post id (successes only)
	Channels    []ChannelResult   // per-channel honest status (ok + failed)
}

// ChannelResult is the honest outcome for ONE channel of a distribution. A channel
// that posted carries its ExternalID; a channel that failed carries a short Error.
// This is what lets a partial fan-out (some channels ok, some down) report the truth
// instead of a 5xx.
type ChannelResult struct {
	Channel    string `json:"channel"`              // the social integration id targeted
	Provider   string `json:"provider,omitempty"`   // "x" | "instagram" | ... when known
	Status     string `json:"status"`               // "distributed" | "scheduled" | "failed"
	ExternalID string `json:"externalId,omitempty"` // social post id, when it went out
	Error      string `json:"error,omitempty"`      // short reason, when it failed
}

// Distributor is the channel edge. Channels lists a brand's connected channels;
// Publish posts (or schedules) one item to them. Implementations are the ONLY place a
// social provider/API is touched.
type Distributor interface {
	Channels(ctx context.Context, org string) ([]Channel, error)
	Publish(ctx context.Context, org string, req DistributeRequest) (DistributeResult, error)
}

// notConfiguredDistributor is the fail-closed default until a real Distributor is wired
// at Mount. It never fakes a post — it returns an honest error the handler maps to 503
// and a transition records as distribution "not_configured".
type notConfiguredDistributor struct{}

func (notConfiguredDistributor) Channels(context.Context, string) ([]Channel, error) {
	return nil, errNotConfigured
}
func (notConfiguredDistributor) Publish(context.Context, string, DistributeRequest) (DistributeResult, error) {
	return DistributeResult{}, errNotConfigured
}

// PublishInput identifies the CMS item to distribute. The item's channels/caption/media
// are read from the document, so callers name the item, not its content.
type PublishInput struct {
	DocType    string `json:"doctype"`
	Name       string `json:"name"`
	ScheduleAt string `json:"scheduleAt,omitempty"` // "" = now
}

// PublishResult reports the distribution outcome. Status is "distributed" |
// "scheduled" | "failed" | "not_configured" so a caller (and a transition response)
// sees the honest state without an error being fatal. "failed" means EVERY targeted
// channel failed (the whole fan-out missed) — a partial success stays "distributed"/
// "scheduled" with the per-channel truth in Results. Results is the per-channel
// breakdown (which channel went out, which did not, and why).
type PublishResult struct {
	Status      string            `json:"status"`
	Channels    []string          `json:"channels,omitempty"`
	ExternalIDs map[string]string `json:"externalIds,omitempty"`
	Results     []ChannelResult   `json:"results,omitempty"`
}

// Publish distributes a CMS content item to its channels and records the returned post
// ids back onto the document (best effort — a distribution outage never wedges the CMS).
// It is the ONE publish path: the HTTP handler, the content_publish automation action,
// and a transition's side effect all call it. org MUST be the validated tenant.
func Publish(ctx context.Context, org string, in PublishInput) (PublishResult, error) {
	s := mounted
	if s == nil {
		return PublishResult{}, errNotMounted
	}
	if !isPublishableDocType(in.DocType) {
		return PublishResult{}, errUnknownDocType
	}
	doc, err := framework.Get(ctx, org, in.DocType, in.Name)
	if err != nil {
		return PublishResult{}, err
	}

	req := distributeRequestFromDoc(doc.Data)
	req.ScheduleAt = strings.TrimSpace(in.ScheduleAt)

	res, err := s.State.dist.Publish(ctx, org, req)
	if err != nil {
		return PublishResult{}, err
	}

	// Record the external ids for reconciliation (best effort). A failure here is logged
	// by the caller; the post already went out, so it must not surface as a 5xx.
	if len(res.ExternalIDs) > 0 {
		data := cloneData(doc.Data)
		data["external_ids"] = res.ExternalIDs
		if err := framework.UpdateData(ctx, org, in.DocType, in.Name, data); err != nil {
			s.Log.Warn("record external ids failed (post already sent)",
				"doctype", in.DocType, "name", in.Name, "err", err)
		}
	}

	return PublishResult{
		Status:      overallStatus(res),
		Channels:    req.Channels,
		ExternalIDs: res.ExternalIDs,
		Results:     res.Channels,
	}, nil
}

// overallStatus folds a fan-out into the ONE honest headline. Any channel that
// went out makes the post "distributed" (or "scheduled") — the failures are still
// itemised in Results. Only a fan-out where NOTHING went out is "failed". A caller
// never has to guess: the headline plus the per-channel Results are always consistent.
func overallStatus(res DistributeResult) string {
	if len(res.ExternalIDs) == 0 {
		return "failed"
	}
	if res.Scheduled {
		return "scheduled"
	}
	return "distributed"
}

// distributeRequestFromDoc extracts the provider-agnostic post from a content document:
// caption (falling back to body), the comma-separated channels, and the media array.
func distributeRequestFromDoc(data map[string]any) DistributeRequest {
	content, _ := data["caption"].(string)
	if strings.TrimSpace(content) == "" {
		content, _ = data["body"].(string)
	}
	return DistributeRequest{
		Content:  strings.TrimSpace(content),
		Channels: splitCSV(dataString(data, "channels")),
		Media:    mediaRefsFrom(data["media"]),
	}
}

// mediaRefsFrom coerces the JSON `media` field ([{url,alt,mime}]) into []MediaRef,
// tolerating a missing/oddly-typed value (returns nil) — never panics on client data.
func mediaRefsFrom(v any) []MediaRef {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]MediaRef, 0, len(arr))
	for _, it := range arr {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		url, _ := m["url"].(string)
		if strings.TrimSpace(url) == "" {
			continue
		}
		alt, _ := m["alt"].(string)
		mime, _ := m["mime"].(string)
		out = append(out, MediaRef{URL: url, Alt: alt, Mime: mime})
	}
	return out
}

// splitCSV splits a trimmed, non-empty comma list into its non-empty items.
func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
