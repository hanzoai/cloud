package social

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/mint"
	"github.com/hanzoai/cloud/sdk/text"
	"github.com/hanzoai/authz"
	luxlog "github.com/luxfi/log"
)

const (
	// postizPollerIntervalEnv overrides the poller cadence (Go duration; "0"/"off"/"false"
	// disables). Default 2 minutes.
	postizPollerIntervalEnv = "CLOUD_SOCIAL_POSTIZ_POLL_INTERVAL"
	defaultPostizInterval   = 2 * time.Minute

	// postizBase is the upstream Postiz API origin.
	postizBase = "https://post.hanzo.ai"

	// postizPage bounds one poller page so a large account never blocks a tick.
	postizPage = 100

	// pollerBatch bounds how many mirrored posts one tick writes.
	pollerBatch = 200
)

// postizPollerInterval resolves the poller cadence from env. "0"/"off"/"false"
// disables; an unparseable value disables (fail-safe).
func postizPollerInterval(log luxlog.Logger) time.Duration {
	raw := env.Or(postizPollerIntervalEnv, "")
	switch raw {
	case "":
		return defaultPostizInterval
	case "0", "off", "false":
		return 0
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		log.Warn("invalid "+postizPollerIntervalEnv+" — social postiz poller disabled", "value", raw, "err", err)
		return 0
	}
	return d
}

// postizPoller polls Postiz and mirrors everything it finds into social_posts with
// status=mirrored, origin=postiz. One process leads it (Coord.Lead), reads the
// POSTIZ_API_KEY from KMS at path social/postiz, and never schedules a mirrored post.
type postizPoller struct {
	s   *cloud.Service[state]
	cl  *http.Client
	key string // sealed value, never logged
}

// startPostizPoller launches the poller loop when the interval is enabled. Returns
// its stop function (never nil, idempotent).
func startPostizPoller(s *cloud.Service[state]) func() {
	interval := postizPollerInterval(s.Log)
	if interval == 0 {
		s.Log.Info("social postiz poller disabled", "env", postizPollerIntervalEnv)
		return func() {}
	}
	p := &postizPoller{s: s, cl: &http.Client{Timeout: 60 * time.Second}}
	lead := s.Coord.Lead(postizPollerLoop)
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		initTimer := time.NewTimer(3 * time.Second)
		defer initTimer.Stop()
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-initTimer.C:
				if s.KMS != nil {
					_ = lead.Do(context.Background(), func(context.Context) { p.poll(context.Background()) })
				}
			case <-t.C:
				if s.KMS != nil {
					_ = lead.Do(context.Background(), func(context.Context) { p.poll(context.Background()) })
				}
			}
		}
	}()
	s.Log.Info("social postiz poller started", "interval", interval, "batch", pollerBatch)

	var once bool
	return func() {
		if once {
			return
		}
		once = true
		close(done)
		<-stopped
	}
}

// postizPollerLoop names the poller's lease: one process in the deployment mirrors.
const postizPollerLoop = "social:postiz:poller"

// poll reads the Postiz API key from KMS, then pages /posts, /integrations and
// /notifications, mirroring each discovered post as status=mirrored. A poller tick
// that cannot read its key answers a social.alert and skips the tick.
func (p *postizPoller) poll(ctx context.Context) {
	if p.s == nil || p.s.KMS == nil {
		return
	}
	key, err := p.s.KMS.GetSecret(cloud.For(ctx, authz.AdminOrg), postizSecretRef(postizAPIKeySecretName))
	if err != nil || len(key) == 0 {
		p.s.Log.Error("social postiz poller: no API key", "err", err)
		emitAlert(ctx, p.s, "postiz poller: no API key configured")
		return
	}
	p.key = strings.TrimSpace(string(key))

	if err := p.pollIntegrations(ctx); err != nil {
		p.s.Log.Error("social postiz poller: channels sync failed", "err", err)
		emitAlert(ctx, p.s, "postiz poller: channels sync failed")
	}
	if err := p.pollPosts(ctx); err != nil {
		p.s.Log.Error("social postiz poller: posts sync failed", "err", err)
		emitAlert(ctx, p.s, "postiz poller: posts sync failed")
	}
	if err := p.pollNotifications(ctx); err != nil {
		p.s.Log.Error("social postiz poller: notifications page failed", "err", err)
	}
}

// pollPosts pages /v1/post (falling back to /v1/posts or /posts) and mirrors
// each post as status=mirrored, origin=postiz. Never scheduled (schedule_at=0).
func (p *postizPoller) pollPosts(ctx context.Context) error {
	now := time.Now().UTC()
	q := url.Values{
		"startDate": {now.AddDate(0, -1, 0).Format(time.RFC3339)},
		"endDate":   {now.AddDate(0, 1, 0).Format(time.RFC3339)},
	}
	var body []byte
	var next string
	var err error
	for _, path := range []string{"/v1/post", "/v1/posts", "/posts"} {
		body, next, err = p.getPage(ctx, postizURL(path, q))
		if err == nil {
			break
		}
	}
	if err != nil {
		return err
	}
	for page := 0; page < pollerBatch; page++ {
		posts, derr := decodePolledPosts(body)
		if derr != nil {
			return fmt.Errorf("decode posts: %w", derr)
		}
		for i := range posts {
			po := &posts[i]
			if po.ID == "" || po.IntegrationID == "" {
				continue
			}
			if _, _, err := p.s.State.store.UpsertMirrored(ctx, postizToPost(po)); err != nil {
				p.s.Log.Error("social postiz poller: upsert mirrored failed", "post", po.ID, "err", err)
			}
			emitPostMirrored(ctx, p.s, orgFromIntegration(po.IntegrationID), po.ID)
		}
		if next == "" {
			break
		}
		body, next, err = p.getPage(ctx, postizURL(next, nil))
		if err != nil {
			return err
		}
	}
	return nil
}

// pollIntegrations syncs channels from /v1/channel (falling back to
// /v1/channels or /integrations) into social_accounts so all
// channels and employee/brand groups are immediately addressable via /v1/channel.
func (p *postizPoller) pollIntegrations(ctx context.Context) error {
	var body []byte
	var next string
	var err error
	for _, path := range []string{"/v1/channel", "/v1/channels", "/integrations"} {
		body, next, err = p.getPage(ctx, postizURL(path, nil))
		if err == nil {
			break
		}
	}
	if err != nil {
		return err
	}
	now := time.Now().Unix()
	for page := 0; page < pollerBatch; page++ {
		integrations, derr := decodePolledIntegrations(body)
		if derr != nil {
			return fmt.Errorf("decode integrations: %w", derr)
		}
		for i := range integrations {
			it := &integrations[i]
			if it.ID == "" {
				continue
			}
			org := it.Org
			if org == "" {
				org = orgFromIntegration(it.ID)
			}
			if org == "" {
				org = "hanzo"
			}
			prov := normalizePolledProvider(it.Identifier, it.Provider)
			handle := formatPolledHandle(prov, it.Profile, it.Name)
			status := "disconnected"
			if it.Connected && !it.Disabled && !it.RefreshNeeded {
				status = "connected"
			}
			groupName := it.GroupName
			groupID := it.GroupID
			if it.Customer != nil {
				if groupName == "" {
					groupName = it.Customer.Name
				}
				if groupID == "" {
					groupID = it.Customer.ID
				}
			}
			_, uerr := p.s.State.store.UpsertSyncedChannel(ctx, socialAccount{
				ID:            it.ID,
				Org:           org,
				Provider:      prov,
				Name:          text.Trim(it.Name, maxField),
				Handle:        text.Trim(handle, maxField),
				Profile:       text.Trim(it.Profile, maxField),
				Picture:       text.Trim(it.Picture, maxField),
				Status:        status,
				ExternalID:    "postiz:" + it.ID,
				GroupID:       text.Trim(groupID, maxField),
				GroupName:     text.Trim(groupName, maxField),
				RefreshNeeded: it.RefreshNeeded,
				Disabled:      it.Disabled,
				CreatedAt:     now,
				UpdatedAt:     now,
			})
			if uerr != nil {
				p.s.Log.Error("social postiz poller: upsert channel failed", "channel", it.ID, "err", uerr)
			}
			emitAccountChanged(ctx, p.s, org)
		}
		if next == "" {
			break
		}
		body, next, err = p.getPage(ctx, postizURL(next, nil))
		if err != nil {
			return err
		}
	}
	return nil
}

// pollNotifications pages /v1/notifications (or fallback) and records social.alert for each.
func (p *postizPoller) pollNotifications(ctx context.Context) error {
	var body []byte
	var err error
	for _, path := range []string{"/v1/notifications", "/notifications"} {
		body, _, err = p.getPage(ctx, postizURL(path, nil))
		if err == nil {
			break
		}
	}
	if err != nil {
		return err
	}
	var notifications []postizNotification
	if json.Unmarshal(body, &notifications) != nil {
		var env struct {
			Notifications []postizNotification `json:"notifications"`
		}
		if derr := json.Unmarshal(body, &env); derr != nil {
			return fmt.Errorf("decode notifications: %w", derr)
		}
		notifications = env.Notifications
	}
	for i := range notifications {
		n := &notifications[i]
		if n.ID == "" {
			continue
		}
		emitAlert(ctx, p.s, n.Message)
	}
	return nil
}

// getPage GETs one page with the API key as the Authorization value (Postiz public API; not Bearer) and returns the body plus
// the Link header's next URL (or "" when there is no next page).
func (p *postizPoller) getPage(ctx context.Context, u string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", p.key)
	resp, err := p.cl.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("postiz %s: %d", u, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	next := linkNext(resp.Header.Get("Link"))
	return body, next, nil
}

// postizURL builds an absolute Postiz API URL with pagination params.
func postizURL(path string, q url.Values) string {
	u, _ := url.Parse(postizBase + path)
	if q != nil {
		u.RawQuery = q.Encode()
	}
	return u.String()
}

// linkNext parses a Link header and returns the next page URL, or "".
func linkNext(link string) string {
	if link == "" {
		return ""
	}
	for _, part := range strings.Split(link, ",") {
		if strings.Contains(part, `rel="next"`) {
			if i := strings.Index(part, "<"); i >= 0 {
				if j := strings.Index(part[i:], ">"); j >= 0 {
					return part[i+1 : i+j]
				}
			}
		}
	}
	return ""
}

// postizPost is one Postiz post as the API returns it.
type postizPost struct {
	ID            string `json:"id"`
	Content       string `json:"content"`
	Channel       string `json:"channel"`
	IntegrationID string `json:"integrationId"`
	ExternalGroup string `json:"externalGroup"`
	ReleaseURL    string `json:"releaseUrl"`
	AccountID     string `json:"accountId"`
}

// postizIntegration is one channel/integration as post.hanzo.ai returns it.
type postizIntegration struct {
	ID            string `json:"id"`
	Org           string `json:"org"`
	Name          string `json:"name"`
	Identifier    string `json:"identifier"`
	Provider      string `json:"provider"`
	Picture       string `json:"picture"`
	Profile       string `json:"profile"`
	Disabled      bool   `json:"disabled"`
	RefreshNeeded bool   `json:"refreshNeeded"`
	Connected     bool   `json:"connected"`
	GroupID       string `json:"groupId"`
	GroupName     string `json:"groupName"`
	Customer      *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"customer"`
}

func decodePolledIntegrations(body []byte) ([]postizIntegration, error) {
	var arr []postizIntegration
	if err := json.Unmarshal(body, &arr); err == nil {
		return arr, nil
	}
	var env struct {
		Data []postizIntegration `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, err
	}
	return env.Data, nil
}

func decodePolledPosts(body []byte) ([]postizPost, error) {
	var flat []postizPost
	if err := json.Unmarshal(body, &flat); err == nil {
		return flat, nil
	}
	var env struct {
		Posts []struct {
			ID          string `json:"id"`
			Content     string `json:"content"`
			Group       string `json:"group"`
			ReleaseURL  string `json:"releaseURL"`
			Integration struct {
				ID                 string `json:"id"`
				ProviderIdentifier string `json:"providerIdentifier"`
			} `json:"integration"`
		} `json:"posts"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, err
	}
	out := make([]postizPost, 0, len(env.Posts))
	for _, p := range env.Posts {
		out = append(out, postizPost{
			ID:            p.ID,
			Content:       p.Content,
			Channel:       normalizePolledProvider(p.Integration.ProviderIdentifier, ""),
			IntegrationID: p.Integration.ID,
			ExternalGroup: p.Group,
			ReleaseURL:    p.ReleaseURL,
			AccountID:     p.Integration.ID,
		})
	}
	return out, nil
}

func normalizePolledProvider(identifier, provider string) string {
	raw := strings.ToLower(strings.TrimSpace(identifier))
	if raw == "" {
		raw = strings.ToLower(strings.TrimSpace(provider))
	}
	switch raw {
	case "linkedin-page":
		return "linkedin"
	case "instagram-standalone":
		return "instagram"
	case "tiktok-business":
		return "tiktok"
	}
	return normChannel(raw)
}

func formatPolledHandle(provider, profile, name string) string {
	p := strings.TrimSpace(profile)
	if p == "" {
		return strings.TrimSpace(name)
	}
	switch provider {
	case "x", "threads", "bluesky", "tiktok", "instagram":
		if !strings.HasPrefix(p, "@") && !strings.HasPrefix(p, "http") {
			return "@" + p
		}
	}
	return p
}

// postizNotification is one Postiz notification as the API returns it.
type postizNotification struct {
	ID      string `json:"id"`
	Message string `json:"message"`
}

// postizToPost converts a Postiz post into a mirrored social post.
func postizToPost(po *postizPost) socialPost {
	return socialPost{
		ID:                    mint.ID("mirror"),
		Org:                   orgFromIntegration(po.IntegrationID),
		Content:               text.Trim(po.Content, maxContent),
		Channel:               normChannel(po.Channel),
		Status:                statusMirrored,
		Origin:                "postiz",
		ExternalIDPostiz:      po.ID,
		ExternalIntegrationId: po.IntegrationID,
		ExternalGroup:         po.ExternalGroup,
		ReleaseURL:            po.ReleaseURL,
		AccountID:             po.AccountID,
		CreatedAt:             time.Now().Unix(),
		UpdatedAt:             time.Now().Unix(),
	}
}
