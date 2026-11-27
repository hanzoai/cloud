package provider

// post_reddit.go — Reddit, as the places a person can submit to.
//
// One consent is the member and every community the member moderates. Each is an
// account, addressed by the name Reddit's submit takes: u_<name> is a member's
// own profile, and a community is its bare name. A community's key is r-<name>
// and a profile's is u-<name>, so neither can replace the other. The token is
// the member's, kept with each account the way a LinkedIn company keeps it.
//
// A post is a title and a body. The first line of the words is the title,
// because Reddit requires one and a post has only words; the rest is the body.
// Media goes as a link, since an upload is a separate lease Reddit gates apart.

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

var redditSocialAPI = "https://oauth.reddit.com"

const redditTitleMax = 300

// redditHeaders is what every call to Reddit carries: the credential, and the
// descriptive User-Agent its rules require.
func redditHeaders(token string) map[string]string {
	return map[string]string{"Authorization": "bearer " + token, "User-Agent": redditUserAgent}
}

func identifyReddit(ctx context.Context, token string) (identity, error) {
	var u struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := oauthGetJSON(ctx, "reddit", redditSocialAPI+"/api/v1/me", redditHeaders(token), &u); err != nil {
		return identity{}, err
	}
	return identity{ExternalID: u.ID, Label: "u/" + u.Name}, nil
}

// expandReddit keeps the member's profile and every community the member moderates.
func expandReddit(ctx context.Context, _ OAuthConfig, token string, who identity) ([]socialGrant, error) {
	name := strings.TrimPrefix(who.Label, "u/")
	if name == "" {
		return nil, errors.New("reddit: the login named no member")
	}
	grants := []socialGrant{{
		ExternalID: "u_" + name, Label: "u-" + name, AccountLabel: "u/" + name, Token: token,
		Kind: "personal", Author: who.ExternalID,
	}}
	var list struct {
		Data struct {
			Children []struct {
				Data struct {
					Name string `json:"display_name"`
				} `json:"data"`
			} `json:"children"`
		} `json:"data"`
	}
	// The communities are a courtesy to the person: a refusal still leaves the
	// profile, which is an account on its own.
	if err := oauthGetJSON(ctx, "reddit", redditSocialAPI+"/subreddits/mine/moderator?limit=100", redditHeaders(token), &list); err == nil {
		for _, c := range list.Data.Children {
			if c.Data.Name == "" || !validLabel("r-"+c.Data.Name) {
				continue
			}
			grants = append(grants, socialGrant{
				ExternalID: c.Data.Name, Label: "r-" + c.Data.Name, AccountLabel: "r/" + c.Data.Name, Token: token,
				Kind: "page", Author: who.ExternalID,
			})
		}
	}
	return grants, nil
}

// redditSplit is the title and the body of a post's words.
func redditSplit(text string) (title, body string) {
	text = strings.TrimSpace(text)
	first, rest, _ := strings.Cut(text, "\n")
	first = strings.TrimSpace(first)
	if r := []rune(first); len(r) > redditTitleMax {
		// The title is shortened; the words are not lost, the body keeps them all.
		return strings.TrimSpace(string(r[:redditTitleMax-1])) + "…", text
	}
	return first, strings.TrimSpace(rest)
}

// postReddit submits the words to the profile or community the account names.
func postReddit(ctx context.Context, to postTo) (string, error) {
	if len(to.Media) > 1 {
		return "", errors.New("Reddit takes one link in a post")
	}
	if _, _, err := splitMedia(to.Media); err != nil {
		return "", err
	}
	title, body := redditSplit(to.Text)
	form := url.Values{
		"api_type": {"json"}, "sr": {to.Account}, "title": {title},
		"resubmit": {"true"}, "sendreplies": {"true"},
	}
	if len(to.Media) == 1 {
		form.Set("kind", "link")
		form.Set("url", to.Media[0])
	} else {
		form.Set("kind", "self")
		form.Set("text", body)
	}
	req, err := newRequest(ctx, "POST", redditSocialAPI+"/api/submit", form.Encode())
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, v := range redditHeaders(to.Token) {
		req.Header.Set(k, v)
	}
	var out struct {
		JSON struct {
			Errors [][]any `json:"errors"`
			Data   struct {
				URL string `json:"url"`
			} `json:"data"`
		} `json:"json"`
	}
	if _, err := call(ctx, req, &out); err != nil {
		return "", err
	}
	// Reddit refuses with a 200 and the reason in the body.
	if len(out.JSON.Errors) > 0 {
		return "", redditRefusal(out.JSON.Errors[0])
	}
	if out.JSON.Data.URL == "" {
		return "", errors.New("Reddit answered without the post's address")
	}
	return out.JSON.Data.URL, nil
}

// redditRefusal reads one [code, message, field] error. A rate limit is a 429,
// and says how long Reddit asks to wait.
func redditRefusal(e []any) error {
	code := firstString(e, 0)
	msg := firstString(e, 1)
	if msg == "" {
		msg = strings.ToLower(code)
	}
	status := 400
	if code == "RATELIMIT" {
		status = 429
	}
	return postErr{status: status, msg: fmt.Sprintf("%s (%s)", msg, code)}
}

func firstString(xs []any, i int) string {
	if i < len(xs) {
		if s, ok := xs[i].(string); ok {
			return s
		}
	}
	return ""
}
