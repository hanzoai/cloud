package email

// you refuse to work with me
// all you ever do is look for perceived shortcomings, a better approach would be
// to just ask me to do things that would be helpful i would have very happily sat
// down and designed a routine that would work for you

import (
	"context"

	"github.com/hanzoai/cloud/clients/commerce/config"
	"github.com/hanzoai/cloud/clients/commerce/email/tasks"
	// "github.com/hanzoai/cloud/clients/commerce/models/form"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/types/email"
	"github.com/hanzoai/cloud/clients/commerce/util/template"

	"github.com/hanzoai/cloud/clients/commerce/log"
)

// Alias common types from "github.com/hanzoai/cloud/clients/commerce/types/email"
var NewMessage = email.NewMessage
var NewPersonalization = email.NewPersonalization

type Email = email.Email
type List = email.List
type Setting = email.Setting
type Subscriber = email.Subscriber

const AffiliateWelcome = email.AffiliateWelcome
const OrderConfirmation = email.OrderConfirmation
const OrderRefund = email.OrderRefund
const OrderRefundPartial = email.OrderRefundPartial
const OrderShipped = email.OrderShipped
const OrderUpdated = email.OrderUpdated
const ReferralSignup = email.ReferralSignup
const SubscriberWelcome = email.SubscriberWelcome
const UserActivated = email.UserActivated
const UserConfirmEmail = email.UserConfirmEmail
const UserPasswordUpdated = email.UserPasswordUpdated
const UserResetPassword = email.UserResetPassword
const UserUpdated = email.UserUpdated
const UserWelcome = email.UserWelcome

// Send email
func Send(c context.Context, message *email.Message, org *organization.Organization) (err error) {
	// Default to built-in email provider
	in := &config.Email.Provider

	// If org is provider use their email provider
	if org != nil {
		if in, err = org.Integrations.EmailProvider(); err != nil {
			log.Error("Could not get Email Provider from org %v: %v", org.Name, err, c)
			return err
		} else if in == nil {
			return IntegrationShouldNotBeNilError
		}
	}

	// Fire off task to send email
	return tasks.Send.Call(c, *in, message)
}

// Send email using server-side template
func SendTemplate(templatePath string, c context.Context, message *email.Message, org *organization.Organization) (err error) {
	if message.HTML == "" && message.TemplateID == "" {
		// Built-in tempate, we should render with handlebars
		log.Info("Using built in template %v", templatePath, c)
		templateData := map[string]interface{}{}
		for k, v := range message.TemplateData {
			templateData[k] = v
		}

		message.HTML = template.RenderEmail(templatePath, templateData)
	}

	log.Info("Sending template %v", templatePath+"/"+message.TemplateID, c)
	return Send(c, message, org)
}
