package order

import (
	"errors"
	"fmt"

	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/store"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
)

// Calculates order totals
func (o *Order) TallyWithoutSubscriptions() {
	o.TallySubtotalWithoutSubscriptions()
	o.TallyTotalWithoutSubscriptions()
}

func (o *Order) TallySubtotalWithoutSubscriptions() {
	// Contributions do not have items
	if o.Mode == DepositMode || o.Mode == ContributionMode || o.TokenSaleId != "" {
		// Contributions just take subtotal into account
		o.TaxableLineTotal = o.Subtotal
		return
	}

	log.Debug("Tallying up order subtotal")

	// Update total
	lineTotal := 0
	taxableLineTotal := 0
	for _, item := range o.Items {
		// Skip subscribeables
		if item.Product != nil && item.Product.IsSubscribeable {
			continue
		}

		if item.Taxable {
			taxableLineTotal += item.Quantity * int(item.Price)
		}

		lineTotal += item.Quantity * int(item.Price)
	}
	o.LineTotal = currency.Cents(lineTotal)
	o.TaxableLineTotal = currency.Cents(taxableLineTotal)
	o.Subtotal = o.LineTotal - o.Discount
}

func (o *Order) TallyTotalWithoutSubscriptions() {
	log.Debug("Tallying up order total")
	o.Total = o.Subtotal + o.Tax + o.Shipping
}

func (o *Order) SyncItems(stor *store.Store) {
	ctx := o.Context()

	log.Info("Order Before Updating Entities: '%v'", json.Encode(o.Items), ctx)

	// Update against store listings
	log.Debug("Updating items against store listing")
	if stor != nil {
		o.UpdateEntitiesFromStore(stor)
	}

	log.Info("Order Before Updating From Entities: '%v'", json.Encode(o.Items), ctx)

	// Update line items using that information
	log.Debug("Updating line items")
	o.UpdateItemsFromEntities()

	log.Info("Order After Updating From Entities: '%v'", json.Encode(o.Items), ctx)
}

// Update order with information from datastore and tally
func (o *Order) UpdateAndTally(stor *store.Store) error {
	ctx := o.Context()

	// Taxless
	useFallback := false
	if stor == nil {
		useFallback = true
		log.Warn("Fallback: Using client tax and shipping values.", ctx)
	}

	// Get coupons from datastore
	log.Debug("Getting coupons for order")
	if err := o.GetCoupons(); err != nil {
		log.Error(err, ctx)
		return errors.New("Failed to get coupons")
	}

	log.Debug("Checking for redeemed coupons")
	for _, coup := range o.Coupons {
		if !coup.Redeemable() {
			return errors.New(fmt.Sprintf("Coupon %v limit reached", coup.Code()))
		}
	}

	// Update the list of free coupon items
	log.Debug("Add free items from coupons")
	o.UpdateCouponItems()

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

	// Update lineitems with current product info
	o.SyncItems(stor)

	// Calculate applicable discount from discount rules
	log.Debug("Calculating discount from discount rules")
	discount, err := o.CalcRuleDiscount()
	if err != nil {
		log.Warn("Failed to calculate discount from discount rules: %v", err, ctx)
		return err
	}

	// Add applicable coupon discount
	log.Debug("Calculating discount from coupons")
	discount += o.CalcCouponDiscount()

	// Update order total discount
	o.Discount = discount

	// Tally up order again
	o.TallySubtotalWithoutSubscriptions()

	// Reduce TaxableLineTotal for item-specific coupon discounts.
	// Order-wide coupons do not reduce the taxable base.
	if taxableReduce := o.CalcItemCouponTaxableDiscount(); taxableReduce > 0 {
		if o.TaxableLineTotal > taxableReduce {
			o.TaxableLineTotal -= taxableReduce
		} else {
			o.TaxableLineTotal = 0
		}
	}

	// If not using fallback mode, skip taxes
	if !useFallback {
		if stor.Currency != "" {
			o.Currency = stor.Currency
		}

		o.Shipping = 0

		// Tax may depend on shipping so calcualte that first
		if srs, err := stor.GetShippingRates(); srs == nil {
			log.Warn("Failed to get shippingrates for discount rules: %v", err, ctx)
		} else if match, _, _ := srs.Match(o.ShippingAddress.Country, o.ShippingAddress.State, o.ShippingAddress.City, o.ShippingAddress.PostalCode, o.Subtotal); match != nil {
			o.Shipping = match.Cost + currency.Cents(float64(o.Subtotal)*match.Percent)
		}

		o.Tax = 0

		if trs, err := stor.GetTaxRates(); trs == nil {
			log.Warn("Failed to get taxrates for discount rules: %v", err, ctx)
		} else if match, _, _ := trs.Match(o.ShippingAddress.Country, o.ShippingAddress.State, o.ShippingAddress.City, o.ShippingAddress.PostalCode, o.Subtotal); match != nil {
			if match.TaxShipping {
				o.Tax = match.Cost + currency.Cents(float64(o.TaxableLineTotal+o.Shipping)*match.Percent)
			} else {
				o.Tax = match.Cost + currency.Cents(float64(o.TaxableLineTotal)*match.Percent)
			}
		}
	}

	o.TallyTotalWithoutSubscriptions()

	return nil
}
