package mailchimp

import (
	"github.com/hanzoai/gochimp3"

	"github.com/hanzoai/cloud/clients/commerce/log"
	// "github.com/hanzoai/cloud/clients/commerce/models/form"
	// "github.com/hanzoai/cloud/clients/commerce/models/subscriber"
	// "github.com/hanzoai/cloud/clients/commerce/models/types/client"
	"github.com/hanzoai/cloud/clients/commerce/types/email"
	"github.com/hanzoai/cloud/clients/commerce/util/json"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

func (api API) Subscribe(list *email.List, sub *email.Subscriber) error {
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

		status := "subscribed"
		if list.DoubleOptin {
			status = "pending"
		}

		metadata := sub.Metadata

		if metadata == nil {
			metadata = map[string]interface{}{}
		}

		if metadata["FNAME"] == nil {
			metadata["FNAME"] = sub.FirstName
		}

		if metadata["LNAME"] == nil {
			metadata["LNAME"] = sub.LastName
		}

		req := &gochimp3.MemberRequest{
			EmailAddress: sub.Email.Address,
			Status:       status,
			StatusIfNew:  status,
			MergeFields:  metadata,
			Interests:    make(map[string]bool),
			// Language:     sub.Client.Language,
			VIP: false,
			Location: &gochimp3.MemberLocation{
				Latitude:  0.0,
				Longitude: 0.0,
				GMTOffset: 0,
				DSTOffset: 0,
				// CountryCode: s.Client.Country,
				Timezone: "",
			},
			Tags: sub.Tags,
		}

		log.Info("Update or create list member: %v", json.Encode(req), c)

		// Try to update subscriber, create new member if that fails.
		log.Info("Update list member '%v'", sub.Email, c)
		if _, err := mcl.UpdateMember(sub.Md5(), req); err != nil {
			log.Info("Create list member '%v'", sub.Email, c)
			if _, err := mcl.CreateMember(req); err != nil {
				log.Info("Failed create Mailchimp list member '%v': %v", sub.Email, json.Encode(err), c)
				return err
			}
		}

		return nil
	})
}

func (api API) SubscribeCustomer(listId string, buy Buyer, referralUrl string) *Error {
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
				"REFERRAL": referralUrl,
			},
		}
		return api.Subscribe(list, sub)
	})
}

func (api API) SubscribeForm(listId, emailStr, firstName, lastName string) *Error {
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
			FirstName: firstName,
			LastName:  lastName,
			Email: email.Email{
				Address: emailStr,
			},
			Metadata: map[string]interface{}{
				"FNAME": firstName,
				"LNAME": lastName,
			},
		}
		return api.Subscribe(list, sub)
	})
}
