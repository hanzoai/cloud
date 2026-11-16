package identity

import (
	"errors"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// Delegation defines bounded permissions for an agent
type Delegation struct {
	ID             string         `json:"id"`
	AgentAddress   common.Address `json:"agent_address"`
	OwnerAddress   common.Address `json:"owner_address"`
	SpendingLimit  *big.Int       `json:"spending_limit"`
	SpentAmount    *big.Int       `json:"spent_amount"`
	AllowedMethods []string       `json:"allowed_methods"` // Scoped tasks
	ExpiresAt      time.Time      `json:"expires_at"`
	Revoked        bool           `json:"revoked"`
}

var (
	ErrUnauthorized = errors.New("agent not authorized for this scope")
	ErrLimitExceeded = errors.New("spending limit exceeded")
	ErrExpired      = errors.New("delegation expired")
	ErrRevoked      = errors.New("delegation has been revoked")
)

// Enforce checks if the agent can perform a specific action within the delegation bounds
func (d *Delegation) Enforce(method string, cost *big.Int) error {
	if d.Revoked {
		return ErrRevoked
	}
	if time.Now().After(d.ExpiresAt) {
		return ErrExpired
	}

	// Check scope
	allowed := false
	for _, m := range d.AllowedMethods {
		if m == "*" || m == method {
			allowed = true
			break
		}
	}
	if !allowed {
		return ErrUnauthorized
	}

	// Check spending limit
	newTotal := new(big.Int).Add(d.SpentAmount, cost)
	if d.SpendingLimit.Cmp(newTotal) < 0 {
		return ErrLimitExceeded
	}

	d.SpentAmount = newTotal
	return nil
}
