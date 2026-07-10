package counter

import (
	"context"
	"strconv"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/lineitem"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/product"
	"github.com/hanzoai/cloud/clients/commerce/models/return"
)

var incrementSep = "."

func IncrementByAll(ctx context.Context, tag, storeId, geo string, value int, t time.Time) error {
	t1 := time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, t.Location())
	t2 := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
	baseKey := tag + incrementSep
	if storeId != "" {
		storeKey := baseKey + storeId + incrementSep
		key := storeKey + string(Hourly) + incrementSep + strconv.FormatInt(t1.Unix(), 10)
		log.Debug("%v incremented by %v", key, value, ctx)
		if err := IncrementBy(ctx, key, tag, storeId, "", Hourly, value, t); err != nil {
			return err
		}
		key = storeKey + string(Monthly) + incrementSep + strconv.FormatInt(t2.Unix(), 10)
		log.Debug("%v incremented by %v", key, value, ctx)
		if err := IncrementBy(ctx, key, tag, storeId, "", Monthly, value, t); err != nil {
			return err
		}
		key = storeKey + string(Total)
		log.Debug("%v incremented by %v", key, value, ctx)
		if err := IncrementBy(ctx, key, tag, storeId, "", Total, value, t); err != nil {
			return err
		}
	}
	if geo != "" {
		geoKey := baseKey + geo + incrementSep
		key := geoKey + string(Hourly) + incrementSep + strconv.FormatInt(t1.Unix(), 10)
		log.Debug("%v incremented by %v", key, value, ctx)
		if err := IncrementBy(ctx, key, tag, "", geo, Hourly, value, t); err != nil {
			return err
		}
		key = geoKey + string(Monthly) + incrementSep + strconv.FormatInt(t2.Unix(), 10)
		log.Debug("%v incremented by %v", key, value, ctx)
		if err := IncrementBy(ctx, key, tag, "", geo, Monthly, value, t); err != nil {
			return err
		}
		key = geoKey + string(Total)
		log.Debug("%v incremented by %v", key, value, ctx)
		if err := IncrementBy(ctx, key, tag, "", geo, Total, value, t); err != nil {
			return err
		}
	}
	if storeId != "" && geo != "" {
		storeGeoKey := baseKey + storeId + incrementSep + geo + incrementSep
		key := storeGeoKey + string(Hourly) + incrementSep + strconv.FormatInt(t1.Unix(), 10)
		log.Debug("%v incremented by %v", key, value, ctx)
		if err := IncrementBy(ctx, key, tag, storeId, geo, Hourly, value, t); err != nil {
			return err
		}
		key = storeGeoKey + string(Monthly) + incrementSep + strconv.FormatInt(t2.Unix(), 10)
		log.Debug("%v incremented by %v", key, value, ctx)
		if err := IncrementBy(ctx, key, tag, storeId, geo, Monthly, value, t); err != nil {
			return err
		}
		key = storeGeoKey + string(Total)
		log.Debug("%v incremented by %v", key, value, ctx)
		if err := IncrementBy(ctx, key, tag, storeId, geo, Total, value, t); err != nil {
			return err
		}
	}

	key := baseKey + string(Hourly) + incrementSep + strconv.FormatInt(t1.Unix(), 10)
	log.Debug("%v incremented by %v", key, value, ctx)
	if err := IncrementBy(ctx, key, tag, "", "", Hourly, value, t); err != nil {
		return err
	}

	key = baseKey + string(Monthly) + incrementSep + strconv.FormatInt(t2.Unix(), 10)
	log.Debug("%v incremented by %v", key, value, ctx)
	if err := IncrementBy(ctx, key, tag, "", "", Monthly, value, t); err != nil {
		return err
	}

	key = baseKey + string(Total)
	log.Debug("%v incremented by %v", key, value, ctx)
	if err := IncrementBy(ctx, key, tag, "", "", Total, value, t); err != nil {
		return err
	}

	return nil
}

func IncrUser(ctx context.Context, t time.Time) error {
	return IncrementByAll(ctx, "user.count", "", "", 1, t)
}

func IncrSubscriber(ctx context.Context, t time.Time) error {
	return IncrementByAll(ctx, "subscriber.count", "", "", 1, t)
}

func IncrOrder(ctx context.Context, ord *order.Order) error {
	if ord.Test {
		return nil
	}

	if err := IncrementByAll(ctx, "order.count", ord.StoreId, ord.ShippingAddress.Country, 1, ord.CreatedAt); err != nil {
		return err
	}

	if err := IncrementByAll(ctx, "order.revenue", ord.StoreId, ord.ShippingAddress.Country, int(ord.Total), ord.CreatedAt); err != nil {
		return err
	}

	projectedPrice := 0
	// Calculate Projected
	ord.GetItemEntities()
	for _, item := range ord.Items {
		projectedPrice += item.Quantity * int(item.ProjectedPrice)
	}

	if err := IncrementByAll(ctx, "order.projected.revenue", ord.StoreId, ord.ShippingAddress.Country, projectedPrice, ord.CreatedAt); err != nil {
		return err
	}
	for _, item := range ord.Items {
		prod := product.New(ord.Datastore())
		if err := prod.GetById(item.ProductId); err != nil {
			return err
		}
		for i := 0; i < item.Quantity; i++ {
			if err := IncrProduct(ctx, prod, ord); err != nil {
				log.Error("IncrProduct Error %v", err, ctx)
				return err
			}
		}
	}

	return nil
}

func IncrProduct(ctx context.Context, prod *product.Product, ord *order.Order) error {
	if ord.Test {
		return nil
	}
	if err := IncrementByAll(ctx, "product."+prod.Id()+".sold", ord.StoreId, ord.ShippingAddress.Country, 1, ord.CreatedAt); err != nil {
		return err
	}
	if err := IncrementByAll(ctx, "product."+prod.Id()+".revenue", ord.StoreId, ord.ShippingAddress.Country, int(prod.Price), ord.CreatedAt); err != nil {
		return err
	}
	if err := IncrementByAll(ctx, "product."+prod.Id()+".projected.revenue", ord.StoreId, ord.ShippingAddress.Country, int(prod.ProjectedPrice), ord.CreatedAt); err != nil {
		return err
	}

	if prod.InventoryCost == 0 {
		return nil
	}

	if err := IncrementByAll(ctx, "product."+prod.Id()+".inventory.cost", ord.StoreId, ord.ShippingAddress.Country, int(prod.InventoryCost), ord.CreatedAt); err != nil {
		return err
	}
	return nil
}

func IncrOrderRefund(ctx context.Context, ord *order.Order, refund int, t time.Time) error {
	if ord.Test {
		return nil
	}
	if ord.Refunded == 0 {
		return nil
	}
	if err := IncrementByAll(ctx, "order.refunded.amount", ord.StoreId, ord.ShippingAddress.Country, refund, t); err != nil {
		return err
	}
	if ord.Refunded != ord.Total {
		return nil
	}
	if err := IncrementByAll(ctx, "order.refunded.count", ord.StoreId, ord.ShippingAddress.Country, 1, t); err != nil {
		return err
	}
	for _, item := range ord.Items {
		prod := product.New(ord.Datastore())
		if err := prod.GetById(item.ProductId); err != nil {
			return err
		}
		for i := 0; i < item.Quantity; i++ {
			if err := IncrProductRefund(ctx, prod, ord); err != nil {
				log.Error("IncrProduct Error %v", err, ctx)
				return err
			}
		}
	}

	return nil
}

func IncrProductRefund(ctx context.Context, prod *product.Product, ord *order.Order) error {
	if ord.Test {
		return nil
	}
	if ord.Refunded != ord.Total {
		return nil
	}
	if err := IncrementByAll(ctx, "product."+prod.Id()+".refunded.count", ord.StoreId, ord.ShippingAddress.Country, 1, ord.CreatedAt); err != nil {
		return err
	}
	if err := IncrementByAll(ctx, "product."+prod.Id()+".refunded.amount", ord.StoreId, ord.ShippingAddress.Country, int(prod.Price), ord.CreatedAt); err != nil {
		return err
	}
	if err := IncrementByAll(ctx, "product."+prod.Id()+".projected.refunded.amount", ord.StoreId, ord.ShippingAddress.Country, int(prod.ProjectedPrice), ord.CreatedAt); err != nil {
		return err
	}
	if err := IncrementByAll(ctx, "order.projected.refunded.amount", ord.StoreId, ord.ShippingAddress.Country, int(prod.ProjectedPrice), ord.CreatedAt); err != nil {
		return err
	}

	if prod.InventoryCost == 0 {
		return nil
	}

	if err := IncrementByAll(ctx, "product."+prod.Id()+".inventory.refunded.cost", ord.StoreId, ord.ShippingAddress.Country, int(prod.InventoryCost), ord.CreatedAt); err != nil {
		return err
	}
	return nil
}

func IncrOrderShip(ctx context.Context, ord *order.Order, t time.Time) error {
	if ord.Test {
		return nil
	}
	if ord.Fulfillment.Pricing == 0 {
		return nil
	}
	if err := IncrementByAll(ctx, "order.shipped.count", ord.StoreId, ord.ShippingAddress.Country, 1, t); err != nil {
		return err
	}
	if err := IncrementByAll(ctx, "order.shipped.cost", ord.StoreId, ord.ShippingAddress.Country, int(ord.Fulfillment.Pricing), t); err != nil {
		return err
	}
	for _, item := range ord.Items {
		prod := product.New(ord.Datastore())
		if err := prod.GetById(item.ProductId); err != nil {
			return err
		}
		for i := 0; i < item.Quantity; i++ {
			if err := IncrProductShip(ctx, prod, ord); err != nil {
				log.Error("IncrProduct Error %v", err, ctx)
				return err
			}
		}
	}

	return nil
}

func IncrProductShip(ctx context.Context, prod *product.Product, ord *order.Order) error {
	if ord.Test {
		return nil
	}
	if err := IncrementByAll(ctx, "product."+prod.Id()+".shipped.count", ord.StoreId, ord.ShippingAddress.Country, 1, ord.CreatedAt); err != nil {
		return err
	}

	return nil
}

func IncrOrderReturn(ctx context.Context, items []lineitem.LineItem, rtn *return_.Return) error {
	ord := order.New(rtn.Datastore())
	if err := ord.GetById(rtn.OrderId); err != nil {
		return err
	}
	if ord.Test {
		return nil
	}
	if err := IncrementByAll(ctx, "order.returned.count", ord.StoreId, ord.ShippingAddress.Country, 1, rtn.CreatedAt); err != nil {
		return err
	}
	for _, item := range items {
		prod := product.New(rtn.Datastore())
		if err := prod.GetById(item.ProductId); err != nil {
			return err
		}
		for i := 0; i < item.Quantity; i++ {
			if err := IncrProductReturn(ctx, prod, ord, rtn); err != nil {
				log.Error("IncrProduct Error %v", err, ctx)
				return err
			}
		}
	}

	return nil
}

func IncrProductReturn(ctx context.Context, prod *product.Product, ord *order.Order, rtn *return_.Return) error {
	if ord.Test {
		return nil
	}
	if err := IncrementByAll(ctx, "product."+prod.Id()+".returned.count", ord.StoreId, ord.ShippingAddress.Country, 1, rtn.CreatedAt); err != nil {
		return err
	}

	return nil
}
