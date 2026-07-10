package billing

import (
	"context"
	"fmt"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/paymentmethod"
	"github.com/hanzoai/cloud/clients/commerce/payment/processor"
)

// squareCustomerProcessor is the subset of processor.CustomerProcessor needed to
// vault a card on file: create/reuse a customer and attach/detach a card. The
// real *square.SquareProcessor satisfies this; the narrow interface keeps the
// billing handlers decoupled from the Square SDK and easy to unit test.
type squareCustomerProcessor interface {
	processor.PaymentProcessor

	// CreateCustomer creates a customer profile and returns its id.
	CreateCustomer(ctx context.Context, email, name string, metadata map[string]interface{}) (string, error)

	// AddPaymentMethod attaches a single-use card nonce to a customer and
	// returns the durable card-on-file id.
	AddPaymentMethod(ctx context.Context, customerID, token string) (string, error)

	// RemovePaymentMethod detaches a card-on-file from a customer.
	RemovePaymentMethod(ctx context.Context, customerID, paymentMethodID string) error
}

// squareCardOnFile holds the durable identifiers for a vaulted Square card.
// A charge against the saved card needs BOTH: SourceID = CardID and the Square
// CustomerID (a card-on-file id is only chargeable in its customer's context).
type squareCardOnFile struct {
	CustomerID string // Square customer id (one per org)
	CardID     string // Square card-on-file id (reusable)
}

// squareCustomerProcessorFrom returns the Square processor from a per-org
// registry as a squareCustomerProcessor, or (nil, false) when Square is not
// configured for the org or doesn't support customer/card-on-file management.
func squareCustomerProcessorFrom(reg *processor.Registry) (squareCustomerProcessor, bool) {
	p, err := reg.Get(processor.Square)
	if err != nil {
		return nil, false
	}
	cp, ok := p.(squareCustomerProcessor)
	if !ok || !cp.IsAvailable(context.Background()) {
		return nil, false
	}
	return cp, true
}

// existingSquareCustomerID returns the Square customer id already associated
// with this org's saved cards (one Square customer per org), or "" if none.
func existingSquareCustomerID(db *datastore.Datastore, customerID string) string {
	rootKey := db.NewKey("synckey", "", 1, nil)
	iter := paymentmethod.Query(db).Ancestor(rootKey).Filter("CustomerId=", customerID).Run()
	for {
		pm := paymentmethod.New(db)
		if _, err := iter.Next(pm); err != nil {
			break
		}
		if pm.Metadata == nil {
			continue
		}
		if v, ok := pm.Metadata["squareCustomerId"].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// attachSquareCardOnFile converts a single-use Square Web Payments nonce into a
// reusable card-on-file. It reuses existingCustomerID when non-empty (one Square
// customer per org), otherwise creates a new Square customer identified by
// name/email.
//
// Creating the card on file also validates the card with Square, so this both
// verifies AND saves in a single nonce use. A separate $1 pre-auth would consume
// the single-use nonce and make the subsequent attach fail — that's why the
// card-on-file path replaces verifyCardWithPreAuth rather than running after it.
func attachSquareCardOnFile(ctx context.Context, cp squareCustomerProcessor, existingCustomerID, email, name, nonce string) (squareCardOnFile, error) {
	customerID := existingCustomerID
	if customerID == "" {
		meta := map[string]interface{}{}
		if name != "" {
			meta["note"] = "Hanzo billing customer " + name
		}
		id, err := cp.CreateCustomer(ctx, email, name, meta)
		if err != nil {
			return squareCardOnFile{}, fmt.Errorf("create square customer: %w", err)
		}
		customerID = id
	}

	cardID, err := cp.AddPaymentMethod(ctx, customerID, nonce)
	if err != nil {
		return squareCardOnFile{}, err
	}
	return squareCardOnFile{CustomerID: customerID, CardID: cardID}, nil
}
