package types

import (
	"encoding/json"
)

type Response struct {
	// Randomly returns errors in different places
	Error  string  `json:"error,omitempty"`
	Errors []Error `json:"errors,omitempty"`

	Status           int    `json:"status"`
	Message          string `json:"message"`
	ResourceLocation string `json:"resourceLocation"`
	Resource         struct {
		Items []struct {
			ResourceLocation string          `json:"resourceLocation"`
			Resource         json.RawMessage `json:"resource"`
		} `json:"items"`
	} `json:"resource"`
}
