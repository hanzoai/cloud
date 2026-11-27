package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// stub is a stand-in for one network: it answers by "METHOD /path", and keeps
// every request it took, in order, with the body.
type stub struct {
	*httptest.Server
	mu   sync.Mutex
	seen []asked
}

type asked struct {
	Key    string // "METHOD /path"
	Query  url.Values
	Header http.Header
	Body   string
}

func (a asked) form() url.Values {
	v, _ := url.ParseQuery(a.Body)
	return v
}

func (a asked) json() map[string]any {
	var m map[string]any
	_ = json.Unmarshal([]byte(a.Body), &m)
	return m
}

func newStub(t *testing.T, answer func(w http.ResponseWriter, r *http.Request, a asked)) *stub {
	t.Helper()
	return serve(t, httptest.NewServer, answer)
}

// newTLSStub is a stand-in a connector will reach, since a connector takes
// https servers only. Its Client trusts the stand-in's certificate.
func newTLSStub(t *testing.T, answer func(w http.ResponseWriter, r *http.Request, a asked)) *stub {
	t.Helper()
	return serve(t, httptest.NewTLSServer, answer)
}

func serve(t *testing.T, start func(http.Handler) *httptest.Server, answer func(w http.ResponseWriter, r *http.Request, a asked)) *stub {
	t.Helper()
	s := &stub{}
	s.Server = start(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		a := asked{Key: r.Method + " " + r.URL.Path, Query: r.URL.Query(), Header: r.Header.Clone(), Body: string(b)}
		s.mu.Lock()
		s.seen = append(s.seen, a)
		s.mu.Unlock()
		answer(w, r, a)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *stub) took(key string) []asked {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []asked
	for _, a := range s.seen {
		if a.Key == key {
			out = append(out, a)
		}
	}
	return out
}

func (s *stub) keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.seen))
	for i, a := range s.seen {
		out[i] = a.Key
	}
	return out
}

func say(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

func refuse(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"message":"` + msg + `"}`))
}

func TestInstagramAccountsAreTheLinkedProfessionalAccounts(t *testing.T) {
	srv := newStub(t, func(w http.ResponseWriter, r *http.Request, a asked) {
		switch a.Key {
		case "GET /oauth/access_token":
			say(w, `{"access_token":"long-user"}`)
		case "GET /me/accounts":
			say(w, `{"data":[
				{"id":"pg1","name":"Acme","access_token":"page-1","instagram_business_account":{"id":"ig-1","username":"acme"}},
				{"id":"pg2","name":"No Instagram","access_token":"page-2"},
				{"id":"pg3","name":"No token","instagram_business_account":{"id":"ig-3","username":"ghost"}}]}`)
		default:
			refuse(w, 404, "no")
		}
	})
	swap(t, &facebookGraph, srv.URL)
	grants, err := expandInstagram(context.Background(), OAuthConfig{ClientID: "id", ClientSecret: "sec"}, "short", identity{ExternalID: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) != 1 {
		t.Fatalf("grants = %+v", grants)
	}
	g := grants[0]
	if g.ExternalID != "ig-1" || g.AccountLabel != "@acme" || g.Token != "page-1" || g.Kind != "page" || g.ExpiresAt != 0 {
		t.Fatalf("grant = %+v", g)
	}
	if grantKey(g) != "ig-1" {
		t.Fatalf("key = %q", grantKey(g))
	}
	// The listing is asked with the long-lived token, in the header.
	if got := srv.took("GET /me/accounts")[0].Header.Get("Authorization"); got != "Bearer long-user" {
		t.Fatalf("listing was asked with %q", got)
	}
}

func TestAnInstagramConsentWithNoLinkedAccountSaysSo(t *testing.T) {
	srv := newStub(t, func(w http.ResponseWriter, _ *http.Request, a asked) {
		if a.Key == "GET /me/accounts" {
			say(w, `{"data":[{"id":"pg","name":"Acme","access_token":"t"}]}`)
			return
		}
		refuse(w, 400, "no")
	})
	swap(t, &facebookGraph, srv.URL)
	if _, err := expandInstagram(context.Background(), OAuthConfig{}, "short", identity{}); err == nil || !strings.Contains(err.Error(), "Instagram professional account") {
		t.Fatalf("err = %v", err)
	}
}

// instagramStub answers the container protocol: a container is made, looked at
// until it is finished, then published.
func instagramStub(t *testing.T, looks ...string) *stub {
	t.Helper()
	n := 0
	srv := newStub(t, func(w http.ResponseWriter, _ *http.Request, a asked) {
		switch {
		case a.Key == "POST /ig-1/media":
			n++
			say(w, `{"id":"c`+string(rune('0'+n))+`"}`)
		case strings.HasPrefix(a.Key, "GET /c"):
			code := "FINISHED"
			if len(looks) > 0 {
				code, looks = looks[0], looks[1:]
			}
			say(w, `{"status_code":"`+code+`"}`)
		case a.Key == "POST /ig-1/media_publish":
			say(w, `{"id":"m1"}`)
		case a.Key == "GET /m1":
			say(w, `{"permalink":"https://www.instagram.com/p/abc/"}`)
		default:
			refuse(w, 404, "no "+a.Key)
		}
	})
	swap(t, &graphAPI, srv.URL)
	quick(t)
	return srv
}

func TestInstagramPublishesAPhotoOnceTheContainerIsFinished(t *testing.T) {
	srv := instagramStub(t, "IN_PROGRESS", "FINISHED")
	link, err := postInstagram(context.Background(), postTo{
		Token: "page-1", Account: "ig-1", Text: "Hello", Media: []string{"https://cdn.example.com/a.jpg"},
	})
	if err != nil || link != "https://www.instagram.com/p/abc/" {
		t.Fatalf("link=%q err=%v", link, err)
	}
	made := srv.took("POST /ig-1/media")[0]
	if f := made.form(); f.Get("image_url") != "https://cdn.example.com/a.jpg" || f.Get("caption") != "Hello" || f.Get("media_type") != "" {
		t.Fatalf("container = %v", f)
	}
	if made.Header.Get("Authorization") != "Bearer page-1" {
		t.Fatalf("the container was made with %q", made.Header.Get("Authorization"))
	}
	if f := srv.took("POST /ig-1/media_publish")[0].form(); f.Get("creation_id") != "c1" {
		t.Fatalf("published %v", f)
	}
}

func TestInstagramPublishesAVideoAsAReel(t *testing.T) {
	srv := instagramStub(t)
	if _, err := postInstagram(context.Background(), postTo{Token: "t", Account: "ig-1", Text: "Watch", Media: []string{"https://cdn.example.com/a.mp4"}}); err != nil {
		t.Fatal(err)
	}
	if f := srv.took("POST /ig-1/media")[0].form(); f.Get("media_type") != "REELS" || f.Get("video_url") != "https://cdn.example.com/a.mp4" {
		t.Fatalf("container = %v", f)
	}
}

func TestInstagramPublishesACarouselOfItsItems(t *testing.T) {
	srv := instagramStub(t)
	_, err := postInstagram(context.Background(), postTo{Token: "t", Account: "ig-1", Text: "Two", Media: []string{
		"https://cdn.example.com/a.jpg", "https://cdn.example.com/b.mp4",
	}})
	if err != nil {
		t.Fatal(err)
	}
	made := srv.took("POST /ig-1/media")
	if len(made) != 3 {
		t.Fatalf("containers = %d", len(made))
	}
	if f := made[0].form(); f.Get("is_carousel_item") != "true" || f.Get("image_url") == "" {
		t.Fatalf("first item = %v", f)
	}
	if f := made[1].form(); f.Get("is_carousel_item") != "true" || f.Get("media_type") != "VIDEO" || f.Get("video_url") == "" {
		t.Fatalf("second item = %v", f)
	}
	if f := made[2].form(); f.Get("media_type") != "CAROUSEL" || f.Get("children") != "c1,c2" || f.Get("caption") != "Two" {
		t.Fatalf("carousel = %v", f)
	}
}

func TestInstagramHasNoTextPostsAndNamesAFailedContainer(t *testing.T) {
	instagramStub(t, "ERROR")
	if _, err := postInstagram(context.Background(), postTo{Token: "t", Account: "ig-1", Text: "words only"}); err == nil || !strings.Contains(err.Error(), "needs a photo or a video") {
		t.Fatalf("a text post = %v", err)
	}
	if _, err := postInstagram(context.Background(), postTo{Token: "t", Account: "ig-1", Text: "x", Media: []string{"https://cdn.example.com/a.jpg"}}); err == nil || !strings.Contains(err.Error(), "could not process the media (error)") {
		t.Fatalf("a failed container = %v", err)
	}
}

func TestFacebookPostsPhotosAndVideo(t *testing.T) {
	srv := newStub(t, func(w http.ResponseWriter, _ *http.Request, a asked) {
		switch a.Key {
		case "POST /pg1/photos":
			if a.form().Get("published") == "false" {
				u := a.form().Get("url")
				say(w, `{"id":"ph`+u[len(u)-5:len(u)-4]+`"}`)
				return
			}
			say(w, `{"id":"ph0","post_id":"pg1_77"}`)
		case "POST /pg1/feed":
			say(w, `{"id":"pg1_88"}`)
		case "POST /pg1/videos":
			say(w, `{"id":"vid1"}`)
		default:
			refuse(w, 404, "no")
		}
	})
	swap(t, &graphAPI, srv.URL)
	ctx := context.Background()

	link, err := postFacebook(ctx, postTo{Token: "pt", Account: "pg1", Text: "One", Media: []string{"https://cdn.example.com/a.png"}})
	if err != nil || link != "https://www.facebook.com/pg1_77" {
		t.Fatalf("one photo = %q %v", link, err)
	}
	if f := srv.took("POST /pg1/photos")[0].form(); f.Get("url") != "https://cdn.example.com/a.png" || f.Get("caption") != "One" || f.Get("published") != "true" {
		t.Fatalf("photo = %v", f)
	}

	link, err = postFacebook(ctx, postTo{Token: "pt", Account: "pg1", Text: "Two", Media: []string{"https://cdn.example.com/a.png", "https://cdn.example.com/b.png"}})
	if err != nil || link != "https://www.facebook.com/pg1_88" {
		t.Fatalf("two photos = %q %v", link, err)
	}
	feed := srv.took("POST /pg1/feed")[0].form()
	if feed.Get("message") != "Two" || feed.Get("attached_media[0]") != `{"media_fbid":"pha"}` || feed.Get("attached_media[1]") != `{"media_fbid":"phb"}` {
		t.Fatalf("feed = %v", feed)
	}

	link, err = postFacebook(ctx, postTo{Token: "pt", Account: "pg1", Text: "Watch", Media: []string{"https://cdn.example.com/a.mp4"}})
	if err != nil || link != "https://www.facebook.com/watch/?v=vid1" {
		t.Fatalf("video = %q %v", link, err)
	}
	if f := srv.took("POST /pg1/videos")[0].form(); f.Get("file_url") != "https://cdn.example.com/a.mp4" || f.Get("description") != "Watch" {
		t.Fatalf("video = %v", f)
	}

	if _, err := postFacebook(ctx, postTo{Token: "pt", Account: "pg1", Text: "x", Media: []string{"https://cdn.example.com/a.mp4", "https://cdn.example.com/a.png"}}); err == nil {
		t.Fatal("a video beside a photo was posted")
	}
}

// ---- Threads ----

func threadsServer(t *testing.T, answer func(w http.ResponseWriter, r *http.Request, a asked)) *stub {
	t.Helper()
	srv := newStub(t, answer)
	swap(t, &threadsGraph, srv.URL)
	swap(t, &threadsAPI, srv.URL+"/v1.0")
	quick(t)
	return srv
}

func TestThreadsTradesTheConsentTokenForOneThatLastsAndRenewsIt(t *testing.T) {
	srv := threadsServer(t, func(w http.ResponseWriter, _ *http.Request, a asked) {
		switch a.Key {
		case "GET /access_token":
			say(w, `{"access_token":"long-1","token_type":"bearer","expires_in":5184000}`)
		case "GET /refresh_access_token":
			say(w, `{"access_token":"long-2","expires_in":5184000}`)
		default:
			refuse(w, 404, "no")
		}
	})
	grants, err := expandThreads(context.Background(), OAuthConfig{ClientID: "id", ClientSecret: "sec"}, "short", identity{ExternalID: "u1", Label: "acme"})
	if err != nil || len(grants) != 1 {
		t.Fatalf("grants=%+v err=%v", grants, err)
	}
	g := grants[0]
	if g.Token != "long-1" || g.Refresh != "long-1" || g.ExpiresAt == 0 || g.AccountLabel != "@acme" || g.ExternalID != "u1" {
		t.Fatalf("grant = %+v", g)
	}
	q := srv.took("GET /access_token")[0].Query
	if q.Get("grant_type") != "th_exchange_token" || q.Get("client_secret") != "sec" || q.Get("access_token") != "short" {
		t.Fatalf("exchange = %v", q)
	}

	res, err := threadsRefresh(context.Background(), OAuthConfig{}, "long-1")
	if err != nil || res.Tokens[accessSecret] != "long-2" || res.Tokens[refreshSecret] != "long-2" || res.ExpiresAt == 0 {
		t.Fatalf("refresh = %+v err=%v", res, err)
	}
	if q := srv.took("GET /refresh_access_token")[0].Query; q.Get("grant_type") != "th_refresh_token" || q.Get("access_token") != "long-1" {
		t.Fatalf("refresh asked %v", q)
	}
}

func TestThreadsWithoutASecretCannotTradeTheToken(t *testing.T) {
	if _, err := expandThreads(context.Background(), OAuthConfig{ClientID: "id"}, "short", identity{ExternalID: "u1"}); err == nil {
		t.Fatal("an hour-long token was kept")
	}
}

func TestThreadsPostsWordsAPhotoAndACarousel(t *testing.T) {
	n := 0
	srv := threadsServer(t, func(w http.ResponseWriter, _ *http.Request, a asked) {
		switch {
		case a.Key == "POST /v1.0/u1/threads":
			n++
			say(w, `{"id":"c`+string(rune('0'+n))+`"}`)
		case strings.HasPrefix(a.Key, "GET /v1.0/c"):
			say(w, `{"status":"FINISHED"}`)
		case a.Key == "POST /v1.0/u1/threads_publish":
			say(w, `{"id":"m1"}`)
		case a.Key == "GET /v1.0/m1":
			say(w, `{"permalink":"https://www.threads.net/@acme/post/abc"}`)
		default:
			refuse(w, 404, "no "+a.Key)
		}
	})
	ctx := context.Background()

	link, err := postThreads(ctx, postTo{Token: "tok", Account: "u1", Text: "Words"})
	if err != nil || link != "https://www.threads.net/@acme/post/abc" {
		t.Fatalf("text = %q %v", link, err)
	}
	c := srv.took("POST /v1.0/u1/threads")[0].form()
	if c.Get("media_type") != "TEXT" || c.Get("text") != "Words" || c.Get("access_token") != "tok" {
		t.Fatalf("container = %v", c)
	}
	// The credential rides the body, never the address of a request.
	if q := srv.took("POST /v1.0/u1/threads")[0].Query; q.Get("access_token") != "" {
		t.Fatalf("the credential is in the address: %v", q)
	}

	if _, err := postThreads(ctx, postTo{Token: "tok", Account: "u1", Text: "Pic", Media: []string{"https://cdn.example.com/a.png"}}); err != nil {
		t.Fatal(err)
	}
	if c := srv.took("POST /v1.0/u1/threads")[1].form(); c.Get("media_type") != "IMAGE" || c.Get("image_url") != "https://cdn.example.com/a.png" {
		t.Fatalf("image = %v", c)
	}

	if _, err := postThreads(ctx, postTo{Token: "tok", Account: "u1", Text: "Two", Media: []string{"https://cdn.example.com/a.png", "https://cdn.example.com/b.mp4"}}); err != nil {
		t.Fatal(err)
	}
	all := srv.took("POST /v1.0/u1/threads")
	if len(all) != 5 {
		t.Fatalf("containers = %d", len(all))
	}
	if f := all[2].form(); f.Get("is_carousel_item") != "true" || f.Get("media_type") != "IMAGE" {
		t.Fatalf("item 1 = %v", f)
	}
	if f := all[3].form(); f.Get("is_carousel_item") != "true" || f.Get("media_type") != "VIDEO" {
		t.Fatalf("item 2 = %v", f)
	}
	if f := all[4].form(); f.Get("media_type") != "CAROUSEL" || f.Get("children") != "c3,c4" {
		t.Fatalf("carousel = %v", f)
	}
}
