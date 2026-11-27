package provider

// mastodon.go — Mastodon, on whichever server the account lives on.
//
// There is no one Mastodon: every server is its own, and an OAuth app is
// registered per server. The credential here is the access token a person makes
// on their own server (Preferences, Development), which is Mastodon's own path
// for a client that acts for one account. The server is kept beside the token,
// since a token is good only where it was made.

import (
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hanzoai/outbound"
)

const mastodonProvider = "mastodon"

// mastodonClient reaches a person's server. Only public addresses answer. A var
// so a test stands in for it.
var mastodonClient = outbound.Client(outbound.Options{Timeout: 5 * time.Minute, Redirects: 3, Limit: 8 << 20})

func init() {
	register(&Provider{
		ID:           mastodonProvider,
		Name:         "Mastodon",
		Description:  "Post to your Mastodon account with an access token from your server.",
		Category:     "Social",
		Kind:         apiKeyKind,
		AdminOnly:    true,
		Secrets:      []string{accessSecret, serverSecret},
		MultiAccount: true,
		PerTarget:    true,
		Configured:   func() bool { return true },
		Creds:        func() OAuthConfig { return OAuthConfig{} },
		Verify:       mastodonVerify,
		Fields: []Field{
			{Name: "server", Label: "Server", Required: true, Help: "The address of your server, like mastodon.social."},
			{Name: "token", Label: "Access token", Secret: true, Required: true,
				Help: "On your server open Preferences, Development, New application. Give it read:accounts, write:statuses and write:media, save, and copy \"Your access token\"."},
		},
	})
}

func mastodonVerify(ctx context.Context, in VerifyInput) (*ExchangeResult, error) {
	token := strings.TrimSpace(in.Fields["token"])
	if token == "" {
		return nil, verifyRefused("Enter the access token.")
	}
	server, err := serverOf(in.Fields["server"], "")
	if err != nil {
		return nil, verifyRefused(err.Error())
	}
	req, err := newRequest(ctx, http.MethodGet, server+"/api/v1/accounts/verify_credentials", "")
	if err != nil {
		return nil, verifyRefused("That is not a server address.")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	var me struct {
		Username string `json:"username"`
	}
	if _, _, err := callOn(mastodonClient, req, &me); err != nil {
		var refused postErr
		if errors.As(err, &refused) && (refused.status == http.StatusUnauthorized || refused.status == http.StatusForbidden) {
			return nil, verifyRefused("That server did not accept the access token.")
		}
		return nil, verifyRefused("That server could not be reached.")
	}
	if me.Username == "" {
		return nil, verifyRefused("That server did not say whose token this is.")
	}
	host := strings.TrimPrefix(server, "https://")
	acct := strings.ToLower(me.Username) + "@" + host
	return &ExchangeResult{
		Tokens:       map[string]string{accessSecret: token, serverSecret: server},
		ExternalID:   acct,
		Key:          keyOf(acct),
		AccountLabel: "@" + acct,
		Scopes:       []string{"read:accounts", "write:statuses", "write:media"},
	}, nil
}

// postMastodon posts the words, with up to four photos or one video.
func postMastodon(ctx context.Context, to postTo) (string, error) {
	server := to.Sealed[serverSecret]
	if server == "" {
		return "", errors.New("Mastodon needs the server this account lives on; reconnect the account")
	}
	images, videos, err := splitMedia(to.Media)
	if err != nil {
		return "", err
	}
	if len(videos) > 1 || (len(videos) == 1 && len(images) > 0) || len(images) > 4 {
		return "", errors.New("Mastodon takes up to four photos, or one video, in a post")
	}
	ids := make([]string, 0, len(to.Media))
	for _, m := range to.Media {
		id, err := mastodonUpload(ctx, server, to.Token, m)
		if err != nil {
			return "", err
		}
		ids = append(ids, id)
	}
	body := map[string]any{"status": to.Text, "visibility": "public"}
	if len(ids) > 0 {
		body["media_ids"] = ids
	}
	req, err := newJSONRequest(ctx, server+"/api/v1/statuses", body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+to.Token)
	var out struct {
		URL string `json:"url"`
	}
	if _, _, err := callOn(mastodonClient, req, &out); err != nil {
		return "", err
	}
	if out.URL == "" {
		return "", errors.New("Mastodon answered without the post's address")
	}
	return out.URL, nil
}

// mastodonMax is the largest file fetched for an upload, 40 MiB, which is what
// a default server takes of a video.
const mastodonMax = 40 << 20

// mastodonUpload sends one file to the server and answers the id a status
// attaches. A file the server is still processing is waited on.
func mastodonUpload(ctx context.Context, server, token, address string) (string, error) {
	file, err := fetchMedia(ctx, address, mastodonMax)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Body.Close() }()

	pr, pw := io.Pipe()
	form := multipart.NewWriter(pw)
	go func() {
		part, err := form.CreateFormFile("file", file.Name)
		if err == nil {
			_, err = io.Copy(part, io.LimitReader(file.Body, mastodonMax+1))
		}
		if err == nil {
			err = form.Close()
		}
		_ = pw.CloseWithError(err)
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server+"/api/v2/media", pr)
	if err != nil {
		_ = pr.Close()
		return "", err
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	var up struct {
		ID string `json:"id"`
	}
	_, status, err := callOn(mastodonClient, req, &up)
	if err != nil {
		return "", err
	}
	if up.ID == "" {
		return "", errors.New("Mastodon answered without the file's id")
	}
	if status == http.StatusAccepted {
		// A video is processed after it is accepted; 200 says it is ready, 206 that
		// it is not.
		err = settle(ctx, func(ctx context.Context) (bool, error) {
			look, err := newRequest(ctx, http.MethodGet, server+"/api/v1/media/"+url.PathEscape(up.ID), "")
			if err != nil {
				return false, err
			}
			look.Header.Set("Authorization", "Bearer "+token)
			_, st, err := callOn(mastodonClient, look, nil)
			if err != nil {
				return false, err
			}
			return st == http.StatusOK, nil
		})
		if err != nil {
			return "", err
		}
	}
	return up.ID, nil
}
