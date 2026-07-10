package email

import (
	"context"

	"github.com/hanzoai/cloud/clients/commerce/email/tasks"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/form"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/subscriber"
)

func Subscribe(c context.Context, f *form.Form, s *subscriber.Subscriber, org *organization.Organization) (err error) {
	in, err := org.Integrations.EmailMarketingProvider()
	if err != nil {
		log.Error("Could not get Email Provider from org %v: %v", org.Name, err, c)
		return err
	} else if in == nil {
		return IntegrationShouldNotBeNilError
	}

	list := List{
		Id: f.EmailList.Id,
	}

	sub := Subscriber{
		Email: Email{
			Address: s.Email,
		},
		FirstName: s.FirstName,
		LastName:  s.LastName,
		Tags:      s.Tags,
	}

	return tasks.Subscribe.Call(c, *in, list, sub)
}
