package provider

// post_tiktok.go — TikTok's Content Posting API: Direct Post, from an address.
//
// TikTok fetches the file itself (PULL_FROM_URL), from a domain the app has
// verified in its developer portal. What a post may be set to is the creator's
// to say: a client that has not passed TikTok's audit is limited to SELF_ONLY,
// so the most public privacy level the creator's own options offer is the one
// used, and an unaudited app posts privately instead of being refused.
//
// A post is a video, or photos. TikTok answers the init with a publish id and
// finishes in its own time, so the status is looked at a few times. A post
// still processing when the looking stops has been accepted, and is reported as
// posted: asking again would make it twice.

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

var tiktokAPI = "https://open.tiktokapis.com"

// tiktokPrivacy is the order a creator's options are taken in: the most public
// first.
var tiktokPrivacy = []string{"PUBLIC_TO_EVERYONE", "MUTUAL_FOLLOW_FRIENDS", "FOLLOWER_OF_CREATOR", "SELF_ONLY"}

// tiktokFailure is TikTok's own error object, present on every answer.
type tiktokFailure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (f tiktokFailure) refused() error {
	if f.Code == "" || f.Code == "ok" {
		return nil
	}
	return postErr{status: 400, msg: fmt.Sprintf("%s (%s)", f.Message, f.Code)}
}

func postTikTok(ctx context.Context, to postTo) (string, error) {
	images, videos, err := splitMedia(to.Media)
	if err != nil {
		return "", err
	}
	switch {
	case len(videos) > 1 || (len(videos) == 1 && len(images) > 0):
		return "", errors.New("TikTok takes one video, or photos, in a post")
	case len(videos) == 0 && len(images) == 0:
		return "", errors.New("TikTok needs a video or photos")
	case len(images) > 35:
		return "", errors.New("TikTok takes thirty-five photos at most in a post")
	}

	var creator struct {
		Data struct {
			Username string   `json:"creator_username"`
			Privacy  []string `json:"privacy_level_options"`
		} `json:"data"`
		Error tiktokFailure `json:"error"`
	}
	if err := sendJSON(ctx, to.Token, tiktokAPI+"/v2/post/publish/creator_info/query/", map[string]any{}, &creator); err != nil {
		return "", err
	}
	if err := creator.Error.refused(); err != nil {
		return "", err
	}
	privacy := ""
	for _, level := range tiktokPrivacy {
		if slices.Contains(creator.Data.Privacy, level) {
			privacy = level
			break
		}
	}
	if privacy == "" {
		return "", errors.New("TikTok offered this account no privacy level to post at")
	}

	var body map[string]any
	endpoint := "/v2/post/publish/video/init/"
	if len(videos) == 1 {
		body = map[string]any{
			"post_info":   map[string]any{"title": to.Text, "privacy_level": privacy},
			"source_info": map[string]any{"source": "PULL_FROM_URL", "video_url": videos[0]},
		}
	} else {
		endpoint = "/v2/post/publish/content/init/"
		body = map[string]any{
			"post_info": map[string]any{
				"title": firstRunes(pinTitle(to.Text), 90), "description": to.Text,
				"privacy_level": privacy, "auto_add_music": true,
			},
			"source_info": map[string]any{"source": "PULL_FROM_URL", "photo_cover_index": 0, "photo_images": images},
			"post_mode":   "DIRECT_POST",
			"media_type":  "PHOTO",
		}
	}
	var started struct {
		Data struct {
			PublishID string `json:"publish_id"`
		} `json:"data"`
		Error tiktokFailure `json:"error"`
	}
	if err := sendJSON(ctx, to.Token, tiktokAPI+endpoint, body, &started); err != nil {
		return "", err
	}
	if err := started.Error.refused(); err != nil {
		return "", err
	}
	if started.Data.PublishID == "" {
		return "", errors.New("TikTok answered without the post's id")
	}

	profile := "https://www.tiktok.com/"
	if creator.Data.Username != "" {
		profile += "@" + url.PathEscape(creator.Data.Username)
	}
	link := profile
	err = settle(ctx, func(ctx context.Context) (bool, error) {
		var st struct {
			Data struct {
				Status string `json:"status"`
				Reason string `json:"fail_reason"`
				IDs    []any  `json:"publicaly_available_post_id"`
			} `json:"data"`
			Error tiktokFailure `json:"error"`
		}
		if err := sendJSON(ctx, to.Token, tiktokAPI+"/v2/post/publish/status/fetch/", map[string]string{"publish_id": started.Data.PublishID}, &st); err != nil {
			return false, err
		}
		if err := st.Error.refused(); err != nil {
			return false, err
		}
		switch st.Data.Status {
		case "PUBLISH_COMPLETE":
			if len(st.Data.IDs) > 0 {
				link = fmt.Sprintf("%s/video/%v", profile, st.Data.IDs[0])
			}
			return true, nil
		case "FAILED":
			return false, fmt.Errorf("TikTok could not publish the post: %s", strings.ToLower(strings.ReplaceAll(st.Data.Reason, "_", " ")))
		}
		return false, nil
	})
	if err != nil && !errors.Is(err, errProcessing) {
		return "", err
	}
	return link, nil
}

// firstRunes bounds a title derived from a post's words to n characters. The
// words themselves are never cut: a network's own limit is a refusal worth reading.
func firstRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
