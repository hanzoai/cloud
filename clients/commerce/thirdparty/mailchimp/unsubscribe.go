package mailchimp

import (
	"github.com/hanzoai/cloud/clients/commerce/log"
	// "github.com/hanzoai/cloud/clients/commerce/models/form"
	// "github.com/hanzoai/cloud/clients/commerce/models/subscriber"
	// "github.com/hanzoai/cloud/clients/commerce/models/types/client"
	"github.com/hanzoai/cloud/clients/commerce/types/email"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

func (api API) Unsubscribe(list *email.List, sub *email.Subscriber) error {
	c := api.Context

	return wrapError(func() error {
		if list.Id == "" {
			log.Warn("Failed to subscribe, list ID missing: %v", list, c)
			return nil
		}

		mcl, err := api.Client.GetList(list.Id, nil)
		if err != nil {
			log.Error("Failed to subscribe %v: %v", sub, err, c)
			return err
		}

		// Try to update subscriber, create new member if that fails.
		log.Info("Delete list member '%v'", sub.Email, c)
		if _, err := mcl.DeleteMember(sub.Md5()); err == nil {
			log.Warn("Delete list member '%v' error '%v'", sub.Email, err, c)
		}

		return nil
	})
}

func (api API) UnsubscribeCustomer(listId string, buy Buyer) *Error {
	return wrapError(func() error {
		// f := new(form.Form)
		// f.EmailList.Id = listId
		// s := &subscriber.Subscriber{
		// 	Email:  buy.Email,
		// 	UserId: idOrEmail(buy.UserId, buy.Email),
		// 	Client: client.Client{
		// 		Country: buy.BillingAddress.Country,
		// 	},
		// 	Metadata: Map{
		// 		"FNAME":    buy.FirstName,
		// 		"LNAME":    buy.LastName,
		// 		"ADDRESS1": buy.BillingAddress.Line1,
		// 		"ADDRESS2": buy.BillingAddress.Line2,
		// 		"CITY":     buy.BillingAddress.City,
		// 		"STATE":    buy.BillingAddress.State,
		// 		"POSTAL":   buy.BillingAddress.PostalCode,
		// 		"COUNTRY":  buy.BillingAddress.Country,
		// 		"PHONE":    buy.Phone,
		// 		"REFERRAL": referralUrl,
		// 	},
		// }
		// return nil
		list := &email.List{
			Id: listId,
		}
		sub := &email.Subscriber{
			Email: email.Email{
				Address: buy.Email,
			},
			Metadata: map[string]interface{}{
				"FNAME":    buy.FirstName,
				"LNAME":    buy.LastName,
				"ADDRESS1": buy.BillingAddress.Line1,
				"ADDRESS2": buy.BillingAddress.Line2,
				"CITY":     buy.BillingAddress.City,
				"STATE":    buy.BillingAddress.State,
				"POSTAL":   buy.BillingAddress.PostalCode,
				"COUNTRY":  buy.BillingAddress.Country,
				"PHONE":    buy.Phone,
			},
		}
		return api.Unsubscribe(list, sub)
	})
}
