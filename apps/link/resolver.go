package link

// resolver.go is the CREDENTIAL-FETCH CONTRACT this package consumes, plus its
// KMS-backed reference implementation.
//
// THE SPLIT OF RESPONSIBILITY. The connector subsystem (the AI-connections plane:
// ai/controllers/connections_*.go, ai/object/kms.go) OWNS a linked account's
// lifecycle — it links the account, seals its secret in KMS per-org, and refreshes
// an OAuth token before it expires. This package only READS: given an account it
// resolves the current sealed credential. Storage, sealing, and refresh stay the
// connector's; selection, cycling, and metering are the router's. One concern each.
//
// WHY THE SEAM IS AN INTERFACE. The router is proven against a fake Resolver in a
// unit test — no KMS, no network — so the isolation guarantee (a request resolves
// only its OWN org's accounts) is a deterministic assertion rather than an
// integration hope. The live binary wires the KMS implementation below.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
)

// defaultProfile is the reserved profile of a provider's default/only account —
// the value KMSRef folds in when a selector names a bare provider ("anthropic")
// with no profile. It keeps the single-account case at a stable, un-suffixed-in-
// spirit key while still giving every account exactly one address.
const defaultProfile = "default"

// Account is the identity of ONE linked provider account: a provider and a profile
// within it. It is the openclaw `@anthropic:work` selector — provider "anthropic",
// profile "work" — and a NON-SECRET identifier: it NAMES an account, never carries
// its credential. An empty Profile denotes the provider's default account.
type Account struct {
	Provider string
	Profile  string
}

// String renders the account as the wire selector "provider:profile" (or the bare
// "provider" for the default profile). It is the audit + metering label — always
// safe to log, because it is an identifier and never a secret.
func (a Account) String() string {
	p := trim(a.Provider)
	prof := trim(a.Profile)
	if prof == "" || prof == defaultProfile {
		return p
	}
	return p + ":" + prof
}

// profile returns the account's effective profile, folding the empty/default case
// to defaultProfile so a bare provider and its ":default" name one account.
func (a Account) profile() string {
	if prof := trim(a.Profile); prof != "" {
		return prof
	}
	return defaultProfile
}

func (a Account) isZero() bool { return trim(a.Provider) == "" }

// Credential is a resolved upstream provider credential, held in memory for the
// life of ONE request and then discarded. It is NEVER persisted, and it never
// appears in a log line, a response, an error, or an argv: String and GoString
// redact, so even an accidental %v / %+v / %#v of a Credential — or of any struct
// that embeds one — cannot leak the token. The router treats it as opaque: it
// hands the value to Upstream.Call and retains nothing.
type Credential struct {
	Token  string            // bearer / api-key / OAuth access token — the secret
	Header string            // auth header to carry it; "" ⟹ Authorization
	Scheme string            // "Bearer" | "" (raw); how Token is presented
	Extra  map[string]string // multi-field providers (region, project, sign-key)
	Expiry time.Time         // token expiry; zero ⟹ non-expiring (a raw api key)
}

// String redacts. A Credential must never render its token, on any code path.
func (Credential) String() string { return "link.Credential(redacted)" }

// GoString redacts the %#v form too, so a %+v of an enclosing struct is safe.
func (Credential) GoString() string { return "link.Credential(redacted)" }

func (c Credential) empty() bool { return strings.TrimSpace(c.Token) == "" && len(c.Extra) == 0 }

// expired reports whether an expiring credential is past its Expiry. A zero Expiry
// (a raw api key) never expires. The router treats an expired credential as
// unavailable and cycles past it — it does NOT itself refresh, because refresh is
// the connector's job (it re-seals a fresh token before expiry, behind this seam).
func (c Credential) expired(now time.Time) bool {
	return !c.Expiry.IsZero() && !now.Before(c.Expiry)
}

// Resolver is the credential-fetch contract this package CONSUMES. Given an
// account it returns the KMS-stored credential, refreshed if the connector keeps
// it fresh. It is scoped to (org, subject): the key is bound HERE from the
// validated principal the router passes, never taken from a request, so a foreign
// org's or user's account is unreachable — resolving across the tenant boundary is
// unrepresentable, not merely refused.
type Resolver interface {
	Resolve(ctx context.Context, org, subject string, a Account) (Credential, error)
}

// ErrNoCredential is the sentinel a Resolver returns when an account has no sealed
// credential in KMS (never linked, or unsealed). The router treats it as "this
// account is unavailable" and cycles past — it NEVER falls back to a platform key.
var ErrNoCredential = errors.New("link: no sealed credential for account")

// KMSRef is the org-scoped KMS coordinate a linked account's credential is sealed
// at — the ONE convention shared by the connector (which WRITES the secret here)
// and this router (which READS it). It folds org, provider, and profile into the
// path exactly as clients/platform's kmsAuthRef folds org+field, so a credential
// can only ever be ADDRESSED within its own org's namespace. The org segment is
// the first path component, so no provider/profile value a caller might influence
// can escape the org prefix.
func KMSRef(org string, a Account) string {
	return "orgs/" + org + "/providers/" + trim(a.Provider) + "/" + a.profile()
}

// sealedCredential is the JSON the connector seals in KMS and this resolver reads
// back. Every field is optional: a bare token (a raw api key) may be sealed as
// plain bytes with no JSON envelope, which decodeCredential also accepts. The
// shape is the connector-facing half of the contract, reported alongside KMSRef.
type sealedCredential struct {
	Token  string            `json:"token"`
	Header string            `json:"header,omitempty"`
	Scheme string            `json:"scheme,omitempty"`
	Extra  map[string]string `json:"extra,omitempty"`
	Expiry string            `json:"expiry,omitempty"` // RFC3339; "" ⟹ non-expiring
}

// decodeCredential turns a sealed KMS value into a Credential. A JSON envelope
// (the connector's OAuth/multi-field seal) is parsed; anything else is treated as
// a raw token (a plain api key), so both seal formats are consumed by one reader.
// It never logs the value.
func decodeCredential(raw []byte) (Credential, error) {
	b := []byte(strings.TrimSpace(string(raw)))
	if len(b) == 0 {
		return Credential{}, ErrNoCredential
	}
	if b[0] == '{' {
		var s sealedCredential
		if err := json.Unmarshal(b, &s); err != nil {
			return Credential{}, fmt.Errorf("link: decode sealed credential: %w", err)
		}
		c := Credential{Token: s.Token, Header: s.Header, Scheme: s.Scheme, Extra: s.Extra}
		if s.Expiry != "" {
			if t, err := time.Parse(time.RFC3339, s.Expiry); err == nil {
				c.Expiry = t.UTC()
			}
		}
		if c.empty() {
			return Credential{}, ErrNoCredential
		}
		return c, nil
	}
	return Credential{Token: string(b)}, nil
}

// kmsResolver is the reference Resolver backed by cloud KMS.
//
// IT NEVER FALLS OPEN. A linked account's credential lives ONLY at its per-org KMS
// ref; unlike a PLATFORM key (which zenKeyResolver / ai.ResolveProviderSecret read
// env-first), a customer's account credential is never an env var and never a
// shared platform key. So this resolver reads exactly one address — KMSRef(org, a)
// — and, if it is absent or empty, returns an error. The router then treats the
// account as unavailable and cycles past it. There is deliberately NO env fallback
// and NO platform-key fallback: falling back would route a customer's request
// through the platform's own key or another tenant's credential — the exact
// fail-open this whole design exists to prevent.
type kmsResolver struct {
	kms cloud.KMSClient
}

// NewKMSResolver builds the live Resolver over cloud KMS. A nil KMS yields a
// resolver that reports every account unavailable (fail-closed), never a panic.
func NewKMSResolver(kms cloud.KMSClient) Resolver { return &kmsResolver{kms: kms} }

// Resolve reads the account's sealed credential from its org-scoped KMS ref. org
// and the account's provider are required; a blank org is refused outright so a
// credential is never resolved org-less. The returned error names the account and
// org (identifiers, safe) but NEVER the secret — and on failure the Credential is
// the zero value, so a partial read can never surface a token.
func (r *kmsResolver) Resolve(ctx context.Context, org, subject string, a Account) (Credential, error) {
	if trim(org) == "" {
		return Credential{}, ErrNoPrincipal
	}
	if a.isZero() {
		return Credential{}, fmt.Errorf("link: resolve requires a provider")
	}
	if r == nil || r.kms == nil {
		return Credential{}, fmt.Errorf("link: KMS unavailable for account %s: %w", a, ErrNoCredential)
	}
	raw, err := r.kms.GetSecret(ctx, KMSRef(org, a))
	if err != nil {
		// The error is about the REF (a path), never the value — safe to wrap.
		return Credential{}, fmt.Errorf("link: fetch credential for %s in org %q: %w", a, org, err)
	}
	cred, err := decodeCredential(raw)
	if err != nil {
		return Credential{}, fmt.Errorf("link: credential for %s in org %q: %w", a, org, err)
	}
	return cred, nil
}
