package types

import (
	"encoding/json"
	"time"
)

// Webhook request
type Request struct {
	Topic                 string    `json:"topic"`
	Attempt               int       `json:"attempt"`
	Timestamp             time.Time `json:"timestamp"`
	UniqueEventId         string    `json:"uniqueEventID"`
	WebhookSubscriptionId int       `json:"webhookSubscriptionID"`
	Body                  Body      `json:"body"`
}

// Webhook request body
type Body struct {
	Status           int             `json:"status"`
	Message          string          `json:"message"`
	Resource         json.RawMessage `json:"resource"`
	ResourceLocation string          `json:"resourceLocation"`
}

// Webhook request resource
type Resource struct {
	Offset   int    `json:"offset"`
	Total    int    `json:"total"`
	Previous string `json:"previous"`
	Next     string `json:"next"`
	Items    []struct {
		ResourceLocation string          `json:"resourceLocation"`
		Resource         json.RawMessage `json:"resource"`
	} `json:"items"`
}
