package provider

// social_post.go — a post to an org's social account, made here.
//
// The org's account credentials are kept by this process (KMS, read and
// refreshed by tokenFor), and an automation or the social plane runs in another,
// so the post is made where the credential is: the caller asks over the plane
// (IntegrationsPost) and is answered with the post's link or the platform's
// reason. The credential never crosses the socket.
//
// One poster per network, each in the file of its connector. A poster is handed
// the account and the post and answers the post's address; it does not retry,
// choose an account, or decide what an org may do — planePost does.
//
// A post is asked again only when the platform answers that it was busy or
// broken (429, 5xx). A timeout, a lost connection or an answer that could not be
// read may follow a post that went, and a platform that does not de-duplicate
// would show it twice.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// postTo is one post and the account it goes through, as a poster receives it.
type postTo struct {
	// Token is the account's credential: an access token, a Page token, an app
	// password or a bot token, whichever the network takes.
	Token string
	// Account is the platform id the post is addressed to: a member, a Page, a
	// channel, a board.
	Account string
	// Kind is personal or page.
	Kind string
	// Handle is the name a person knows the account by.
	Handle string
	// Text is the post, as it is published.
	Text string
	// Media are the public addresses of the photos or the video, in order.
	Media []string
	// Sealed are the account's other custodied values by name: a server, an
	// instance. They are not secrets of their own and are never returned.
	Sealed map[string]string
}

// poster is one platform a post goes to: its name, the connector that holds the
// account's credential, and how a post is made.
type poster struct {
	name, provider string
	publish        func(ctx context.Context, to postTo) (string, error)
}

// posters are the platforms an org posts to. The key is the network's id in the
// social plane, which is its connector's id.
var posters = map[string]poster{
	"x":               {name: "X", provider: xProvider, publish: postX},
	"linkedin":        {name: "LinkedIn", provider: "linkedin", publish: postLinkedIn},
	"facebook":        {name: "Facebook", provider: "facebook", publish: postFacebook},
	"instagram":       {name: "Instagram", provider: "instagram", publish: postInstagram},
	"threads":         {name: "Threads", provider: "threads", publish: postThreads},
	"tiktok":          {name: "TikTok", provider: "tiktok", publish: postTikTok},
	"youtube":         {name: "YouTube", provider: "youtube", publish: postYouTube},
	"pinterest":       {name: "Pinterest", provider: "pinterest", publish: postPinterest},
	"reddit":          {name: "Reddit", provider: "reddit", publish: postReddit},
	"bluesky":         {name: "Bluesky", provider: "bluesky", publish: postBluesky},
	"mastodon":        {name: "Mastodon", provider: "mastodon", publish: postMastodon},
	"telegram-bot":    {name: "Telegram", provider: "telegram-bot", publish: postTelegram},
	"discord-webhook": {name: "Discord", provider: "discord-webhook", publish: postDiscord},
	"devto":           {name: "Dev.to", provider: "devto", publish: postDevTo},
	"medium":          {name: "Medium", provider: "medium", publish: postMedium},
	"hashnode":        {name: "Hashnode", provider: "hashnode", publish: postHashnode},
	"hanzo-cms":       {name: "Hanzo CMS", provider: "hanzo-cms", publish: postHanzoCMS},
}

// The platforms, as values so a test stands in for them. Production never
// reassigns them.
var (
	xAPI        = "https://api.x.com"
	linkedinAPI = "https://api.linkedin.com"
	graphAPI    = "https://graph.facebook.com/v21.0"
	postClient  = &http.Client{Timeout: 30 * time.Second}
	// postWaits are the pauses before each retry of a post the platform could
	// not take just then.
	postWaits = []time.Duration{2 * time.Second, 6 * time.Second}
)

// exposePost publishes the social post and connect ops on the internal plane. Mount calls it.
func exposePost() {
	cloud.Plane().Post("/integrations/post", planePost,
		zip.WithOperationID(client.IntegrationsPost),
		zip.WithSummary("Publish one text post to a social account the caller's org connected"))
	cloud.Plane().Post("/integrations/connect", planeConnect,
		zip.WithOperationID(client.IntegrationsConnect),
		zip.WithSummary("Start an OAuth consent or verify and seal a social credential for the caller's org"))
}

// planeConnect initiates a social OAuth flow or verifies and seals a credential
// on behalf of the caller's org over the internal plane. It asks who is calling
// the way the route does (connect): an AdminOnly connector is its org admin's to
// connect, by the admin bit the caller carries for this org.
func planeConnect(ctx context.Context, in *client.IntegrationsConnectIn) (*client.IntegrationsConnectOut, error) {
	s := mounted
	if s == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "connect: integrations is not serving")
	}
	who := cloud.Who(ctx)
	if who.Org == "" {
		return nil, zip.ErrForbidden("connect: no org on the call")
	}
	if in == nil || strings.TrimSpace(in.Provider) == "" {
		return nil, zip.ErrBadRequest("connect: provider is required")
	}
	out, err := connectFor(ctx, s, who.Org, who.User, orgAdminOf(who), &connectIn{
		Provider:  in.Provider,
		Token:     in.Credential,
		AccountID: in.AccountID,
		Fields:    client.Named(in.Fields),
		Return:    in.Return,
	})
	if err != nil {
		return nil, err
	}
	res := &client.IntegrationsConnectOut{
		AuthorizeURL: out.AuthorizeURL,
		Connected:    out.Connected,
		Provider:     out.Provider,
	}
	if out.Account != nil {
		res.Account = *out.Account
	}
	if out.ExternalID != nil {
		res.ExternalID = *out.ExternalID
	}
	if out.Scopes != nil {
		res.Scopes = *out.Scopes
	}
	return res, nil
}

// planePost makes one post for the CALLER'S org, read off the peer context and
// never from the request, with the credential that org connected. A post the
// platform refused, or an account not connected, is an answer (Error), so the
// run that asked records why. A named function, so zipdoc lifts this prose.
func planePost(ctx context.Context, in *client.SocialPostIn) (*client.SocialPostOut, error) {
	s := mounted
	if s == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "post: integrations is not serving")
	}
	org, ok := cloud.Tenant(ctx)
	if !ok {
		return nil, zip.ErrForbidden("post: no org on the call")
	}
	p, ok := posters[strings.ToLower(strings.TrimSpace(in.To))]
	if !ok {
		return nil, zip.ErrBadRequest("post: an org posts to " + strings.Join(slices.Sorted(maps.Keys(posters)), ", "))
	}
	if strings.TrimSpace(in.Text) == "" {
		return nil, zip.ErrBadRequest("post: there is nothing to post")
	}
	token, conn, err := tokenAndConn(s, ctx, org, p.provider, in.Account)
	if err != nil && strings.Contains(err.Error(), "name the account") {
		return &client.SocialPostOut{Error: err.Error()}, nil
	}
	if err != nil || len(token) == 0 {
		return &client.SocialPostOut{Error: p.name + " is not connected: connect it in Settings → Integrations"}, nil
	}
	to := postTo{
		Token: string(token), Account: conn.ExternalID, Kind: conn.BotUserID, Handle: conn.AccountLabel,
		Text: in.Text, Media: in.Media, Sealed: sealedOf(s, org, p.provider, conn),
	}
	for i := 0; ; i++ {
		link, err := p.publish(ctx, to)
		if err == nil {
			return &client.SocialPostOut{URL: link}, nil
		}
		if !againPost(err) || i >= len(postWaits) {
			return &client.SocialPostOut{Error: postRefused(p.name, err)}, nil
		}
		select {
		case <-ctx.Done():
			return &client.SocialPostOut{Error: postRefused(p.name, ctx.Err())}, nil
		case <-time.After(postWaits[i]):
		}
	}
}

// sealedOf reads the values a connection custodies beside its credential: the
// server an app password belongs to, the instance a token was issued by. A
// value that cannot be read is left out, and the poster says what it needed.
func sealedOf(s *cloud.Service[state], org, provider string, conn Connection) map[string]string {
	p := s.State.providers[provider]
	if p == nil {
		return nil
	}
	path := custodyPath(p, org, conn.Label)
	out := map[string]string{}
	for _, name := range p.Secrets {
		if name == accessSecret || name == refreshSecret {
			continue
		}
		if v, err := kmsGet(s, path, name); err == nil && len(v) > 0 {
			out[name] = string(v)
		}
	}
	return out
}

// postErr is a platform's refusal: its status and its own words.
type postErr struct {
	status int
	msg    string
}

func (e postErr) Error() string { return fmt.Sprintf("%d: %s", e.status, e.msg) }

// again reports whether a refusal is worth asking again: the platform answered
// that it was busy or broken. Nothing else is asked twice — a timeout, a lost
// connection or an answer that could not be read may follow a post that went,
// and a platform that does not de-duplicate would show it twice.
func againPost(err error) bool {
	var pe postErr
	return errors.As(err, &pe) && (pe.status == http.StatusTooManyRequests || pe.status >= 500)
}

// call sends one request and decodes a JSON answer into out, or answers the
// platform's refusal in its own words.
func call(ctx context.Context, req *http.Request, out any) (http.Header, error) {
	h, _, err := callOn(postClient, req.WithContext(ctx), out)
	return h, err
}

// callWith is call for a request that already carries its context, on a client
// of the caller's choosing: an upload outlasts the usual bound.
func callWith(c *http.Client, req *http.Request, out any) error {
	_, _, err := callOn(c, req, out)
	return err
}

// callOn is the one request: it answers the headers and the status of a
// success, and the platform's refusal as a postErr.
func callOn(c *http.Client, req *http.Request, out any) (http.Header, int, error) {
	resp, err := c.Do(req)
	if err != nil {
		// The error names the address, and some networks take the credential
		// in it. What went wrong is the cause under it.
		var addr *url.Error
		if errors.As(err, &addr) {
			err = addr.Err
		}
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return nil, resp.StatusCode, postErr{status: resp.StatusCode, msg: postSaid(body)}
	}
	if out != nil && len(body) > 0 {
		if err := json.Unmarshal(body, out); err != nil {
			return nil, resp.StatusCode, fmt.Errorf("the platform's answer could not be read: %w", err)
		}
	}
	return resp.Header, resp.StatusCode, nil
}

// said is the sentence a platform's error body carries, whichever shape it has.
func postSaid(body []byte) string {
	var b struct {
		Detail  string `json:"detail"`
		Title   string `json:"title"`
		Message string `json:"message"`
		Error   any    `json:"error"`
		Errors  []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if json.Unmarshal(body, &b) == nil {
		switch e := b.Error.(type) {
		case map[string]any:
			if m, ok := e["message"].(string); ok && m != "" {
				return m
			}
		case string:
			if e != "" {
				return e
			}
		}
		for _, m := range []string{b.Detail, b.Message, b.Title} {
			if m != "" {
				return m
			}
		}
		if len(b.Errors) > 0 && b.Errors[0].Message != "" {
			return b.Errors[0].Message
		}
	}
	return firstLine(strings.TrimSpace(string(body)))
}

func withBearer(req *http.Request, token string) *http.Request {
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}

// tokenAndConn reads the named account's token. An empty account is the only
// connection. Several connections and no name is a refusal.
func tokenAndConn(s *cloud.Service[state], ctx context.Context, org, provider, account string) ([]byte, Connection, error) {
	conns, err := s.State.store.ListFor(ctx, org, provider)
	if err != nil {
		return nil, Connection{}, err
	}
	conn, err := pickConn(conns, account)
	if err != nil {
		return nil, Connection{}, err
	}
	tok, err := tokenForTarget(s, ctx, org, provider, conn.ExternalID, accessSecret)
	if err != nil && conn.Label != "" {
		tok, err = tokenForTarget(s, ctx, org, provider, conn.Label, accessSecret)
	}
	return tok, conn, err
}

// postX posts text as the connected X account (POST /2/tweets, scope tweet.write).
func postX(ctx context.Context, to postTo) (string, error) {
	if len(to.Media) > 0 {
		return "", errors.New("X posts go out as text from here; media does not yet")
	}
	token := to.Token
	b, _ := json.Marshal(map[string]string{"text": to.Text})
	req, _ := http.NewRequest(http.MethodPost, xAPI+"/2/tweets", strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	var out struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if _, err := call(ctx, withBearer(req, token), &out); err != nil {
		return "", err
	}
	if out.Data.ID == "" {
		return "", errors.New("X answered without the post's id")
	}
	return "https://x.com/i/status/" + out.Data.ID, nil
}

// postLinkedIn shares text as the connected member, or as a company when the
// account is a page. The company post uses the member token and the
// organization id.
func postLinkedIn(ctx context.Context, to postTo) (string, error) {
	if len(to.Media) > 0 {
		return "", errors.New("LinkedIn posts go out as text from here; media does not yet")
	}
	token, account, kind, text := to.Token, to.Account, to.Kind, to.Text
	author := ""
	if kind == "page" && account != "" {
		author = "urn:li:organization:" + account
	}
	if author == "" {
		req, _ := http.NewRequest(http.MethodGet, linkedinAPI+"/v2/userinfo", nil)
		var me struct {
			Sub string `json:"sub"`
		}
		if _, err := call(ctx, withBearer(req, token), &me); err != nil {
			return "", err
		}
		if me.Sub == "" && account != "" {
			me.Sub = account
		}
		if me.Sub == "" {
			return "", errors.New("LinkedIn did not say who the connected member is")
		}
		author = "urn:li:person:" + me.Sub
	}
	share := map[string]any{
		"author":         author,
		"lifecycleState": "PUBLISHED",
		"specificContent": map[string]any{"com.linkedin.ugc.ShareContent": map[string]any{
			"shareCommentary": map[string]string{"text": text}, "shareMediaCategory": "NONE",
		}},
		"visibility": map[string]string{"com.linkedin.ugc.MemberNetworkVisibility": "PUBLIC"},
	}
	b, _ := json.Marshal(share)
	req, _ := http.NewRequest(http.MethodPost, linkedinAPI+"/v2/ugcPosts", strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Restli-Protocol-Version", "2.0.0")
	var out struct {
		ID string `json:"id"`
	}
	h, err := call(ctx, withBearer(req, token), &out)
	if err != nil {
		return "", err
	}
	id := out.ID
	if id == "" {
		id = h.Get("X-Restli-Id")
	}
	if id == "" {
		return "", errors.New("LinkedIn answered without the post's id")
	}
	return "https://www.linkedin.com/feed/update/" + id, nil
}

// postFacebook publishes to the Page whose own token is stored for it: words, or
// words with one video, one photo, or several photos. A Page has no personal
// profile, so an account with no id is the token's own ("me").
func postFacebook(ctx context.Context, to postTo) (string, error) {
	id := to.Account
	if id == "" {
		id = "me"
	}
	page := graphAPI + "/" + url.PathEscape(id)
	images, videos, err := splitMedia(to.Media)
	if err != nil {
		return "", err
	}
	var out struct {
		ID     string `json:"id"`
		PostID string `json:"post_id"`
	}
	switch {
	case len(videos) > 1 || (len(videos) == 1 && len(images) > 0):
		return "", errors.New("Facebook takes one video, or photos, in a post")
	case len(videos) == 1:
		err = postForm(ctx, to.Token, page+"/videos", url.Values{"file_url": {videos[0]}, "description": {to.Text}}, &out)
		if err != nil {
			return "", err
		}
		if out.ID == "" {
			return "", errors.New("Facebook answered without the video's id")
		}
		return "https://www.facebook.com/watch/?v=" + out.ID, nil
	case len(images) == 1:
		err = postForm(ctx, to.Token, page+"/photos", url.Values{"url": {images[0]}, "caption": {to.Text}, "published": {"true"}}, &out)
		if err != nil {
			return "", err
		}
		return facebookLink(out.PostID, out.ID)
	case len(images) > 1:
		// Each photo is stored unpublished, then one post attaches them all.
		form := url.Values{"message": {to.Text}}
		for i, img := range images {
			var up struct {
				ID string `json:"id"`
			}
			if err := postForm(ctx, to.Token, page+"/photos", url.Values{"url": {img}, "published": {"false"}}, &up); err != nil {
				return "", err
			}
			if up.ID == "" {
				return "", errors.New("Facebook answered without the photo's id")
			}
			form.Set(fmt.Sprintf("attached_media[%d]", i), `{"media_fbid":"`+up.ID+`"}`)
		}
		if err := postForm(ctx, to.Token, page+"/feed", form, &out); err != nil {
			return "", err
		}
		return facebookLink(out.ID, "")
	}
	if err := postForm(ctx, to.Token, page+"/feed", url.Values{"message": {to.Text}}, &out); err != nil {
		return "", err
	}
	return facebookLink(out.ID, "")
}

// facebookLink is the address of a post by the first id the platform gave.
func facebookLink(ids ...string) (string, error) {
	for _, id := range ids {
		if id != "" {
			return "https://www.facebook.com/" + id, nil
		}
	}
	return "", errors.New("Facebook answered without the post's id")
}

// refused says why a platform did not take a post, in words a person acts on.
func postRefused(name string, err error) string {
	var pe postErr
	if !errors.As(err, &pe) {
		return name + " could not be reached: " + err.Error()
	}
	switch pe.status {
	case http.StatusUnauthorized:
		return fmt.Sprintf("%s refused the connection (401): %s. Reconnect %s in Settings → Integrations", name, pe.msg, name)
	case http.StatusForbidden:
		return fmt.Sprintf("%s refused the post (403): %s", name, pe.msg)
	case http.StatusTooManyRequests:
		return fmt.Sprintf("%s is limiting posts just now (429): %s", name, pe.msg)
	}
	return fmt.Sprintf("%s did not take the post (%d): %s", name, pe.status, pe.msg)
}

// firstLine is a platform's answer cut to one line a run can keep.
func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if r := []rune(s); len(r) > 240 {
		return string(r[:240]) + "…"
	}
	return s
}
