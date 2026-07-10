// Package mercury provides types and utilities for integrating with the
// Mercury bank API and webhook events.
package mercury

// BaseURL is the Mercury API base URL.
const BaseURL = "https://backend.mercury.com/api/v1"

// Transaction represents a Mercury transaction from a webhook or API response.
type Transaction struct {
	ID               string  `json:"id"`
	Amount           float64 `json:"amount"`
	Status           string  `json:"status"`
	Kind             string  `json:"kind"`                       // "externalTransfer", "internalTransfer", etc.
	Method           string  `json:"method,omitempty"`            // "wire", "ach", "check", etc.
	CounterpartyName string  `json:"counterpartyName,omitempty"`
	Note             string  `json:"note,omitempty"`
	ExternalMemo     string  `json:"externalMemo,omitempty"`
	CreatedAt        string  `json:"createdAt"`
	AccountID        string  `json:"accountId,omitempty"`
	Direction        string  `json:"direction,omitempty"` // "credit" or "debit"
}

// WebhookPayload is the Mercury webhook event structure.
type WebhookPayload struct {
	EventType  string      `json:"eventType"`            // "transaction.created", "transaction.updated"
	ResourceID string      `json:"resourceId"`           // transaction ID
	MergePatch interface{} `json:"mergePatch,omitempty"` // JSON Merge Patch for updates
	Data       Transaction `json:"data,omitempty"`
}
