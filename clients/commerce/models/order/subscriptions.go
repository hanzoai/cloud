package order

import (
	"errors"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/lineitem"
	"github.com/hanzoai/cloud/clients/commerce/models/store"
	"github.com/hanzoai/cloud/clients/commerce/models/types/accounts"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/models/types/productcachedvalues"
	"github.com/hanzoai/cloud/clients/commerce/models/types/refs"
	"github.com/hanzoai/cloud/clients/commerce/util/timeutil"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

type SubscriptionBillingType string

const (
	Charge  SubscriptionBillingType = "charge_automatically"
	Invoice SubscriptionBillingType = "send_invoice"
)

type SubscriptionStatus string

const (
	TrialingSubscriptionStatus  SubscriptionStatus = "trialing"
	ActiveSubscriptionStatus    SubscriptionStatus = "active"
	PastDueSubscriptionStatus   SubscriptionStatus = "past_due"
	CancelledSubscriptionStatus SubscriptionStatus = "cancelled"
	UnpaidSubscriptionStatus    SubscriptionStatus = "unpaid"
)

type Subscription struct {
	productcachedvalues.ProductCachedValues

	Subtotal currency.Cents `json:"subtotal"`

	// Discount amount applied to the order. Amount in cents.
	Discount currency.Cents `json:"discount"`

	// Shipping cost applied. Amount in cents.
	Shipping currency.Cents `json:"shipping"`

	// Sales tax applied. Amount in cents.
	Tax currency.Cents `json:"tax"`

	// Price adjustments applied. Amount in cents.
	Adjustment currency.Cents `json:"-"`

	// Total = subtotal + shipping + taxes + adjustments. Amount in cents.
	Total currency.Cents `json:"total"`

	Number int `json:"number,omitempty" datastore:"-"`

	// Immutable buyer data from time of payment, may or may not be associated
	// with a user.
	Buyer Buyer `json:"buyer"`

	Type SubscriptionBillingType `json:"billingType"`

	PlanId    string `json:"planId"`
	UserId    string `json:"userId"`
	ProductId string `json:"productId"`

	FeePercent float64 `json:"applicationFeePercent"`
	EndCancel  bool    `json:"cancelAtPeriodEnd"`

	PeriodStart time.Time `json:"currentPeriodStart"`
	PeriodEnd   time.Time `json:"currentPeriodEnd"`

	Start      time.Time `json:"start"`
	Ended      time.Time `json:"endedAt"`
	Canceled   bool      `json:"canceled"`
	CanceledAt time.Time `json:"canceledAt"`

	TrialStart time.Time `json:"trialStart"`
	TrialEnd   time.Time `json:"trialEnd"`

	Status SubscriptionStatus `json:"status"`

	Account accounts.Account  `json:"account,omitempty"`
	Ref     refs.EcommerceRef `json:"ref,omitempty"`
}

func (s Subscription) TrialPeriodsRemaining() int {
	years, months := timeutil.YearMonthDiff(s.TrialStart, s.TrialEnd)

	if s.Interval == Monthly {
		return months
	}
	return years
}

func (s Subscription) PeriodsRemaining() int {
	months, years := timeutil.YearMonthDiff(s.PeriodStart, s.PeriodEnd)

	if s.Interval == Monthly {
		return months
	}
	return years
}

func (o *Order) CreateAndTallySubscriptionFromItem(stor *store.Store, item lineitem.LineItem) Subscription {
	sub := Subscription{}
	sub.ProductCachedValues = item.ProductCachedValues
	sub.ProductId = item.ProductId
	sub.Currency = stor.Currency
	sub.Status = UnpaidSubscriptionStatus
	sub.Subtotal = currency.Cents(int(item.Price) * item.Quantity)

	ctx := o.Context()

	sub.Shipping = 0

	// Tax may depend on shipping so calcualte that first
	if srs, err := stor.GetShippingRates(); srs == nil {
		log.Warn("Failed to get shippingrates for discount rules: %v", err, ctx)
	} else if match, _, _ := srs.Match(o.ShippingAddress.Country, o.ShippingAddress.State, o.ShippingAddress.City, o.ShippingAddress.PostalCode, o.Subtotal); match != nil {
		sub.Shipping = match.Cost + currency.Cents(float64(sub.Subtotal)*match.Percent)
	}

	sub.Tax = 0

	if trs, err := stor.GetTaxRates(); trs == nil {
		log.Warn("Failed to get taxrates for discount rules: %v", err, ctx)
	} else if match, _, _ := trs.Match(o.ShippingAddress.Country, o.ShippingAddress.State, o.ShippingAddress.City, o.ShippingAddress.PostalCode, o.Subtotal); match != nil {
		if match.TaxShipping {
			sub.Tax = match.Cost + currency.Cents(float64(sub.Subtotal+sub.Shipping)*match.Percent)
		} else {
			sub.Tax = match.Cost + currency.Cents(float64(sub.Subtotal)*match.Percent)
		}
	}

	sub.Total = sub.Subtotal + sub.Shipping + sub.Tax

	return sub
}

// Update order with information from datastore and tally
func (o *Order) CreateSubscriptionsFromItems(stor *store.Store) error {
	ctx := o.Context()

	// Create subscriptions if they don't exist
	if o.Subscriptions == nil {
		o.Subscriptions = make([]Subscription, 0)
	}

	// Taxless
	useFallback := false
	if stor == nil {
		useFallback = true
		log.Warn("Fallback: Using client tax and shipping values.", ctx)
	}

	log.Info("Order Mode: '%v'\nTokenSaleId: '%s'", o.Mode, o.TokenSaleId, ctx)
	// Tokensales and contributions have no items
	if o.Mode != DepositMode && o.Mode != ContributionMode && o.TokenSaleId == "" {
		// Get underlying product/variant entities
		log.Debug("Fetching underlying line items")
		if err := o.GetItemEntities(); err != nil {
			log.Error(err, ctx)
			return errors.New("Failed to get all underlying line items")
		}
	}

	// Update against store listings
	if stor != nil {
		o.UpdateEntitiesFromStore(stor)
	}

	// Update line items using that information
	o.SyncItems(stor)

	// Loop over all items looking for subscribeables
	for _, item := range o.Items {
		// Skip non subscribeables
		if item.Product != nil && !item.Product.IsSubscribeable {
			continue
		}

		// If not using fallback mode, skip taxes
		if !item.Taxable || useFallback {
			continue
		}

		// Create one subscription per quantity unit
		qty := item.Quantity
		if qty < 1 {
			qty = 1
		}
		for q := 0; q < qty; q++ {
			single := item
			single.Quantity = 1
			sub := o.CreateAndTallySubscriptionFromItem(stor, single)
			o.Subscriptions = append(o.Subscriptions, sub)
		}
	}

	return nil
}
