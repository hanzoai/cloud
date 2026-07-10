package mailchimp

import (
	"strconv"

	"github.com/hanzoai/gochimp3"

	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/cart"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/product"
	"github.com/hanzoai/cloud/clients/commerce/models/store"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/models/variant"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
)

func centsToFloat(cents currency.Cents, typ currency.Type) float64 {
	amount := float64(cents)
	if !typ.IsZeroDecimal() {
		// Convert cents to dollars
		amount = amount * 0.01
	}
	return amount
}

func idOrEmail(id, email string) string {
	if id == "" {
		return email
	}
	return id
}

func (api API) CreateStore(stor *store.Store) *Error {
	return wrapError(func() error {
		req := &gochimp3.Store{
			// Required
			ID:           stor.Id(),
			ListID:       stor.Mailchimp.ListId, // Immutable after creation
			Name:         stor.Name,
			CurrencyCode: string(stor.Currency),

			// Optional
			Platform:      "Hanzo",
			Domain:        stor.Domain,
			EmailAddress:  stor.Email,
			PrimaryLocale: "en",
			Timezone:      stor.Timezone,
			Phone:         stor.Phone,
			Address: &gochimp3.Address{
				Address1:     stor.Address.Line1,
				Address2:     stor.Address.Line2,
				City:         stor.Address.City,
				ProvinceCode: stor.Address.State,
				PostalCode:   stor.Address.PostalCode,
				CountryCode:  stor.Address.Country,
			},
		}
		_, err := api.Client.CreateStore(req)
		return err
	})
}

func (api API) StoreExists(id string) *Error {
	return wrapError(func() error {
		_, err := api.Client.GetStore(id, nil)
		return err
	})
}

func (api API) UpdateStore(stor *store.Store) *Error {
	return wrapError(func() error {
		req := &gochimp3.Store{
			// Required
			ID:           stor.Id(),
			ListID:       stor.Mailchimp.ListId, // Immutable after creation
			Name:         stor.Name,
			CurrencyCode: string(stor.Currency),

			// Optional
			Platform:      "Hanzo",
			Domain:        stor.Domain,
			EmailAddress:  stor.Email,
			PrimaryLocale: "en",
			Timezone:      stor.Timezone,
			Phone:         stor.Phone,
			Address: &gochimp3.Address{
				Address1:     stor.Address.Line1,
				Address2:     stor.Address.Line2,
				City:         stor.Address.City,
				ProvinceCode: stor.Address.State,
				PostalCode:   stor.Address.PostalCode,
				CountryCode:  stor.Address.Country,
			},
		}
		_, err := api.Client.UpdateStore(req)
		return err
	})
}

func (api API) DeleteStore(stor *store.Store) *Error {
	return wrapError(func() error {
		_, err := api.Client.DeleteStore(stor.Id())
		return err
	})
}

func (api API) CreateCustomer(storeId string, usr *user.User) *Error {
	return wrapError(func() error {
		req := &gochimp3.Customer{
			// Required
			ID: usr.Id(),

			// Optional
			EmailAddress: usr.Email,
			OptInStatus:  true,
			Company:      usr.Company,
			FirstName:    usr.FirstName,
			LastName:     usr.LastName,
			// OrdersCount:  0,
			// TotalSpent:   0,
			// Address:      gochimp3.Address{},
		}

		stor, err := api.Client.GetStore(storeId, nil)
		if err != nil {
			log.Warn("Unable to get mailchimp Store '%s': %v", storeId, err, usr.Datastore().Context)
			return err
		}

		_, err = stor.CreateCustomer(req)
		return err
	})
}

func (api API) UpdateCustomer(storeId string, usr *user.User) *Error {
	return wrapError(func() error {
		req := &gochimp3.Customer{
			// Required
			ID: usr.Id(),

			// Optional
			EmailAddress: usr.Email,
			OptInStatus:  true,
			Company:      usr.Company,
			// FirstName:    "",
			// LastName:     "",
			// OrdersCount:  0,
			// TotalSpent:   0,
			// Address:      gochimp3.Address{},
		}

		stor, err := api.Client.GetStore(storeId, nil)
		if err != nil {
			log.Warn("Unable to get mailchimp Store '%s': %v", storeId, err, usr.Datastore().Context)
			return err
		}

		_, err = stor.UpdateCustomer(req)
		return err
	})
}

func (api API) CreateCart(storeId string, car *cart.Cart) *Error {
	return wrapError(func() error {
		lines := make([]gochimp3.LineItem, 0)
		for i, line := range car.Items {
			lines = append(lines, gochimp3.LineItem{
				ID:               "line" + strconv.Itoa(i),
				ProductID:        line.ProductId,
				ProductVariantID: line.Id(),
				Quantity:         line.Quantity,
				Price:            centsToFloat(line.Price, car.Currency),
			})
		}

		req := &gochimp3.Cart{
			// Required
			CurrencyCode: string(car.Currency),
			OrderTotal:   centsToFloat(car.Total, car.Currency),
			Customer: gochimp3.Customer{
				// Required
				ID: idOrEmail(car.UserId, car.Email),

				// Optional
				EmailAddress: car.Email,
				OptInStatus:  true,
				Company:      car.Company,
				// FirstName:    "",
				// LastName:     "",
				// OrdersCount:  0,
				// TotalSpent:   0,
				// Address:      gochimp3.Address{},
			},

			Lines: lines,

			// Optional
			ID:          car.Id(),
			TaxTotal:    centsToFloat(car.Tax, car.Currency),
			CampaignID:  car.Mailchimp.CampaignId,
			CheckoutURL: car.Mailchimp.CheckoutUrl,
		}

		stor, err := api.Client.GetStore(storeId, nil)
		if err != nil {
			log.Warn("Unable to get mailchimp Store '%s': %v", storeId, err, car.Context())
			return err
		}

		_, err = stor.CreateCart(req)
		return err
	})
}

func (api API) DeleteCustomer(storeId string, usr *user.User) *Error {
	return wrapError(func() error {
		stor, err := api.Client.GetStore(storeId, nil)
		if err != nil {
			log.Warn("Unable to get mailchimp Store '%s': %v", storeId, err, usr.Datastore().Context)
			return err
		}

		_, err = stor.DeleteCustomer(usr.Id())
		return err
	})
}

func (api API) UpdateCart(storeId string, car *cart.Cart) *Error {
	return wrapError(func() error {
		lines := make([]gochimp3.LineItem, 0)
		for i, line := range car.Items {
			lines = append(lines, gochimp3.LineItem{
				ID:               "line" + strconv.Itoa(i),
				ProductID:        line.ProductId,
				ProductVariantID: line.Id(),
				Quantity:         line.Quantity,
				Price:            centsToFloat(line.Price, car.Currency),
			})
		}

		req := &gochimp3.Cart{
			// Required
			CurrencyCode: string(car.Currency),
			OrderTotal:   centsToFloat(car.Total, car.Currency),
			Customer: gochimp3.Customer{
				// Required
				ID: idOrEmail(car.UserId, car.Email),

				// Optional
				EmailAddress: car.Email,
				OptInStatus:  true,
				Company:      car.Company,
				// FirstName:    "",
				// LastName:     "",
				// OrdersCount:  0,
				// TotalSpent:   0,
				// Address:      gochimp3.Address{},
			},
			Lines: lines,

			// Optional
			ID:          car.Id(),
			TaxTotal:    centsToFloat(car.Tax, car.Currency),
			CampaignID:  car.Mailchimp.CampaignId,
			CheckoutURL: car.Mailchimp.CheckoutUrl,
		}

		stor, err := api.Client.GetStore(storeId, nil)
		if err != nil {
			log.Warn("Unable to get mailchimp Store '%s': %v", storeId, err, car.Context())
			return err
		}

		_, err = stor.UpdateCart(req)
		return err
	})
}

func (api API) UpdateOrCreateCart(storeId string, car *cart.Cart) *Error {
	return wrapError(func() error {
		if err := api.UpdateCart(storeId, car); err != nil {
			return api.CreateCart(storeId, car)
		}
		return nil
	})
}

func (api API) DeleteCart(storeId string, car *cart.Cart) *Error {
	return wrapError(func() error {
		stor, err := api.Client.GetStore(storeId, nil)
		if err != nil {
			log.Warn("Unable to get mailchimp Store '%s': %v", storeId, err, car.Context())
			return err
		}

		_, err = stor.DeleteCart(car.Id())
		return err
	})
}

func (api API) CreateOrder(storeId string, ord *order.Order) *Error {
	return wrapError(func() error {
		// Fetch user
		usr := user.New(ord.Datastore())
		if err := usr.GetById(ord.UserId); err != nil {
			return err
		}

		// Create line items
		lines := make([]gochimp3.LineItem, 0)
		for i, line := range ord.Items {
			lines = append(lines, gochimp3.LineItem{
				ID:               "line" + strconv.Itoa(i),
				ProductID:        line.ProductId,
				ProductVariantID: line.Id(),
				Quantity:         line.Quantity,
				Price:            centsToFloat(line.Price, ord.Currency),
			})
		}

		// Create Order
		req := &gochimp3.Order{
			// Required
			ID:           strconv.Itoa(ord.Number),
			CurrencyCode: string(ord.Currency),
			OrderTotal:   centsToFloat(ord.Total-ord.Refunded, ord.Currency),
			Customer: gochimp3.Customer{
				// Required
				ID: usr.Id(),

				// Optional
				EmailAddress: usr.Email,
				OptInStatus:  true,
				Company:      ord.Company,
				FirstName:    usr.FirstName,
				LastName:     usr.LastName,
				// OrdersCount:  1,
				// TotalSpent:   centsToFloat(usr.Total, usr.Currency),
				Address: &gochimp3.Address{
					Address1:     ord.ShippingAddress.Line1,
					Address2:     ord.ShippingAddress.Line2,
					City:         ord.ShippingAddress.City,
					ProvinceCode: ord.ShippingAddress.State,
					PostalCode:   ord.ShippingAddress.PostalCode,
					CountryCode:  ord.ShippingAddress.Country,
				},
			},
			Lines: lines,

			// Optional
			TaxTotal:          centsToFloat(ord.Tax, ord.Currency),
			ShippingTotal:     centsToFloat(ord.Shipping, ord.Currency),
			FinancialStatus:   string(ord.PaymentStatus),
			FulfillmentStatus: string(ord.Fulfillment.Status),
			CampaignID:        ord.Mailchimp.CampaignId,
			TrackingCode:      ord.Mailchimp.TrackingCode,
			BillingAddress: &gochimp3.Address{
				Address1:     ord.BillingAddress.Line1,
				Address2:     ord.BillingAddress.Line2,
				City:         ord.BillingAddress.City,
				ProvinceCode: ord.BillingAddress.State,
				PostalCode:   ord.BillingAddress.PostalCode,
				CountryCode:  ord.BillingAddress.Country,
			},
			ShippingAddress: &gochimp3.Address{
				Address1:     ord.ShippingAddress.Line1,
				Address2:     ord.ShippingAddress.Line2,
				City:         ord.ShippingAddress.City,
				ProvinceCode: ord.ShippingAddress.State,
				PostalCode:   ord.ShippingAddress.PostalCode,
				CountryCode:  ord.ShippingAddress.Country,
			},
			ProcessedAtForeign: ord.CreatedAt,
			CancelledAtForeign: ord.CancelledAt,
			UpdatedAtForeign:   ord.UpdatedAt,
		}

		log.Debug("Create Order Request: '%v'", json.Encode(req), ord.Datastore().Context)

		stor, err := api.Client.GetStore(storeId, nil)
		if err != nil {
			log.Warn("Unable to get mailchimp Store '%s': %v", storeId, err, ord.Datastore().Context)
			return err
		}

		res, err := stor.CreateOrder(req)
		log.Debug("Create Order Response: '%v'", json.Encode(res), ord.Datastore().Context)
		return err
	})
}

func (api API) UpdateOrder(storeId string, ord *order.Order) *Error {
	return wrapError(func() error {
		// Fetch user
		usr := user.New(ord.Datastore())
		if err := usr.GetById(ord.UserId); err != nil {
			return err
		}

		// Create line items
		lines := make([]gochimp3.LineItem, 0)
		for i, line := range ord.Items {
			lines = append(lines, gochimp3.LineItem{
				ID:               "line" + strconv.Itoa(i),
				ProductID:        line.ProductId,
				ProductVariantID: line.Id(),
				Quantity:         line.Quantity,
				Price:            centsToFloat(line.Price, ord.Currency),
			})
		}

		// Update Order
		req := &gochimp3.Order{
			// Required
			ID:           strconv.Itoa(ord.Number),
			CurrencyCode: string(ord.Currency),
			OrderTotal:   centsToFloat(ord.Total-ord.Refunded, ord.Currency),
			Customer: gochimp3.Customer{
				// Required
				ID: usr.Id(),

				// Optional
				EmailAddress: usr.Email,
				OptInStatus:  true,
				Company:      ord.Company,
				FirstName:    usr.FirstName,
				LastName:     usr.LastName,
				// OrdersCount:  1,
				// TotalSpent:   centsToFloat(usr.Total, usr.Currency),
				Address: &gochimp3.Address{
					Address1:     ord.ShippingAddress.Line1,
					Address2:     ord.ShippingAddress.Line2,
					City:         ord.ShippingAddress.City,
					ProvinceCode: ord.ShippingAddress.State,
					PostalCode:   ord.ShippingAddress.PostalCode,
					CountryCode:  ord.ShippingAddress.Country,
				},
			},
			Lines: lines,

			// Optional
			TaxTotal:          centsToFloat(ord.Tax, ord.Currency),
			ShippingTotal:     centsToFloat(ord.Shipping, ord.Currency),
			FinancialStatus:   string(ord.PaymentStatus),
			FulfillmentStatus: string(ord.Fulfillment.Status),
			CampaignID:        ord.Mailchimp.CampaignId,
			TrackingCode:      ord.Mailchimp.TrackingCode,
			BillingAddress: &gochimp3.Address{
				Address1:     ord.BillingAddress.Line1,
				Address2:     ord.BillingAddress.Line2,
				City:         ord.BillingAddress.City,
				ProvinceCode: ord.BillingAddress.State,
				PostalCode:   ord.BillingAddress.PostalCode,
				CountryCode:  ord.BillingAddress.Country,
			},
			ShippingAddress: &gochimp3.Address{
				Address1:     ord.ShippingAddress.Line1,
				Address2:     ord.ShippingAddress.Line2,
				City:         ord.ShippingAddress.City,
				ProvinceCode: ord.ShippingAddress.State,
				PostalCode:   ord.ShippingAddress.PostalCode,
				CountryCode:  ord.ShippingAddress.Country,
			},
			ProcessedAtForeign: ord.CreatedAt,
			CancelledAtForeign: ord.CancelledAt,
			UpdatedAtForeign:   ord.UpdatedAt,
		}

		stor, err := api.Client.GetStore(storeId, nil)
		if err != nil {
			log.Warn("Unable to get mailchimp Store '%s': %v", storeId, err, ord.Datastore().Context)
			return err
		}

		_, err = stor.UpdateOrder(req)
		return err
	})
}

func (api API) DeleteOrder(storeId string, ord *order.Order) *Error {
	return wrapError(func() error {
		stor, err := api.Client.GetStore(storeId, nil)
		if err != nil {
			log.Warn("Unable to get mailchimp Store '%s': %v", storeId, err, ord.Datastore().Context)
			return err
		}

		_, err = stor.DeleteOrder(ord.Id())
		return err
	})
}

func (api API) CreateProduct(storeId string, prod *product.Product) *Error {
	return wrapError(func() error {
		req := &gochimp3.Product{
			ID:          prod.Id(),
			Title:       prod.Name,
			Description: prod.Description,
			// Handle:      "",
			ImageURL: prod.Image.Url,
			// Type:        "",
			// URL:         "",
			// Vendor:      "",
			Variants: []gochimp3.Variant{
				gochimp3.Variant{
					// Required
					ID:    prod.Id(),
					Title: prod.Name,

					// Optional
					SKU:               prod.Slug,
					Price:             centsToFloat(prod.Price, prod.Currency),
					InventoryQuantity: prod.Inventory,
					Visibility:        "visible",
					// Backorders:        "",
					ImageURL: prod.Image.Url,
					// Url:               "",
				},
			},
			PublishedAtForeign: prod.CreatedAt,
		}

		stor, err := api.Client.GetStore(storeId, nil)
		if err != nil {
			log.Warn("Unable to get mailchimp Store '%s': %v", storeId, err, prod.Datastore().Context)
			return err
		}

		_, err = stor.CreateProduct(req)
		return err
	})
}

func (api API) UpdateProduct(storeId string, prod *product.Product) *Error {
	return wrapError(func() error {
		req := &gochimp3.Product{
			ID:          prod.Id(),
			Title:       prod.Name,
			Description: prod.Description,
			// Handle:      "",
			ImageURL: prod.Image.Url,
			// Type:        "",
			// URL:         "",
			// Vendor:      "",
			Variants: []gochimp3.Variant{
				gochimp3.Variant{
					// Required
					ID:    prod.Id(),
					Title: prod.Name,

					// Optional
					SKU:               prod.Slug,
					Price:             centsToFloat(prod.Price, prod.Currency),
					InventoryQuantity: prod.Inventory,
					Visibility:        "visible",
					// Backorders:        "",
					ImageURL: prod.Image.Url,
					// Url:               "",
				},
			},
			PublishedAtForeign: prod.CreatedAt,
		}

		stor, err := api.Client.GetStore(storeId, nil)
		if err != nil {
			log.Warn("Unable to get mailchimp Store '%s': %v", storeId, err, prod.Datastore().Context)
			return err
		}

		_, err = stor.UpdateProduct(req)
		return err
	})
}

func (api API) DeleteProduct(storeId string, prod *product.Product) *Error {
	return wrapError(func() error {
		stor, err := api.Client.GetStore(storeId, nil)
		if err != nil {
			log.Warn("Unable to get mailchimp Store '%s': %v", storeId, err, prod.Datastore().Context)
			return err
		}

		_, err = stor.DeleteProduct(prod.Id())
		return err
	})
}

func (api API) CreateVariant(storeId, productId string, vari *variant.Variant) *Error {
	return wrapError(func() error {
		req := &gochimp3.Variant{
			// Required
			ID:    vari.Id(),
			Title: vari.Name,

			// Optional
			SKU:               vari.SKU,
			Price:             centsToFloat(vari.Price, vari.Currency),
			InventoryQuantity: vari.Inventory,
			Visibility:        "visible",
			// Backorders:        "",
			ImageURL: vari.Image.Url,
			// Url:               "",
		}

		stor, err := api.Client.GetStore(storeId, nil)
		if err != nil {
			log.Warn("Unable to get mailchimp Store '%s': %v", storeId, err, vari.Datastore().Context)
			return err
		}

		prod, err := stor.GetProduct(productId, nil)
		if err != nil {
			log.Warn("Unable to get mailchimp product '%s': %v", productId, err, vari.Datastore().Context)
			return err
		}

		_, err = prod.CreateVariant(req)
		return err
	})
}

func (api API) UpdateVariant(storeId, productId string, vari *variant.Variant) *Error {
	return wrapError(func() error {
		req := &gochimp3.Variant{
			// Required
			ID:    vari.Id(),
			Title: vari.Name,

			// Optional
			SKU:               vari.SKU,
			Price:             centsToFloat(vari.Price, vari.Currency),
			InventoryQuantity: vari.Inventory,
			Visibility:        "visible",
			// Backorders:        "",
			ImageURL: vari.Image.Url,
			// Url:               "",
		}

		stor, err := api.Client.GetStore(storeId, nil)
		if err != nil {
			log.Warn("Unable to get mailchimp Store '%s': %v", storeId, err, vari.Datastore().Context)
			return err
		}

		prod, err := stor.GetProduct(productId, nil)
		if err != nil {
			log.Warn("Unable to get mailchimp product '%s': %v", productId, err, vari.Datastore().Context)
			return err
		}

		_, err = prod.UpdateVariant(req)
		return err
	})
}

func (api API) DeleteVariant(storeId, productId string, vari *variant.Variant) *Error {
	return wrapError(func() error {
		stor, err := api.Client.GetStore(storeId, nil)
		if err != nil {
			log.Warn("Unable to get mailchimp Store '%s': %v", storeId, err, vari.Datastore().Context)
			return err
		}

		prod, err := stor.GetProduct(productId, nil)
		if err != nil {
			log.Warn("Unable to get mailchimp product '%s': %v", productId, err, vari.Datastore().Context)
			return err
		}

		_, err = prod.DeleteVariant(vari.Id())
		return err
	})
}
