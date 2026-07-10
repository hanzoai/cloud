package square

import (
	"context"
	"fmt"
	"strings"

	square "github.com/square/square-go-sdk/v3"
	"github.com/square/square-go-sdk/v3/customers"

	"github.com/hanzoai/cloud/clients/commerce/payment/processor"
)

// CreateCustomer creates a customer profile in Square.
// Returns the Square customer ID on success.
func (sp *SquareProcessor) CreateCustomer(ctx context.Context, email, name string, metadata map[string]interface{}) (string, error) {
	req := &square.CreateCustomerRequest{
		EmailAddress: square.String(email),
	}

	// Split name into given/family; Square stores them separately.
	if name != "" {
		parts := strings.SplitN(name, " ", 2)
		req.GivenName = square.String(parts[0])
		if len(parts) == 2 {
			req.FamilyName = square.String(parts[1])
		}
	}

	// Attach metadata as a note (Square has no generic metadata field).
	if note, ok := metadata["note"].(string); ok && note != "" {
		req.Note = square.String(note)
	}

	resp, err := sp.customersClient.Create(ctx, req)
	if err != nil {
		return "", fmt.Errorf("square create customer: %w", err)
	}
	if resp.Customer == nil || resp.Customer.ID == nil {
		return "", fmt.Errorf("square create customer: empty response")
	}
	return *resp.Customer.ID, nil
}

// GetCustomer retrieves a Square customer profile by ID.
func (sp *SquareProcessor) GetCustomer(ctx context.Context, customerID string) (map[string]interface{}, error) {
	resp, err := sp.customersClient.Get(ctx, &square.GetCustomersRequest{
		CustomerID: customerID,
	})
	if err != nil {
		return nil, fmt.Errorf("square get customer: %w", err)
	}
	if resp.Customer == nil {
		return nil, fmt.Errorf("square get customer: not found")
	}

	c := resp.Customer
	result := map[string]interface{}{
		"id": safeStr(c.ID),
	}
	if c.EmailAddress != nil {
		result["email"] = *c.EmailAddress
	}
	if c.GivenName != nil || c.FamilyName != nil {
		result["name"] = strings.TrimSpace(safeStr(c.GivenName) + " " + safeStr(c.FamilyName))
	}
	if c.Note != nil {
		result["note"] = *c.Note
	}
	if c.CreatedAt != nil {
		result["created_at"] = *c.CreatedAt
	}
	if c.UpdatedAt != nil {
		result["updated_at"] = *c.UpdatedAt
	}
	return result, nil
}

// UpdateCustomer updates mutable fields on a Square customer profile.
// Recognised keys: email, name, note.
func (sp *SquareProcessor) UpdateCustomer(ctx context.Context, customerID string, updates map[string]interface{}) error {
	req := &square.UpdateCustomerRequest{
		CustomerID: customerID,
	}

	if v, ok := updates["email"].(string); ok {
		req.EmailAddress = square.String(v)
	}
	if v, ok := updates["name"].(string); ok && v != "" {
		parts := strings.SplitN(v, " ", 2)
		req.GivenName = square.String(parts[0])
		if len(parts) == 2 {
			req.FamilyName = square.String(parts[1])
		}
	}
	if v, ok := updates["note"].(string); ok {
		req.Note = square.String(v)
	}

	_, err := sp.customersClient.Update(ctx, req)
	if err != nil {
		return fmt.Errorf("square update customer: %w", err)
	}
	return nil
}

// DeleteCustomer removes a Square customer profile.
func (sp *SquareProcessor) DeleteCustomer(ctx context.Context, customerID string) error {
	_, err := sp.customersClient.Delete(ctx, &square.DeleteCustomersRequest{
		CustomerID: customerID,
	})
	if err != nil {
		return fmt.Errorf("square delete customer: %w", err)
	}
	return nil
}

// AddPaymentMethod attaches a card nonce (token) to an existing Square customer.
// Returns the card-on-file ID.
func (sp *SquareProcessor) AddPaymentMethod(ctx context.Context, customerID, token string) (string, error) {
	resp, err := sp.customersClient.Cards.Create(ctx, &customers.CreateCustomerCardRequest{
		CustomerID: customerID,
		CardNonce:  token,
	})
	if err != nil {
		return "", fmt.Errorf("square add payment method: %w", err)
	}
	if resp.Card == nil || resp.Card.ID == nil {
		return "", fmt.Errorf("square add payment method: empty response")
	}
	return *resp.Card.ID, nil
}

// RemovePaymentMethod disables (deletes) a card on file from a Square customer.
func (sp *SquareProcessor) RemovePaymentMethod(ctx context.Context, customerID, paymentMethodID string) error {
	_, err := sp.customersClient.Cards.Delete(ctx, &customers.DeleteCardsRequest{
		CustomerID: customerID,
		CardID:     paymentMethodID,
	})
	if err != nil {
		return fmt.Errorf("square remove payment method: %w", err)
	}
	return nil
}

// safeStr dereferences a *string safely, returning "" for nil.
func safeStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// Compile-time assertion: SquareProcessor satisfies CustomerProcessor.
var _ processor.CustomerProcessor = (*SquareProcessor)(nil)
