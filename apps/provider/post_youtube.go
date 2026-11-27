package provider

// post_youtube.go — YouTube, one resumable upload.
//
// A YouTube post is a video, so a post here is exactly one. The file is fetched
// from its address (the host must say how large it is: YouTube is told before
// the first byte) and streamed to YouTube in the second request of the
// resumable protocol, never held in memory. The first line of the words is the
// title; the words are the description.
//
// A video from a project Google has not audited is published private, whatever
// is asked for. That is Google's rule, and it is reported in the answer's own
// words rather than worked around.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	youtubeUpload = "https://www.googleapis.com/upload/youtube/v3/videos"
	youtubeAPI    = "https://www.googleapis.com/youtube/v3"
	// uploadClient carries a video's bytes, which take longer than any call.
	uploadClient = &http.Client{Timeout: 8 * time.Minute}
)

// youtubeMax is the largest video fetched for an upload, 256 MiB: what moves in
// the eight minutes an upload is given.
const youtubeMax = 256 << 20

func identifyYouTube(ctx context.Context, token string) (identity, error) {
	var out struct {
		Items []struct {
			ID      string `json:"id"`
			Snippet struct {
				Title string `json:"title"`
			} `json:"snippet"`
		} `json:"items"`
	}
	if err := getJSON(ctx, youtubeAPI+"/channels?part=snippet&mine=true", token, &out); err != nil {
		return identity{}, err
	}
	if len(out.Items) == 0 {
		return identity{}, errNoIdentity
	}
	return identity{ExternalID: out.Items[0].ID, Label: out.Items[0].Snippet.Title}, nil
}

func postYouTube(ctx context.Context, to postTo) (string, error) {
	images, videos, err := splitMedia(to.Media)
	if err != nil {
		return "", err
	}
	if len(videos) != 1 || len(images) > 0 {
		return "", errors.New("YouTube needs one video, and nothing else, in a post")
	}
	file, err := fetchMedia(ctx, videos[0], youtubeMax)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Body.Close() }()
	if file.Size <= 0 {
		return "", errors.New("the video's host does not say how large it is, and YouTube must be told")
	}
	ctype := file.Type
	if !strings.HasPrefix(ctype, "video/") {
		ctype = "video/*"
	}

	title := strings.NewReplacer("<", "", ">", "").Replace(pinTitle(to.Text))
	meta := map[string]any{
		"snippet": map[string]any{"title": title, "description": to.Text, "categoryId": "22"},
		"status":  map[string]any{"privacyStatus": "public", "selfDeclaredMadeForKids": false},
	}
	start, err := newJSONRequest(ctx, youtubeUpload+"?uploadType=resumable&part=snippet,status", meta)
	if err != nil {
		return "", err
	}
	start.Header.Set("X-Upload-Content-Type", ctype)
	start.Header.Set("X-Upload-Content-Length", fmt.Sprint(file.Size))
	h, err := call(ctx, withBearer(start, to.Token), nil)
	if err != nil {
		return "", err
	}
	session := h.Get("Location")
	if session == "" {
		return "", errors.New("YouTube answered without an upload address")
	}
	// The video goes where YouTube says, and only to YouTube's own upload host.
	su, serr := url.Parse(session)
	uu, _ := url.Parse(youtubeUpload)
	if serr != nil || su.Scheme != uu.Scheme || su.Host != uu.Host {
		return "", errors.New("YouTube answered with an upload address that is not its own")
	}

	put, err := http.NewRequestWithContext(ctx, http.MethodPut, session, file.Body)
	if err != nil {
		return "", err
	}
	put.ContentLength = file.Size
	put.Header.Set("Content-Type", ctype)
	var out struct {
		ID string `json:"id"`
	}
	if err := callWith(uploadClient, put, &out); err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", errors.New("YouTube answered without the video's id")
	}
	return "https://www.youtube.com/watch?v=" + out.ID, nil
}
