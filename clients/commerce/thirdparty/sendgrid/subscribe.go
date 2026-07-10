package sendgrid

import (
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/types/email"
)

// Add subscriber to list
func (api API) Subscribe(list *email.List, sub *email.Subscriber) error {
	c := api.Context

	// Ensure contact exists
	contact, err := api.UpdateContact(sub)
	if err != nil {
		return err
	}

	// Add contact to list
	res, err := api.Request("POST", "/v3/contactdb/lists/"+list.Id+"/recipients/"+contact.Id, nil, nil)
	if err != nil {
		return log.Error("Failed to add contact to list: %v", err, c)
	}
	log.Info(res.StatusCode, c)
	log.Info(res.Body, c)
	log.Info(res.Headers, c)

	return nil
}
