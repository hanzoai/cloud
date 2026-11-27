package provider

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

const (
	hookID    = "112233445566778899"
	hookToken = "abcdefghijklmnopqrstuvwxyz0123456789_-ABCD"
)

func discordServer(t *testing.T) *stub {
	t.Helper()
	srv := newStub(t, func(w http.ResponseWriter, _ *http.Request, a asked) {
		switch a.Key {
		case "GET /api/webhooks/" + hookID + "/" + hookToken:
			say(w, `{"id":"`+hookID+`","name":"Launch bot","channel_id":"555","guild_id":"444"}`)
		case "POST /api/webhooks/" + hookID + "/" + hookToken:
			say(w, `{"id":"9001","channel_id":"555"}`)
		default:
			refuse(w, 404, "Unknown Webhook")
		}
	})
	swap(t, &discordBase, srv.URL)
	return srv
}

func TestDiscordConnectsAChannelByItsWebhook(t *testing.T) {
	discordServer(t)
	for _, addr := range []string{
		"https://discord.com/api/webhooks/" + hookID + "/" + hookToken,
		"https://canary.discord.com/api/v10/webhooks/" + hookID + "/" + hookToken,
		"https://discordapp.com/api/webhooks/" + hookID + "/" + hookToken + "?wait=true",
	} {
		res, err := discordVerify(context.Background(), VerifyInput{Fields: map[string]string{"webhook": addr}})
		if err != nil {
			t.Fatalf("%s: %v", addr, err)
		}
		if res.ExternalID != hookID || res.Key != hookID || res.AccountLabel != "Launch bot" ||
			res.Tokens[accessSecret] != "https://discord.com/api/webhooks/"+hookID+"/"+hookToken || res.Tokens[linkSecret] != "444/555" {
			t.Fatalf("account = %+v", res)
		}
	}
}

func TestDiscordTakesOnlyDiscordsOwnWebhooks(t *testing.T) {
	srv := discordServer(t)
	var said verifyRefused
	for _, bad := range []string{
		"",
		"https://evil.example/api/webhooks/" + hookID + "/" + hookToken,
		"http://discord.com/api/webhooks/" + hookID + "/" + hookToken,
		"https://discord.com.evil.example/api/webhooks/" + hookID + "/" + hookToken,
		"https://user@discord.com/api/webhooks/" + hookID + "/" + hookToken,
		"https://discord.com/api/webhooks/" + hookID,
		"https://discord.com/api/channels/" + hookID + "/" + hookToken,
	} {
		_, err := discordVerify(context.Background(), VerifyInput{Fields: map[string]string{"webhook": bad}})
		if !asRefused(err, &said) {
			t.Errorf("%q was taken: %v", bad, err)
		}
	}
	if len(srv.keys()) != 0 {
		t.Errorf("a webhook that is not Discord's was called: %v", srv.keys())
	}
	// A webhook Discord does not know is refused without repeating its secret.
	_, err := discordVerify(context.Background(), VerifyInput{Fields: map[string]string{"webhook": "https://discord.com/api/webhooks/999999/" + hookToken}})
	if !asRefused(err, &said) || strings.Contains(string(said), hookToken) {
		t.Fatalf("err = %v", err)
	}
}

func TestDiscordPostsWordsPhotosAndAVideoWithoutPingingAnyone(t *testing.T) {
	srv := discordServer(t)
	to := postTo{
		Token: "https://discord.com/api/webhooks/" + hookID + "/" + hookToken, Account: hookID,
		Text: "Launch day @everyone", Sealed: map[string]string{linkSecret: "444/555"},
	}
	link, err := postDiscord(context.Background(), to)
	if err != nil || link != "https://discord.com/channels/444/555/9001" {
		t.Fatalf("link=%q err=%v", link, err)
	}
	post := srv.took("POST /api/webhooks/" + hookID + "/" + hookToken)[0]
	body := post.json()
	if post.Query.Get("wait") != "true" || body["content"] != "Launch day @everyone" {
		t.Fatalf("post = %v %v", post.Query, body)
	}
	if parse := body["allowed_mentions"].(map[string]any)["parse"].([]any); len(parse) != 0 {
		t.Fatalf("a post may ping: %v", parse)
	}

	to.Media = []string{"https://c.example.com/a.png", "https://c.example.com/b.mp4"}
	if _, err := postDiscord(context.Background(), to); err != nil {
		t.Fatal(err)
	}
	body = srv.took("POST /api/webhooks/" + hookID + "/" + hookToken)[1].json()
	embeds := body["embeds"].([]any)
	if len(embeds) != 1 || embeds[0].(map[string]any)["image"].(map[string]any)["url"] != "https://c.example.com/a.png" {
		t.Fatalf("embeds = %v", embeds)
	}
	if !strings.HasSuffix(body["content"].(string), "\nhttps://c.example.com/b.mp4") {
		t.Fatalf("content = %q", body["content"])
	}
}

func TestDiscordRefusesAStoredAddressThatIsNotDiscords(t *testing.T) {
	discordServer(t)
	_, err := postDiscord(context.Background(), postTo{Token: "https://evil.example/api/webhooks/" + hookID + "/" + hookToken, Text: "x"})
	if err == nil || strings.Contains(err.Error(), hookToken) {
		t.Fatalf("err = %v", err)
	}
}
