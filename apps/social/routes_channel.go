package social

import (
	"context"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
)

type channelOps struct{ s *cloud.Service[state] }

// mountChannelRoutes mounts the unified /v1/channel (and /v1/channels) surface
// directly onto social's router so /v1/channel, /v1/post, and /v1/social share
// one store, scheduler, and publisher in-process.
func mountChannelRoutes(app cloud.Router, s *cloud.Service[state]) {
	g := app.Group("/v1/channel")
	g.Use(cloud.Bridge())

	o := channelOps{s: s}
	g.Get("", o.list)
	g.Get("/groups", o.groups)
	g.Get("/providers", o.providers)
	g.Post("/connect", o.connect)
	g.Get("/:id", o.get)
	g.Put("/:id", o.update)
	g.Put("/:id/group", o.setGroup)
	g.Delete("/:id", o.delete)
	g.Get("/:id/analytics", o.analytics)
}

// channelItem is one connected social or publishing channel owned by the caller's org.
type channelItem struct {
	// ID is the unique channel identifier used across /v1/channel, /v1/post, CLI, MCP, and post.hanzo.ai.
	ID string `json:"id"`
	// Provider is the social or publishing network identifier (for example x, linkedin, facebook, instagram, bluesky, discord, youtube, tiktok, threads, mastodon).
	Provider string `json:"provider"`
	// Name is the human-readable display name of the channel profile or page.
	Name string `json:"name,omitempty"`
	// Handle is the public handle or account label on the network (for example "@antje_worring" or "@hanzoai").
	Handle string `json:"handle"`
	// Profile is the upstream profile username or slug on the network.
	Profile string `json:"profile,omitempty"`
	// Picture is the avatar image URL of the connected channel.
	Picture string `json:"picture,omitempty"`
	// Status is the connection lifecycle state: connected, disconnected, or error.
	Status string `json:"status"`
	// ExternalID is the upstream platform or relay identifier bound to the sealed credential.
	ExternalID string `json:"externalId,omitempty"`
	// GroupID is the identifier of the employee or brand group this channel belongs to.
	GroupID string `json:"groupId,omitempty"`
	// GroupName is the employee or brand group name (for example "Antje Worring", "Zach Kelling", or "Hanzo AI").
	GroupName string `json:"groupName,omitempty"`
	// RefreshNeeded reports whether the upstream OAuth grant has expired and needs interactive reconnection.
	RefreshNeeded bool `json:"refreshNeeded,omitempty"`
	// Disabled reports whether publishing to this channel is paused.
	Disabled bool `json:"disabled,omitempty"`
	// CreatedAt is when the channel was recorded, in unix seconds.
	CreatedAt int64 `json:"createdAt"`
	// UpdatedAt is when the channel row was last updated, in unix seconds.
	UpdatedAt int64 `json:"updatedAt"`
}

// channelListFilter narrows the channel listing by network provider, group, or page size.
type channelListFilter struct {
	// Provider keeps only channels on the named network (for example "x" or "linkedin"). Omit for all networks.
	Provider string `json:"provider,omitempty"`
	// Group keeps only channels belonging to the named employee or brand group (matching groupId or case-insensitive groupName).
	Group string `json:"group,omitempty"`
	// Limit bounds the maximum number of channels returned (default 200, max 1000).
	Limit string `json:"limit,omitempty"`
}

// channelList is the envelope returned when listing channels.
type channelList struct {
	// Data holds the matching channels ordered newest-updated first.
	Data []channelItem `json:"data"`
}

// channelEmptyIn is the empty query input for collection metadata reads.
type channelEmptyIn struct{}

// channelGroupItem summarizes one employee or brand channel group and how many channels belong to it.
type channelGroupItem struct {
	// ID is the group identifier.
	ID string `json:"id"`
	// Name is the human-readable employee or brand group label (for example "Antje Worring" or "Hanzo AI").
	Name string `json:"name"`
	// Channels is the number of channels assigned to this group.
	Channels int `json:"channels"`
}

// channelGroupList is the envelope returned when listing channel groups.
type channelGroupList struct {
	// Data holds all non-empty channel groups in the caller's org.
	Data []channelGroupItem `json:"data"`
}

// channelProviderItem reports whether OAuth or credential publishing is ready for one social network.
type channelProviderItem struct {
	// Provider is the network identifier (for example "x", "linkedin", "bluesky").
	Provider string `json:"provider"`
	// CredentialsConfigured is true when OAuth app credentials are configured for the deployment or sealed by the org, or when the network uses direct credentials.
	CredentialsConfigured bool `json:"credentialsConfigured"`
	// MissingCredentials lists any unset environment variable names when CredentialsConfigured is false.
	MissingCredentials []string `json:"missingCredentials,omitempty"`
}

// channelProviderList is the envelope returned when listing channel providers.
type channelProviderList struct {
	// Data holds readiness entries for all supported social and publishing networks.
	Data []channelProviderItem `json:"data"`
}

// channelConnectIn starts an OAuth authorization flow or connects a credentialed channel directly.
type channelConnectIn struct {
	// Provider is the network to connect (for example "x", "linkedin", "facebook", "instagram", "bluesky", "discord-webhook").
	Provider string `json:"provider" url:"-"`
	// Group optionally assigns the connected channel to an employee or brand group (for example "Antje Worring" or "Hanzo AI").
	Group string `json:"group,omitempty" url:"-"`
	// Return is the optional validated HTTPS URL to redirect the browser back to after OAuth consent completes.
	Return string `json:"return,omitempty" url:"-"`
	// Credential is the secret app password, bot secret, or webhook URL for non-OAuth networks (sealed directly into Hanzo KMS).
	Credential *string `json:"credential,omitempty" url:"-"`
	// AccountID is the network-side identifier or handle paired with Credential when required (for example Bluesky handle or Mastodon server).
	AccountID string `json:"accountId,omitempty" url:"-"`
	// Fields holds any additional non-secret connector parameters.
	Fields map[string]string `json:"fields,omitempty" url:"-"`
	// Handle records a display handle when registering a channel without immediate OAuth redirect.
	Handle string `json:"handle,omitempty" url:"-"`
}

// channelConnectOut returns either the interactive OAuth authorization URL or the newly connected channel.
type channelConnectOut struct {
	// AuthorizeURL is the browser OAuth consent URL to open when connecting an OAuth-based social network.
	AuthorizeURL string `json:"authorizeUrl,omitempty"`
	// Connected is true when the channel was connected immediately without requiring a browser redirect.
	Connected bool `json:"connected,omitempty"`
	// Provider is the normalized network identifier.
	Provider string `json:"provider,omitempty"`
	// Group echoes the employee or brand group assigned to this connection flow.
	Group string `json:"group,omitempty"`
	// Channel carries the connected channel record when Connected is true.
	Channel *channelItem `json:"channel,omitempty"`
}

// channelRef addresses one channel by path id.
type channelRef struct {
	// ID is the channel identifier from the URL path.
	ID string `json:"id"`
}

// channelUpdateIn updates a channel's display metadata, connection status, disabled state, or group.
type channelUpdateIn struct {
	// ID is the channel identifier from the URL path.
	ID string `json:"-" url:"id"`
	// Provider optionally updates the network identifier.
	Provider string `json:"provider,omitempty" url:"-"`
	// Name updates the human-readable display name of the channel.
	Name string `json:"name,omitempty" url:"-"`
	// Handle updates the public handle of the channel.
	Handle string `json:"handle,omitempty" url:"-"`
	// Picture updates the avatar image URL of the channel.
	Picture string `json:"picture,omitempty" url:"-"`
	// Status updates the lifecycle state (connected, disconnected, or error).
	Status string `json:"status,omitempty" url:"-"`
	// Group updates the employee or brand group name when non-empty.
	Group string `json:"group,omitempty" url:"-"`
	// Disabled pauses or resumes publishing to this channel.
	Disabled bool `json:"disabled,omitempty" url:"-"`
}

// channelSetGroupIn assigns or clears a channel's employee or brand group.
type channelSetGroupIn struct {
	// ID is the channel identifier from the URL path.
	ID string `json:"-" url:"id"`
	// Group is the employee or brand group name to assign (for example "Antje Worring" or "Hanzo AI"). Pass an empty string to clear the group.
	Group string `json:"group" url:"-"`
}

// channelAnalyticsIn addresses one channel and an optional day window for analytics.
type channelAnalyticsIn struct {
	// ID is the channel identifier from the URL path.
	ID string `json:"id"`
	// Days is the lookback window in days (7, 30, or 90; defaults to 7).
	Days string `json:"days,omitempty"`
}

// channelAnalyticsTick is one dated data point inside an analytics metric series.
type channelAnalyticsTick struct {
	// Date is the YYYY-MM-DD date of the measurement.
	Date string `json:"date"`
	// Total is the metric value recorded for Date.
	Total int64 `json:"total"`
}

// channelAnalyticsMetric is one named analytics time series (such as followers, impressions, likes, or clicks).
type channelAnalyticsMetric struct {
	// Label names the metric (for example "Impressions", "Followers", "Likes").
	Label string `json:"label"`
	// Total sums the metric across the requested window.
	Total int64 `json:"total"`
	// Data holds the per-day points across the window.
	Data []channelAnalyticsTick `json:"data"`
}

// channelAnalyticsOut reports post delivery counts and network engagement series for one channel.
type channelAnalyticsOut struct {
	// ChannelID is the channel identifier.
	ChannelID string `json:"channelId"`
	// Provider is the social network identifier.
	Provider string `json:"provider"`
	// Handle is the channel's public handle.
	Handle string `json:"handle"`
	// GroupName is the employee or brand group the channel belongs to, when set.
	GroupName string `json:"groupName,omitempty"`
	// Posts is the total number of posts associated with this channel.
	Posts int `json:"posts"`
	// Published is the number of published posts on this channel.
	Published int `json:"published"`
	// Scheduled is the number of scheduled posts queued for this channel.
	Scheduled int `json:"scheduled"`
	// Failed is the number of failed publish attempts on this channel.
	Failed int `json:"failed"`
	// Metrics holds engagement time series reported by the upstream social network.
	Metrics []channelAnalyticsMetric `json:"metrics"`
}

func fromPlaneChannel(c client.PlaneChannel) channelItem {
	return channelItem{
		ID:            c.ID,
		Provider:      c.Provider,
		Name:          c.Name,
		Handle:        c.Handle,
		Profile:       c.Profile,
		Picture:       c.Picture,
		Status:        c.Status,
		ExternalID:    c.ExternalID,
		GroupID:       c.GroupID,
		GroupName:     c.GroupName,
		RefreshNeeded: c.RefreshNeeded,
		Disabled:      c.Disabled,
		CreatedAt:     c.CreatedAt,
		UpdatedAt:     c.UpdatedAt,
	}
}

// list returns the caller organization's connected and registered social channels,
// optionally filtered by provider (`?provider=x`) or employee/brand group (`?group=Antje%20Worring`).
func (o channelOps) list(ctx context.Context, in *channelListFilter) (*channelList, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	res, err := planeChannelsList(cloud.For(ctx, org),
		&client.PlaneChannelFilter{Provider: in.Provider, Group: in.Group, Limit: in.Limit})
	if err != nil {
		return nil, err
	}
	items := make([]channelItem, 0, len(res.Data))
	for _, c := range res.Data {
		items = append(items, fromPlaneChannel(c))
	}
	return &channelList{Data: items}, nil
}

// groups lists the employee and brand channel groups configured across the caller
// organization's channels (such as executive profiles and brand accounts) with channel counts.
func (o channelOps) groups(ctx context.Context, _ *channelEmptyIn) (*channelGroupList, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	res, err := planeChannelsGroups(cloud.For(ctx, org), &client.PlaneEmptyIn{})
	if err != nil {
		return nil, err
	}
	items := make([]channelGroupItem, 0, len(res.Data))
	for _, g := range res.Data {
		items = append(items, channelGroupItem{ID: g.ID, Name: g.Name, Channels: g.Channels})
	}
	return &channelGroupList{Data: items}, nil
}

// providers lists every supported social and publishing network along with whether
// OAuth application credentials are ready on this deployment or sealed for the org.
func (o channelOps) providers(ctx context.Context, _ *channelEmptyIn) (*channelProviderList, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	res, err := planeChannelsProviders(cloud.For(ctx, org), &client.PlaneEmptyIn{})
	if err != nil {
		return nil, err
	}
	items := make([]channelProviderItem, 0, len(res.Data))
	for _, p := range res.Data {
		items = append(items, channelProviderItem{
			Provider:              p.Provider,
			CredentialsConfigured: p.CredentialsConfigured,
			MissingCredentials:    p.MissingCredentials,
		})
	}
	return &channelProviderList{Data: items}, nil
}

// connect initiates a 3-legged OAuth flow (returning `authorizeUrl`) or immediately seals
// and connects a credentialed social channel into Hanzo KMS, optionally assigning it to
// an employee or brand group (`group`).
func (o channelOps) connect(ctx context.Context, in *channelConnectIn) (*channelConnectOut, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	res, err := planeChannelsConnect(cloud.For(ctx, org), &client.PlaneChannelConnectIn{
		Provider:   in.Provider,
		Group:      in.Group,
		Return:     in.Return,
		Credential: in.Credential,
		AccountID:  in.AccountID,
		Fields:     client.Fields(in.Fields),
		Handle:     in.Handle,
	})
	if err != nil {
		return nil, err
	}
	out := &channelConnectOut{
		AuthorizeURL: res.AuthorizeURL,
		Connected:    res.Connected,
		Provider:     res.Provider,
		Group:        res.Group,
	}
	if res.Channel != nil {
		ch := fromPlaneChannel(*res.Channel)
		out.Channel = &ch
	}
	return out, nil
}

// get returns one connected social channel by id for the caller's organization.
func (o channelOps) get(ctx context.Context, in *channelRef) (*channelItem, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	res, err := planeChannelGet(cloud.For(ctx, org), &client.PlaneChannelRef{ID: in.ID})
	if err != nil {
		return nil, err
	}
	item := fromPlaneChannel(*res)
	return &item, nil
}

// update modifies a social channel's display metadata, status, group, or disabled flag.
func (o channelOps) update(ctx context.Context, in *channelUpdateIn) (*channelItem, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	res, err := planeChannelUpdate(cloud.For(ctx, org), &client.PlaneChannelUpdateIn{
		ID:       in.ID,
		Provider: in.Provider,
		Name:     in.Name,
		Handle:   in.Handle,
		Picture:  in.Picture,
		Status:   in.Status,
		Group:    in.Group,
		Disabled: in.Disabled,
	})
	if err != nil {
		return nil, err
	}
	item := fromPlaneChannel(*res)
	return &item, nil
}

// setGroup assigns a channel to an employee or brand group (or clears the group when empty),
// keeping /v1/channel and post.hanzo.ai sidebar groups synchronized.
func (o channelOps) setGroup(ctx context.Context, in *channelSetGroupIn) (*channelItem, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	res, err := planeChannelSetGroup(cloud.For(ctx, org), &client.PlaneChannelSetGroupIn{ID: in.ID, Group: in.Group})
	if err != nil {
		return nil, err
	}
	item := fromPlaneChannel(*res)
	return &item, nil
}

// delete disconnects a social channel from the caller's organization and purges its sealed token from KMS.
func (o channelOps) delete(ctx context.Context, in *channelRef) (*cloud.Unit, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := planeChannelDelete(cloud.For(ctx, org), &client.PlaneChannelRef{ID: in.ID}); err != nil {
		return nil, err
	}
	return nil, nil
}

// analytics returns post delivery counts and upstream engagement metrics for one social channel.
func (o channelOps) analytics(ctx context.Context, in *channelAnalyticsIn) (*channelAnalyticsOut, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	res, err := planeChannelAnalytics(cloud.For(ctx, org), &client.PlaneChannelAnalyticsIn{ID: in.ID, Days: in.Days})
	if err != nil {
		return nil, err
	}
	metrics := make([]channelAnalyticsMetric, 0, len(res.Metrics))
	for _, m := range res.Metrics {
		ticks := make([]channelAnalyticsTick, 0, len(m.Data))
		for _, d := range m.Data {
			ticks = append(ticks, channelAnalyticsTick{Date: d.Date, Total: d.Total})
		}
		metrics = append(metrics, channelAnalyticsMetric{Label: m.Label, Total: m.Total, Data: ticks})
	}
	return &channelAnalyticsOut{
		ChannelID: res.ChannelID,
		Provider:  res.Provider,
		Handle:    res.Handle,
		GroupName: res.GroupName,
		Posts:     res.Posts,
		Published: res.Published,
		Scheduled: res.Scheduled,
		Failed:    res.Failed,
		Metrics:   metrics,
	}, nil
}
