package provider

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// botAPI is a stand-in Telegram. The bot token is in every address, and the
// stand-in answers only the token it knows.
func botAPI(t *testing.T, member string) *stub {
	t.Helper()
	srv := newStub(t, func(w http.ResponseWriter, _ *http.Request, a asked) {
		if !strings.HasPrefix(a.Key, "POST /bot123:good/") {
			w.WriteHeader(http.StatusUnauthorized)
			say(w, `{"ok":false,"error_code":401,"description":"Unauthorized"}`)
			return
		}
		switch strings.TrimPrefix(a.Key, "POST /bot123:good/") {
		case "getMe":
			say(w, `{"ok":true,"result":{"id":777,"is_bot":true,"username":"acme_bot"}}`)
		case "getChat":
			if a.json()["chat_id"] == "@missing" {
				w.WriteHeader(http.StatusBadRequest)
				say(w, `{"ok":false,"description":"Bad Request: chat not found"}`)
				return
			}
			say(w, `{"ok":true,"result":{"id":-1001234567890,"type":"channel","title":"Acme News","username":"acme_news"}}`)
		case "getChatMember":
			say(w, `{"ok":true,"result":`+member+`}`)
		case "sendMessage", "sendPhoto", "sendVideo":
			say(w, `{"ok":true,"result":{"message_id":42}}`)
		case "sendMediaGroup":
			say(w, `{"ok":true,"result":[{"message_id":50},{"message_id":51}]}`)
		default:
			refuse(w, 404, "no")
		}
	})
	swap(t, &telegramAPIBase, srv.URL)
	return srv
}

func TestTelegramConnectsAChannelTheBotAdministers(t *testing.T) {
	srv := botAPI(t, `{"status":"administrator","can_post_messages":true}`)
	res, err := telegramVerify(context.Background(), VerifyInput{Fields: map[string]string{"token": "123:good", "chat": "acme_news"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExternalID != "-1001234567890" || res.Key != "-1001234567890" || res.AccountLabel != "@acme_news" || res.Tokens[accessSecret] != "123:good" || !validLabel(res.Key) {
		t.Fatalf("account = %+v", res)
	}
	// A bare name is a channel's name.
	if got := srv.took("POST /bot123:good/getChat")[0].json()["chat_id"]; got != "@acme_news" {
		t.Fatalf("asked for %v", got)
	}
}

func TestTelegramRefusesABotThatCannotPostAndSaysWhatToDo(t *testing.T) {
	for member, want := range map[string]string{
		`{"status":"member"}`: "administrator",
		`{"status":"administrator","can_post_messages":false}`: "permission to post",
		`{"status":"left"}`: "administrator",
	} {
		botAPI(t, member)
		var said verifyRefused
		_, err := telegramVerify(context.Background(), VerifyInput{Fields: map[string]string{"token": "123:good", "chat": "@acme_news"}})
		if !asRefused(err, &said) || !strings.Contains(string(said), want) {
			t.Errorf("%s: err = %v", member, err)
		}
	}
	botAPI(t, `{"status":"creator"}`)
	var said verifyRefused
	for _, in := range []map[string]string{
		{"token": "123:bad", "chat": "@acme_news"},
		{"token": "123:good", "chat": "@missing"},
		{"token": "123:good"},
	} {
		_, err := telegramVerify(context.Background(), VerifyInput{Fields: in})
		if !asRefused(err, &said) || strings.Contains(string(said), "123:") {
			t.Errorf("%v: err = %v", in, err)
		}
	}
}

// The token is in every address, so a failure never reports one.
func TestATelegramFailureNeverReportsTheToken(t *testing.T) {
	srv := botAPI(t, `{}`)
	addr := srv.URL
	srv.Close()
	swap(t, &telegramAPIBase, addr)
	err := tgCall(context.Background(), "123:SECRETTOKEN", "getMe", map[string]any{}, nil)
	if err == nil || strings.Contains(err.Error(), "SECRETTOKEN") {
		t.Fatalf("err = %v", err)
	}
}

func TestTelegramPostsWordsPhotosAndAlbums(t *testing.T) {
	srv := botAPI(t, `{}`)
	ctx := context.Background()
	to := postTo{Token: "123:good", Account: "-1001234567890", Handle: "@acme_news", Text: "Hello channel"}
	const sent = "POST /bot123:good/"

	link, err := postTelegram(ctx, to)
	if err != nil || link != "https://t.me/acme_news/42" {
		t.Fatalf("text = %q %v", link, err)
	}
	if m := srv.took(sent + "sendMessage")[0].json(); m["chat_id"] != "-1001234567890" || m["text"] != "Hello channel" {
		t.Fatalf("message = %v", m)
	}

	to.Media = []string{"https://c.example.com/a.png"}
	if _, err := postTelegram(ctx, to); err != nil {
		t.Fatal(err)
	}
	if m := srv.took(sent + "sendPhoto")[0].json(); m["photo"] != "https://c.example.com/a.png" || m["caption"] != "Hello channel" {
		t.Fatalf("photo = %v", m)
	}

	to.Media = []string{"https://c.example.com/a.mp4"}
	if _, err := postTelegram(ctx, to); err != nil {
		t.Fatal(err)
	}
	if m := srv.took(sent + "sendVideo")[0].json(); m["video"] != "https://c.example.com/a.mp4" {
		t.Fatalf("video = %v", m)
	}

	to.Media = []string{"https://c.example.com/a.png", "https://c.example.com/b.mp4"}
	link, err = postTelegram(ctx, to)
	if err != nil || link != "https://t.me/acme_news/50" {
		t.Fatalf("album = %q %v", link, err)
	}
	group := srv.took(sent + "sendMediaGroup")[0].json()["media"].([]any)
	first, second := group[0].(map[string]any), group[1].(map[string]any)
	if first["type"] != "photo" || first["caption"] != "Hello channel" || second["type"] != "video" || second["caption"] != nil {
		t.Fatalf("album = %v", group)
	}
}

// Words too long for a caption follow the media as a message of their own.
func TestTelegramSendsLongWordsAfterThePhoto(t *testing.T) {
	srv := botAPI(t, `{}`)
	long := strings.Repeat("w", telegramCaption+1)
	link, err := postTelegram(context.Background(), postTo{Token: "123:good", Account: "-1001234567890", Handle: "@acme_news", Text: long, Media: []string{"https://c.example.com/a.png"}})
	if err != nil || link != "https://t.me/acme_news/42" {
		t.Fatalf("link=%q err=%v", link, err)
	}
	if m := srv.took("POST /bot123:good/sendPhoto")[0].json(); m["caption"] != "" {
		t.Fatalf("photo caption = %v", m["caption"])
	}
	if m := srv.took("POST /bot123:good/sendMessage")[0].json(); m["text"] != long {
		t.Fatal("the words did not follow")
	}
}

func TestTelegramLinksByNameOrByChannelID(t *testing.T) {
	for _, c := range []struct{ chat, handle, want string }{
		{"-1001234567890", "@acme_news", "https://t.me/acme_news/9"},
		{"-1001234567890", "Acme News", "https://t.me/c/1234567890/9"},
		{"-4455", "Team", "https://t.me/"},
	} {
		if got := telegramMessageLink(c.chat, c.handle, 9); got != c.want {
			t.Errorf("%+v = %q", c, got)
		}
	}
}
