package order

import (
	"errors"
	"math"
	"strings"

	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/coupon"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"

	"github.com/hanzoai/cloud/clients/commerce/models/lineitem"
)

// Get line items from datastore
func (o *Order) GetCoupons() error {
	o.DedupeCouponCodes()
	db := o.Datastore()
	ctx := db.Context

	log.Debug("CouponCodes: %#v", o.CouponCodes)
	num := len(o.CouponCodes)
	o.Coupons = make([]coupon.Coupon, num, num)

	for i := 0; i < num; i++ {
		cpn := coupon.New(db)
		code := strings.TrimSpace(o.CouponCodes[i])

		log.Debug("CODE: %s", code)
		err := cpn.GetById(code)

		if err != nil {
			log.Warn("Could not find CouponCodes[%v] => %v, Error: %v", i, code, err, ctx)
			return errors.New("Invalid coupon code: " + code)
		}

		o.Coupons[i] = *cpn
	}

	return nil
}

func (o *Order) DedupeCouponCodes() {
	found := make(map[string]bool)
	j := 0
	for i, code := range o.CouponCodes {
		if !found[code] {
			found[code] = true
			o.CouponCodes[j] = o.CouponCodes[i]
			j++
		}
	}
	o.CouponCodes = o.CouponCodes[:j]
}

func (o *Order) CalcCouponDiscount() currency.Cents {
	var discount currency.Cents

	num := len(o.CouponCodes)

	ctx := o.Datastore().Context

	log.Debug("Applying coupons: %v", o.CouponCodes, ctx)
	for i := 0; i < num; i++ {
		c := &o.Coupons[i]
		if !c.ValidFor(o.CreatedAt) {
			continue
		}

		log.Debug("Applying coupon '%v'", c.Code(), ctx)

		if c.ItemId() == "" {
			log.Debug("Coupon applies to order", ctx)

			// Not per product
			switch c.Type {
			case coupon.Flat:
				log.Warn("Flat", ctx)
				discount += currency.Cents(c.Amount)
			case coupon.Percent:
				log.Warn("Percent", ctx)
				for _, item := range o.Items {
					discount += currency.Cents(int(math.Floor(float64(item.TotalPrice()) * float64(c.Amount) * 0.01)))
				}
			case coupon.FreeShipping:
				log.Warn("FreeShipping", ctx)
				discount += currency.Cents(int(o.Shipping))
			}
		} else {
			log.Debug("Coupon applies to '%v'", c.ItemId(), ctx)

			// Coupons per product
			for _, item := range o.Items {
				log.Debug("Coupon.ProductId: %v, Item.ProductId: %v", c.ProductId, item.ProductId, ctx)
				if item.Id() == c.ItemId() {
					switch c.Type {
					case coupon.Flat:
						log.Debug("Flat %d", c.Amount, ctx)
						quantity := item.Quantity
						if c.Once {
							quantity = 1
						}
						discount += currency.Cents(quantity * c.Amount)
					case coupon.Percent:
						log.Debug("Percent %d", c.Amount, ctx)
						discount += currency.Cents(math.Floor(float64(item.TotalPrice()) * float64(c.Amount) * 0.01))
					case coupon.FreeItem:
						log.Debug("FreeShipping", ctx)
						discount += currency.Cents(item.Price)
					}

					// Break out unless required to apply to each product
					if c.Once {
						break
					}
				}
			}
		}
	}
	return discount
}

// CalcItemCouponTaxableDiscount returns the taxable discount from item-specific coupons only.
// Order-wide coupons (no ProductId/VariantId) do not reduce the taxable base.
func (o *Order) CalcItemCouponTaxableDiscount() currency.Cents {
	var taxableDiscount currency.Cents

	for i := range o.Coupons {
		c := &o.Coupons[i]
		if c.ItemId() == "" {
			continue // order-level coupons don't reduce taxable base
		}
		if !c.ValidFor(o.CreatedAt) {
			continue
		}
		for _, item := range o.Items {
			if item.Id() != c.ItemId() || !item.Taxable {
				continue
			}
			switch c.Type {
			case coupon.Flat:
				qty := item.Quantity
				if c.Once {
					qty = 1
				}
				taxableDiscount += currency.Cents(qty * c.Amount)
			case coupon.Percent:
				taxableDiscount += currency.Cents(math.Floor(float64(item.TotalPrice()) * float64(c.Amount) * 0.01))
			}
			if c.Once {
				break
			}
		}
	}
	return taxableDiscount
}

// Update discount using coupon codes/order info.
// Refactor later when we have more time to think about it
func (o *Order) UpdateCouponItems() error {
	nCodes := len(o.CouponCodes)

	items := make([]lineitem.LineItem, 0)
	for _, item := range o.Items {
		if item.AddedBy != "coupon" {
			items = append(items, item)
		}
	}

	o.Items = items

	for i := 0; i < nCodes; i++ {
		c := &o.Coupons[i]
		if !c.ValidFor(o.CreatedAt) {
			continue
		}
		if c.ProductId == "" {
			switch c.Type {
			case coupon.FreeItem:
				o.Items = append(o.Items, lineitem.LineItem{
					ProductId: c.FreeProductId,
					VariantId: c.FreeVariantId,
					Quantity:  c.FreeQuantity,
					Free:      true,
					AddedBy:   "coupon",
				})
			}
		} else {
			for _, item := range o.Items {
				if item.ProductId == c.ProductId {
					switch c.Type {
					case coupon.FreeItem:
						o.Items = append(o.Items, lineitem.LineItem{
							ProductId: c.FreeProductId,
							VariantId: c.FreeVariantId,
							Quantity:  c.FreeQuantity,
							Free:      true,
							AddedBy:   "coupon",
						})
					}
				}
			}
		}
	}

	return nil
}
