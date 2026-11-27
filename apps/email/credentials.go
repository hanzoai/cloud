package email

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/secret"
)

// ErrNoCredentials is an org that holds no commercial rail, and says what to set.
var ErrNoCredentials = errors.New("email: the org holds no SES or SendGrid credential; set one with " +
	"PUT /v1/notify/credentials/ses/<key> (access-key-id, secret-access-key, from-email) or " +
	"PUT /v1/notify/credentials/sendgrid/<key> (api-key, from-email)")

// ProviderDef defines the keys required to instantiate an email provider.
type ProviderDef struct {
	ID    string
	Keys  []string
	Needs []string
}

// EmailProviders are the two rails commercial mail leaves by, in the order the
// default is chosen when an org names none: SES, then SendGrid. The transactional
// rails (Twilio Email, SMTP) are notify's, and /v1/email never sends by them.
var EmailProviders = []ProviderDef{
	{
		ID:    "ses",
		Keys:  []string{"access-key-id", "secret-access-key", "region", "from-email", "from-name", "configuration-set", "sns-topic-arn"},
		Needs: []string{"access-key-id", "secret-access-key", "from-email"},
	},
	{
		ID:    "sendgrid",
		Keys:  []string{"api-key", "from-email", "from-name", "reply-to", "ip-pool", "asm-group-id", "webhook-public-key"},
		Needs: []string{"api-key", "from-email"},
	},
}

// CredentialResolver resolves email provider credentials for an authenticated organization.
type CredentialResolver interface {
	ResolveEmailCredentials(ctx context.Context, org, pinnedProvider string) (provider string, creds map[string]string, err error)
	// SetDefault records the provider an org's mail leaves by when a request pins none.
	SetDefault(ctx context.Context, org, provider string) error
}

// order is the providers to try for org: the one pinned alone, else the org's
// default first and the rest in EmailProviders' order.
func order(pinned, preferred string) ([]ProviderDef, error) {
	if pinned != "" {
		for _, p := range EmailProviders {
			if strings.EqualFold(p.ID, pinned) {
				return []ProviderDef{p}, nil
			}
		}
		return nil, fmt.Errorf("%w: %q is not a provider /v1/email sends by; it sends by ses or sendgrid", ErrNoCredentials, pinned)
	}
	out := slices.Clone(EmailProviders)
	slices.SortStableFunc(out, func(a, b ProviderDef) int {
		switch preferred {
		case a.ID:
			return -1
		case b.ID:
			return 1
		}
		return 0
	})
	return out, nil
}

// complete is the provider whose credentials creds holds every needed key of.
func complete(p ProviderDef, creds map[string]string) bool {
	for _, need := range p.Needs {
		if creds[need] == "" {
			return false
		}
	}
	return true
}

// KMSCredentialResolver resolves credentials from Hanzo KMS under org-scoped paths.
type KMSCredentialResolver struct {
	kms secret.Store
}

// NewKMSCredentialResolver constructs a KMS-backed credential resolver.
func NewKMSCredentialResolver(kms secret.Store) *KMSCredentialResolver {
	return &KMSCredentialResolver{kms: kms}
}

// defaultRef is where an org's default provider is kept: in the email subtree KMS
// keeps on the org's behalf (apps/kms custody.go).
func defaultRef(org string) string { return "orgs/" + org + "/email/provider" }

// ResolveEmailCredentials retrieves provider credentials for the given organization.
// A credential is read at orgs/<org>/notify/<provider>/<key>, where PUT
// /v1/notify/credentials/:provider/:key seals it, else at orgs/<org>/email/… or
// orgs/<org>/marketing/….
func (r *KMSCredentialResolver) ResolveEmailCredentials(ctx context.Context, org, pinnedProvider string) (string, map[string]string, error) {
	if org == "" {
		return "", nil, errors.New("email: organization is required")
	}
	if r.kms == nil {
		return "", nil, errors.New("email: KMS client not configured")
	}
	kctx := cloud.For(context.Background(), org)
	if d, ok := ctx.Deadline(); ok {
		var cancel context.CancelFunc
		kctx, cancel = context.WithDeadline(kctx, d)
		defer cancel()
	}
	preferred := ""
	if pinnedProvider == "" {
		if v, err := r.kms.GetSecret(kctx, defaultRef(org)); err == nil {
			preferred = strings.TrimSpace(string(v))
		}
	}
	targets, err := order(pinnedProvider, preferred)
	if err != nil {
		return "", nil, err
	}
	for _, p := range targets {
		creds := make(map[string]string, len(p.Keys))
		for _, k := range p.Keys {
			for _, root := range []string{"notify", "email", "marketing"} {
				if sec, err := r.kms.GetSecret(kctx, fmt.Sprintf("orgs/%s/%s/%s/%s", org, root, p.ID, k)); err == nil && len(sec) > 0 {
					creds[k] = string(sec)
					break
				}
			}
		}
		if complete(p, creds) {
			return p.ID, creds, nil
		}
	}
	if pinnedProvider != "" {
		return "", nil, fmt.Errorf("%w: provider %s credentials not configured for org %s", ErrNoCredentials, pinnedProvider, org)
	}
	return "", nil, ErrNoCredentials
}

// SetDefault records org's default provider in KMS.
func (r *KMSCredentialResolver) SetDefault(ctx context.Context, org, provider string) error {
	if r.kms == nil {
		return errors.New("email: KMS client not configured")
	}
	return r.kms.PutSecret(cloud.For(ctx, org), defaultRef(org), []byte(provider))
}

// StaticCredentialResolver is an in-memory resolver useful for isolated testing.
type StaticCredentialResolver struct {
	// org -> provider -> key -> value
	Store map[string]map[string]map[string]string
	// Default is each org's default provider.
	Default map[string]string
}

// NewStaticCredentialResolver creates a static test credential resolver.
func NewStaticCredentialResolver() *StaticCredentialResolver {
	return &StaticCredentialResolver{
		Store:   make(map[string]map[string]map[string]string),
		Default: make(map[string]string),
	}
}

// Set stores a credential value in memory for testing.
func (s *StaticCredentialResolver) Set(org, provider, key, value string) {
	if s.Store[org] == nil {
		s.Store[org] = make(map[string]map[string]string)
	}
	if s.Store[org][provider] == nil {
		s.Store[org][provider] = make(map[string]string)
	}
	s.Store[org][provider][key] = value
}

// SetDefault records org's default provider in memory.
func (s *StaticCredentialResolver) SetDefault(_ context.Context, org, provider string) error {
	s.Default[org] = provider
	return nil
}

// ResolveEmailCredentials retrieves credentials from the in-memory map.
func (s *StaticCredentialResolver) ResolveEmailCredentials(_ context.Context, org, pinnedProvider string) (string, map[string]string, error) {
	if org == "" {
		return "", nil, errors.New("email: organization is required")
	}
	targets, err := order(pinnedProvider, s.Default[org])
	if err != nil {
		return "", nil, err
	}
	for _, p := range targets {
		// Every key set is answered, a test's endpoint among them, so a test
		// points a provider at a server of its own and never at the provider.
		creds := maps.Clone(s.Store[org][p.ID])
		if complete(p, creds) {
			return p.ID, creds, nil
		}
	}
	if pinnedProvider != "" {
		return "", nil, fmt.Errorf("%w: provider %s credentials not configured for org %s", ErrNoCredentials, pinnedProvider, org)
	}
	return "", nil, ErrNoCredentials
}
