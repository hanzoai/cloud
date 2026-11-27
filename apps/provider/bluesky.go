package provider

// bluesky.go — Bluesky, with an app password.
//
// Bluesky has no app registration to wait on: a person makes an app password in
// their own settings and the PDS they live on trusts it for what a client does.
// That is the path AT Protocol clients use that do not run OAuth (DPoP, PAR and
// a client document served over https), and it is the one the owner named. The
// app password is the credential; the PDS address is kept beside it so a
// person on their own server posts to that server.
//
// A session is made for each post and not kept: the password is the credential
// sealed in KMS, the session lives for the post, and nothing about a session
// has to be renewed or lost with a restart.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/outbound"
)

const (
	blueskyProvider = "bluesky"
	blueskyDefault  = "https://bsky.social"
	// blueskyImageMax is the most a blob may be: Bluesky's own limit on an image.
	blueskyImageMax = 1_000_000
	// serverSecret names the sealed server a credential belongs to.
	serverSecret = "server"
)

// blueskyClient reaches a PDS. The server is the person's to name, so only
// public addresses answer. A var so a test stands in for it.
var blueskyClient = outbound.Client(outbound.Options{Timeout: 30 * time.Second, Redirects: 3, Limit: 8 << 20})

// An app password is four groups of four, lower-case letters and digits. The
// account's own password is not, and is refused: it would give this service
// more than posting.
var appPassword = regexp.MustCompile(`^[a-z0-9]{4}(-[a-z0-9]{4}){3}$`)

func init() {
	register(&Provider{
		ID:          blueskyProvider,
		Name:        "Bluesky",
		Description: "Post to Bluesky with an app password.",
		Category:    "Social",
		Kind:        apiKeyKind,
		AdminOnly:   true,
		// The password, and the server it was made on. The server is not secret, but
		// it is kept the way the password is, so one custody holds the account.
		Secrets:      []string{accessSecret, serverSecret},
		MultiAccount: true,
		PerTarget:    true,
		Configured:   func() bool { return true },
		Creds:        func() OAuthConfig { return OAuthConfig{} },
		Verify:       blueskyVerify,
		Fields: []Field{
			{Name: "identifier", Label: "Handle", Required: true, Help: "Your handle, like you.bsky.social."},
			{Name: "password", Label: "App password", Secret: true, Required: true,
				Help: "Make one in Bluesky under Settings, Privacy and security, App passwords. It looks like abcd-efgh-ijkl-mnop. Never use your account password."},
			{Name: "server", Label: "Server", Help: "Leave empty for bsky.social. Fill it in only if your account lives on another server.",
				Default: blueskyDefault},
		},
	})
}

// blueskySession is what createSession answers.
type blueskySession struct {
	DID       string `json:"did"`
	Handle    string `json:"handle"`
	AccessJWT string `json:"accessJwt"`
}

// atRequest sends one XRPC call. A refusal is a postErr in Bluesky's own words.
func atRequest(ctx context.Context, server, nsid, token string, body any, out any) error {
	req, err := newJSONRequest(ctx, server+"/xrpc/"+nsid, body)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	_, _, err = callOn(blueskyClient, req, out)
	return err
}

// createSession signs in with the app password. It tries the handle, then the
// DID, since a handle a person has changed no longer names the account but the
// DID always does.
func createSession(ctx context.Context, server, identifier, did, password string) (blueskySession, error) {
	var sess blueskySession
	err := atRequest(ctx, server, "com.atproto.server.createSession", "",
		map[string]string{"identifier": identifier, "password": password}, &sess)
	var refused postErr
	if errors.As(err, &refused) && refused.status == http.StatusUnauthorized && did != "" && did != identifier {
		err = atRequest(ctx, server, "com.atproto.server.createSession", "",
			map[string]string{"identifier": did, "password": password}, &sess)
	}
	if err != nil {
		return blueskySession{}, err
	}
	if sess.DID == "" || sess.AccessJWT == "" {
		return blueskySession{}, errors.New("Bluesky answered without a session")
	}
	return sess, nil
}

func blueskyVerify(ctx context.Context, in VerifyInput) (*ExchangeResult, error) {
	handle := strings.TrimPrefix(strings.TrimSpace(in.Fields["identifier"]), "@")
	password := strings.TrimSpace(in.Fields["password"])
	if handle == "" || password == "" {
		return nil, verifyRefused("Enter your handle and an app password.")
	}
	if !appPassword.MatchString(password) {
		return nil, verifyRefused("That is not an app password. Make one in Bluesky under Settings, Privacy and security, App passwords. Never use your account password.")
	}
	server, err := serverOf(in.Fields["server"], blueskyDefault)
	if err != nil {
		return nil, verifyRefused(err.Error())
	}
	sess, err := createSession(ctx, server, handle, "", password)
	if err != nil {
		var refused postErr
		if errors.As(err, &refused) && refused.status >= 400 && refused.status < 500 {
			return nil, verifyRefused("Bluesky did not accept that handle and app password.")
		}
		return nil, verifyRefused("Bluesky could not be reached at " + server + ".")
	}
	return &ExchangeResult{
		Tokens:       map[string]string{accessSecret: password, serverSecret: server},
		ExternalID:   sess.DID,
		Key:          keyOf(sess.DID),
		AccountLabel: "@" + sess.Handle,
	}, nil
}

// serverOf reads the address of a server a person typed: a host, with or
// without https://, and nothing else. An empty answer is the default.
func serverOf(raw, def string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		if def == "" {
			return "", errors.New("Enter the address of your server.")
		}
		return def, nil
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", errors.New("The server is its address alone, like example.social.")
	}
	if u.Scheme != "https" {
		return "", errors.New("The server must use https.")
	}
	return "https://" + strings.ToLower(u.Host), nil
}

// keyOf makes a connection key out of an id that has characters a key cannot.
func keyOf(id string) string {
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	k := b.String()
	if len(k) > 64 {
		k = k[:64]
	}
	return k
}

// postBluesky publishes the words, with up to four photos, to the account.
func postBluesky(ctx context.Context, to postTo) (string, error) {
	server := to.Sealed[serverSecret]
	if server == "" {
		server = blueskyDefault
	}
	images, videos, err := splitMedia(to.Media)
	if err != nil {
		return "", err
	}
	if len(videos) > 0 {
		return "", errors.New("Bluesky takes photos here, not video")
	}
	if len(images) > 4 {
		return "", errors.New("Bluesky takes four photos at most in a post")
	}
	sess, err := createSession(ctx, server, strings.TrimPrefix(to.Handle, "@"), to.Account, to.Token)
	if err != nil {
		return "", err
	}
	record := map[string]any{
		"$type":     "app.bsky.feed.post",
		"text":      to.Text,
		"createdAt": time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
	}
	if facets := blueskyFacets(to.Text); len(facets) > 0 {
		record["facets"] = facets
	}
	if len(images) > 0 {
		embed := make([]map[string]any, 0, len(images))
		for _, img := range images {
			f, data, err := readMedia(ctx, img, blueskyImageMax)
			if err != nil {
				if errors.Is(err, errMediaTooLarge) {
					return "", errors.New("Bluesky takes an image up to 1 MB")
				}
				return "", err
			}
			ctype := f.Type
			if !strings.HasPrefix(ctype, "image/") {
				return "", fmt.Errorf("the media address %q is not an image", firstLine(img))
			}
			req, err := newRequest(ctx, http.MethodPost, server+"/xrpc/com.atproto.repo.uploadBlob", string(data))
			if err != nil {
				return "", err
			}
			req.Header.Set("Content-Type", ctype)
			req.Header.Set("Authorization", "Bearer "+sess.AccessJWT)
			var up struct {
				Blob json.RawMessage `json:"blob"`
			}
			if _, _, err := callOn(blueskyClient, req, &up); err != nil {
				return "", err
			}
			if len(up.Blob) == 0 {
				return "", errors.New("Bluesky answered without the image")
			}
			embed = append(embed, map[string]any{"alt": "", "image": up.Blob})
		}
		record["embed"] = map[string]any{"$type": "app.bsky.embed.images", "images": embed}
	}
	var made struct {
		URI string `json:"uri"`
	}
	err = atRequest(ctx, server, "com.atproto.repo.createRecord", sess.AccessJWT, map[string]any{
		"repo": sess.DID, "collection": "app.bsky.feed.post", "record": record,
	}, &made)
	if err != nil {
		return "", err
	}
	if made.URI == "" {
		return "", errors.New("Bluesky answered without the post's address")
	}
	return "https://bsky.app/profile/" + sess.Handle + "/post/" + path.Base(made.URI), nil
}

// A hashtag starts a word and holds at least one letter.
var blueskyTag = regexp.MustCompile(`(^|\s)#([\p{L}\p{N}_]*[\p{L}_][\p{L}\p{N}_]*)`)

// blueskyFacets marks the links and hashtags in the words. A link in plain text
// is not clickable on Bluesky; it is the facet, by byte range, that makes it
// one. Mentions would need each handle resolved to a DID and are left as text.
func blueskyFacets(text string) []map[string]any {
	var out []map[string]any
	facet := func(start, end int, feature map[string]any) {
		out = append(out, map[string]any{
			"index":    map[string]int{"byteStart": start, "byteEnd": end},
			"features": []map[string]any{feature},
		})
	}
	for _, m := range webAddress.FindAllStringIndex(text, -1) {
		start, end := m[0], m[1]
		// A sentence's last mark is not part of the address that ends it.
		for end > start && strings.ContainsRune(".,;:!?)]}'\"", rune(text[end-1])) {
			end--
		}
		if end > start {
			facet(start, end, map[string]any{"$type": "app.bsky.richtext.facet#link", "uri": text[start:end]})
		}
	}
	for _, m := range blueskyTag.FindAllStringSubmatchIndex(text, -1) {
		facet(m[4]-1, m[5], map[string]any{"$type": "app.bsky.richtext.facet#tag", "tag": text[m[4]:m[5]]})
	}
	return out
}
