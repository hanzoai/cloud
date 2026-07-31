// Copyright © 2026 Hanzo AI. MIT License.

package cloud

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/cloud/plane"
)

// KMSPeer is the KMS client for a process that does not hold the sealed store —
// which is every process but one.
//
// The tenant comes from the REF, which is already fully qualified
// ("orgs/<org>/notify/mail/..." is what every caller writes and what the store
// resolves). It is stated on the call so the two always agree, and the callee
// refuses a ref outside the org it was called for — that catches an app asking
// for one tenant while acting for another, which is a bug worth failing on.
//
// It is NOT a trust boundary between our own apps: the socket is 0600 and
// SO_PEERCRED-authenticated, so a peer is already one of ours and could name
// any org it liked. Tenancy is enforced where a request principal is resolved.
// Saying so here keeps the check honest about what it is.
type KMSPeer struct{}

const kmsPeerTimeout = 10 * time.Second

func (k KMSPeer) call(ctx context.Context, op, ref string, value []byte) ([]byte, error) {
	// A ref names its tenant ("orgs/<org>/…") or it names none, which makes it the
	// DEPLOYMENT's own material — the marketing unsubscribe HMAC, for one. Both are
	// legitimate, so the org is stated when there is one and left empty when there
	// is not, rather than a platform identity being invented to fill the slot.
	ctx, cancel := context.WithTimeout(ctx, kmsPeerTimeout)
	defer cancel()
	if org := orgOf(ref); org != "" {
		ctx = For(ctx, org)
	}
	out, err := Ask[plane.SecretIn, plane.Secret](ctx, "kms", op, &plane.SecretIn{Ref: ref, Value: value})
	if err != nil {
		return nil, fmt.Errorf("kms %s: %w", op, err)
	}
	if out == nil {
		return nil, nil
	}
	return out.Value, nil
}

// GetSecret reads one secret. A missing secret is an error, never empty bytes:
// callers treat empty as "not configured" and fail closed on it, so returning
// empty for a transport failure would read as a deliberate absence.
func (k KMSPeer) GetSecret(ctx context.Context, ref string) ([]byte, error) {
	return k.call(ctx, plane.KMSGet, ref, nil)
}

// PutSecret writes one secret.
func (k KMSPeer) PutSecret(ctx context.Context, ref string, value []byte) error {
	_, err := k.call(ctx, plane.KMSPut, ref, value)
	return err
}

// Sign signs a payload with a key that never leaves the store's process — which
// is the reason signing is an op here rather than a key fetch.
func (k KMSPeer) Sign(ctx context.Context, keyRef string, payload []byte) ([]byte, error) {
	return k.call(ctx, plane.KMSSign, keyRef, payload)
}

// orgOf reads the tenant out of a fully-qualified ref. Refs are "orgs/<org>/…";
// anything else names deployment material rather than a tenant's and has no org
// to act for.
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
