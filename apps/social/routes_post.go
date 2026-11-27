package social

import (
	"context"
	"net/http"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

type postOps struct{ s *cloud.Service[state] }

// mountPostRoutes mounts the unified /v1/post (and /v1/posts) surface directly
// onto social's router so /v1/post, /v1/channel, and /v1/social share one store,
// scheduler, and publisher in-process.
func mountPostRoutes(app cloud.Router, s *cloud.Service[state]) {
	g := app.Group("/v1/post")
	g.Use(cloud.Bridge())

	o := postOps{s: s}
	g.Get("", o.list)
	g.Post("", o.create, zip.WithStatus(http.StatusCreated))
	g.Get("/:id", o.get)
	g.Put("/:id", o.update)
	g.Delete("/:id", o.delete)
	g.Post("/:id/publish", o.publish)
	g.Get("/:id/analytics", o.analytics)
}

// postItem is one social post owned by the caller's organization.
type postItem struct {
	// ID is the unique post identifier.
	ID string `json:"id"`
	// Content is the post body text (up to 8192 characters).
	Content string `json:"content"`
	// Channel is the target social network identifier (for example "x", "linkedin", "bluesky").
	Channel string `json:"channel"`
	// Status is the post lifecycle state: draft, scheduled, published, failed, or mirrored.
	Status string `json:"status"`
	// ScheduleAt is the scheduled publish time in unix seconds (0 when unscheduled or immediate).
	ScheduleAt int64 `json:"scheduleAt"`
	// Media lists attached image or video URLs (always serialized as an array).
	Media []string `json:"media"`
	// AccountID is the specific connected channel ID targeted by or used to publish this post.
	AccountID string `json:"accountId,omitempty"`
	// ExternalID is the upstream network post ID or URL returned upon publish.
	ExternalID string `json:"externalId,omitempty"`
	// Error records the failure or partial-fanout reason from the last publish attempt, if any.
	Error string `json:"error,omitempty"`
	// Origin indicates whether the post was created natively ("native") or mirrored ("postiz").
	Origin string `json:"origin,omitempty"`
	// ExternalIDPostiz is the upstream calendar post ID when mirrored from post.hanzo.ai.
	ExternalIDPostiz string `json:"externalIdPostiz,omitempty"`
	// ExternalIntegrationId is the upstream channel integration ID when mirrored.
	ExternalIntegrationId string `json:"externalIntegrationId,omitempty"`
	// ExternalGroup is the employee, brand, or batch group associated with the post.
	ExternalGroup string `json:"externalGroup,omitempty"`
	// ReleaseURL is the public permalink of the published post on the social network when known.
	ReleaseURL string `json:"releaseUrl,omitempty"`
	// CreatedAt is when the post was created, in unix seconds.
	CreatedAt int64 `json:"createdAt"`
	// UpdatedAt is when the post was last updated, in unix seconds.
	UpdatedAt int64 `json:"updatedAt"`
}

// postListFilter narrows the post listing by status, network channel, specific channel ID, group, or limit.
type postListFilter struct {
	// Status keeps only posts in one state (draft, scheduled, published, failed, or mirrored).
	Status string `json:"status,omitempty"`
	// Channel keeps only posts targeting one social network (for example "x" or "linkedin").
	Channel string `json:"channel,omitempty"`
	// AccountID keeps only posts targeting or published by one specific connected channel ID.
	AccountID string `json:"accountId,omitempty"`
	// Group keeps only posts belonging to one employee or brand channel group (for example "Antje Worring" or "Hanzo AI").
	Group string `json:"group,omitempty"`
	// Limit bounds the maximum number of posts returned (default 200, max 1000).
	Limit string `json:"limit,omitempty"`
}

// postList is the envelope returned when listing posts.
type postList struct {
	// Data holds the matching posts ordered newest-updated first.
	Data []postItem `json:"data"`
}

// postCreateIn creates, schedules, drafts, or immediately publishes a post across
// a network, multiple networks, specific connected channel IDs, or an employee/brand group.
type postCreateIn struct {
	// Content is the text body of the post (required, bounded at 8192 characters).
	Content string `json:"content" url:"-"`
	// Channel is the target social network (for example "x", "linkedin", "bluesky"). Defaults to "x" when no Channels, AccountID, AccountIDs, or Group is given.
	Channel string `json:"channel,omitempty" url:"-"`
	// Channels fans the post out across multiple social networks in one call (for example ["x", "linkedin", "bluesky"]).
	Channels []string `json:"channels,omitempty" url:"-"`
	// AccountID targets one specific connected channel ID (for example a personal executive X account rather than every X account in the org).
	AccountID string `json:"accountId,omitempty" url:"-"`
	// AccountIDs fans the post out across a specific list of connected channel IDs.
	AccountIDs []string `json:"accountIds,omitempty" url:"-"`
	// Group fans the post out across all connected channels in the named employee or brand group (for example "Antje Worring" or "Hanzo AI").
	Group string `json:"group,omitempty" url:"-"`
	// Status sets the initial lifecycle: "draft", "scheduled", or "now" (alias for immediate publish). When omitted, defaults to "scheduled" if ScheduleAt > 0, else "draft".
	Status string `json:"status,omitempty" url:"-"`
	// ScheduleAt is the unix timestamp in seconds when a scheduled post becomes due (0 or <= now publishes immediately when Status is "scheduled" or "now").
	ScheduleAt int64 `json:"scheduleAt,omitempty" url:"-"`
	// Media attaches up to 10 image or video URLs to the post.
	Media []string `json:"media,omitempty" url:"-"`
}

// postCreateOut returns the created post (and all fanout posts in Posts when targeting multiple channels).
type postCreateOut struct {
	// ID is the primary created post identifier.
	ID string `json:"id"`
	// Content is the stored post body text.
	Content string `json:"content"`
	// Channel is the primary post's social network identifier.
	Channel string `json:"channel"`
	// Status is the resulting lifecycle state after creation (and immediate publish when requested).
	Status string `json:"status"`
	// ScheduleAt is the scheduled publish time in unix seconds.
	ScheduleAt int64 `json:"scheduleAt"`
	// Media lists the attached media URLs.
	Media []string `json:"media"`
	// AccountID is the connected channel ID targeted or published through.
	AccountID string `json:"accountId,omitempty"`
	// ExternalID is the upstream post ID or URL when published immediately.
	ExternalID string `json:"externalId,omitempty"`
	// Error carries any publish refusal message when an immediate publish failed.
	Error string `json:"error,omitempty"`
	// ExternalGroup carries the employee or brand group name when targeted by group.
	ExternalGroup string `json:"externalGroup,omitempty"`
	// ReleaseURL is the live post URL when reported by the network.
	ReleaseURL string `json:"releaseUrl,omitempty"`
	// CreatedAt is when the post was created, in unix seconds.
	CreatedAt int64 `json:"createdAt"`
	// UpdatedAt is when the post was last updated, in unix seconds.
	UpdatedAt int64 `json:"updatedAt"`
	// Posts lists every created post row when a multi-channel or group fanout produced more than one post.
	Posts []postItem `json:"posts,omitempty"`
}

// postRef addresses one post by path id.
type postRef struct {
	// ID is the post identifier from the URL path.
	ID string `json:"id"`
}

// postUpdateIn replaces one post's content, target channel, schedule, status, or media.
type postUpdateIn struct {
	// ID is the post identifier from the URL path.
	ID string `json:"-" url:"id"`
	// Content is the updated post body text (required).
	Content string `json:"content" url:"-"`
	// Channel is the target social network identifier (defaults to "x").
	Channel string `json:"channel,omitempty" url:"-"`
	// AccountID optionally pins the post to a specific connected channel ID.
	AccountID string `json:"accountId,omitempty" url:"-"`
	// Status is the post lifecycle state (draft, scheduled, published, or failed; defaults to draft).
	Status string `json:"status,omitempty" url:"-"`
	// ScheduleAt is the scheduled unix timestamp in seconds.
	ScheduleAt int64 `json:"scheduleAt,omitempty" url:"-"`
	// Media replaces the attached media URL list.
	Media []string `json:"media,omitempty" url:"-"`
}

// postAnalyticsTick is one dated data point inside a post engagement metric series.
type postAnalyticsTick struct {
	// Date is the YYYY-MM-DD date of the measurement.
	Date string `json:"date"`
	// Total is the metric value recorded on Date.
	Total int64 `json:"total"`
}

// postAnalyticsMetric is one named engagement metric series for a post.
type postAnalyticsMetric struct {
	// Label names the metric (for example "Impressions", "Likes", "Reposts", "Replies").
	Label string `json:"label"`
	// Total sums the metric over the window.
	Total int64 `json:"total"`
	// Data holds the dated points for the metric.
	Data []postAnalyticsTick `json:"data"`
}

// postAnalyticsOut reports delivery metadata and upstream engagement metrics for one post.
type postAnalyticsOut struct {
	// PostID is the post identifier.
	PostID string `json:"postId"`
	// Channel is the social network identifier.
	Channel string `json:"channel"`
	// AccountID is the connected channel ID that published the post, when set.
	AccountID string `json:"accountId,omitempty"`
	// Status is the post's current delivery status.
	Status string `json:"status"`
	// ReleaseURL is the public permalink of the live post when available.
	ReleaseURL string `json:"releaseUrl,omitempty"`
	// ExternalID is the upstream platform post identifier or URL.
	ExternalID string `json:"externalId,omitempty"`
	// Metrics holds upstream engagement metrics for the post when available.
	Metrics []postAnalyticsMetric `json:"metrics"`
}

func fromPlanePost(p client.PlanePost) postItem {
	media := p.Media
	if media == nil {
		media = []string{}
	}
	return postItem{
		ID:                    p.ID,
		Content:               p.Content,
		Channel:               p.Channel,
		Status:                p.Status,
		ScheduleAt:            p.ScheduleAt,
		Media:                 media,
		AccountID:             p.AccountID,
		ExternalID:            p.ExternalID,
		Error:                 p.Error,
		Origin:                p.Origin,
		ExternalIDPostiz:      p.ExternalIDPostiz,
		ExternalIntegrationId: p.ExternalIntegrationId,
		ExternalGroup:         p.ExternalGroup,
		ReleaseURL:            p.ReleaseURL,
		CreatedAt:             p.CreatedAt,
		UpdatedAt:             p.UpdatedAt,
	}
}

// list returns the caller organization's social posts, optionally filtered by
// status, network channel, specific channel ID (`accountId`), or employee/brand `group`.
func (o postOps) list(ctx context.Context, in *postListFilter) (*postList, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	res, err := planePostsList(cloud.For(ctx, org), &client.PlanePostFilter{
		Status:    in.Status,
		Channel:   in.Channel,
		AccountID: in.AccountID,
		Group:     in.Group,
		Limit:     in.Limit,
	})
	if err != nil {
		return nil, err
	}
	items := make([]postItem, 0, len(res.Data))
	for _, p := range res.Data {
		items = append(items, fromPlanePost(p))
	}
	return &postList{Data: items}, nil
}

// create stores a new draft, scheduled post, or immediate publish (`status: "now"`),
// supporting single-network, multi-network (`channels`), specific channel ID (`accountId` / `accountIds`),
// and employee/brand group (`group`) fanout.
func (o postOps) create(ctx context.Context, in *postCreateIn) (*postCreateOut, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	res, err := planePostCreate(cloud.For(ctx, org), &client.PlanePostCreateIn{
		Content:    in.Content,
		Channel:    in.Channel,
		Channels:   in.Channels,
		AccountID:  in.AccountID,
		AccountIDs: in.AccountIDs,
		Group:      in.Group,
		Status:     in.Status,
		ScheduleAt: in.ScheduleAt,
		Media:      in.Media,
	})
	if err != nil {
		return nil, err
	}
	first := fromPlanePost(res.PlanePost)
	out := &postCreateOut{
		ID:            first.ID,
		Content:       first.Content,
		Channel:       first.Channel,
		Status:        first.Status,
		ScheduleAt:    first.ScheduleAt,
		Media:         first.Media,
		AccountID:     first.AccountID,
		ExternalID:    first.ExternalID,
		Error:         first.Error,
		ExternalGroup: first.ExternalGroup,
		ReleaseURL:    first.ReleaseURL,
		CreatedAt:     first.CreatedAt,
		UpdatedAt:     first.UpdatedAt,
	}
	if len(res.Posts) > 0 {
		out.Posts = make([]postItem, 0, len(res.Posts))
		for _, p := range res.Posts {
			out.Posts = append(out.Posts, fromPlanePost(p))
		}
	}
	return out, nil
}

// get returns one social post by id for the caller's organization.
func (o postOps) get(ctx context.Context, in *postRef) (*postItem, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	res, err := planePostGet(cloud.For(ctx, org), &client.PlanePostRef{ID: in.ID})
	if err != nil {
		return nil, err
	}
	item := fromPlanePost(*res)
	return &item, nil
}

// update replaces one social post's content, target channel, schedule, status, and media.
func (o postOps) update(ctx context.Context, in *postUpdateIn) (*postItem, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	res, err := planePostUpdate(cloud.For(ctx, org), &client.PlanePostUpdateIn{
		ID:         in.ID,
		Content:    in.Content,
		Channel:    in.Channel,
		AccountID:  in.AccountID,
		Status:     in.Status,
		ScheduleAt: in.ScheduleAt,
		Media:      in.Media,
	})
	if err != nil {
		return nil, err
	}
	item := fromPlanePost(*res)
	return &item, nil
}

// delete removes one social post record from the caller's organization.
func (o postOps) delete(ctx context.Context, in *postRef) (*cloud.Unit, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := planePostDelete(cloud.For(ctx, org), &client.PlanePostRef{ID: in.ID}); err != nil {
		return nil, err
	}
	return nil, nil
}

// publish immediately publishes a draft, scheduled, or failed post to its target channel(s)
// and returns the updated post record with its external post ID or failure reason.
func (o postOps) publish(ctx context.Context, in *postRef) (*postItem, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	res, err := planePostPublish(cloud.For(ctx, org), &client.PlanePostRef{ID: in.ID})
	if err != nil {
		return nil, err
	}
	item := fromPlanePost(*res)
	return &item, nil
}

// analytics returns delivery status, release URL, and upstream engagement metrics for one post.
func (o postOps) analytics(ctx context.Context, in *postRef) (*postAnalyticsOut, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	res, err := planePostAnalytics(cloud.For(ctx, org), &client.PlanePostRef{ID: in.ID})
	if err != nil {
		return nil, err
	}
	metrics := make([]postAnalyticsMetric, 0, len(res.Metrics))
	for _, m := range res.Metrics {
		ticks := make([]postAnalyticsTick, 0, len(m.Data))
		for _, d := range m.Data {
			ticks = append(ticks, postAnalyticsTick{Date: d.Date, Total: d.Total})
		}
		metrics = append(metrics, postAnalyticsMetric{Label: m.Label, Total: m.Total, Data: ticks})
	}
	return &postAnalyticsOut{
		PostID:     res.PostID,
		Channel:    res.Channel,
		AccountID:  res.AccountID,
		Status:     res.Status,
		ReleaseURL: res.ReleaseURL,
		ExternalID: res.ExternalID,
		Metrics:    metrics,
	}, nil
}
