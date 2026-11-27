package provider

// post_instagram.go — Instagram professional accounts, through Meta's Graph.
//
// An Instagram account is reached through the Facebook Page it is linked to, so
// one Facebook consent yields the accounts, not the person: each Page that has
// a linked professional account becomes an account here, keyed by the
// Instagram id, and the credential sealed for it is that Page's own token,
// which does not expire (the same reason Facebook keeps the Page token).
//
// Publishing is two calls. A container is made from the media address, the
// network fetches the file itself, and when the container says it is finished
// it is published. Instagram has no text post: every post is a photo, a video
// (a Reel) or a carousel of up to ten of them.

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// expandInstagram turns one Facebook consent into the Instagram professional
// accounts of the Pages it manages.
func expandInstagram(ctx context.Context, creds OAuthConfig, token string, who identity) ([]socialGrant, error) {
	long := facebookLongLived(ctx, creds, token)
	var out struct {
		Data []struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			Token string `json:"access_token"`
			IG    struct {
				ID       string `json:"id"`
				Username string `json:"username"`
			} `json:"instagram_business_account"`
		} `json:"data"`
	}
	u := facebookGraph + "/me/accounts?fields=" + url.QueryEscape("id,name,access_token,instagram_business_account{id,username}") + "&limit=100"
	if err := getJSON(ctx, u, long, &out); err != nil {
		return nil, fmt.Errorf("instagram accounts: %w", err)
	}
	grants := make([]socialGrant, 0, len(out.Data))
	for _, p := range out.Data {
		if p.IG.ID == "" || p.Token == "" {
			// A Page with no linked professional account has nothing to post to, and
			// one listed without its own token cannot publish.
			continue
		}
		name := "@" + p.IG.Username
		if p.IG.Username == "" {
			name = p.Name
		}
		grants = append(grants, socialGrant{
			ExternalID: p.IG.ID, AccountLabel: name, Token: p.Token,
			Kind: "page", Author: who.ExternalID,
		})
	}
	if len(grants) == 0 {
		return nil, errors.New("instagram: no Page you manage is linked to an Instagram professional account")
	}
	return grants, nil
}

// postInstagram publishes one photo, one video or a carousel to the account.
func postInstagram(ctx context.Context, to postTo) (string, error) {
	images, videos, err := splitMedia(to.Media)
	if err != nil {
		return "", err
	}
	if len(to.Media) == 0 {
		return "", errors.New("Instagram needs a photo or a video: it has no text posts")
	}
	if len(to.Media) > 10 {
		return "", errors.New("Instagram takes ten photos or videos at most in a post")
	}
	base := graphAPI + "/" + url.PathEscape(to.Account)
	container := func(form url.Values) (string, error) {
		var out struct {
			ID string `json:"id"`
		}
		if err := postForm(ctx, to.Token, base+"/media", form, &out); err != nil {
			return "", err
		}
		if out.ID == "" {
			return "", errors.New("Instagram answered without the container's id")
		}
		return out.ID, nil
	}

	var creation string
	switch {
	case len(to.Media) == 1 && len(videos) == 1:
		creation, err = container(url.Values{"media_type": {"REELS"}, "video_url": {videos[0]}, "caption": {to.Text}})
	case len(to.Media) == 1:
		creation, err = container(url.Values{"image_url": {images[0]}, "caption": {to.Text}})
	default:
		kids := make([]string, 0, len(to.Media))
		for _, m := range to.Media {
			form := url.Values{"is_carousel_item": {"true"}}
			if videoAddress.MatchString(pathOf(m)) {
				form.Set("media_type", "VIDEO")
				form.Set("video_url", m)
			} else {
				form.Set("image_url", m)
			}
			id, cerr := container(form)
			if cerr != nil {
				return "", cerr
			}
			if werr := instagramReady(ctx, to.Token, id); werr != nil {
				return "", werr
			}
			kids = append(kids, id)
		}
		creation, err = container(url.Values{"media_type": {"CAROUSEL"}, "children": {strings.Join(kids, ",")}, "caption": {to.Text}})
	}
	if err != nil {
		return "", err
	}
	if err := instagramReady(ctx, to.Token, creation); err != nil {
		return "", err
	}
	var published struct {
		ID string `json:"id"`
	}
	if err := postForm(ctx, to.Token, base+"/media_publish", url.Values{"creation_id": {creation}}, &published); err != nil {
		return "", err
	}
	if published.ID == "" {
		return "", errors.New("Instagram answered without the post's id")
	}
	var link struct {
		Permalink string `json:"permalink"`
	}
	if err := getJSONAt(ctx, to.Token, graphAPI+"/"+url.PathEscape(published.ID)+"?fields=permalink", &link); err != nil || link.Permalink == "" {
		// The post is out. Its address is a courtesy.
		return "https://www.instagram.com/", nil
	}
	return link.Permalink, nil
}

// instagramReady waits for a container to finish processing. Instagram fetches
// the media itself, and a video takes a while.
func instagramReady(ctx context.Context, token, id string) error {
	return settle(ctx, func(ctx context.Context) (bool, error) {
		var st struct {
			Code string `json:"status_code"`
		}
		if err := getJSONAt(ctx, token, graphAPI+"/"+url.PathEscape(id)+"?fields=status_code", &st); err != nil {
			return false, err
		}
		switch st.Code {
		case "FINISHED", "PUBLISHED":
			return true, nil
		case "ERROR", "EXPIRED":
			return false, fmt.Errorf("Instagram could not process the media (%s)", strings.ToLower(st.Code))
		}
		return false, nil
	})
}
