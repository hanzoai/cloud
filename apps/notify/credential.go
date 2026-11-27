package notify

// credential.go is where an org sets the provider credentials it sends with.
//
//	PUT    /v1/notify/credentials/:provider/:key  set one; org admin
//	DELETE /v1/notify/credentials/:provider/:key  remove one; org admin
//
// A credential is sealed at orgs/<org>/notify/<provider>/<key>, a subtree KMS
// keeps on the org's behalf (apps/kms custody.go): notify reads it on the
// internal plane at the moment it sends, and the KMS REST surface opens it for
// SuperAdmin and a declared grant alone — to write as to read. So an org admin
// sets one here, where the key is held to the set its provider reads, and never
// through /v1/kms/secrets.
//
// Neither op answers a value. Which keys are set is a name listing, and
// GET /v1/kms/secrets?path=/notify/<provider> answers it to an admin of the org.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/secret"
	"github.com/zap-proto/zip"
)

// credentialCap bounds one credential: a token, a password, an SMTP host.
const credentialCap = 8 << 10

// notifyCredential is one provider credential to set.
type notifyCredential struct {
	// Provider is the delivery provider the credential is for: twilio, plivo,
	// twilio_email or mail.
	Provider string `json:"provider"`
	// Key is one of the credentials that provider reads, such as auth-token or
	// smtp-host. A key the provider does not read is refused.
	Key string `json:"key"`
	// Value is sealed in KMS and never answered, logged or echoed. It is never
	// read from a query string, because a URL is logged in more places than a
	// body is.
	Value string `json:"value" url:"-"`
}

// notifyCredentialRef names one provider credential.
type notifyCredentialRef struct {
	// Provider is the delivery provider the credential is for.
	Provider string `json:"provider"`
	// Key is the credential's name within that provider.
	Key string `json:"key"`
}

// notifyStored is the receipt for a credential that was sealed. It names the
// credential and never carries its value.
type notifyStored struct {
	// Provider is the provider the credential was set for.
	Provider string `json:"provider"`
	// Key is the credential that was set.
	Key string `json:"key"`
	// Stored is true: the value is sealed and the next send reads it.
	Stored bool `json:"stored"`
}

// setCredential sets one of your org's notify provider credentials.
//
// The value is sealed in KMS under your org and read by notify alone, at the
// moment it sends; no route answers it back, this one included. Setting a key
// that is already set replaces it, which is how a credential is rotated, and the
// next send uses the new value. The key must be one the provider reads — Twilio's
// account-sid, auth-token and from-number, say — and anything else is a 400
// naming the keys it does read. An unknown provider is a 404.
//
// Org admin only: a credential set here is what every message the org sends
// goes out with.
//
// Example: {"provider": "twilio", "key": "auth-token", "value": "…"}
func (s *service) setCredential(ctx context.Context, in *notifyCredential) (*notifyStored, error) {
	org, err := s.steward(ctx)
	if err != nil {
		return nil, err
	}
	provider, key, err := credentialOf(in.Provider, in.Key)
	if err != nil {
		return nil, err
	}
	if in.Value == "" || len(in.Value) > credentialCap {
		return nil, zip.ErrBadRequest(fmt.Sprintf("notify: a credential's value is 1 to %d bytes", credentialCap))
	}
	if err := s.kms.PutSecret(cloud.For(ctx, org), credentialRef(org, provider, key), []byte(in.Value)); err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "notify: the credential was not stored")
	}
	return &notifyStored{Provider: provider, Key: key, Stored: true}, nil
}

// removeCredential removes one of your org's notify provider credentials.
//
// The value is forgotten in KMS. A provider missing a key it cannot send
// without is no longer picked for its channel, and a send that pins it fails
// with the key it lacks. Removing a key that is not set succeeds, since what was
// asked for is already true. Org admin only.
func (s *service) removeCredential(ctx context.Context, in *notifyCredentialRef) (*cloud.Unit, error) {
	org, err := s.steward(ctx)
	if err != nil {
		return nil, err
	}
	provider, key, err := credentialOf(in.Provider, in.Key)
	if err != nil {
		return nil, err
	}
	err = s.kms.DeleteSecret(cloud.For(ctx, org), credentialRef(org, provider, key))
	if err != nil && !errors.Is(err, secret.ErrNone) {
		return nil, zip.Errorf(http.StatusBadGateway, "notify: the credential was not removed")
	}
	return &cloud.Unit{}, nil
}

// steward admits an org admin of the org the request acts in, and answers that
// org. It fails closed with no request behind the call — a tool or a CLI invoke
// has no admin to be — and with no KMS to seal into.
func (s *service) steward(ctx context.Context) (string, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return "", err
	}
	if !cloud.Admin.Admits(cloud.AuthorityIn(ctx)) {
		return "", cloud.Admin.Refusal()
	}
	if s.kms == nil {
		return "", zip.Errorf(http.StatusServiceUnavailable, "notify: credentials are not available on this deployment")
	}
	return org, nil
}

// credentialOf holds an address to the providers this binary links and the keys
// each one reads (provider.Keys), so a credential is only ever sealed where a
// send will look for it.
func credentialOf(provider, key string) (string, string, error) {
	provider, key = strings.TrimSpace(provider), strings.TrimSpace(key)
	p, ok := providers[provider]
	if !ok {
		return "", "", zip.ErrNotFound(fmt.Sprintf("notify: no provider %q", provider))
	}
	if !slices.Contains(p.Keys, key) {
		return "", "", zip.ErrBadRequest(fmt.Sprintf("notify: %s reads %s", p.ID, strings.Join(p.Keys, ", ")))
	}
	return p.ID, key, nil
}

// credentialRef is where one credential is sealed — the ref creds reads it from.
// Injective in (org, provider, key): a provider id and a key are registered
// names, and neither holds a '/'.
func credentialRef(org, provider, key string) string {
	return "orgs/" + org + "/notify/" + provider + "/" + key
}
