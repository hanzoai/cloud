package provider

// telegram_bot.go — a Telegram channel or group an org posts to, with its own bot.
//
// This is not the `telegram` connector, which binds one chat to the Hanzo bot so
// people can talk to it. Here the org brings a bot of its own (BotFather makes
// one in a minute), makes it an administrator of the channel, and posts as that
// bot. The channel is the account: its id is the key, so one bot can serve
// several channels and one channel belongs to one org.
//
// The bot token is in every Bot API address, so a failure never reports the
// address: tgCall answers what Telegram said, or that Telegram was not reached.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

const telegramBotProvider = "telegram-bot"

// numericChat is a chat named by its id; anything else is a channel's name.
var numericChat = regexp.MustCompile(`^-?\d+$`)

func init() {
	register(&Provider{
		ID:           telegramBotProvider,
		Name:         "Telegram",
		Description:  "Post to a Telegram channel or group with a bot you own.",
		Category:     "Social",
		Kind:         apiKeyKind,
		AdminOnly:    true,
		Secrets:      []string{accessSecret},
		MultiAccount: true,
		PerTarget:    true,
		Configured:   func() bool { return true },
		Creds:        func() OAuthConfig { return OAuthConfig{} },
		Verify:       telegramVerify,
		Fields: []Field{
			{Name: "token", Label: "Bot token", Secret: true, Required: true,
				Help: "Message @BotFather, send /newbot, and paste the token it gives you."},
			{Name: "chat", Label: "Channel or group", Required: true,
				Help: "The channel's @username, like @acme_news, or its numeric id. Add the bot to it as an administrator first."},
		},
	})
}

// tgCall asks the Bot API one question. Telegram answers ok:false with a
// description, and that is the answer; the address, which holds the token, is
// never part of an error.
func tgCall(ctx context.Context, token, method string, args map[string]any, out any) error {
	b, err := json.Marshal(args)
	if err != nil {
		return err
	}
	req, err := newRequest(ctx, http.MethodPost, telegramAPIBase+"/bot"+token+"/"+method, string(b))
	if err != nil {
		return errors.New("Telegram could not be asked")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := postClient.Do(req)
	if err != nil {
		return errors.New("Telegram could not be reached")
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var env struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
	}
	if json.Unmarshal(body, &env) != nil {
		return postErr{status: resp.StatusCode, msg: "Telegram's answer could not be read"}
	}
	if !env.OK || resp.StatusCode >= 300 {
		return postErr{status: resp.StatusCode, msg: env.Description}
	}
	if out != nil && len(env.Result) > 0 {
		return json.Unmarshal(env.Result, out)
	}
	return nil
}

type tgChat struct {
	ID       int64  `json:"id"`
	Type     string `json:"type"`
	Title    string `json:"title"`
	Username string `json:"username"`
}

func telegramVerify(ctx context.Context, in VerifyInput) (*ExchangeResult, error) {
	token := strings.TrimSpace(in.Fields["token"])
	chat := strings.TrimSpace(in.Fields["chat"])
	if token == "" || chat == "" {
		return nil, verifyRefused("Enter the bot token and the channel.")
	}
	var bot struct {
		ID    int64 `json:"id"`
		IsBot bool  `json:"is_bot"`
	}
	if err := tgCall(ctx, token, "getMe", map[string]any{}, &bot); err != nil || !bot.IsBot {
		return nil, verifyRefused("Telegram did not accept that bot token.")
	}
	if !strings.HasPrefix(chat, "@") && !numericChat.MatchString(chat) {
		chat = "@" + chat
	}
	var c tgChat
	if err := tgCall(ctx, token, "getChat", map[string]any{"chat_id": chat}, &c); err != nil {
		return nil, verifyRefused("Telegram does not know that channel, or the bot is not in it.")
	}
	var member struct {
		Status  string `json:"status"`
		CanPost *bool  `json:"can_post_messages"`
	}
	if err := tgCall(ctx, token, "getChatMember", map[string]any{"chat_id": c.ID, "user_id": bot.ID}, &member); err != nil {
		return nil, verifyRefused("Telegram would not say whether the bot is in that channel.")
	}
	if c.Type == "channel" {
		if member.Status != "administrator" && member.Status != "creator" {
			return nil, verifyRefused("Make the bot an administrator of the channel, with permission to post.")
		}
		if member.CanPost != nil && !*member.CanPost {
			return nil, verifyRefused("The bot is an administrator, but without permission to post.")
		}
	} else if member.Status == "left" || member.Status == "kicked" {
		return nil, verifyRefused("The bot is not a member of that group.")
	}
	label := c.Title
	if c.Username != "" {
		label = "@" + c.Username
	}
	id := strconv.FormatInt(c.ID, 10)
	return &ExchangeResult{
		Tokens:       map[string]string{accessSecret: token},
		ExternalID:   id,
		Key:          keyOf(id),
		AccountLabel: label,
	}, nil
}

// telegramCaption is the most a photo or video's caption holds.
const telegramCaption = 1024

// postTelegram posts the words, with photos or videos, to the channel. Words
// too long for a caption follow the media as a message of their own.
func postTelegram(ctx context.Context, to postTo) (string, error) {
	images, videos, err := splitMedia(to.Media)
	if err != nil {
		return "", err
	}
	if len(to.Media) > 10 {
		return "", errors.New("Telegram takes ten photos or videos at most in a post")
	}
	chat := to.Account
	var sent struct {
		ID int64 `json:"message_id"`
	}
	words := to.Text
	caption := ""
	if len(to.Media) > 0 && len([]rune(words)) <= telegramCaption {
		caption, words = words, ""
	}
	switch {
	case len(to.Media) == 1 && len(images) == 1:
		err = tgCall(ctx, to.Token, "sendPhoto", map[string]any{"chat_id": chat, "photo": images[0], "caption": caption}, &sent)
	case len(to.Media) == 1:
		err = tgCall(ctx, to.Token, "sendVideo", map[string]any{"chat_id": chat, "video": videos[0], "caption": caption}, &sent)
	case len(to.Media) > 1:
		group := make([]map[string]any, 0, len(to.Media))
		for _, m := range to.Media {
			kind := "photo"
			if videoAddress.MatchString(pathOf(m)) {
				kind = "video"
			}
			item := map[string]any{"type": kind, "media": m}
			if len(group) == 0 && caption != "" {
				item["caption"] = caption
			}
			group = append(group, item)
		}
		var album []struct {
			ID int64 `json:"message_id"`
		}
		err = tgCall(ctx, to.Token, "sendMediaGroup", map[string]any{"chat_id": chat, "media": group}, &album)
		if err == nil && len(album) > 0 {
			sent.ID = album[0].ID
		}
	}
	if err != nil {
		return "", err
	}
	if words != "" || len(to.Media) == 0 {
		err = tgCall(ctx, to.Token, "sendMessage", map[string]any{"chat_id": chat, "text": words}, &sent)
		if err != nil {
			return "", err
		}
	}
	if sent.ID == 0 {
		return "", errors.New("Telegram answered without the message's id")
	}
	return telegramMessageLink(chat, to.Handle, sent.ID), nil
}

// telegramMessageLink is the address of a message: by the channel's name when it
// has one, by its id otherwise. A plain group has no address a link can reach.
func telegramMessageLink(chat, handle string, message int64) string {
	if strings.HasPrefix(handle, "@") {
		return fmt.Sprintf("https://t.me/%s/%d", url.PathEscape(strings.TrimPrefix(handle, "@")), message)
	}
	if rest, ok := strings.CutPrefix(chat, "-100"); ok {
		return fmt.Sprintf("https://t.me/c/%s/%d", rest, message)
	}
	return "https://t.me/"
}
