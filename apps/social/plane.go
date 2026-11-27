package social

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
	"sync"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/mint"
	"github.com/hanzoai/cloud/sdk/text"
	"github.com/hanzoai/authz"
	"github.com/zap-proto/zip"
)

var exposePlaneOnce sync.Once

// exposePlane registers the unified /v1/channel and /v1/post plane operations
// so apps/channel and apps/post share social's store, scheduler, and publisher
// across plugin processes.
func exposePlane() {
	exposePlaneOnce.Do(func() {
		p := cloud.Plane()
		p.Post("/social/channels/list", planeChannelsList,
			zip.WithOperationID(client.SocialChannelsList),
			zip.WithSummary("List connected social channels for the caller's org"))
		p.Post("/social/channels/groups", planeChannelsGroups,
			zip.WithOperationID(client.SocialChannelsGroups),
			zip.WithSummary("List employee and brand channel groups for the caller's org"))
		p.Post("/social/channels/providers", planeChannelsProviders,
			zip.WithOperationID(client.SocialChannelsProviders),
			zip.WithSummary("List supported social channel providers and credential readiness"))
		p.Post("/social/channels/connect", planeChannelsConnect,
			zip.WithOperationID(client.SocialChannelsConnect),
			zip.WithSummary("Start OAuth or connect a credentialed social channel"))
		p.Post("/social/channels/get", planeChannelGet,
			zip.WithOperationID(client.SocialChannelGet),
			zip.WithSummary("Read one social channel by id"))
		p.Post("/social/channels/update", planeChannelUpdate,
			zip.WithOperationID(client.SocialChannelUpdate),
			zip.WithSummary("Update one social channel by id"))
		p.Post("/social/channels/group", planeChannelSetGroup,
			zip.WithOperationID(client.SocialChannelSetGroup),
			zip.WithSummary("Assign or clear a channel's employee or brand group"))
		p.Post("/social/channels/delete", planeChannelDelete,
			zip.WithOperationID(client.SocialChannelDelete),
			zip.WithSummary("Disconnect and delete one social channel"))
		p.Post("/social/channels/analytics", planeChannelAnalytics,
			zip.WithOperationID(client.SocialChannelAnalytics),
			zip.WithSummary("Read publishing and engagement analytics for one channel"))

		p.Post("/social/posts/list", planePostsList,
			zip.WithOperationID(client.SocialPostsList),
			zip.WithSummary("List social posts for the caller's org"))
		p.Post("/social/posts/create", planePostCreate,
			zip.WithOperationID(client.SocialPostCreate),
			zip.WithSummary("Create, schedule, draft, or immediately publish social posts"))
		p.Post("/social/posts/get", planePostGet,
			zip.WithOperationID(client.SocialPostGet),
			zip.WithSummary("Read one social post by id"))
		p.Post("/social/posts/update", planePostUpdate,
			zip.WithOperationID(client.SocialPostUpdate),
			zip.WithSummary("Replace one social post by id"))
		p.Post("/social/posts/delete", planePostDelete,
			zip.WithOperationID(client.SocialPostDelete),
			zip.WithSummary("Delete one social post by id"))
		p.Post("/social/posts/publish", planePostPublish,
			zip.WithOperationID(client.SocialPostPublish),
			zip.WithSummary("Publish one social post immediately"))
		p.Post("/social/posts/analytics", planePostAnalytics,
			zip.WithOperationID(client.SocialPostAnalytics),
			zip.WithSummary("Read delivery and engagement analytics for one post"))
	})
}

func planeOrg(ctx context.Context) (string, error) {
	org, ok := cloud.Tenant(ctx)
	if !ok || strings.TrimSpace(org) == "" || mounted == nil {
		return "", zip.ErrForbidden("social: no org on the call")
	}
	return org, nil
}

func toPlaneChannel(a socialAccount) client.PlaneChannel {
	return client.PlaneChannel{
		ID:            a.ID,
		Provider:      a.Provider,
		Name:          a.Name,
		Handle:        a.Handle,
		Profile:       a.Profile,
		Picture:       a.Picture,
		Status:        a.Status,
		ExternalID:    a.ExternalID,
		GroupID:       a.GroupID,
		GroupName:     a.GroupName,
		RefreshNeeded: a.RefreshNeeded,
		Disabled:      a.Disabled,
		CreatedAt:     a.CreatedAt,
		UpdatedAt:     a.UpdatedAt,
	}
}

func toPlanePost(p socialPost) client.PlanePost {
	media := p.Media
	if media == nil {
		media = []string{}
	}
	return client.PlanePost{
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

func planeChannelsList(ctx context.Context, in *client.PlaneChannelFilter) (*client.PlaneChannels, error) {
	org, err := planeOrg(ctx)
	if err != nil {
		return nil, err
	}
	prov := strings.ToLower(strings.TrimSpace(in.Provider))
	grp := strings.TrimSpace(in.Group)
	rows, err := mounted.State.store.ListChannelsFiltered(ctx, org, prov, grp, limitOf(in.Limit))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list channels: %v", err)
	}
	out := make([]client.PlaneChannel, 0, len(rows))
	for _, r := range rows {
		out = append(out, toPlaneChannel(r))
	}
	return &client.PlaneChannels{Data: out}, nil
}

func planeChannelsGroups(ctx context.Context, _ *client.PlaneEmptyIn) (*client.PlaneChannelGroups, error) {
	org, err := planeOrg(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := mounted.State.store.ListChannelGroups(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list channel groups: %v", err)
	}
	out := make([]client.PlaneChannelGroup, 0, len(rows))
	for _, r := range rows {
		out = append(out, client.PlaneChannelGroup{
			ID:       r.ID,
			Name:     r.Name,
			Channels: r.Channels,
		})
	}
	return &client.PlaneChannelGroups{Data: out}, nil
}

func planeChannelsProviders(ctx context.Context, _ *client.PlaneEmptyIn) (*client.PlaneChannelProviders, error) {
	org, err := planeOrg(ctx)
	if err != nil {
		return nil, err
	}
	caps := providerCapabilitiesFor(mounted.KMS, org)
	out := make([]client.PlaneChannelProvider, 0, len(caps))
	for _, c := range caps {
		out = append(out, client.PlaneChannelProvider{
			Provider:              c.Provider,
			CredentialsConfigured: c.CredentialsConfigured,
			MissingCredentials:    c.MissingCredentials,
		})
	}
	return &client.PlaneChannelProviders{Data: out}, nil
}

func planeChannelsConnect(ctx context.Context, in *client.PlaneChannelConnectIn) (*client.PlaneChannelConnectOut, error) {
	org, err := planeOrg(ctx)
	if err != nil {
		return nil, err
	}
	prov, ok := normProvider(in.Provider)
	if !ok {
		return nil, zip.ErrBadRequest("provider must be one of " + strings.Join(providerOrder, ", "))
	}
	groupName := text.Trim(in.Group, maxField)
	now := time.Now().Unix()
	if groupName != "" {
		if err := mounted.State.store.PutPendingGroup(ctx, org, prov, groupName, now); err != nil {
			return nil, mapErr(err, "")
		}
	}

	// Manual channel record when handle is supplied without OAuth or raw credential.
	if strings.TrimSpace(in.Handle) != "" && in.Credential == nil && len(in.Fields) == 0 && strings.TrimSpace(in.Return) == "" {
		acct := socialAccount{
			ID:        mint.ID("acct"),
			Org:       org,
			Provider:  prov,
			Handle:    text.Trim(in.Handle, maxField),
			Name:      text.Trim(in.Handle, maxField),
			Status:    "connected",
			GroupName: groupName,
			CreatedAt: now,
			UpdatedAt: now,
		}
		saved, cerr := mounted.State.store.CreateAccount(ctx, acct)
		if cerr != nil {
			return nil, mapErr(cerr, "")
		}
		ch := toPlaneChannel(saved)
		return &client.PlaneChannelConnectOut{
			Connected: true,
			Provider:  prov,
			Group:     groupName,
			Channel:   &ch,
		}, nil
	}

	// Primary path: native Hanzo Cloud provider connector (OAuth 3-legged + KMS credential seal).
	connOut, cerr := cloud.Ask[client.IntegrationsConnectIn, client.IntegrationsConnectOut](
		cloud.For(ctx, org), "provider", client.IntegrationsConnect,
		&client.IntegrationsConnectIn{
			Provider:   prov,
			Return:     strings.TrimSpace(in.Return),
			Credential: in.Credential,
			AccountID:  strings.TrimSpace(in.AccountID),
			Fields:     in.Fields,
		})
	if cerr == nil && connOut != nil {
		if connOut.Connected {
			var chPtr *client.PlaneChannel
			if connOut.ExternalID != "" {
				if got, gerr := mounted.State.store.GetAccount(ctx, org, idFor(org, prov, connOut.ExternalID)); gerr == nil {
					if groupName != "" && got.GroupName != groupName {
						if updated, uerr := mounted.State.store.SetChannelGroup(ctx, org, got.ID, groupName, now); uerr == nil {
							got = updated
						}
					}
					ch := toPlaneChannel(got)
					chPtr = &ch
				}
			}
			return &client.PlaneChannelConnectOut{
				Connected: true,
				Provider:  prov,
				Group:     groupName,
				Channel:   chPtr,
			}, nil
		}
		if connOut.AuthorizeURL != "" {
			return &client.PlaneChannelConnectOut{
				AuthorizeURL: connOut.AuthorizeURL,
				Provider:     prov,
				Group:        groupName,
			}, nil
		}
	}

	// Secondary path for Hanzo org when OAuth app lives on post.hanzo.ai frontend.
	if (org == "hanzo" || org == authz.AdminOrg) && mounted.KMS != nil && in.Credential == nil && unconfigured(cerr) {
		if authURL, ferr := connectViaPostUI(ctx, prov, groupName, strings.TrimSpace(in.Return)); ferr == nil && authURL != "" {
			return &client.PlaneChannelConnectOut{
				AuthorizeURL: authURL,
				Provider:     prov,
				Group:        groupName,
			}, nil
		}
	}

	if cerr != nil {
		return nil, cerr
	}
	return nil, providerNotConfiguredErr(prov)
}

// unconfigured is whether the provider declined because it holds no app for the
// network, or is not deployed here at all: the answers the post UI may stand in
// for. A refusal is the caller's own, and a provider that failed to answer has
// said nothing, so neither is offered a second door.
func unconfigured(err error) bool {
	if err == nil || errors.Is(err, cloud.ErrNoPeer) {
		return true
	}
	var he *zip.HTTPError
	return errors.As(err, &he) && he.Status == http.StatusServiceUnavailable
}

func connectViaPostUI(ctx context.Context, provider, group, redirectURL string) (string, error) {
	key, err := mounted.KMS.GetSecret(cloud.For(ctx, authz.AdminOrg), postizSecretRef(postizAPIKeySecretName))
	if err != nil || len(key) == 0 {
		return "", err
	}
	q := url.Values{}
	if group != "" {
		q.Set("group", group)
	}
	if redirectURL != "" {
		q.Set("redirectUrl", redirectURL)
	}
	cl := &http.Client{Timeout: 15 * time.Second}
	for _, base := range []string{"/v1/channel/connect/" + provider, "/v1/social/" + provider} {
		u := postizURL(base, q)
		req, rerr := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if rerr != nil {
			continue
		}
		req.Header.Set("Authorization", strings.TrimSpace(string(key)))
		resp, derr := cl.Do(req)
		if derr != nil {
			continue
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			continue
		}
		var out struct {
			URL string `json:"url"`
		}
		if json.Unmarshal(raw, &out) == nil && out.URL != "" {
			return out.URL, nil
		}
	}
	return "", errors.New("no oauth url returned")
}

func planeChannelGet(ctx context.Context, in *client.PlaneChannelRef) (*client.PlaneChannel, error) {
	org, err := planeOrg(ctx)
	if err != nil {
		return nil, err
	}
	a, err := mounted.State.store.GetAccount(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, mapErr(err, "channel not found")
	}
	ch := toPlaneChannel(a)
	return &ch, nil
}

func planeChannelUpdate(ctx context.Context, in *client.PlaneChannelUpdateIn) (*client.PlaneChannel, error) {
	org, err := planeOrg(ctx)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(in.ID)
	cur, err := mounted.State.store.GetAccount(ctx, org, id)
	if err != nil {
		return nil, mapErr(err, "channel not found")
	}
	prov := cur.Provider
	if strings.TrimSpace(in.Provider) != "" {
		p, ok := normProvider(in.Provider)
		if !ok {
			return nil, zip.ErrBadRequest("provider must be one of " + strings.Join(providerOrder, ", "))
		}
		prov = p
	}
	st := cur.Status
	if strings.TrimSpace(in.Status) != "" {
		s, ok := normAccountStatus(in.Status)
		if !ok {
			return nil, zip.ErrBadRequest("status must be one of connected, disconnected, error")
		}
		st = s
	}
	handle := cur.Handle
	if strings.TrimSpace(in.Handle) != "" {
		handle = text.Trim(in.Handle, maxField)
	}
	name := cur.Name
	if strings.TrimSpace(in.Name) != "" {
		name = text.Trim(in.Name, maxField)
	}
	pic := cur.Picture
	if strings.TrimSpace(in.Picture) != "" {
		pic = text.Trim(in.Picture, maxField)
	}
	grp := cur.GroupName
	if in.Group != "" {
		grp = text.Trim(in.Group, maxField)
	}
	cur.Provider = prov
	cur.Status = st
	cur.Handle = handle
	cur.Name = name
	cur.Picture = pic
	cur.GroupName = grp
	cur.Disabled = in.Disabled
	cur.UpdatedAt = time.Now().Unix()
	saved, err := mounted.State.store.UpdateChannel(ctx, cur)
	if err != nil {
		return nil, mapErr(err, "channel not found")
	}
	ch := toPlaneChannel(saved)
	return &ch, nil
}

func planeChannelSetGroup(ctx context.Context, in *client.PlaneChannelSetGroupIn) (*client.PlaneChannel, error) {
	org, err := planeOrg(ctx)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(in.ID)
	grp := text.Trim(in.Group, maxField)
	saved, err := mounted.State.store.SetChannelGroup(ctx, org, id, grp, time.Now().Unix())
	if err != nil {
		return nil, mapErr(err, "channel not found")
	}
	if strings.HasPrefix(saved.ExternalID, "postiz:") && mounted.KMS != nil && (org == "hanzo" || org == authz.AdminOrg) {
		syncGroupToPostUI(ctx, strings.TrimPrefix(saved.ExternalID, "postiz:"), grp)
	}
	ch := toPlaneChannel(saved)
	return &ch, nil
}

func syncGroupToPostUI(ctx context.Context, integrationID, groupName string) {
	key, err := mounted.KMS.GetSecret(cloud.For(ctx, authz.AdminOrg), postizSecretRef(postizAPIKeySecretName))
	if err != nil || len(key) == 0 {
		return
	}
	payload, _ := json.Marshal(map[string]string{"name": groupName})
	cl := &http.Client{Timeout: 10 * time.Second}
	for _, path := range []string{"/v1/channel/" + integrationID + "/group", "/v1/integrations/" + integrationID + "/group"} {
		req, rerr := http.NewRequestWithContext(ctx, http.MethodPut, postizBase+path, bytes.NewReader(payload))
		if rerr != nil {
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", strings.TrimSpace(string(key)))
		resp, derr := cl.Do(req)
		if derr != nil {
			continue
		}
		_ = resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return
		}
	}
}

func planeChannelDelete(ctx context.Context, in *client.PlaneChannelRef) (*cloud.Unit, error) {
	org, err := planeOrg(ctx)
	if err != nil {
		return nil, err
	}
	deleted, err := mounted.State.store.DeleteAccount(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "delete channel: %v", err)
	}
	if !deleted {
		return nil, zip.ErrNotFound("channel not found")
	}
	return nil, nil
}

func planeChannelAnalytics(ctx context.Context, in *client.PlaneChannelAnalyticsIn) (*client.PlaneChannelAnalyticsOut, error) {
	org, err := planeOrg(ctx)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(in.ID)
	acct, err := mounted.State.store.GetAccount(ctx, org, id)
	if err != nil {
		return nil, mapErr(err, "channel not found")
	}
	posts, published, scheduled, failed, err := mounted.State.store.ChannelPostCounts(ctx, org, acct.ID, acct.Provider)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
	days := 7
	if d, perr := strconv.Atoi(strings.TrimSpace(in.Days)); perr == nil && d > 0 && d <= 90 {
		days = d
	}
	metrics := []client.PlaneAnalyticsMetric{}
	if strings.HasPrefix(acct.ExternalID, "postiz:") && mounted.KMS != nil && (org == "hanzo" || org == authz.AdminOrg) {
		integrationID := strings.TrimPrefix(acct.ExternalID, "postiz:")
		q := url.Values{"date": {strconv.Itoa(days)}}
		if m := fetchPostUIAnalytics(ctx, []string{
			postizURL("/v1/channel/"+integrationID+"/analytics", q),
			postizURL("/v1/analytics/"+integrationID, q),
		}); len(m) > 0 {
			metrics = m
		}
	}
	return &client.PlaneChannelAnalyticsOut{
		ChannelID: acct.ID,
		Provider:  acct.Provider,
		Handle:    acct.Handle,
		GroupName: acct.GroupName,
		Posts:     posts,
		Published: published,
		Scheduled: scheduled,
		Failed:    failed,
		Metrics:   metrics,
	}, nil
}

func fetchPostUIAnalytics(ctx context.Context, urls []string) []client.PlaneAnalyticsMetric {
	key, err := mounted.KMS.GetSecret(cloud.For(ctx, authz.AdminOrg), postizSecretRef(postizAPIKeySecretName))
	if err != nil || len(key) == 0 {
		return nil
	}
	cl := &http.Client{Timeout: 15 * time.Second}
	for _, u := range urls {
		req, rerr := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if rerr != nil {
			continue
		}
		req.Header.Set("Authorization", strings.TrimSpace(string(key)))
		resp, derr := cl.Do(req)
		if derr != nil {
			continue
		}
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			continue
		}
		var series []struct {
			Label string `json:"label"`
			Data  []struct {
				Total json.Number `json:"total"`
				Date  string      `json:"date"`
			} `json:"data"`
		}
		if json.Unmarshal(raw, &series) != nil {
			continue
		}
		out := make([]client.PlaneAnalyticsMetric, 0, len(series))
		for _, s := range series {
			var sum int64
			ticks := make([]client.PlaneAnalyticsTick, 0, len(s.Data))
			for _, pt := range s.Data {
				n, _ := pt.Total.Int64()
				sum += n
				ticks = append(ticks, client.PlaneAnalyticsTick{Date: pt.Date, Total: n})
			}
			out = append(out, client.PlaneAnalyticsMetric{
				Label: s.Label,
				Total: sum,
				Data:  ticks,
			})
		}
		return out
	}
	return nil
}

func planePostsList(ctx context.Context, in *client.PlanePostFilter) (*client.PlanePosts, error) {
	org, err := planeOrg(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := mounted.State.store.ListPostsFiltered(
		ctx,
		org,
		strings.ToLower(strings.TrimSpace(in.Status)),
		strings.ToLower(strings.TrimSpace(in.Channel)),
		strings.TrimSpace(in.AccountID),
		strings.TrimSpace(in.Group),
		limitOf(in.Limit),
	)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list posts: %v", err)
	}
	out := make([]client.PlanePost, 0, len(rows))
	for _, r := range rows {
		out = append(out, toPlanePost(r))
	}
	return &client.PlanePosts{Data: out}, nil
}

type postTargetSpec struct {
	channel   string
	accountID string
	group     string
}

func planePostCreate(ctx context.Context, in *client.PlanePostCreateIn) (*client.PlanePostCreateOut, error) {
	org, err := planeOrg(ctx)
	if err != nil {
		return nil, err
	}
	rawStatus := strings.ToLower(strings.TrimSpace(in.Status))
	schedAt := nonNeg(in.ScheduleAt)
	switch rawStatus {
	case "now", "publish":
		rawStatus = statusScheduled
		schedAt = 0
	case "":
		if schedAt > 0 {
			rawStatus = statusScheduled
		} else {
			rawStatus = statusDraft
		}
	}

	targets, err := resolvePostTargets(ctx, org, in)
	if err != nil {
		return nil, err
	}

	now := time.Now().Unix()
	created := make([]client.PlanePost, 0, len(targets))
	for _, t := range targets {
		p, perr := postFrom(mint.ID("post"), org, in.Content, t.channel, rawStatus, schedAt, in.Media)
		if perr != nil {
			return nil, perr
		}
		p.AccountID = t.accountID
		p.ExternalGroup = t.group
		p.CreatedAt, p.UpdatedAt = now, now
		saved, cerr := mounted.State.store.CreatePost(ctx, p)
		if cerr != nil {
			return nil, mapErr(cerr, "")
		}
		if saved.Status == statusScheduled && saved.ScheduleAt <= now {
			if updated, pubErr := publishPost(ctx, mounted, org, saved.ID); pubErr == nil || errors.Is(pubErr, errProviderNotConfigured) {
				saved = updated
			}
		}
		created = append(created, toPlanePost(saved))
	}

	res := &client.PlanePostCreateOut{PlanePost: created[0]}
	if len(created) > 1 {
		res.Posts = created
	}
	return res, nil
}

func resolvePostTargets(ctx context.Context, org string, in *client.PlanePostCreateIn) ([]postTargetSpec, error) {
	acctIDs := make([]string, 0, 1+len(in.AccountIDs))
	if id := strings.TrimSpace(in.AccountID); id != "" {
		acctIDs = append(acctIDs, id)
	}
	for _, raw := range in.AccountIDs {
		if id := strings.TrimSpace(raw); id != "" {
			acctIDs = append(acctIDs, id)
		}
	}
	if len(acctIDs) > 0 {
		specs := make([]postTargetSpec, 0, len(acctIDs))
		seen := map[string]bool{}
		for _, id := range acctIDs {
			if seen[id] {
				continue
			}
			seen[id] = true
			a, err := mounted.State.store.GetAccount(ctx, org, id)
			if err != nil {
				return nil, mapErr(err, fmt.Sprintf("channel %s not found", id))
			}
			specs = append(specs, postTargetSpec{
				channel:   a.Provider,
				accountID: a.ID,
				group:     a.GroupName,
			})
		}
		return specs, nil
	}

	grp := strings.TrimSpace(in.Group)
	if grp != "" {
		provFilter := strings.ToLower(strings.TrimSpace(in.Channel))
		accts, err := mounted.State.store.ListChannelsFiltered(ctx, org, provFilter, grp, maxLimit)
		if err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "resolve group channels: %v", err)
		}
		allowed := map[string]bool{}
		for _, ch := range in.Channels {
			if p, ok := normProvider(ch); ok {
				allowed[p] = true
			}
		}
		specs := make([]postTargetSpec, 0, len(accts))
		for _, a := range accts {
			if a.Status != "connected" || a.Disabled {
				continue
			}
			if len(allowed) > 0 && !allowed[a.Provider] {
				continue
			}
			specs = append(specs, postTargetSpec{
				channel:   a.Provider,
				accountID: a.ID,
				group:     a.GroupName,
			})
		}
		if len(specs) == 0 {
			return nil, zip.ErrBadRequest("no connected channels in group " + grp)
		}
		return specs, nil
	}

	if len(in.Channels) > 0 {
		specs := make([]postTargetSpec, 0, len(in.Channels))
		seen := map[string]bool{}
		for _, raw := range in.Channels {
			ch, ok := normProvider(raw)
			if !ok {
				return nil, zip.ErrBadRequest("channel must be one of " + strings.Join(providerOrder, ", "))
			}
			if seen[ch] {
				continue
			}
			seen[ch] = true
			specs = append(specs, postTargetSpec{channel: ch})
		}
		if len(specs) > 0 {
			return specs, nil
		}
	}

	ch, ok := normProvider(in.Channel)
	if !ok {
		return nil, zip.ErrBadRequest("channel must be one of " + strings.Join(providerOrder, ", "))
	}
	return []postTargetSpec{{channel: ch}}, nil
}

func planePostGet(ctx context.Context, in *client.PlanePostRef) (*client.PlanePost, error) {
	org, err := planeOrg(ctx)
	if err != nil {
		return nil, err
	}
	p, err := mounted.State.store.GetPost(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, mapErr(err, "post not found")
	}
	out := toPlanePost(p)
	return &out, nil
}

func planePostUpdate(ctx context.Context, in *client.PlanePostUpdateIn) (*client.PlanePost, error) {
	org, err := planeOrg(ctx)
	if err != nil {
		return nil, err
	}
	p, err := postFrom(strings.TrimSpace(in.ID), org, in.Content, in.Channel, in.Status, in.ScheduleAt, in.Media)
	if err != nil {
		return nil, err
	}
	p.AccountID = text.Trim(in.AccountID, maxField)
	p.UpdatedAt = time.Now().Unix()
	saved, err := mounted.State.store.UpdatePost(ctx, p)
	if err != nil {
		return nil, mapErr(err, "post not found")
	}
	out := toPlanePost(saved)
	return &out, nil
}

func planePostDelete(ctx context.Context, in *client.PlanePostRef) (*cloud.Unit, error) {
	org, err := planeOrg(ctx)
	if err != nil {
		return nil, err
	}
	deleted, err := mounted.State.store.DeletePost(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "delete post: %v", err)
	}
	if !deleted {
		return nil, zip.ErrNotFound("post not found")
	}
	return nil, nil
}

func planePostPublish(ctx context.Context, in *client.PlanePostRef) (*client.PlanePost, error) {
	org, err := planeOrg(ctx)
	if err != nil {
		return nil, err
	}
	p, err := publishPost(ctx, mounted, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, mapPublishErr(err)
	}
	out := toPlanePost(p)
	return &out, nil
}

func planePostAnalytics(ctx context.Context, in *client.PlanePostRef) (*client.PlanePostAnalyticsOut, error) {
	org, err := planeOrg(ctx)
	if err != nil {
		return nil, err
	}
	p, err := mounted.State.store.GetPost(ctx, org, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, mapErr(err, "post not found")
	}
	metrics := []client.PlaneAnalyticsMetric{}
	postUIID := p.ExternalIDPostiz
	if postUIID == "" && strings.HasPrefix(p.ExternalID, "cm") {
		postUIID = p.ExternalID
	}
	if postUIID != "" && mounted.KMS != nil && (org == "hanzo" || org == authz.AdminOrg) {
		q := url.Values{"date": {"30"}}
		if m := fetchPostUIAnalytics(ctx, []string{
			postizURL("/v1/post/"+postUIID+"/analytics", q),
			postizURL("/v1/analytics/post/"+postUIID, q),
		}); len(m) > 0 {
			metrics = m
		}
	}
	return &client.PlanePostAnalyticsOut{
		PostID:     p.ID,
		Channel:    p.Channel,
		AccountID:  p.AccountID,
		Status:     p.Status,
		ReleaseURL: p.ReleaseURL,
		ExternalID: p.ExternalID,
		Metrics:    metrics,
	}, nil
}
