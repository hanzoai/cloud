package email

import (
	"context"
	"encoding/gob"
	"strings"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/form"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/payment"
	"github.com/hanzoai/cloud/clients/commerce/models/referrer"
	"github.com/hanzoai/cloud/clients/commerce/models/subscriber"
	"github.com/hanzoai/cloud/clients/commerce/models/token"
	"github.com/hanzoai/cloud/clients/commerce/models/types/country"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/types/email"
	"github.com/hanzoai/cloud/clients/commerce/util/json"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

// Create new message using provided defaults
func message(settings email.Setting, org *organization.Organization) *email.Message {
	m := email.NewMessage()
	m.From = settings.From
	m.ReplyTo = settings.ReplyTo
	m.Subject = settings.Subject
	m.TemplateID = settings.TemplateId

	if org != nil {
		m.TemplateData["organization"] = map[string]interface{}{
			"id":      org.Id(),
			"name":    org.Name,
			"logourl": org.LogoUrl,
		}
	}

	return m
}

// Transactional email for user
func userMessage(settings email.Setting, usr *user.User, org *organization.Organization) *email.Message {
	m := message(settings, org)
	m.AddTos(email.Email{Name: usr.Name(), Address: usr.Email})

	user := map[string]interface{}{
		"id":        usr.Id(),
		"name":      usr.Name(),
		"firstName": usr.FirstName,
		"lastName":  usr.LastName,
		"email":     usr.Email,
	}
	m.TemplateData["user"] = user

	return m
}

// Transactional email for subscriber
func subscriberMessage(settings email.Setting, sub *subscriber.Subscriber, org *organization.Organization) *email.Message {
	m := message(settings, org)
	m.AddTos(email.Email{Name: sub.Name(), Address: sub.Email})

	subscriber := map[string]interface{}{
		"id":   sub.Id(),
		"name": sub.Name(),
	}
	m.TemplateData["subscriber"] = subscriber

	return m
}

// Transactional email related to an order
func orderMessage(settings email.Setting, ord *order.Order, usr *user.User, pay *payment.Payment, org *organization.Organization) *email.Message {
	m := userMessage(settings, usr, org)

	currencyCode := strings.ToUpper(ord.Currency.Code())
	countryName := country.ByISO3166_2[ord.ShippingAddress.Country].Name.Common
	stateName := ord.ShippingAddress.State
	if len(stateName) <= 2 {
		stateName = strings.ToUpper(stateName)
	}
	items := make([]map[string]interface{}, len(ord.Items))

	for i, item := range ord.Items {
		items[i] = map[string]interface{}{
			"productName":  item.ProductName,
			"quantity":     item.Quantity,
			"price":        item.DisplayPrice(ord.Currency),
			"currencyCode": currencyCode,
		}
	}

	// Include all relevant order information
	order := map[string]interface{}{
		"number": ord.Number,
		// "id":        ord.DisplayId(),
		"subtotal":  ord.DisplaySubtotal(),
		"tax":       ord.DisplayTax(),
		"shipping":  ord.DisplayShipping(),
		"total":     ord.DisplayTotal(),
		"refunded":  ord.DisplayRefunded(),
		"remaining": ord.DisplayRemaining(),
		"currency":  currencyCode,
		"items":     items,
		"billingAddress": map[string]interface{}{
			"name":       strings.Title(ord.BillingAddress.Name),
			"line1":      strings.Title(ord.BillingAddress.Line1),
			"line2":      strings.Title(ord.BillingAddress.Line2),
			"postalCode": ord.BillingAddress.PostalCode,
			"city":       strings.Title(ord.BillingAddress.City),
			"state":      stateName,
			"country":    countryName,
		},
		"shippingAddress": map[string]interface{}{
			"name":       strings.Title(ord.ShippingAddress.Name),
			"line1":      strings.Title(ord.ShippingAddress.Line1),
			"line2":      strings.Title(ord.ShippingAddress.Line2),
			"postalCode": ord.ShippingAddress.PostalCode,
			"city":       strings.Title(ord.ShippingAddress.City),
			"state":      stateName,
			"country":    countryName,
		},
		"day":       ord.CreatedAt.Day(),
		"month":     int(ord.CreatedAt.Month()),
		"monthName": ord.CreatedAt.Month().String(),
		"year":      ord.CreatedAt.Year(),
		"storeId":   ord.StoreId,
		"metadata":  ord.Metadata,
	}

	// Include discount
	if ord.Discount != 0 {
		order["discount"] = ord.DisplayDiscount()
	}

	// Include fulfillment data if it exists
	if len(ord.Fulfillment.Trackings) > 0 {
		order["fulfillment"] = map[string]interface{}{
			"trackingNumber": ord.Fulfillment.Trackings[0],
			"service":        ord.Fulfillment.Service,
			"carrier":        ord.Fulfillment.Carrier,
		}
	}

	m.TemplateData["order"] = order

	// Include payment data if available
	if pay != nil {
		m.TemplateData["payment"] = map[string]interface{}{
			"lastFour": pay.Account.LastFour,
		}
	}

	// Customize based on notification settings
	if ord.TemplateId != "" {
		m.TemplateID = ord.TemplateId
	}

	// Customize based on notification settings
	if ord.Notifications.Email.TemplateId != "" {
		m.TemplateID = ord.Notifications.Email.TemplateId
	}

	return m
}

// Send reset password email
func SendResetPassword(c context.Context, org *organization.Organization, usr *user.User, tok *token.Token) {
	// Get configuration for this email
	settings := org.Email.Get(email.UserResetPassword)
	if !settings.Enabled {
		return
	}

	message := userMessage(settings, usr, org)
	message.TemplateData["token"] = map[string]interface{}{
		"id":     tok.Id(),
		"email":  tok.Email,
		"userId": tok.UserId,
		"used":   tok.Used,
	}

	SendTemplate("password-reset", c, message, org)
}

func SendUpdatePassword(c context.Context, org *organization.Organization, usr *user.User, tok *token.Token) {
	// Get configuration for this email
	settings := org.Email.Get(email.UserResetPassword)
	if !settings.Enabled {
		return
	}

	message := userMessage(settings, usr, org)
	message.TemplateData["token"] = map[string]interface{}{
		"id":     tok.Id(),
		"email":  tok.Email,
		"userId": tok.UserId,
		"used":   tok.Used,
	}

	SendTemplate("password-update", c, message, org)
}

// Send email asking for user to confirm email address
func SendUserConfirmEmail(c context.Context, org *organization.Organization, usr *user.User) {
	settings := org.Email.Get(email.UserConfirmEmail)
	log.Info("Try sending UserConfirmEmail with settings: %v", json.Encode(settings), c)
	if !settings.Enabled {
		log.Info("UserConfirmEmail disabled", c)
		return
	}

	// Create token
	tok := token.New(usr.Datastore())
	tok.Email = usr.Email
	tok.UserId = usr.Id()
	tok.Expires = time.Now().Add(time.Hour * 24 * 7)

	err := tok.Put()
	if err != nil {
		panic(err)
	}

	message := userMessage(settings, usr, org)
	message.TemplateData["token"] = map[string]interface{}{
		"id":     tok.Id(),
		"email":  tok.Email,
		"userId": tok.UserId,
		"used":   tok.Used,
	}

	SendTemplate("user-email-confirmation", c, message, org)
}

// Send email confirming email address is confirmed
func SendUserActivated(c context.Context, org *organization.Organization, usr *user.User) {
	settings := org.Email.Get(email.UserActivated)
	if !settings.Enabled {
		return
	}

	message := userMessage(settings, usr, org)
	SendTemplate("user-email-confirmed", c, message, org)
}

// Send welcome email to subscriber
func SendSubscriberWelcome(c context.Context, org *organization.Organization, s *subscriber.Subscriber, f *form.Form) {
	settings := org.Email.Get(email.SubscriberWelcome)
	if !settings.Enabled {
		return
	}

	if f.WelcomeTemplateId != "" {
		settings.TemplateId = f.WelcomeTemplateId
	}

	message := subscriberMessage(settings, s, org)
	SendTemplate("subscriber-welcome", c, message, org)
}

// Send welcome email to user
func SendUserWelcome(c context.Context, org *organization.Organization, usr *user.User) {
	settings := org.Email.Get(email.UserWelcome)
	log.Info("Try sending UserWelcome with settings: %v", json.Encode(settings), c)
	if !settings.Enabled {
		log.Info("UserWelcome disabled", c)
		return
	}

	message := userMessage(settings, usr, org)
	SendTemplate("user-welcome", c, message, org)
}

// Send welcome email to user
func SendAffiliateWelcome(c context.Context, org *organization.Organization, usr *user.User) {
	settings := org.Email.Get(email.AffiliateWelcome)
	log.Info("Try sending UserAffiliate with settings: %v", json.Encode(settings), c)
	if !settings.Enabled {
		log.Info("UserAffiliate disabled", c)
		return
	}

	message := userMessage(settings, usr, org)
	SendTemplate("affiliate-welcome", c, message, org)
}

func SendOrderConfirmation(c context.Context, org *organization.Organization, ord *order.Order, usr *user.User) {
	settings := org.Email.Get(email.OrderConfirmation)
	log.Info("Try sending OrderConfirmation with settings: %v", json.Encode(settings), c)
	if !settings.Enabled {
		log.Info("OrderConfirmation disabled", c)
		return
	}

	message := orderMessage(settings, ord, usr, nil, org)

	referralCode := ""
	referrers := make([]referrer.Referrer, 0)
	if _, err := referrer.Query(ord.Datastore()).Filter("UserId=", usr.Id()).GetAll(&referrers); err != nil {
		log.Warn("Failed to load referrals for user: %v", err, c)
	}

	if len(referrers) > 0 {
		referralCode = referrers[0].Id_
	}
	order := message.TemplateData["order"]
	order["referralCode"] = referralCode

	SendTemplate("order-confirmation", c, message, org)
}

func SendOrderPartiallyRefunded(c context.Context, org *organization.Organization, ord *order.Order, usr *user.User, pay *payment.Payment) {
	settings := org.Email.Get(email.OrderRefundPartial)
	if !settings.Enabled {
		return
	}

	message := orderMessage(settings, ord, usr, pay, org)
	message.TemplateData["payment"] = map[string]interface{}{
		"lastFour": pay.Account.LastFour,
	}
	SendTemplate("order-partially-refunded", c, message, org)
}

func SendOrderRefunded(c context.Context, org *organization.Organization, ord *order.Order, usr *user.User, pay *payment.Payment) {
	settings := org.Email.Get(email.OrderRefund)
	if !settings.Enabled {
		return
	}

	message := orderMessage(settings, ord, usr, pay, org)
	SendTemplate("order-refunded", c, message, org)
}

func SendOrderShipped(c context.Context, org *organization.Organization, ord *order.Order, usr *user.User, pay *payment.Payment) {
	settings := org.Email.Get(email.OrderShipped)
	if !settings.Enabled {
		return
	}

	message := orderMessage(settings, ord, usr, pay, org)
	SendTemplate("order-shipped", c, message, org)
}

func init() {
	gob.Register([]map[string]interface{}{})
	gob.Register(Map{})
}
