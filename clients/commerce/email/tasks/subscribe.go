package tasks

import (
	"context"
	"errors"

	"github.com/hanzoai/cloud/clients/commerce/delay"

	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/mailchimp"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/sendgrid"
	"github.com/hanzoai/cloud/clients/commerce/types/email"
	"github.com/hanzoai/cloud/clients/commerce/types/integration"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
)

func getEmailMarketer(c context.Context, in integration.Integration) (email.Marketer, error) {
	switch in.Type {
	case integration.MailchimpType:
		log.Info("Using Mailchimp: %v", json.Encode(in.Mailchimp), c)
		return mailchimp.New(c, in.Mailchimp), nil
	case integration.SendGridType:
		log.Info("Using SendGrid: %v", json.Encode(in.SendGrid), c)
		return sendgrid.New(c, in.SendGrid), nil
	default:
		log.Error("Invalid Email Marketing Provider", c)
		return nil, errors.New("Invalid Email Provider")
	}
}

// Subscribe contact to mailing list with appropriate provider
var Subscribe = delay.Func("email-subscribe", func(c context.Context, in integration.Integration, l email.List, sub email.Subscriber) {
	log.Debug("Adding subscriber %s to external email list %s", sub.Email, l, c)
	provider, err := getEmailMarketer(c, in)
	if err != nil {
		log.Error("Email provider integration not found: %v", err, c)
		panic(err)
	}
	err = provider.Subscribe(&l, &sub)
	if err != nil {
		log.Error("Email provider error: %v", err, c)
		// panic(err)
	}
})
