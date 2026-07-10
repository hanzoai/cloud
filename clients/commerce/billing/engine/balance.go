package engine

import (
	"fmt"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/balancetransaction"
	"github.com/hanzoai/cloud/clients/commerce/models/customerbalance"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
)

// GetOrCreateCustomerBalance retrieves the customer balance for a given
// customer+currency, creating it if it doesn't exist.
func GetOrCreateCustomerBalance(db *datastore.Datastore, customerId string, cur currency.Type) (*customerbalance.CustomerBalance, error) {
	if cur == "" {
		cur = "usd"
	}

	rootKey := db.NewKey("synckey", "", 1, nil)
	balances := make([]*customerbalance.CustomerBalance, 0)
	q := customerbalance.Query(db).Ancestor(rootKey).
		Filter("CustomerId=", customerId).
		Filter("Currency=", string(cur)).
		Limit(1)

	if _, err := q.GetAll(&balances); err == nil && len(balances) > 0 {
		return balances[0], nil
	}

	// Create new balance record
	cb := customerbalance.New(db)
	cb.CustomerId = customerId
	cb.Currency = cur
	cb.Balance = 0

	if err := cb.Create(); err != nil {
		return nil, fmt.Errorf("failed to create customer balance: %w", err)
	}

	return cb, nil
}

// AdjustCustomerBalance adjusts a customer's balance and creates a ledger entry.
func AdjustCustomerBalance(db *datastore.Datastore, customerId string, amount int64, cur currency.Type, txnType, description string) (*balancetransaction.BalanceTransaction, error) {
	cb, err := GetOrCreateCustomerBalance(db, customerId, cur)
	if err != nil {
		return nil, err
	}

	cb.Balance += amount
	if err := cb.Update(); err != nil {
		return nil, fmt.Errorf("failed to update balance: %w", err)
	}

	// Create ledger entry
	bt := balancetransaction.New(db)
	bt.CustomerId = customerId
	bt.Amount = amount
	bt.Currency = cur
	bt.Type = txnType
	bt.Description = description
	bt.EndingBalance = cb.Balance

	if err := bt.Create(); err != nil {
		return nil, fmt.Errorf("failed to create balance transaction: %w", err)
	}

	return bt, nil
}

// ApplyBalanceToInvoice deducts from customer balance to pay an invoice.
// Returns the amount applied and the ledger entry.
func ApplyBalanceToInvoice(db *datastore.Datastore, customerId, invoiceId string, amountDue int64, cur currency.Type) (int64, *balancetransaction.BalanceTransaction, error) {
	cb, err := GetOrCreateCustomerBalance(db, customerId, cur)
	if err != nil {
		return 0, nil, err
	}

	if cb.Balance <= 0 {
		return 0, nil, nil
	}

	// Apply up to the available balance
	applied := amountDue
	if applied > cb.Balance {
		applied = cb.Balance
	}

	cb.Balance -= applied
	if err := cb.Update(); err != nil {
		return 0, nil, fmt.Errorf("failed to update balance: %w", err)
	}

	bt := balancetransaction.New(db)
	bt.CustomerId = customerId
	bt.Amount = -applied // debit
	bt.Currency = cur
	bt.Type = "invoice_payment"
	bt.InvoiceId = invoiceId
	bt.Description = fmt.Sprintf("Invoice payment: %d cents", applied)
	bt.EndingBalance = cb.Balance

	if err := bt.Create(); err != nil {
		return applied, nil, fmt.Errorf("failed to create balance transaction: %w", err)
	}

	return applied, bt, nil
}
