// Copyright © 2026 Hanzo AI. MIT License.

package notify

import (
	"context"

	"github.com/hanzoai/cloud"
)

// Send delivers ONE message through the notify rail in-process, resolving the
// provider from the org's KMS credentials and constructing the notifyd provider
// exactly as POST /v1/notify/send?sync=true does. It IS the one platform sender:
// the HTTP handler (handleSend) and every in-process caller (e.g. the marketing
// drip engine sending sequence steps) funnel through the SAME sendReal delivery
// path, so there is never a second sender and never a divergence in how a
// provider is chosen or built.
//
// The caller supplies the KMS client (cloud.Deps.KMS / cloud.Base.KMS) and the
// VALIDATED org — a background task carries no request principal, so the org is
// the task's owner-scoped value, never a client header. provider "" selects the
// org's configured default for the channel; channel is "email" or "sms". It
// returns the provider service it used (for the caller's audit log) and any
// delivery error. A missing credential fails closed inside constructProvider.
//
// Send performs NO suppression/opt-out check — that policy is the caller's, so
// transactional sends (IAM OTP) are never suppressed while marketing sends pass
// through the marketing suppression gate before reaching here. One sender, one
// place; the opt-out decision stays with the sender that owns the audience.
func Send(ctx context.Context, kms cloud.KMSClient, org, channel, provider string, to []string, subject, body string) (usedProvider string, err error) {
	s := &service{kms: kms}
	return s.sendReal(ctx, org, channel, provider, to, subject, body)
}
