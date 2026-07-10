package checkout

import (
	"strings"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/events"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/payment"
	"github.com/hanzoai/cloud/clients/commerce/models/types/accounts"
	"github.com/hanzoai/cloud/clients/commerce/models/types/fulfillment"
	"github.com/hanzoai/cloud/clients/commerce/models/user"

	stringutil "github.com/hanzoai/cloud/clients/commerce/util/strings"
)

// orderLineItemInfos extracts line item metadata from an order for event publishing.
func orderLineItemInfos(ord *order.Order) []events.LineItemInfo {
	if ord == nil || len(ord.Items) == 0 {
		return nil
	}
	items := make([]events.LineItemInfo, len(ord.Items))
	for i, li := range ord.Items {
		items[i] = events.LineItemInfo{
			ProductID:   li.ProductId,
			ProductName: li.ProductName,
			SKU:         li.ProductSKU,
			Quantity:    li.Quantity,
			PriceCents:  int64(li.Price),
		}
	}
	return items
}

type TokenSale struct {
	Passphrase string `json:"passphrase"`
}

type Authorization struct {
	User      *user.User       `json:"user"`
	Payment   *payment.Payment `json:"payment"`
	Order     *order.Order     `json:"order"`
	TokenSale *TokenSale       `json:"tokenSale"`
}

// Copy newer attributes from request user onto existing user
func mergeUsers(newer, usr *user.User) {
	// Preserve new attributes if set on request
	if newer.FirstName != "" {
		usr.FirstName = newer.FirstName
	}

	if newer.LastName != "" {
		usr.LastName = newer.LastName
	}

	if newer.Phone != "" {
		usr.Phone = newer.Phone
	}

	if newer.Company != "" {
		usr.Company = newer.Company
	}

	if !newer.BillingAddress.Empty() {
		usr.BillingAddress = newer.BillingAddress
	}

	if !newer.ShippingAddress.Empty() {
		usr.ShippingAddress = newer.ShippingAddress
	}

	// Update email or username, but only if those aren't persisted
	if usr.Email != "" && newer.Email != "" {
		usr.Email = newer.Email
	}

	if usr.Username != "" && newer.Username != "" {
		usr.Username = newer.Username
	}

	// Merge metadata
	for k, v := range newer.Metadata {
		usr.Metadata[k] = v
	}
}

// Correctly initialize user provided in authorization
func initUser(db *datastore.Datastore, usr *user.User, ord *order.Order) error {
	usr.Init(db)

	// If Id_ is specified this is an existing user, ensure they exist and
	// re-use existing attributes and data.
	if id := usr.Id_; id != "" {
		// Preserve user passed in request
		newer := usr.Clone().(*user.User)

		// Try to fetch existing user
		if err := usr.GetById(id); err != nil {
			return UserDoesNotExist
		}

		// Copy newer attributes from request user onto existing user
		mergeUsers(newer, usr)
	}

	// Use order billing and shipping address if missing on user
	if usr.ShippingAddress.Empty() {
		usr.ShippingAddress = ord.ShippingAddress
	}

	if usr.BillingAddress.Empty() {
		usr.BillingAddress = ord.BillingAddress
	}

	// Normalize email and username
	usr.Email = strings.ToLower(strings.TrimSpace(usr.Email))
	usr.Username = strings.ToLower(strings.TrimSpace(usr.Username))

	return nil
}

// Correctly initialize order provided in authorization
func initOrder(db *datastore.Datastore, ord *order.Order, usr *user.User) {
	ord.Init(db)

	// Use user shipping and billing information if absent from request
	if ord.ShippingAddress.Empty() {
		ord.ShippingAddress = usr.ShippingAddress
	}

	if ord.ShippingAddress.Empty() {
		ord.ShippingAddress = ord.BillingAddress
	}

	if ord.BillingAddress.Empty() {
		ord.BillingAddress = usr.BillingAddress
	}

	if ord.BillingAddress.Empty() {
		ord.BillingAddress = ord.ShippingAddress
	}

	// Normalize country
	ord.BillingAddress.Country = strings.ToUpper(ord.BillingAddress.Country)
	ord.ShippingAddress.Country = strings.ToUpper(ord.ShippingAddress.Country)

	// Use user's name for addresses if not present in request
	if ord.BillingAddress.Name == "" {
		ord.BillingAddress.Name = usr.Name()
	}

	if ord.ShippingAddress.Name == "" {
		ord.ShippingAddress.Name = usr.Name()
	}

	// Set statuses (if they are not set)
	if ord.Status == "" {
		ord.Status = order.Open
	}
	if ord.PaymentStatus == "" {
		ord.PaymentStatus = payment.Unpaid
	}
	if ord.Fulfillment.Status == "" {
		ord.Fulfillment.Status = fulfillment.Pending
	}

	// Ensure key is allocated before setting parent, Order is parented to user
	ord.Parent = usr.Key()
	ord.UserId = usr.Id()
	ord.Email = usr.Email
	ord.SetKey(ord.Key())

	// Update order number
	ord.Number = ord.NumberFromId()
}

// Correctly initialize payment provided in authorization
func initPayment(db *datastore.Datastore, pay *payment.Payment, usr *user.User, ord *order.Order) {
	if pay == nil {
		return
	}

	pay.Init(db)

	// Update payment status
	pay.Status = "unpaid"

	// Normalize card number, save last four
	number := pay.Account.Number
	number = stringutil.StripWhitespace(number)
	if len(number) >= 4 {
		pay.Account.LastFour = number[len(number)-4:]
	}
	pay.Account.Number = number

	// Default all payment types to Square
	if pay.Type == "" {
		pay.Type = accounts.SquareType
	}

	// Ensure order has same type as payment
	// TODO: Remove this from order
	ord.Type = pay.Type

	// User buyer information on user
	pay.Buyer = usr.Buyer()

	// Update payment with order information
	pay.Currency = ord.Currency
	pay.Description = ord.Description()

	// Payment is parented to order
	pay.Parent = ord.Key()
	pay.OrderId = ord.Id()
	pay.UserId = usr.Id()
}

func (a *Authorization) Init(db *datastore.Datastore) error {
	// Handle the nil user case
	if a.User == nil && a.Order.UserId != "" {
		// If the user is on the order, do that
		a.User = user.New(db)
		a.User.Id_ = a.Order.UserId
	} else if a.User == nil {
		log.Error("No User Found:\nUser: '%v'\nOrder.UserId: '%s'", a.User, a.Order.UserId, db.Context)
		return UserNotProvided
	} else if a.User != nil && strings.Contains(a.User.Email, "@") {
		u := a.User
		a.User = user.New(db)
		if err := a.User.GetById(u.Email); err != nil {
			a.User = u
			log.Info("Creating new User with Email: %v", u.Email, db.Context)
		}
	} else if a.User != nil && a.User.Id_ != "" {
		u := a.User
		a.User = user.New(db)
		if err := a.User.GetById(u.Id_); err != nil {
			a.User = u
			log.Info("Creating new User with Id: %v", u.Id_, db.Context)
		}
	}

	if err := initUser(db, a.User, a.Order); err != nil {
		return err
	}

	// Normalize payment and order
	initOrder(db, a.Order, a.User)
	initPayment(db, a.Payment, a.User, a.Order)

	return nil
}
