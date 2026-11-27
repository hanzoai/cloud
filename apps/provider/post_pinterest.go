package provider

// post_pinterest.go — Pinterest, as boards.
//
// A pin belongs to a board, so a board is the account: one consent is every
// board the member can pin to, each keyed by its id and holding the member's
// token. A pin is a picture with a title, a description and the address it
// points to. Video pins are a separate upload the API gates apart, so a post
// here is one photo, or two to five as a carousel.

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strings"
)

var pinterestAPI = "https://api.pinterest.com"

const pinTitleMax = 100

// expandPinterest keeps every board the member owns or collaborates on.
func expandPinterest(ctx context.Context, _ OAuthConfig, token string, who identity) ([]socialGrant, error) {
	var grants []socialGrant
	bookmark := ""
	for page := 0; page < 5; page++ {
		q := url.Values{"page_size": {"100"}}
		if bookmark != "" {
			q.Set("bookmark", bookmark)
		}
		var out struct {
			Items []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"items"`
			Bookmark string `json:"bookmark"`
		}
		if err := getJSON(ctx, pinterestAPI+"/v5/boards?"+q.Encode(), token, &out); err != nil {
			if len(grants) == 0 {
				return nil, err
			}
			break
		}
		for _, b := range out.Items {
			if b.ID == "" || !validLabel(b.ID) {
				continue
			}
			label := b.Name
			if who.Label != "" {
				label = who.Label + " / " + b.Name
			}
			grants = append(grants, socialGrant{
				ExternalID: b.ID, AccountLabel: label, Token: token,
				Kind: "page", Author: who.ExternalID,
			})
		}
		if out.Bookmark == "" {
			break
		}
		bookmark = out.Bookmark
	}
	if len(grants) == 0 {
		return nil, errors.New("pinterest: the member has no board to pin to")
	}
	return grants, nil
}

var webAddress = regexp.MustCompile(`https?://[^\s<>"]+`)

// pinTitle is the first line of the words, short enough for a pin's title.
func pinTitle(text string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	first = strings.TrimSpace(first)
	if r := []rune(first); len(r) > pinTitleMax {
		return strings.TrimSpace(string(r[:pinTitleMax-1])) + "…"
	}
	return first
}

// postPinterest pins one photo, or a carousel of two to five, to the board.
func postPinterest(ctx context.Context, to postTo) (string, error) {
	images, videos, err := splitMedia(to.Media)
	if err != nil {
		return "", err
	}
	switch {
	case len(videos) > 0:
		return "", errors.New("Pinterest takes photos here, not video")
	case len(images) == 0:
		return "", errors.New("Pinterest needs a photo")
	case len(images) > 5:
		return "", errors.New("Pinterest takes five photos at most in a pin")
	}
	source := map[string]any{"source_type": "image_url", "url": images[0]}
	if len(images) > 1 {
		items := make([]map[string]string, len(images))
		for i, img := range images {
			items[i] = map[string]string{"url": img}
		}
		source = map[string]any{"source_type": "multiple_image_urls", "items": items, "index": 0}
	}
	pin := map[string]any{
		"board_id":     to.Account,
		"title":        pinTitle(to.Text),
		"description":  to.Text,
		"media_source": source,
	}
	if link := webAddress.FindString(to.Text); link != "" {
		pin["link"] = strings.TrimRight(link, ".,;:!?)")
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := sendJSON(ctx, to.Token, pinterestAPI+"/v5/pins", pin, &out); err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", errors.New("Pinterest answered without the pin's id")
	}
	return "https://www.pinterest.com/pin/" + out.ID + "/", nil
}
