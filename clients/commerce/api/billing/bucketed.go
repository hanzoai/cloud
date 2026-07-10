package billing

import (
	"context"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/billing/bucket"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/paymentmethod"
	txutil "github.com/hanzoai/cloud/clients/commerce/models/transaction/util"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
)

// bucketedSplit fetches the raw ledger for a subject+currency and derives the
// three-bucket split (credits granted/remaining, prepaid, holds) — the honest
// projection of the SAME transactions the gateway balance gate reads.
func bucketedSplit(ctx context.Context, subject string, cur currency.Type, test bool) (bucket.Split, error) {
	raw, err := txutil.GetRawByCurrency(ctx, subject, "iam-user", cur, test)
	if err != nil {
		return bucket.Split{}, err
	}
	return bucket.Compute(raw, subject, time.Now()), nil
}

// cardOnFile is the card-payment-method status for a subject: whether a
// chargeable card is vaulted, and (best-effort) the default card's brand/last4.
// A "card on file" is a payment method that can actually be charged — a card
// type OR any method carrying a vaulted provider reference (Square card-on-file).
type cardOnFileStatus struct {
	OnFile    bool   `json:"onFile"`
	Brand     string `json:"brand,omitempty"`
	Last4     string `json:"last4,omitempty"`
	IsDefault bool   `json:"isDefault,omitempty"`
}

// getCardOnFile reports whether the subject has a chargeable card vaulted. The
// GPU charge gate requires this to be true before a real-money GPU debit.
func getCardOnFile(db *datastore.Datastore, subject string) cardOnFileStatus {
	subject = strings.ToLower(strings.TrimSpace(subject))
	if subject == "" {
		return cardOnFileStatus{}
	}
	rootKey := db.NewKey("synckey", "", 1, nil)
	iter := paymentmethod.Query(db).Ancestor(rootKey).
		Filter("CustomerId=", subject).
		Run()

	var status cardOnFileStatus
	for {
		pm := paymentmethod.New(db)
		if _, err := iter.Next(pm); err != nil {
			break
		}
		// A chargeable card: a card type OR a vaulted provider ref (Square
		// card-on-file). A bare bank/wire/paypal method is not a GPU-chargeable card.
		chargeable := pm.Card != nil || strings.TrimSpace(pm.ProviderRef) != ""
		if !chargeable {
			continue
		}
		status.OnFile = true
		// Prefer the default card's display details; otherwise the first chargeable.
		if pm.IsDefault || status.Brand == "" {
			if pm.Card != nil {
				status.Brand = pm.Card.Brand
				status.Last4 = pm.Card.Last4
			}
			status.IsDefault = status.IsDefault || pm.IsDefault
		}
		if pm.IsDefault {
			// A default card is authoritative — stop scanning.
			break
		}
	}
	return status
}

// bucketFields merges the three-bucket split + card status into a balance
// response. It is ADDITIVE — the caller keeps its existing balance/holds/available
// fields; these expose the split so the console can show credits (granted/left)
// distinctly from prepaid real money and the card status. The cloud-api customer
// billing proxy forwards this body verbatim, so the fields reach the console
// with no cloud change.
func bucketFields(split bucket.Split, card cardOnFileStatus) gin.H {
	return gin.H{
		"creditsGranted":   int64(split.CreditsGranted),
		"creditsRemaining": int64(split.CreditsRemaining),
		"prepaidBalance":   int64(split.PrepaidBalance),
		"prepaidAvailable": int64(split.PrepaidAvailable),
		// Consumer-facing aliases: "trial" == the non-cash credit bucket
		// (starter/welcome/comp grants), "prepaid" == real money. Same values as
		// creditsGranted/creditsRemaining, named unambiguously for the wallet/
		// credit modal ("$5 trial + $X.XX credits"). Additive — never removes the
		// existing fields.
		"trialGranted": int64(split.CreditsGranted),
		"trialBalance": int64(split.CreditsRemaining),
		"card":         card,
	}
}
