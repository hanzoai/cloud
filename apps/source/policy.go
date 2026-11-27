package source

// Provider policy (HIP-1350, Provider policy). Each account has a set of route
// classes, fixed by Hanzo from the provider's own terms and enforced here, never
// chosen by a client. A change to this table is a HIP amendment.
//
//	desktop  may run a web session on the linked desktop, executed by the provider's
//	         unmodified official client on that machine; the credential never leaves it
//	local    runs only in the app that holds the login; not offered to web sessions
//	key      runs from the user's own API key in KMS, from any session
const (
	classDesktop = "desktop"
	classLocal   = "local"
	classKey     = "key"
)

// loginClasses are the subscription logins whose provider ships remote control of
// its own official client. Every other login is local only.
var loginClasses = map[string][]string{
	"claude":  {classLocal, classDesktop},
	"codex":   {classLocal, classDesktop},
	"copilot": {classLocal, classDesktop},
}

// keyProviders are the providers a person may keep an API key for. Anything not
// listed takes no key.
var keyProviders = map[string]bool{
	"anthropic":  true,
	"openai":     true,
	"google":     true,
	"xai":        true,
	"openrouter": true,
	"opencode":   true,
	"kiro":       true,
}

// classes are the route classes of one account. Fail closed: a provider the table
// does not name is local, and every login on a browser source is local, because a
// browser is view-only.
func classes(provider, kind, sourceKind string) []string {
	switch {
	case kind == "key" && keyProviders[provider]:
		return []string{classKey}
	case kind == "login" && sourceKind == "desktop":
		if c, ok := loginClasses[provider]; ok {
			return append([]string(nil), c...)
		}
	}
	return []string{classLocal}
}

func has(cs []string, c string) bool {
	for _, x := range cs {
		if x == c {
			return true
		}
	}
	return false
}
