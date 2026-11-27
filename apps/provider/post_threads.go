package provider

// post_threads.go — Threads, on its own Graph (graph.threads.net).
//
// The consent returns a token that lives an hour. It is traded at once for a
// long-lived one (60 days), and that token renews itself: Threads has no
// refresh token, so the long-lived token IS the refresh credential, and it is
// sealed under both names so the one refresh engine (freshOrg) works unchanged.
// A token can be renewed once it is a day old and before it expires.
//
// Threads takes its credential as a parameter, as its documentation shows, and
// not as a header. The request is built so a failure never reports the address.

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

var (
	threadsGraph = "https://graph.threads.net"
	threadsAPI   = threadsGraph + "/v1.0"
)

// threadsLong trades the consent's short-lived token for a long-lived one.
func threadsLong(ctx context.Context, creds OAuthConfig, short string) (token string, expires int64, err error) {
	if creds.ClientSecret == "" {
		return "", 0, errors.New("threads: the app has no secret to trade the token with")
	}
	return threadsToken(ctx, url.Values{
		"grant_type": {"th_exchange_token"}, "client_secret": {creds.ClientSecret}, "access_token": {short},
	}, "/access_token")
}

// threadsToken asks Threads for a token and reads when it stops working.
func threadsToken(ctx context.Context, q url.Values, endpoint string) (string, int64, error) {
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := oauthGetJSON(ctx, "threads", threadsGraph+endpoint+"?"+q.Encode(), nil, &out); err != nil {
		return "", 0, err
	}
	if strings.TrimSpace(out.AccessToken) == "" {
		return "", 0, errors.New("threads: no access token in the answer")
	}
	var at int64
	if out.ExpiresIn > 0 {
		at = time.Now().Add(time.Duration(out.ExpiresIn) * time.Second).Unix()
	}
	return out.AccessToken, at, nil
}

// expandThreads keeps the one account a Threads consent names, with a token
// that lasts.
func expandThreads(ctx context.Context, creds OAuthConfig, token string, who identity) ([]socialGrant, error) {
	if who.ExternalID == "" {
		return nil, errors.New("threads: the login named no account")
	}
	long, expires, err := threadsLong(ctx, creds, token)
	if err != nil {
		return nil, err
	}
	name := who.Label
	if name != "" {
		name = "@" + name
	}
	if name == "" {
		name = who.ExternalID
	}
	return []socialGrant{{
		ExternalID: who.ExternalID, AccountLabel: name, Token: long, Refresh: long, ExpiresAt: expires,
		Kind: "personal", Author: who.ExternalID,
	}}, nil
}

// threadsRefresh renews a long-lived token. It answers a new pair that is the
// same token twice, because that is what the engine seals.
func threadsRefresh(ctx context.Context, _ OAuthConfig, token string) (*ExchangeResult, error) {
	fresh, expires, err := threadsToken(ctx, url.Values{"grant_type": {"th_refresh_token"}, "access_token": {token}}, "/refresh_access_token")
	if err != nil {
		return nil, err
	}
	return &ExchangeResult{
		Tokens:    map[string]string{accessSecret: fresh, refreshSecret: fresh},
		ExpiresAt: expires,
	}, nil
}

// postThreads publishes words, one photo, one video, or a carousel of up to
// twenty, to the account.
func postThreads(ctx context.Context, to postTo) (string, error) {
	images, videos, err := splitMedia(to.Media)
	if err != nil {
		return "", err
	}
	if len(to.Media) > 20 {
		return "", errors.New("Threads takes twenty photos or videos at most in a post")
	}
	base := threadsAPI + "/" + url.PathEscape(to.Account)
	container := func(form url.Values) (string, error) {
		form.Set("access_token", to.Token)
		var out struct {
			ID string `json:"id"`
		}
		if err := threadsPost(ctx, base+"/threads", form, &out); err != nil {
			return "", err
		}
		if out.ID == "" {
			return "", errors.New("Threads answered without the container's id")
		}
		return out.ID, nil
	}

	var creation string
	switch {
	case len(to.Media) == 0:
		creation, err = container(url.Values{"media_type": {"TEXT"}, "text": {to.Text}})
	case len(to.Media) == 1 && len(videos) == 1:
		creation, err = container(url.Values{"media_type": {"VIDEO"}, "video_url": {videos[0]}, "text": {to.Text}})
	case len(to.Media) == 1:
		creation, err = container(url.Values{"media_type": {"IMAGE"}, "image_url": {images[0]}, "text": {to.Text}})
	default:
		kids := make([]string, 0, len(to.Media))
		for _, m := range to.Media {
			form := url.Values{"is_carousel_item": {"true"}}
			if videoAddress.MatchString(pathOf(m)) {
				form.Set("media_type", "VIDEO")
				form.Set("video_url", m)
			} else {
				form.Set("media_type", "IMAGE")
				form.Set("image_url", m)
			}
			id, cerr := container(form)
			if cerr != nil {
				return "", cerr
			}
			if werr := threadsReady(ctx, to.Token, id); werr != nil {
				return "", werr
			}
			kids = append(kids, id)
		}
		creation, err = container(url.Values{"media_type": {"CAROUSEL"}, "children": {strings.Join(kids, ",")}, "text": {to.Text}})
	}
	if err != nil {
		return "", err
	}
	if err := threadsReady(ctx, to.Token, creation); err != nil {
		return "", err
	}
	var published struct {
		ID string `json:"id"`
	}
	if err := threadsPost(ctx, base+"/threads_publish", url.Values{"creation_id": {creation}, "access_token": {to.Token}}, &published); err != nil {
		return "", err
	}
	if published.ID == "" {
		return "", errors.New("Threads answered without the post's id")
	}
	var link struct {
		Permalink string `json:"permalink"`
	}
	if err := threadsGet(ctx, threadsAPI+"/"+url.PathEscape(published.ID), url.Values{"fields": {"permalink"}, "access_token": {to.Token}}, &link); err != nil || link.Permalink == "" {
		return "https://www.threads.net/", nil
	}
	return link.Permalink, nil
}

// threadsReady waits for a container to finish. A text container is ready at
// once; a video is fetched and processed first.
func threadsReady(ctx context.Context, token, id string) error {
	return settle(ctx, func(ctx context.Context) (bool, error) {
		var st struct {
			Status string `json:"status"`
			Error  string `json:"error_message"`
		}
		q := url.Values{"fields": {"status,error_message"}, "access_token": {token}}
		if err := threadsGet(ctx, threadsAPI+"/"+url.PathEscape(id), q, &st); err != nil {
			return false, err
		}
		switch st.Status {
		case "FINISHED", "PUBLISHED":
			return true, nil
		case "ERROR", "EXPIRED":
			if st.Error != "" {
				return false, fmt.Errorf("Threads could not process the media: %s", st.Error)
			}
			return false, fmt.Errorf("Threads could not process the media (%s)", strings.ToLower(st.Status))
		}
		return false, nil
	})
}

// threadsPost sends a form with the credential in the body.
func threadsPost(ctx context.Context, endpoint string, form url.Values, out any) error {
	req, err := newRequest(ctx, "POST", endpoint, form.Encode())
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	_, err = call(ctx, req, out)
	return err
}

// threadsGet reads with the credential in the query. call strips the address
// from a transport failure, so the credential is not reported.
func threadsGet(ctx context.Context, endpoint string, q url.Values, out any) error {
	req, err := newRequest(ctx, "GET", endpoint+"?"+q.Encode(), "")
	if err != nil {
		return err
	}
	_, err = call(ctx, req, out)
	return err
}
