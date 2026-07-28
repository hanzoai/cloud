// Copyright © 2026 Hanzo AI. MIT License.

package cloud

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// KMSPeer is the KMS client for a process that does not hold the sealed store —
// which is every process but one.
//
// It replaces clients.KMSRPCAt, whose methods all returned "not yet wired
// (zapc-gen pending)". pickKMSClient reached that stub only when an address was
// configured and otherwise handed back DisabledKMS, so once apps became their own
// binaries they had no secrets at all and said nothing about it: a mail provider
// stored through the KMS app read back as "no email provider configured for org".
//
// The tenant comes from the REF, which is already fully qualified
// ("orgs/<org>/notify/mail/..." is what every caller writes and what the store
// resolves). It is echoed onto the capability so the two always agree, and the
// callee refuses a ref outside the org it was called for — that catches an app
// asking for one tenant while acting for another, which is a bug worth failing on.
//
// It is NOT a trust boundary between our own apps: the socket is 0600 and
// SO_PEERCRED-authenticated, so a peer is already one of ours and could name any
// org it liked. Tenancy is enforced where a request principal is resolved. Saying
// so here keeps the check honest about what it is.
type KMSPeer struct{}

// secret is the wire shape shared with the KMS app's methods. Values are base64
// because a secret is bytes and a JSON string is not a safe carrier for them.
type secret struct {
	Ref   string `json:"ref"`
	Value string `json:"value,omitempty"`
}

const kmsPeerTimeout = 10 * time.Second

func (k KMSPeer) call(ctx context.Context, method string, in secret) (string, error) {
	// A ref names its tenant ("orgs/<org>/…") or it names none, which makes it the
	// DEPLOYMENT's own material — the marketing unsubscribe HMAC, for one. Both are
	// legitimate, so the org is echoed when there is one and left empty when there
	// is not, rather than a platform identity being invented to fill the slot.
	org := orgOf(in.Ref)
	body, err := json.Marshal(in)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, kmsPeerTimeout)
	defer cancel()
	out, err := Dial("kms").For(org).Call(ctx, method, body)
	if err != nil {
		return "", fmt.Errorf("kms %s: %w", method, err)
	}
	var reply struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(out, &reply); err != nil {
		return "", fmt.Errorf("kms %s: decode: %w", method, err)
	}
	return reply.Value, nil
}

// GetSecret reads one secret. A missing secret is an error, never empty bytes:
// callers treat empty as "not configured" and fail closed on it, so returning
// empty for a transport failure would read as a deliberate absence.
func (k KMSPeer) GetSecret(ctx context.Context, ref string) ([]byte, error) {
	v, err := k.call(ctx, "kms.get", secret{Ref: ref})
	if err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(v)
}

// PutSecret writes one secret.
func (k KMSPeer) PutSecret(ctx context.Context, ref string, value []byte) error {
	_, err := k.call(ctx, "kms.put", secret{Ref: ref, Value: base64.StdEncoding.EncodeToString(value)})
	return err
}

// Sign signs a payload with a key that never leaves the store's process — which is
// the reason signing is a method here rather than a key fetch.
func (k KMSPeer) Sign(ctx context.Context, keyRef string, payload []byte) ([]byte, error) {
	v, err := k.call(ctx, "kms.sign", secret{Ref: keyRef, Value: base64.StdEncoding.EncodeToString(payload)})
	if err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(v)
}

// orgOf reads the tenant out of a fully-qualified ref. Refs are "orgs/<org>/…";
// anything else names deployment material rather than a tenant's and has no org to
// act for.
func orgOf(ref string) string {
	const p = "orgs/"
	if !strings.HasPrefix(ref, p) {
		return ""
	}
	rest := ref[len(p):]
	i := strings.IndexByte(rest, '/')
	if i <= 0 {
		return ""
	}
	return rest[:i]
}
