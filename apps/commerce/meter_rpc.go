// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/metering"
)

// The prepaid GATE and the debit, published on the internal plane for the same
// reason the balance read is: the ledger has one writer and it lives here.
//
// Without these an app in its own process finds no metering client, and
// ResourceMeter.Gate is documented to ALLOW when billing is unconfigured — which
// is right for a deployment that does not bill and catastrophic for one that does.
// Split into per-app binaries, every priced create became free: the gate could not
// tell "nobody bills here" from "the biller is one socket away".
const (
	authorizeMethod = "finance.authorize"
	recordMethod    = "finance.record"
)

// AuthorizeRequest gates one priced act before it runs.
type AuthorizeRequest struct {
	Subject          string `json:"subject"`
	AmountCents      int64  `json:"amountCents"`
	Project          string `json:"project"`
	ProjectValidated bool   `json:"projectValidated"`
	Service          string `json:"service"`
	Currency         string `json:"currency"`
}

// AuthorizeReply says whether the act may proceed, and if not, in the vocabulary
// the caller renders: an out-of-funds refusal and a cap refusal are different
// remedies and must not collapse into one error string.
type AuthorizeReply struct {
	OK       bool `json:"ok"`
	NoFunds  bool `json:"noFunds"`
	CapSpent bool `json:"capSpent"`
	// Reason carries anything that is neither of the above — an upstream failure
	// the caller must treat as "unknown", never as "allowed".
	Reason string `json:"reason,omitempty"`
}

// RecordRequest is one debit against the caller's org ledger.
type RecordRequest struct {
	Subject     string `json:"subject"`
	AmountCents int64  `json:"amountCents"`
	Model       string `json:"model"`
	Project     string `json:"project"`
	Provider    string `json:"provider"`
	Service     string `json:"service"`
	RequestID   string `json:"requestId"`
	ClientIP    string `json:"clientIp"`
	Currency    string `json:"currency"`
}

// exposeMeter publishes the gate and the debit. Mount calls it.
func exposeMeter(m *metering.Client) {
	cloud.Expose(authorizeMethod, func(ctx context.Context, who cloud.Ident, req []byte) ([]byte, error) {
		var in AuthorizeRequest
		if err := json.Unmarshal(req, &in); err != nil {
			return nil, fmt.Errorf("authorize: decode: %w", err)
		}
		org := who.Org
		if org == "" {
			return nil, fmt.Errorf("authorize: no org on the capability")
		}
		if m == nil || !m.Enabled() {
			// This process owns the money plane. If ITS meter is unconfigured the
			// honest answer is "unknown", not "allowed" — the caller fails closed on
			// a reason it can log, rather than handing out work nobody can bill.
			return json.Marshal(AuthorizeReply{Reason: "commerce has no metering client"})
		}
		subject := in.Subject
		if subject == "" {
			subject = org
		}
		err := m.Authorize(ctx, metering.AuthInput{
			User: subject, Org: org,
			AmountCents:      in.AmountCents,
			Project:          in.Project,
			ProjectValidated: in.ProjectValidated,
			Service:          in.Service,
			Currency:         in.Currency,
		})
		switch {
		case err == nil:
			return json.Marshal(AuthorizeReply{OK: true})
		case errors.Is(err, metering.ErrInsufficientBalance):
			return json.Marshal(AuthorizeReply{NoFunds: true})
		case errors.Is(err, metering.ErrSpendCapExceeded):
			return json.Marshal(AuthorizeReply{CapSpent: true})
		default:
			return json.Marshal(AuthorizeReply{Reason: err.Error()})
		}
	})

	cloud.Expose(recordMethod, func(ctx context.Context, who cloud.Ident, req []byte) ([]byte, error) {
		var in RecordRequest
		if err := json.Unmarshal(req, &in); err != nil {
			return nil, fmt.Errorf("record: decode: %w", err)
		}
		org := who.Org
		if org == "" {
			return nil, fmt.Errorf("record: no org on the capability")
		}
		if m == nil || !m.Enabled() {
			return nil, fmt.Errorf("record: commerce has no metering client")
		}
		subject := in.Subject
		if subject == "" {
			subject = org
		}
		// User and Org are set HERE from the capability, never from the payload: a
		// caller that could name the billed org could bill someone else.
		if _, err := m.Record(ctx, metering.Usage{
			User: subject, Org: org,
			AmountCents: in.AmountCents,
			Model:       in.Model,
			Project:     in.Project,
			Provider:    in.Provider,
			Service:     in.Service,
			RequestID:   in.RequestID,
			ClientIP:    in.ClientIP,
			Currency:    in.Currency,
			Status:      "success",
		}); err != nil {
			return nil, fmt.Errorf("record: %w", err)
		}
		return []byte(`{"ok":true}`), nil
	})
}
