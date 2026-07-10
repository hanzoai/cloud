package order

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore/iface"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/discount"
	"github.com/hanzoai/cloud/clients/commerce/models/discount/scope"
	"github.com/hanzoai/cloud/clients/commerce/models/discount/target"
	"github.com/hanzoai/cloud/clients/commerce/models/discount/trigger"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
)

// Append discounts which are valid for order creation date
func (o *Order) filterValidDiscounts(discounts []*discount.Discount) []*discount.Discount {
	valid := make([]*discount.Discount, 0)
	log.Error("discounts = %v", discounts)
	for _, dis := range discounts {
		log.Error("dis = %v, o = %v", dis, o)
		if dis.ValidFor(o.CreatedAt) {
			valid = append(valid, dis)
		}
	}
	return valid
}

func (o *Order) GetDiscounts() ([]*discount.Discount, error) {
	db := o.Datastore()
	ctx := db.Context

	// Count actual goroutines: org + store + only items that have ProductId or VariantId.
	// Items with neither don't spawn a goroutine, so we must not over-count channels
	// or the receive loops will deadlock.
	itemChannels := 0
	for _, item := range o.Items {
		if item.ProductId != "" || item.VariantId != "" {
			itemChannels++
		}
	}
	channels := 2 + itemChannels
	errc := make(chan error, channels)
	keyc := make(chan []iface.Key, channels)

	// Fetch any organization-level discounts
	go discount.GetScopedDiscounts(ctx, scope.Organization, "", keyc, errc)

	// Fetch any store-level discounts
	go discount.GetScopedDiscounts(ctx, scope.Store, o.StoreId, keyc, errc)

	// Fetch any product or variant level discounts
	for _, item := range o.Items {
		if item.ProductId != "" {
			go discount.GetScopedDiscounts(ctx, scope.Product, item.ProductId, keyc, errc)
		} else if item.VariantId != "" {
			go discount.GetScopedDiscounts(ctx, scope.Variant, item.VariantId, keyc, errc)
		}
	}

	// Check for any query errors
	for i := 0; i < channels; i++ {
		err := <-errc
		if err != nil {
			log.Warn("Unable to fetch all discounts: %v", err, ctx)
			return nil, err
		}
	}

	// Merge results together
	keys := make([]iface.Key, 0)
	for i := 0; i < channels; i++ {
		keys = append(keys, <-keyc...)
	}

	// Fetch discounts
	discounts := make([]*discount.Discount, 0)
	err := db.GetMulti(keys, &discounts)
	if err != nil {
		log.Error("GetMulti failed: %v", err, ctx)
	}

	// Update discounts on order
	o.Discounts = discounts

	return discounts, err
}

// Discount for this order calculated using applicable discount rules
func (o *Order) CalcRuleDiscount() (currency.Cents, error) {
	totalDiscount := currency.Cents(0)
	totalQuantity := 0

	// Get applicable discount rules
	discounts, err := o.GetDiscounts()

	if err != nil {
		return totalDiscount, err
	}

	// Calculate total quantity
	for _, li := range o.Items {
		totalQuantity += li.Quantity
	}

	// Figure out scope's price and quantity. The same scope applies to all
	// rules of a given discount.
	for _, dis := range discounts {
		price := currency.Cents(0)
		quantity := 0

		switch dis.Scope.Type {
		case scope.Product:
			// Find product this discount is scoped to
			for _, li := range o.Items {
				if li.ProductId == dis.Scope.ProductId {
					price = li.Price
					quantity = li.Quantity
					break
				}
			}
		case scope.Variant:
			// Find variant this discount is scoped to
			for _, li := range o.Items {
				if li.VariantId == dis.Scope.VariantId {
					price = li.Price
					quantity = li.Quantity
					break
				}
			}
		case scope.Collection:
			continue
		case scope.Store, scope.Organization:
			// Use total price, quantity for store and organization scopes.
			price = o.LineTotal
			quantity = totalQuantity
		}

		// Check if rule is triggered
		quantityMax := 0
		quantityIx := -1
		var priceMax currency.Cents
		priceIx := -1
		for i, rule := range dis.Rules {
			switch rule.Trigger.Type() {
			case trigger.Quantity:
				ruleQuantity := rule.Trigger.Quantity.Start
				if quantity > ruleQuantity && ruleQuantity > quantityMax {
					quantityMax = ruleQuantity
					quantityIx = i
				}
			case trigger.Price:
				rulePrice := rule.Trigger.Price.Start
				if price > rulePrice && rulePrice > priceMax {
					priceMax = rulePrice
					priceIx = i
				}
			}
		}

		// Find target
		switch dis.Target.Type {
		case target.Product:
			for _, li := range o.Items {
				if li.ProductId == dis.Target.ProductId {
					quantity = li.Quantity
					price = li.Price
					break
				}
			}
		case target.Variant:
			for _, li := range o.Items {
				if li.VariantId == dis.Target.VariantId {
					quantity = li.Quantity
					price = li.Price
					break
				}
			}
		case target.Cart:
			quantity = totalQuantity
			price = o.LineTotal
		}

		// Apply rule
		if quantityIx >= 0 {
			rule := dis.Rules[quantityIx]
			amt := rule.Action.Discount // Only handling Discount-type actions for now
			if amt.Flat != 0 {
				totalDiscount += amt.Flat
			} else if amt.Percent != 0 {
				totalDiscount += currency.Cents(float64(price) * amt.Percent)
			}
		} else if priceIx >= 0 {
			rule := dis.Rules[priceIx]
			amt := rule.Action.Discount // Only handles Discount-type actions for now
			if amt.Flat != 0 {
				totalDiscount += amt.Flat
			} else if amt.Percent != 0 {
				totalDiscount += currency.Cents(float64(price) * amt.Percent)
			}
		}
	}

	return totalDiscount, nil
}
