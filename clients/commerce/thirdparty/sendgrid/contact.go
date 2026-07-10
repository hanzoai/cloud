package sendgrid

import (
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/types/email"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
)

type Error struct {
	Message      string `json:"message"`
	ErrorIndices []int  `json:"error_indices"`
}

type ContactResponse struct {
	NewCount            int      `json:"new_count"`
	UpdatedCounti       int      `json:"unmodified_count"`
	ErrorCount          int      `json:"error_count"`
	ErrorIndices        []int    `json:"error_indices"`
	Errors              []Error  `json:"errors"`
	PersistedRecipients []string `json:"persisted_recipients"`
}

type Contact struct {
	Id string `json:"id"`
}

func newContact(s *email.Subscriber) []byte {
	m := []map[string]interface{}{s.Metadata}
	if m[0] == nil {
		m[0] = map[string]interface{}{}
	}
	m[0]["email"] = s.Email.Address

	return json.EncodeBytes(m)
}

// Update or create contact if it doesn't exist
func (api API) UpdateContact(sub *email.Subscriber) (*Contact, error) {
	c := api.Context

	cont := newContact(sub)
	log.Info("New Contact %s", cont, c)

	res, err := api.Request("PATCH", "/v3/contactdb/recipients", nil, cont)
	if err != nil {
		return nil, log.Error("Failed to create contact: %v", err, c)
	}
	log.Info(res.StatusCode, c)
	log.Info(res.Body, c)
	log.Info(res.Headers, c)

	// Decode response and get contact details
	contactRes := new(ContactResponse)
	json.DecodeBytes([]byte(res.Body), contactRes)

	return &Contact{contactRes.PersistedRecipients[0]}, nil
}
