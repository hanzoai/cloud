package appcred

import "github.com/hanzoai/cloud/sdk/env"

// Pair is one id and the secret that belongs with it. They are read together:
// a client id from one pair is never sent with the secret of another.
type Pair struct {
	ID, Secret string
}

// pairs is the lookup, canonical first. A network that is not here has no
// shared ladder; its own declaration names the variables.
var pairs = map[string][]Pair{
	"x": {
		{"X_CLIENT_ID", "X_CLIENT_SECRET"},
		{"X_API_KEY", "X_API_SECRET"},
	},
	"facebook": {
		{"FACEBOOK_CLIENT_ID", "FACEBOOK_CLIENT_SECRET"},
		{"FACEBOOK_APP_ID", "FACEBOOK_APP_SECRET"},
	},
	"instagram": {
		{"INSTAGRAM_CLIENT_ID", "INSTAGRAM_CLIENT_SECRET"},
		{"FACEBOOK_CLIENT_ID", "FACEBOOK_CLIENT_SECRET"},
		{"FACEBOOK_APP_ID", "FACEBOOK_APP_SECRET"},
	},
	"linkedin": {
		{"LINKEDIN_CLIENT_ID", "LINKEDIN_CLIENT_SECRET"},
	},
	"tiktok": {
		{"TIKTOK_CLIENT_ID", "TIKTOK_CLIENT_SECRET"},
	},
	"youtube": {
		{"YOUTUBE_CLIENT_ID", "YOUTUBE_CLIENT_SECRET"},
	},
	"threads": {
		{"THREADS_CLIENT_ID", "THREADS_CLIENT_SECRET"},
		{"THREADS_APP_ID", "THREADS_APP_SECRET"},
	},
	"pinterest": {
		{"PINTEREST_CLIENT_ID", "PINTEREST_CLIENT_SECRET"},
	},
	"reddit": {
		{"REDDIT_CLIENT_ID", "REDDIT_CLIENT_SECRET"},
	},
}

// Pairs returns the ladder for a network, canonical first. Nil means this
// network is not one of the shared social apps.
func Pairs(provider string) []Pair {
	return pairs[provider]
}

// Names is every variable the ladder consults, in order, for the hint a
// caller shows when none of them form a complete app.
func Names(provider string) []string {
	ps := pairs[provider]
	out := make([]string, 0, len(ps)*2)
	for _, p := range ps {
		out = append(out, p.ID, p.Secret)
	}
	return out
}

// Resolve reads the process environment. The first pair that has both values
// wins. When none does, missing names every variable that is unset, which is
// the list an operator was told to set.
func Resolve(provider string) (id, secret string, missing []string) {
	ps := pairs[provider]
	if len(ps) == 0 {
		return "", "", nil
	}
	for _, p := range ps {
		id = env.Or(p.ID, "")
		secret = env.Or(p.Secret, "")
		if id != "" && secret != "" {
			return id, secret, nil
		}
	}
	for _, name := range Names(provider) {
		if env.Or(name, "") == "" {
			missing = append(missing, name)
		}
	}
	return "", "", missing
}
