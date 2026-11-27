package provider

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func mastodonServer(t *testing.T) *stub {
	t.Helper()
	polled := 0
	srv := newTLSStub(t, func(w http.ResponseWriter, r *http.Request, a asked) {
		if r.Header.Get("Authorization") != "Bearer good-token" {
			w.WriteHeader(http.StatusUnauthorized)
			say(w, `{"error":"The access token is invalid"}`)
			return
		}
		switch {
		case a.Key == "GET /api/v1/accounts/verify_credentials":
			say(w, `{"id":"109","username":"Acme","acct":"Acme"}`)
		case a.Key == "POST /api/v2/media":
			w.WriteHeader(http.StatusAccepted)
			say(w, `{"id":"m-1"}`)
		case a.Key == "GET /api/v1/media/m-1":
			polled++
			if polled < 2 {
				w.WriteHeader(http.StatusPartialContent)
			}
			say(w, `{"id":"m-1"}`)
		case a.Key == "POST /api/v1/statuses":
			say(w, `{"id":"1","url":"https://mastodon.example/@Acme/1"}`)
		default:
			refuse(w, 404, "no "+a.Key)
		}
	})
	swap(t, &mastodonClient, srv.Client())
	swap(t, &mediaClient, http.DefaultClient)
	quick(t)
	return srv
}

func TestMastodonConnectsWithTheTokenAMadeOnItsOwnServer(t *testing.T) {
	srv := mastodonServer(t)
	host := strings.TrimPrefix(srv.URL, "https://")
	res, err := mastodonVerify(context.Background(), VerifyInput{Fields: map[string]string{"server": host, "token": "good-token"}})
	if err != nil {
		t.Fatal(err)
	}
	acct := "acme@" + host
	if res.ExternalID != acct || res.AccountLabel != "@"+acct || res.Tokens[accessSecret] != "good-token" || res.Tokens[serverSecret] != srv.URL {
		t.Fatalf("account = %+v", res)
	}
	if !validLabel(res.Key) || strings.ContainsAny(res.Key, "@:") {
		t.Fatalf("key = %q", res.Key)
	}
}

func TestMastodonRefusesATokenTheServerDoesNotKnow(t *testing.T) {
	srv := mastodonServer(t)
	var said verifyRefused
	_, err := mastodonVerify(context.Background(), VerifyInput{Fields: map[string]string{"server": srv.URL, "token": "stolen-token"}})
	if !asRefused(err, &said) || !strings.Contains(string(said), "did not accept the access token") || strings.Contains(string(said), "stolen") {
		t.Fatalf("err = %v", err)
	}
	for _, in := range []map[string]string{{"server": srv.URL}, {"token": "good-token"}, {"server": "http://x.example", "token": "good-token"}} {
		if _, err := mastodonVerify(context.Background(), VerifyInput{Fields: in}); !asRefused(err, &said) {
			t.Errorf("%v was taken: %v", in, err)
		}
	}
}

func TestMastodonPostsWordsAndWaitsForAVideoToBeProcessed(t *testing.T) {
	srv := mastodonServer(t)
	media := newStub(t, func(w http.ResponseWriter, _ *http.Request, _ asked) {
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write([]byte("movie"))
	})
	to := postTo{Token: "good-token", Text: "Hello fediverse", Sealed: map[string]string{serverSecret: srv.URL}}

	link, err := postMastodon(context.Background(), to)
	if err != nil || link != "https://mastodon.example/@Acme/1" {
		t.Fatalf("link=%q err=%v", link, err)
	}
	st := srv.took("POST /api/v1/statuses")[0].json()
	if st["status"] != "Hello fediverse" || st["visibility"] != "public" || st["media_ids"] != nil {
		t.Fatalf("status = %v", st)
	}

	to.Media = []string{media.URL + "/clip.mp4"}
	if _, err := postMastodon(context.Background(), to); err != nil {
		t.Fatal(err)
	}
	up := srv.took("POST /api/v2/media")[0]
	if !strings.HasPrefix(up.Header.Get("Content-Type"), "multipart/form-data") || !strings.Contains(up.Body, "movie") || !strings.Contains(up.Body, `filename="clip.mp4"`) {
		t.Fatalf("upload = %q (%s)", up.Body, up.Header.Get("Content-Type"))
	}
	if n := len(srv.took("GET /api/v1/media/m-1")); n != 2 {
		t.Fatalf("looked at the video %d times", n)
	}
	if ids := srv.took("POST /api/v1/statuses")[1].json()["media_ids"].([]any); len(ids) != 1 || ids[0] != "m-1" {
		t.Fatalf("media_ids = %v", ids)
	}
}

func TestMastodonNeedsItsServerAndTakesFourPhotosOrOneVideo(t *testing.T) {
	if _, err := postMastodon(context.Background(), postTo{Token: "t", Text: "x"}); err == nil {
		t.Error("a post with no server was made")
	}
	srv := mastodonServer(t)
	to := postTo{Token: "good-token", Text: "x", Sealed: map[string]string{serverSecret: srv.URL}}
	to.Media = []string{"https://c.example.com/1.png", "https://c.example.com/2.png", "https://c.example.com/3.png", "https://c.example.com/4.png", "https://c.example.com/5.png"}
	if _, err := postMastodon(context.Background(), to); err == nil {
		t.Error("five photos were posted")
	}
	to.Media = []string{"https://c.example.com/1.mp4", "https://c.example.com/2.mp4"}
	if _, err := postMastodon(context.Background(), to); err == nil {
		t.Error("two videos were posted")
	}
	if len(srv.keys()) != 0 {
		t.Errorf("the server was asked: %v", srv.keys())
	}
}
