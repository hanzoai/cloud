package provider

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestRedditAccountsAreTheProfileAndTheCommunitiesTheMemberModerates(t *testing.T) {
	srv := newStub(t, func(w http.ResponseWriter, _ *http.Request, a asked) {
		if a.Key != "GET /subreddits/mine/moderator" {
			refuse(w, 404, "no")
			return
		}
		say(w, `{"data":{"children":[{"data":{"display_name":"hanzo"}},{"data":{"display_name":"zen_models"}},{"data":{"display_name":""}}]}}`)
	})
	swap(t, &redditSocialAPI, srv.URL)
	grants, err := expandReddit(context.Background(), OAuthConfig{}, "tok", identity{ExternalID: "t2_abc", Label: "u/acme"})
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) != 3 {
		t.Fatalf("grants = %+v", grants)
	}
	me, hanzo := grants[0], grants[1]
	if me.ExternalID != "u_acme" || grantKey(me) != "u-acme" || me.Kind != "personal" || me.AccountLabel != "u/acme" {
		t.Fatalf("profile = %+v", me)
	}
	if hanzo.ExternalID != "hanzo" || grantKey(hanzo) != "r-hanzo" || hanzo.Kind != "page" || hanzo.Token != "tok" {
		t.Fatalf("community = %+v", hanzo)
	}
	h := srv.took("GET /subreddits/mine/moderator")[0].Header
	if h.Get("Authorization") != "bearer tok" || !strings.Contains(h.Get("User-Agent"), "hanzo") {
		t.Fatalf("headers = %v", h)
	}
}

// The communities are a courtesy: a refusal still leaves the profile.
func TestRedditKeepsTheProfileWhenTheCommunitiesRefuse(t *testing.T) {
	srv := newStub(t, func(w http.ResponseWriter, _ *http.Request, _ asked) { refuse(w, 403, "no") })
	swap(t, &redditSocialAPI, srv.URL)
	grants, err := expandReddit(context.Background(), OAuthConfig{}, "tok", identity{Label: "u/acme"})
	if err != nil || len(grants) != 1 {
		t.Fatalf("grants=%+v err=%v", grants, err)
	}
}

func TestRedditTitleIsTheFirstLineAndTheBodyTheRest(t *testing.T) {
	title, body := redditSplit("Shipping today\n\nThe whole story.")
	if title != "Shipping today" || body != "The whole story." {
		t.Fatalf("%q / %q", title, body)
	}
	long := strings.Repeat("w", 400)
	title, body = redditSplit(long)
	if len([]rune(title)) != redditTitleMax || !strings.HasSuffix(title, "…") || body != long {
		t.Fatalf("a long first line: title %d runes, body %d", len([]rune(title)), len(body))
	}
}

func TestRedditSubmitsASelfPostALinkAndSaysWhyItWasRefused(t *testing.T) {
	srv := newStub(t, func(w http.ResponseWriter, _ *http.Request, a asked) {
		if a.Key != "POST /api/submit" {
			refuse(w, 404, "no")
			return
		}
		switch a.form().Get("sr") {
		case "banned":
			say(w, `{"json":{"errors":[["SUBREDDIT_NOTALLOWED","you aren't allowed to post there.","sr"]]}}`)
		case "slow":
			say(w, `{"json":{"errors":[["RATELIMIT","you are doing that too much. try again in 9 minutes.","ratelimit"]]}}`)
		default:
			say(w, `{"json":{"errors":[],"data":{"url":"https://www.reddit.com/r/hanzo/comments/abc/shipping_today/"}}}`)
		}
	})
	swap(t, &redditSocialAPI, srv.URL)
	quick(t)
	ctx := context.Background()

	link, err := postReddit(ctx, postTo{Token: "tok", Account: "hanzo", Text: "Shipping today\nDetails here."})
	if err != nil || link != "https://www.reddit.com/r/hanzo/comments/abc/shipping_today/" {
		t.Fatalf("self = %q %v", link, err)
	}
	f := srv.took("POST /api/submit")[0].form()
	if f.Get("kind") != "self" || f.Get("title") != "Shipping today" || f.Get("text") != "Details here." || f.Get("sr") != "hanzo" || f.Get("api_type") != "json" {
		t.Fatalf("submit = %v", f)
	}

	if _, err := postReddit(ctx, postTo{Token: "tok", Account: "hanzo", Text: "Look", Media: []string{"https://cdn.example.com/a.png"}}); err != nil {
		t.Fatal(err)
	}
	if f := srv.took("POST /api/submit")[1].form(); f.Get("kind") != "link" || f.Get("url") != "https://cdn.example.com/a.png" || f.Get("text") != "" {
		t.Fatalf("link = %v", f)
	}

	_, err = postReddit(ctx, postTo{Token: "tok", Account: "banned", Text: "x"})
	if err == nil || !strings.Contains(err.Error(), "SUBREDDIT_NOTALLOWED") || againPost(err) {
		t.Fatalf("a refusal = %v", err)
	}
	_, err = postReddit(ctx, postTo{Token: "tok", Account: "slow", Text: "x"})
	if err == nil || !strings.Contains(err.Error(), "9 minutes") || !againPost(err) {
		t.Fatalf("a rate limit = %v", err)
	}
	if _, err := postReddit(ctx, postTo{Token: "tok", Account: "hanzo", Text: "x", Media: []string{"https://cdn.example.com/a.png", "https://cdn.example.com/b.png"}}); err == nil {
		t.Fatal("two links were submitted")
	}
}

func TestPinterestAccountsAreTheBoards(t *testing.T) {
	srv := newStub(t, func(w http.ResponseWriter, _ *http.Request, a asked) {
		if a.Key != "GET /v5/boards" {
			refuse(w, 404, "no")
			return
		}
		if a.Query.Get("bookmark") == "" {
			say(w, `{"items":[{"id":"111","name":"Launches"}],"bookmark":"next"}`)
			return
		}
		say(w, `{"items":[{"id":"222","name":"Team"},{"id":"bad/id","name":"Bad"}]}`)
	})
	swap(t, &pinterestAPI, srv.URL)
	grants, err := expandPinterest(context.Background(), OAuthConfig{}, "tok", identity{ExternalID: "acme", Label: "acme"})
	if err != nil || len(grants) != 2 {
		t.Fatalf("grants=%+v err=%v", grants, err)
	}
	if grants[0].ExternalID != "111" || grants[0].AccountLabel != "acme / Launches" || grantKey(grants[0]) != "111" || grants[1].ExternalID != "222" {
		t.Fatalf("grants = %+v", grants)
	}
}

func TestAMemberWithNoBoardHasNothingToPinTo(t *testing.T) {
	srv := newStub(t, func(w http.ResponseWriter, _ *http.Request, _ asked) { say(w, `{"items":[]}`) })
	swap(t, &pinterestAPI, srv.URL)
	if _, err := expandPinterest(context.Background(), OAuthConfig{}, "tok", identity{}); err == nil {
		t.Fatal("an account with no board was connected")
	}
}

func TestPinterestPinsAPhotoOrACarouselToTheBoard(t *testing.T) {
	srv := newStub(t, func(w http.ResponseWriter, _ *http.Request, a asked) {
		if a.Key != "POST /v5/pins" {
			refuse(w, 404, "no")
			return
		}
		say(w, `{"id":"9001"}`)
	})
	swap(t, &pinterestAPI, srv.URL)
	ctx := context.Background()

	link, err := postPinterest(ctx, postTo{Token: "tok", Account: "111", Text: "Our launch\nRead it at https://hanzo.ai/launch.", Media: []string{"https://cdn.example.com/a.png"}})
	if err != nil || link != "https://www.pinterest.com/pin/9001/" {
		t.Fatalf("pin = %q %v", link, err)
	}
	pin := srv.took("POST /v5/pins")[0].json()
	src := pin["media_source"].(map[string]any)
	if pin["board_id"] != "111" || pin["title"] != "Our launch" || pin["link"] != "https://hanzo.ai/launch" || src["source_type"] != "image_url" || src["url"] != "https://cdn.example.com/a.png" {
		t.Fatalf("pin = %v", pin)
	}

	if _, err := postPinterest(ctx, postTo{Token: "tok", Account: "111", Text: "Set", Media: []string{"https://cdn.example.com/a.png", "https://cdn.example.com/b.png"}}); err != nil {
		t.Fatal(err)
	}
	src = srv.took("POST /v5/pins")[1].json()["media_source"].(map[string]any)
	if src["source_type"] != "multiple_image_urls" || len(src["items"].([]any)) != 2 {
		t.Fatalf("carousel = %v", src)
	}

	for name, media := range map[string][]string{
		"no photo": nil,
		"a video":  {"https://cdn.example.com/a.mp4"},
		"six":      {"https://c.example.com/1.png", "https://c.example.com/2.png", "https://c.example.com/3.png", "https://c.example.com/4.png", "https://c.example.com/5.png", "https://c.example.com/6.png"},
	} {
		if _, err := postPinterest(ctx, postTo{Token: "tok", Account: "111", Text: "x", Media: media}); err == nil {
			t.Errorf("%s was pinned", name)
		}
	}
}
