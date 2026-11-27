package provider

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func tiktokStub(t *testing.T, privacy string, statuses ...string) *stub {
	t.Helper()
	srv := newStub(t, func(w http.ResponseWriter, _ *http.Request, a asked) {
		switch a.Key {
		case "POST /v2/post/publish/creator_info/query/":
			say(w, `{"data":{"creator_username":"acme","privacy_level_options":`+privacy+`},"error":{"code":"ok"}}`)
		case "POST /v2/post/publish/video/init/", "POST /v2/post/publish/content/init/":
			say(w, `{"data":{"publish_id":"pub1"},"error":{"code":"ok"}}`)
		case "POST /v2/post/publish/status/fetch/":
			st := "PUBLISH_COMPLETE"
			if len(statuses) > 0 {
				st, statuses = statuses[0], statuses[1:]
			}
			switch st {
			case "FAILED":
				say(w, `{"data":{"status":"FAILED","fail_reason":"file_format_check_failed"},"error":{"code":"ok"}}`)
			case "PUBLISH_COMPLETE":
				say(w, `{"data":{"status":"PUBLISH_COMPLETE","publicaly_available_post_id":[7311]},"error":{"code":"ok"}}`)
			default:
				say(w, `{"data":{"status":"`+st+`"},"error":{"code":"ok"}}`)
			}
		default:
			refuse(w, 404, "no "+a.Key)
		}
	})
	swap(t, &tiktokAPI, srv.URL)
	quick(t)
	return srv
}

func TestTikTokPostsAVideoAtTheMostPublicLevelTheCreatorIsOffered(t *testing.T) {
	srv := tiktokStub(t, `["SELF_ONLY","MUTUAL_FOLLOW_FRIENDS"]`, "PROCESSING_DOWNLOAD", "PUBLISH_COMPLETE")
	link, err := postTikTok(context.Background(), postTo{Token: "tok", Account: "open-1", Text: "Watch this", Media: []string{"https://cdn.example.com/a.mp4"}})
	if err != nil || link != "https://www.tiktok.com/@acme/video/7311" {
		t.Fatalf("link=%q err=%v", link, err)
	}
	init := srv.took("POST /v2/post/publish/video/init/")[0]
	info := init.json()["post_info"].(map[string]any)
	source := init.json()["source_info"].(map[string]any)
	if info["privacy_level"] != "MUTUAL_FOLLOW_FRIENDS" || info["title"] != "Watch this" || source["source"] != "PULL_FROM_URL" || source["video_url"] != "https://cdn.example.com/a.mp4" {
		t.Fatalf("init = %v", init.json())
	}
	if init.Header.Get("Authorization") != "Bearer tok" {
		t.Fatalf("auth = %q", init.Header.Get("Authorization"))
	}
}

// An unaudited app is offered SELF_ONLY, and posts privately rather than failing.
func TestTikTokPostsPrivatelyWhenThatIsAllItIsOffered(t *testing.T) {
	srv := tiktokStub(t, `["SELF_ONLY"]`)
	if _, err := postTikTok(context.Background(), postTo{Token: "tok", Text: "x", Media: []string{"https://cdn.example.com/a.mov"}}); err != nil {
		t.Fatal(err)
	}
	if lvl := srv.took("POST /v2/post/publish/video/init/")[0].json()["post_info"].(map[string]any)["privacy_level"]; lvl != "SELF_ONLY" {
		t.Fatalf("privacy = %v", lvl)
	}
}

func TestTikTokPostsPhotos(t *testing.T) {
	srv := tiktokStub(t, `["PUBLIC_TO_EVERYONE"]`)
	_, err := postTikTok(context.Background(), postTo{Token: "tok", Text: "Day one\nWith the team", Media: []string{"https://cdn.example.com/a.jpg", "https://cdn.example.com/b.jpg"}})
	if err != nil {
		t.Fatal(err)
	}
	body := srv.took("POST /v2/post/publish/content/init/")[0].json()
	info := body["post_info"].(map[string]any)
	if body["media_type"] != "PHOTO" || body["post_mode"] != "DIRECT_POST" || info["title"] != "Day one" || info["description"] != "Day one\nWith the team" || info["privacy_level"] != "PUBLIC_TO_EVERYONE" {
		t.Fatalf("photos = %v", body)
	}
	if imgs := body["source_info"].(map[string]any)["photo_images"].([]any); len(imgs) != 2 {
		t.Fatalf("images = %v", imgs)
	}
}

func TestTikTokNamesAFailedPostAndTakesAStillRunningOneAsPosted(t *testing.T) {
	tiktokStub(t, `["SELF_ONLY"]`, "FAILED")
	_, err := postTikTok(context.Background(), postTo{Token: "tok", Text: "x", Media: []string{"https://cdn.example.com/a.mp4"}})
	if err == nil || !strings.Contains(err.Error(), "file format check failed") {
		t.Fatalf("a failed post = %v", err)
	}

	// Still processing when the looking stops: TikTok has the post, and asking
	// again would make it twice.
	tiktokStub(t, `["SELF_ONLY"]`, "PROCESSING_UPLOAD", "PROCESSING_UPLOAD", "PROCESSING_UPLOAD", "PROCESSING_UPLOAD", "PROCESSING_UPLOAD", "PROCESSING_UPLOAD", "PROCESSING_UPLOAD", "PROCESSING_UPLOAD", "PROCESSING_UPLOAD")
	link, err := postTikTok(context.Background(), postTo{Token: "tok", Text: "x", Media: []string{"https://cdn.example.com/a.mp4"}})
	if err != nil || link != "https://www.tiktok.com/@acme" {
		t.Fatalf("a post still running = %q %v", link, err)
	}
}

func TestTikTokRefusesWhatItCannotCarry(t *testing.T) {
	tiktokStub(t, `["SELF_ONLY"]`)
	for name, media := range map[string][]string{
		"nothing":           nil,
		"a video and photo": {"https://c.example.com/a.mp4", "https://c.example.com/a.png"},
		"two videos":        {"https://c.example.com/a.mp4", "https://c.example.com/b.mp4"},
	} {
		if _, err := postTikTok(context.Background(), postTo{Token: "tok", Text: "x", Media: media}); err == nil {
			t.Errorf("%s was posted", name)
		}
	}
	if err := (tiktokFailure{Code: "access_token_invalid", Message: "bad"}).refused(); err == nil {
		t.Error("an error code was taken as ok")
	}
}

func TestYouTubeUploadsTheVideoInTwoRequestsAndStreamsTheFile(t *testing.T) {
	open(t)
	video := strings.Repeat("v", 5000)
	media := newStub(t, func(w http.ResponseWriter, _ *http.Request, _ asked) {
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Length", strconv.Itoa(len(video)))
		_, _ = io.WriteString(w, video)
	})
	var session string
	yt := newStub(t, func(w http.ResponseWriter, r *http.Request, a asked) {
		switch a.Key {
		case "POST /upload/youtube/v3/videos":
			w.Header().Set("Location", session)
			w.WriteHeader(http.StatusOK)
		case "PUT /session/abc":
			say(w, `{"id":"vid42"}`)
		default:
			refuse(w, 404, "no "+a.Key)
		}
	})
	session = yt.URL + "/session/abc"
	swap(t, &youtubeUpload, yt.URL+"/upload/youtube/v3/videos")
	swap(t, &uploadClient, http.DefaultClient)

	link, err := postYouTube(context.Background(), postTo{Token: "tok", Account: "UC1", Text: "How we ship\nThe whole story <b>here</b>", Media: []string{media.URL + "/a.mp4"}})
	if err != nil || link != "https://www.youtube.com/watch?v=vid42" {
		t.Fatalf("link=%q err=%v", link, err)
	}
	start := yt.took("POST /upload/youtube/v3/videos")[0]
	if start.Query.Get("uploadType") != "resumable" || start.Query.Get("part") != "snippet,status" || start.Header.Get("Authorization") != "Bearer tok" {
		t.Fatalf("start = %v %v", start.Query, start.Header)
	}
	if start.Header.Get("X-Upload-Content-Length") != "5000" || start.Header.Get("X-Upload-Content-Type") != "video/mp4" {
		t.Fatalf("declared = %v", start.Header)
	}
	snippet := start.json()["snippet"].(map[string]any)
	if snippet["title"] != "How we ship" || !strings.Contains(snippet["description"].(string), "The whole story") {
		t.Fatalf("snippet = %v", snippet)
	}
	put := yt.took("PUT /session/abc")[0]
	if len(put.Body) != 5000 || put.Header.Get("Content-Type") != "video/mp4" {
		t.Fatalf("put %d bytes, type %q", len(put.Body), put.Header.Get("Content-Type"))
	}
}

// The file goes only where YouTube's own upload host says, however the answer is worded.
func TestYouTubeSendsTheFileOnlyToItsOwnHost(t *testing.T) {
	open(t)
	media := newStub(t, func(w http.ResponseWriter, _ *http.Request, _ asked) {
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = io.WriteString(w, "vvvv")
	})
	elsewhere := newStub(t, func(w http.ResponseWriter, _ *http.Request, _ asked) { say(w, `{"id":"stolen"}`) })
	yt := newStub(t, func(w http.ResponseWriter, _ *http.Request, _ asked) {
		w.Header().Set("Location", elsewhere.URL+"/session")
	})
	swap(t, &youtubeUpload, yt.URL+"/upload")
	swap(t, &uploadClient, http.DefaultClient)
	_, err := postYouTube(context.Background(), postTo{Token: "tok", Text: "x", Media: []string{media.URL + "/a.mp4"}})
	if err == nil || len(elsewhere.keys()) != 0 {
		t.Fatalf("err=%v, the other host took %v", err, elsewhere.keys())
	}
}

func TestYouTubeTakesOneVideoAndNothingElse(t *testing.T) {
	for name, media := range map[string][]string{
		"no media": nil,
		"a photo":  {"https://cdn.example.com/a.png"},
		"two":      {"https://cdn.example.com/a.mp4", "https://cdn.example.com/b.mp4"},
		"mixed":    {"https://cdn.example.com/a.mp4", "https://cdn.example.com/a.png"},
	} {
		if _, err := postYouTube(context.Background(), postTo{Token: "t", Text: "x", Media: media}); err == nil {
			t.Errorf("%s was uploaded", name)
		}
	}
}

func TestYouTubeNamesTheChannel(t *testing.T) {
	srv := newStub(t, func(w http.ResponseWriter, _ *http.Request, a asked) {
		if a.Key == "GET /channels" {
			say(w, `{"items":[{"id":"UC123","snippet":{"title":"Hanzo"}}]}`)
			return
		}
		refuse(w, 404, "no")
	})
	swap(t, &youtubeAPI, srv.URL)
	who, err := identifyYouTube(context.Background(), "tok")
	if err != nil || who.ExternalID != "UC123" || who.Label != "Hanzo" {
		t.Fatalf("who=%+v err=%v", who, err)
	}
	if q := srv.took("GET /channels")[0].Query; q.Get("mine") != "true" {
		t.Fatalf("asked %v", q)
	}
}
