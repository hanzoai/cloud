package provider

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

const goodAppPassword = "qrst-uvwx-1234-5678"

// pds is a stand-in Bluesky server.
func pds(t *testing.T) *stub {
	t.Helper()
	srv := newTLSStub(t, func(w http.ResponseWriter, r *http.Request, a asked) {
		switch a.Key {
		case "POST /xrpc/com.atproto.server.createSession":
			in := a.json()
			if in["password"] != goodAppPassword || (in["identifier"] != "acme.bsky.social" && in["identifier"] != "did:plc:abc123") {
				w.WriteHeader(http.StatusUnauthorized)
				say(w, `{"error":"AuthenticationRequired","message":"Invalid identifier or password"}`)
				return
			}
			say(w, `{"did":"did:plc:abc123","handle":"acme.bsky.social","accessJwt":"jwt-1"}`)
		case "POST /xrpc/com.atproto.repo.uploadBlob":
			say(w, `{"blob":{"$type":"blob","ref":{"$link":"bafy"},"mimeType":"image/png","size":4}}`)
		case "POST /xrpc/com.atproto.repo.createRecord":
			if r.Header.Get("Authorization") != "Bearer jwt-1" {
				w.WriteHeader(http.StatusUnauthorized)
				say(w, `{"error":"AuthenticationRequired","message":"no session"}`)
				return
			}
			say(w, `{"uri":"at://did:plc:abc123/app.bsky.feed.post/3kxyz","cid":"bafyrei"}`)
		default:
			refuse(w, 404, "no "+a.Key)
		}
	})
	open(t)
	swap(t, &blueskyClient, srv.Client())
	return srv
}

func TestBlueskyConnectsWithAnAppPasswordAndKeepsItsServer(t *testing.T) {
	srv := pds(t)
	res, err := blueskyVerify(context.Background(), VerifyInput{Fields: map[string]string{
		"identifier": "@acme.bsky.social", "password": goodAppPassword, "server": srv.URL,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExternalID != "did:plc:abc123" || res.Key != "did-plc-abc123" || res.AccountLabel != "@acme.bsky.social" {
		t.Fatalf("account = %+v", res)
	}
	if res.Tokens[accessSecret] != goodAppPassword || res.Tokens[serverSecret] != srv.URL {
		t.Fatalf("sealed = %v", res.Tokens)
	}
	if !validLabel(res.Key) {
		t.Fatalf("the key %q is not a valid connection key", res.Key)
	}
}

func TestBlueskyDoesNotTakeAWrongPasswordOrAnHTTPServer(t *testing.T) {
	srv := pds(t)
	var said verifyRefused
	_, err := blueskyVerify(context.Background(), VerifyInput{Fields: map[string]string{
		"identifier": "acme.bsky.social", "password": "wxyz-wxyz-wxyz-wxyz", "server": srv.URL,
	}})
	if !asRefused(err, &said) || !strings.Contains(string(said), "did not accept that handle") {
		t.Fatalf("a wrong password = %v", err)
	}
	if strings.Contains(string(said), "wxyz") {
		t.Fatalf("the refusal repeats the password: %q", said)
	}
	_, err = blueskyVerify(context.Background(), VerifyInput{Fields: map[string]string{
		"identifier": "acme.bsky.social", "password": goodAppPassword, "server": strings.Replace(srv.URL, "https", "http", 1),
	}})
	if !asRefused(err, &said) || !strings.Contains(string(said), "https") {
		t.Fatalf("an http server = %v", err)
	}
}

func TestBlueskyRefusesAnAccountPasswordAndSaysWhy(t *testing.T) {
	srv := pds(t)
	_, err := blueskyVerify(context.Background(), VerifyInput{Fields: map[string]string{"identifier": "acme.bsky.social", "password": "my real password"}})
	var said verifyRefused
	if err == nil || !asRefused(err, &said) || !strings.Contains(string(said), "not an app password") {
		t.Fatalf("err = %v", err)
	}
	_, err = blueskyVerify(context.Background(), VerifyInput{Fields: map[string]string{"identifier": "", "password": goodAppPassword}})
	if err == nil || !asRefused(err, &said) {
		t.Fatalf("an empty handle = %v", err)
	}
	if len(srv.keys()) != 0 {
		t.Fatalf("Bluesky was asked: %v", srv.keys())
	}
}

func TestAServerIsAnAddressAlone(t *testing.T) {
	for in, want := range map[string]string{
		"":                         "https://bsky.social",
		"example.social":           "https://example.social",
		"https://Example.Social/":  "https://example.social",
		"https://pds.example:2583": "https://pds.example:2583",
	} {
		got, err := serverOf(in, "https://bsky.social")
		if err != nil || got != want {
			t.Errorf("serverOf(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"http://example.social", "https://example.social/path", "https://user@example.social", "https://example.social?x=1", "ftp://example.social"} {
		if _, err := serverOf(bad, "https://bsky.social"); err == nil {
			t.Errorf("serverOf(%q) was accepted", bad)
		}
	}
	if _, err := serverOf("", ""); err == nil {
		t.Error("an empty server with no default was accepted")
	}
}

func TestBlueskyPostsWithLinksTagsAndPhotos(t *testing.T) {
	srv := pds(t)
	media := newStub(t, func(w http.ResponseWriter, _ *http.Request, _ asked) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("png!"))
	})
	swap(t, &mediaClient, http.DefaultClient)
	text := "Shipping 🚀 today: https://hanzo.ai/launch. #zen models"
	link, err := postBluesky(context.Background(), postTo{
		Token: goodAppPassword, Account: "did:plc:abc123", Handle: "@acme.bsky.social", Text: text,
		Media: []string{media.URL + "/a.png"}, Sealed: map[string]string{serverSecret: srv.URL},
	})
	if err != nil || link != "https://bsky.app/profile/acme.bsky.social/post/3kxyz" {
		t.Fatalf("link=%q err=%v", link, err)
	}
	rec := srv.took("POST /xrpc/com.atproto.repo.createRecord")[0].json()
	record := rec["record"].(map[string]any)
	if rec["repo"] != "did:plc:abc123" || rec["collection"] != "app.bsky.feed.post" || record["$type"] != "app.bsky.feed.post" || record["text"] != text {
		t.Fatalf("record = %v", rec)
	}
	facets := record["facets"].([]any)
	if len(facets) != 2 {
		t.Fatalf("facets = %v", facets)
	}
	// A facet is a byte range of the text, and the text holds a four-byte emoji.
	link0 := facets[0].(map[string]any)
	idx := link0["index"].(map[string]any)
	start, end := int(idx["byteStart"].(float64)), int(idx["byteEnd"].(float64))
	if text[start:end] != "https://hanzo.ai/launch" || link0["features"].([]any)[0].(map[string]any)["uri"] != "https://hanzo.ai/launch" {
		t.Fatalf("link facet = %v covers %q", link0, text[start:end])
	}
	tag := facets[1].(map[string]any)
	idx = tag["index"].(map[string]any)
	start, end = int(idx["byteStart"].(float64)), int(idx["byteEnd"].(float64))
	if text[start:end] != "#zen" || tag["features"].([]any)[0].(map[string]any)["tag"] != "zen" {
		t.Fatalf("tag facet = %v covers %q", tag, text[start:end])
	}
	embed := record["embed"].(map[string]any)
	if embed["$type"] != "app.bsky.embed.images" || len(embed["images"].([]any)) != 1 {
		t.Fatalf("embed = %v", embed)
	}
	up := srv.took("POST /xrpc/com.atproto.repo.uploadBlob")[0]
	if up.Body != "png!" || up.Header.Get("Content-Type") != "image/png" || up.Header.Get("Authorization") != "Bearer jwt-1" {
		t.Fatalf("upload = %q %v", up.Body, up.Header)
	}
}

// A handle a person changed no longer names the account; the DID always does.
func TestBlueskySignsInByDIDWhenTheHandleNoLongerWorks(t *testing.T) {
	srv := pds(t)
	link, err := postBluesky(context.Background(), postTo{
		Token: goodAppPassword, Account: "did:plc:abc123", Handle: "@old.handle.example", Text: "hi",
		Sealed: map[string]string{serverSecret: srv.URL},
	})
	if err != nil || link == "" {
		t.Fatalf("link=%q err=%v", link, err)
	}
	if n := len(srv.took("POST /xrpc/com.atproto.server.createSession")); n != 2 {
		t.Fatalf("sessions asked = %d", n)
	}
}

func TestBlueskyTakesFourPhotosAndNoVideo(t *testing.T) {
	srv := pds(t)
	to := postTo{Token: goodAppPassword, Account: "did:plc:abc123", Handle: "@acme.bsky.social", Text: "x", Sealed: map[string]string{serverSecret: srv.URL}}
	to.Media = []string{"https://c.example.com/a.mp4"}
	if _, err := postBluesky(context.Background(), to); err == nil {
		t.Error("a video was posted")
	}
	to.Media = []string{"https://c.example.com/1.png", "https://c.example.com/2.png", "https://c.example.com/3.png", "https://c.example.com/4.png", "https://c.example.com/5.png"}
	if _, err := postBluesky(context.Background(), to); err == nil {
		t.Error("five photos were posted")
	}
	if len(srv.keys()) != 0 {
		t.Errorf("Bluesky was asked before the post was judged: %v", srv.keys())
	}
}

func TestBlueskyFacetsAreByteRanges(t *testing.T) {
	text := "é #tag1 and http://x.example/a?b=1, (https://y.example/z) #1234 #_"
	var covered []string
	for _, f := range blueskyFacets(text) {
		idx := f["index"].(map[string]int)
		covered = append(covered, text[idx["byteStart"]:idx["byteEnd"]])
	}
	want := []string{"http://x.example/a?b=1", "https://y.example/z", "#tag1", "#_"}
	if strings.Join(covered, "|") != strings.Join(want, "|") {
		t.Fatalf("covered %q, want %q", covered, want)
	}
}

func asRefused(err error, out *verifyRefused) bool {
	v, ok := err.(verifyRefused)
	if ok {
		*out = v
	}
	return ok
}
