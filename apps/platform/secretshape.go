// secretshape.go — what a credential LOOKS like, decided on the server.
//
// The CTO rule is absolute: secrets live in KMS, never in plaintext. sealSecretEnv
// enforces it for every entry a client MARKED secret. The hole was everything a
// client did not mark: an unmarked value is stored verbatim in the app's EnvJSON
// column and inlined into the pod spec, so the whole confidentiality property
// rested on a key-NAME regex running in a browser.
//
// Two things are wrong with that, and the second is the one that matters.
//
// A key name is not the secret; the VALUE is. `GCP_SA_JSON` names nothing
// credential-ish and holds a service-account private key. `STRIPE_SK`, `SK_LIVE`,
// `GH_PAT`, `PGPASS`, `SMTP_PASS`, `HMAC`, `TLS_CERT` are all ordinary-looking
// names the client pattern missed. Any name-based rule is a denylist over an
// unbounded set, and a denylist of names cannot be a confidentiality control.
//
// And it ran on the CLIENT. A control a caller can edit is not a control. The
// client's `secret` flag stays — as UX, and as a hint that may only ADD secrecy —
// but this file is what decides, on the server, before anything is persisted.
//
// ── the rule ────────────────────────────────────────────────────────────────
//
// An entry is sealed if the KEY suggests a credential OR the VALUE looks like
// one. Both, because they fail in opposite directions: `DB_PASS=hunter2` is a
// low-entropy secret no value test would catch, and `GCP_SA_JSON={"private_key":…}`
// is a high-signal value under a name no key test would catch. Either signal is
// enough — over-sealing is a masked read, under-sealing is a plaintext
// credential in a database and a pod spec.
//
// NOT default-seal-everything. That was the other option and it is worse here:
// PORT, NODE_ENV and LOG_LEVEL would become write-only values an operator can no
// longer read back, and every trivial config line would take a KMS round trip on
// the deploy path. The asymmetry is real but bounded — this seals what is shaped
// like a credential and leaves what is shaped like configuration.

package platform

import (
	"math"
	"net/url"
	"strings"
)

// credentialMarkers are substrings that IDENTIFY a credential by construction:
// issuer prefixes that only ever appear on real tokens, and PEM armour. These
// are high-confidence — a value carrying one is a credential whatever it is
// called and whatever entropy it has.
var credentialMarkers = []string{
	"-----begin",  // any PEM block: private key, certificate, encrypted key
	"sk_live_",    // Stripe secret
	"sk_test_",    // Stripe test secret — still a credential
	"rk_live_",    // Stripe restricted
	"whsec_",      // Stripe webhook signing
	"ghp_",        // GitHub personal access token
	"gho_",        // GitHub OAuth
	"ghu_",        // GitHub user-to-server
	"ghs_",        // GitHub server-to-server
	"ghr_",        // GitHub refresh
	"github_pat_", // GitHub fine-grained PAT
	"glpat-",      // GitLab PAT
	"xoxb-",       // Slack bot
	"xoxp-",       // Slack user
	"xoxa-",       // Slack app
	"xoxs-",       // Slack workspace
	"xapp-",       // Slack app-level
	"akia",        // AWS long-term access key id
	"asia",        // AWS temporary access key id
	"aiza",        // Google API key
	"ya29.",       // Google OAuth access token
	"sg.",         // SendGrid
	"dop_v1_",     // DigitalOcean PAT
	"doo_v1_",     // DigitalOcean OAuth
	"dor_v1_",     // DigitalOcean refresh
	"npm_",        // npm automation token
	"hf_",         // Hugging Face
	"sk-",         // OpenAI / Anthropic-style secret key
	"hk-",         // Hanzo key
	"pypi-",       // PyPI
	"shpat_",      // Shopify
	"private_key", // a JSON service account (GCP SA, Firebase) carries this field
	"begin openssh",
}

// keyTokens are the name fragments that MEAN credential, matched as whole
// separator-delimited tokens rather than substrings — "sk" must match STRIPE_SK
// without matching TASK, RISK or DISK.
//
// "key" alone is deliberately absent: SORT_KEY, PARTITION_KEY and IDEMPOTENCY_KEY
// are ordinary configuration, and sealing them would make a routine value
// write-only for no gain. It is admitted only in the compounds below, where it
// unambiguously names a credential.
var keyTokens = map[string]bool{
	"pass": true, "passwd": true, "password": true, "pwd": true, "pgpass": true,
	"secret": true, "secrets": true, "token": true, "apikey": true, "credential": true,
	"credentials": true, "creds": true, "auth": true, "hmac": true, "salt": true,
	"cert": true, "certificate": true, "pem": true, "privkey": true, "privatekey": true,
	"sk": true, "pat": true, "dsn": true, "signature": true, "sig": true, "seed": true,
	"mnemonic": true, "otp": true, "jwt": true, "session": true, "cookie": true,
	"bearer": true, "signing": true, "encryption": true, "keypair": true, "passphrase": true,
}

// keyPairs are two adjacent tokens that together name a credential, for the
// compounds "key" is only credential-ish inside.
var keyPairs = map[string]bool{
	"api key": true, "secret key": true, "private key": true, "access key": true,
	"signing key": true, "encryption key": true, "client secret": true,
	"service account": true, "access token": true, "refresh token": true,
	"id token": true, "auth token": true, "session key": true,
}

// secretKey reports whether an env KEY names a credential.
func secretKey(key string) bool {
	toks := strings.FieldsFunc(strings.ToLower(key), func(r rune) bool {
		return r == '_' || r == '-' || r == '.' || r == ' '
	})
	for _, t := range toks {
		if keyTokens[t] {
			return true
		}
	}
	for i := 0; i+1 < len(toks); i++ {
		if keyPairs[toks[i]+" "+toks[i+1]] {
			return true
		}
	}
	return false
}

// secretValue reports whether a VALUE looks like a credential regardless of what
// it is called. Three tiers, most confident first.
func secretValue(value string) bool {
	v := strings.TrimSpace(value)
	if v == "" {
		return false
	}
	lower := strings.ToLower(v)
	for _, m := range credentialMarkers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	// A connection string carrying a password IS a credential, however the
	// variable is named: postgres://user:pw@host/db, mongodb+srv://…, amqp://…
	if u, err := url.Parse(v); err == nil && u.User != nil {
		if pw, set := u.User.Password(); set && pw != "" {
			return true
		}
	}
	return opaqueToken(v)
}

// opaqueToken reports whether a value is a long, high-entropy, structureless
// string — the shape of a bearer token or generated password that carries no
// recognisable issuer prefix.
//
// It is the LEAST confident tier, so it is deliberately narrow: configuration is
// mostly short, or structured (a URL, a path, a host:port, a list, a number, a
// duration), and structure is what this excludes. The cost of a false positive
// is a value that reads back masked; the cost of a false negative is a plaintext
// credential in a database. The threshold is set accordingly, but not so low
// that a long public URL or a comma-separated list trips it.
func opaqueToken(v string) bool {
	const minLen = 24
	if len(v) < minLen || strings.ContainsAny(v, " \t\n\r") {
		return false
	}
	// Structured configuration, not an opaque token.
	if strings.HasPrefix(v, "/") || strings.HasPrefix(v, "./") || strings.HasPrefix(v, "~/") {
		return false // a filesystem path
	}
	if strings.Contains(v, "://") || strings.Contains(v, ",") {
		return false // a URL with no credential (handled above), or a list
	}
	// A token's alphabet is the base64/base62/hex family plus the URL-safe and
	// separator characters real tokens use. Anything outside it (punctuation,
	// spaces, quotes) says "sentence or structured value", not "token".
	classes := 0
	var lettersLower, lettersUpper, digits, others bool
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z':
			lettersLower = true
		case r >= 'A' && r <= 'Z':
			lettersUpper = true
		case r >= '0' && r <= '9':
			digits = true
		case r == '-' || r == '_' || r == '+' || r == '/' || r == '=' || r == '.':
			// token-alphabet punctuation, not a class of its own
		default:
			others = true
		}
	}
	if others {
		return false
	}
	for _, b := range []bool{lettersLower, lettersUpper, digits} {
		if b {
			classes++
		}
	}
	// Hex and lowercase-base32 secrets have only two classes but are long; a
	// mixed-case alphanumeric token has three. Require either.
	if classes < 3 && len(v) < 32 {
		return false
	}
	if classes < 2 {
		return false
	}
	return shannon(v) >= 3.0
}

// shannon is the per-character entropy of a string, in bits. A generated token
// sits well above 3.5; an English word or a repeated pattern sits below 3.
func shannon(s string) float64 {
	if s == "" {
		return 0
	}
	var freq [256]float64
	for i := 0; i < len(s); i++ {
		freq[s[i]]++
	}
	n := float64(len(s))
	h := 0.0
	for _, c := range freq {
		if c == 0 {
			continue
		}
		p := c / n
		h -= p * math.Log2(p)
	}
	return h
}

// mustSeal is the ONE predicate sealSecretEnv asks of an entry the client did
// not mark. The client's flag may only ADD secrecy, so this is never consulted
// to make something public.
func mustSeal(key, value string) bool {
	return secretKey(key) || secretValue(value)
}
