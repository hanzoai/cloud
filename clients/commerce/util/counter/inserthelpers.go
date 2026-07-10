package counter

import (
	"context"
	"strconv"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/config"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/payment"
	"github.com/hanzoai/cloud/clients/commerce/models/referral"
	"github.com/hanzoai/cloud/clients/commerce/models/types/accounts"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
)

var (
	sep               = "_"
	currencySetKey    = "currencies"
	feesKey           = "fees"
	ordersKey         = "orders"
	salesKey          = "sales"
	soldKey           = "sold"
	subsKey           = "subscribers"
	usersKey          = "users"
	mailinglistAllKey = "ml_all"

	allTime = "all"
)

type TimeFunc func(t time.Time) string

func monthly(t time.Time) string {
	t2 := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
	return "monthly" + sep + strconv.FormatInt(t2.Unix(), 10)
}

func daily(t time.Time) string {
	t2 := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	return "daily" + sep + strconv.FormatInt(t2.Unix(), 10)
}

func hourly(t time.Time) string {
	t2 := time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, t.Location())
	return "hourly" + sep + strconv.FormatInt(t2.Unix(), 10)
}

func addEnvironment(key string) string {
	env := "unknown"

	if config.IsDevelopment {
		env = "dev"
	} else if config.IsStaging {
		env = "staging"
	} else if config.IsSandbox {
		env = "sandbox"
	} else if config.IsProduction {
		env = "prod"
	}

	return key + sep + env
}

func setName(org *organization.Organization, name string) string {
	name = org.Name + sep + name
	name = addEnvironment(name)

	return name
}

func totalKey(org *organization.Organization, key string, timeStamp string) string {
	key = org.Name + sep + key
	key = addEnvironment(key)
	key = key + sep + timeStamp

	return key
}

func affiliateKey(org *organization.Organization, affId, key string, timeStamp string) string {
	key = org.Name + sep + affId + sep + key
	key = addEnvironment(key)
	key = key + sep + timeStamp

	return key
}

func referrerKey(org *organization.Organization, refId, key string, timeStamp string) string {
	key = org.Name + sep + refId + sep + key
	key = addEnvironment(key)
	key = key + sep + timeStamp

	return key
}

func storeKey(org *organization.Organization, storeId, key string, timeStamp string) string {
	key = org.Name + sep + storeId + sep + key
	key = addEnvironment(key)
	key = key + sep + timeStamp

	return key
}

func subKey(org *organization.Organization, key string, timeStamp string) string {
	key = org.Name + sep + subsKey + sep + key
	key = addEnvironment(key)
	key = key + sep + timeStamp

	return key
}

func userKey(org *organization.Organization, timeStamp string) string {
	key := org.Name + sep + usersKey
	key = addEnvironment(key)
	key = key + sep + timeStamp

	return key
}

func salesKeyId(cur currency.Type) string {
	return salesKey + sep + string(cur)
}

func feesKeyId(cur currency.Type) string {
	return feesKey + sep + string(cur)
}

func productKeyId(productId string) string {
	return soldKey + sep + productId
}

func IncrTotalSales(ctx context.Context, org *organization.Organization, pays []*payment.Payment, t time.Time) error {
	ctx = org.Namespaced(ctx)

	var total currency.Cents
	var currency currency.Type

	for _, pay := range pays {
		if pay.CurrencyTransferred != "" {
			// This is first because we care about it more :p
			total += pay.AmountTransferred
			if currency == "" {
				currency = pay.CurrencyTransferred
			} else if currency != pay.CurrencyTransferred {
				log.Error("Multiple currencies in a single payment set should not happen", org.Datastore().Context)
			}
		} else {
			total += pay.Amount
			if currency == "" {
				currency = pay.Currency
			} else if currency != pay.Currency {
				log.Error("Multiple currencies in a single payment set should not happen", org.Datastore().Context)
			}
		}
	}

	keyId := salesKeyId(currency)
	key := totalKey(org, keyId, hourly(t))
	log.Debug("%v incremented by %v", key, int(total), org.Datastore().Context)
	err := IncrementBy(ctx, key, key, "", "", Hourly, int(total), t)
	if err != nil {
		return err
	}

	keyId = salesKeyId(currency)
	key = totalKey(org, keyId, daily(t))
	log.Debug("%v incremented by %v", key, int(total), org.Datastore().Context)
	err = IncrementBy(ctx, key, key, "", "", Daily, int(total), t)
	if err != nil {
		return err
	}

	keyId = salesKeyId(currency)
	key = totalKey(org, keyId, monthly(t))
	log.Debug("%v incremented by %v", key, int(total), org.Datastore().Context)
	err = IncrementBy(ctx, key, key, "", "", Monthly, int(total), t)
	if err != nil {
		return err
	}

	currencySet := setName(org, currencySetKey)
	err = AddSetMember(ctx, currencySet, currencySet, "", "", None, string(currency), t)
	if err != nil {
		return err
	}

	key = totalKey(org, keyId, allTime)
	log.Debug("%v incremented by %v", key, int(total), org.Datastore().Context)
	return IncrementBy(ctx, key, key, "", "", Total, int(total), time.Now())
}

func IncrStoreSales(ctx context.Context, org *organization.Organization, storeId string, pays []*payment.Payment, t time.Time) error {
	ctx = org.Namespaced(ctx)

	var total currency.Cents
	var cur currency.Type

	for _, pay := range pays {
		// This is first because we care about it more :p
		if (pay.Account.Type == accounts.StripeType && pay.CurrencyTransferred != "") || (pay.Type == accounts.StripeType && pay.CurrencyTransferred != "") {
			total += pay.AmountTransferred
			if cur == "" {
				cur = pay.CurrencyTransferred
			} else if cur != pay.CurrencyTransferred {
				log.Error("Multiple currencies in a single payment set should not happen", org.Datastore().Context)
			}
		} else {
			total += pay.Amount
			if cur == "" {
				cur = pay.Currency
			} else if cur != pay.Currency {
				log.Error("Multiple currencies in a single payment set should not happen", org.Datastore().Context)
			}
		}
	}

	keyId := salesKeyId(cur)
	key := storeKey(org, storeId, keyId, hourly(t))
	log.Debug("%v incremented by %v", key, int(total), org.Datastore().Context)
	if err := IncrementBy(ctx, key, key, storeId, "", Hourly, int(total), t); err != nil {
		return err
	}

	keyId = salesKeyId(cur)
	key = storeKey(org, storeId, keyId, daily(t))
	log.Debug("%v incremented by %v", key, int(total), org.Datastore().Context)
	if err := IncrementBy(ctx, key, key, storeId, "", Daily, int(total), t); err != nil {
		return err
	}

	keyId = salesKeyId(cur)
	key = storeKey(org, storeId, keyId, monthly(t))
	log.Debug("%v incremented by %v", key, int(total), org.Datastore().Context)
	if err := IncrementBy(ctx, key, key, storeId, "", Monthly, int(total), t); err != nil {
		return err
	}

	key = storeKey(org, storeId, keyId, allTime)
	log.Debug("%v incremented by %v", key, int(total), org.Datastore().Context)
	return IncrementBy(ctx, key, key, storeId, "", Total, int(total), t)
}

func AddCurrency(ctx context.Context, org *organization.Organization, cur currency.Type) error {
	currencySet := setName(org, currencySetKey)
	return AddSetMember(ctx, currencySet, currencySet, "", "", Total, string(cur), time.Now())
}

func IncrTotalOrders(ctx context.Context, org *organization.Organization, t time.Time) error {
	ctx = org.Namespaced(ctx)

	key := totalKey(org, ordersKey, hourly(t))
	log.Debug("%v incremented by %v", key, 1, org.Datastore().Context)

	if err := Increment(ctx, key, key, "", "", Hourly, t); err != nil {
		return err
	}

	key = totalKey(org, ordersKey, daily(t))
	log.Debug("%v incremented by %v", key, 1, org.Datastore().Context)
	if err := Increment(ctx, key, key, "", "", Daily, t); err != nil {
		return err
	}

	key = totalKey(org, ordersKey, monthly(t))
	log.Debug("%v incremented by %v", key, 1, org.Datastore().Context)
	if err := Increment(ctx, key, key, "", "", Monthly, t); err != nil {
		return err
	}

	key = totalKey(org, ordersKey, allTime)
	log.Debug("%v incremented by %v", key, 1, org.Datastore().Context)
	return Increment(ctx, key, key, "", "", Total, t)
}

func IncrStoreOrders(ctx context.Context, org *organization.Organization, storeId string, t time.Time) error {
	ctx = org.Namespaced(ctx)

	key := storeKey(org, storeId, ordersKey, hourly(t))
	log.Debug("%v incremented by %v", key, 1, org.Datastore().Context)
	if err := Increment(ctx, key, key, storeId, "", Hourly, t); err != nil {
		return err
	}

	key = storeKey(org, storeId, ordersKey, daily(t))
	log.Debug("%v incremented by %v", key, 1, org.Datastore().Context)
	if err := Increment(ctx, key, key, storeId, "", Daily, t); err != nil {
		return err
	}

	key = storeKey(org, storeId, ordersKey, monthly(t))
	log.Debug("%v incremented by %v", key, 1, org.Datastore().Context)
	if err := Increment(ctx, key, key, storeId, "", Monthly, t); err != nil {
		return err
	}

	key = storeKey(org, storeId, ordersKey, allTime)
	log.Debug("%v incremented by %v", key, 1, org.Datastore().Context)
	return Increment(ctx, key, key, storeId, "", Total, t)
}

func IncrSubscribers(ctx context.Context, org *organization.Organization, mailinglistId string, t time.Time) error {
	ctx = org.Namespaced(ctx)

	key := subKey(org, mailinglistId, hourly(t))
	log.Debug("%v incremented by %v", key, 1, org.Datastore().Context)
	if err := Increment(ctx, key, key, "", "", Hourly, t); err != nil {
		return err
	}

	key = subKey(org, mailinglistId, daily(t))
	log.Debug("%v incremented by %v", key, 1, org.Datastore().Context)
	if err := Increment(ctx, key, key, "", "", Daily, t); err != nil {
		return err
	}

	key = subKey(org, mailinglistId, monthly(t))
	log.Debug("%v incremented by %v", key, 1, org.Datastore().Context)
	if err := Increment(ctx, key, key, "", "", Monthly, t); err != nil {
		return err
	}

	key = subKey(org, mailinglistId, allTime)
	log.Debug("%v incremented by %v", key, 1, org.Datastore().Context)
	if err := Increment(ctx, key, key, "", "", Total, t); err != nil {
		return err
	}

	key = subKey(org, mailinglistAllKey, hourly(t))
	log.Debug("%v incremented by %v", key, 1, org.Datastore().Context)
	if err := Increment(ctx, key, key, "", "", Hourly, t); err != nil {
		return err
	}

	key = subKey(org, mailinglistAllKey, daily(t))
	log.Debug("%v incremented by %v", key, 1, org.Datastore().Context)
	if err := Increment(ctx, key, key, "", "", Daily, t); err != nil {
		return err
	}

	key = subKey(org, mailinglistAllKey, monthly(t))
	log.Debug("%v incremented by %v", key, 1, org.Datastore().Context)
	if err := Increment(ctx, key, key, "", "", Monthly, t); err != nil {
		return err
	}

	key = subKey(org, mailinglistAllKey, allTime)
	log.Debug("%v incremented by %v", key, 1, org.Datastore().Context)
	return Increment(ctx, key, key, "", "", Total, t)
}

func IncrUsers(ctx context.Context, org *organization.Organization, t time.Time) error {
	ctx = org.Namespaced(ctx)

	key := userKey(org, hourly(t))
	log.Debug("%v incremented by %v", key, 1, org.Datastore().Context)
	if err := Increment(ctx, key, key, "", "", Hourly, t); err != nil {
		return err
	}

	key = userKey(org, daily(t))
	log.Debug("%v incremented by %v", key, 1, org.Datastore().Context)
	if err := Increment(ctx, key, key, "", "", Daily, t); err != nil {
		return err
	}

	key = userKey(org, monthly(t))
	log.Debug("%v incremented by %v", key, 1, org.Datastore().Context)
	if err := Increment(ctx, key, key, "", "", Monthly, t); err != nil {
		return err
	}

	key = userKey(org, allTime)
	log.Debug("%v incremented by %v", key, 1, org.Datastore().Context)
	return Increment(ctx, key, key, "", "", Total, t)
}

func IncrTotalProductOrders(ctx context.Context, org *organization.Organization, ord *order.Order, t time.Time) error {
	ctx = org.Namespaced(ctx)

	for _, item := range ord.Items {
		productsKey := productKeyId(item.ProductId)

		key := totalKey(org, productsKey, hourly(t))
		log.Debug("%v incremented by %v", key, 1, org.Datastore().Context)

		if err := Increment(ctx, key, key, "", "", Hourly, t); err != nil {
			return err
		}

		key = totalKey(org, productsKey, daily(t))
		log.Debug("%v incremented by %v", key, 1, org.Datastore().Context)
		if err := Increment(ctx, key, key, "", "", Daily, t); err != nil {
			return err
		}

		key = totalKey(org, productsKey, monthly(t))
		log.Debug("%v incremented by %v", key, 1, org.Datastore().Context)
		if err := Increment(ctx, key, key, "", "", Monthly, t); err != nil {
			return err
		}

		key = totalKey(org, productsKey, allTime)
		log.Debug("%v incremented by %v", key, 1, org.Datastore().Context)
		if err := Increment(ctx, key, key, "", "", Total, t); err != nil {
			return err
		}
	}
	return nil
}

func IncrStoreProductOrders(ctx context.Context, org *organization.Organization, storeId string, ord *order.Order, t time.Time) error {
	ctx = org.Namespaced(ctx)

	for _, item := range ord.Items {
		productsKey := productKeyId(item.ProductId)

		key := storeKey(org, storeId, productsKey, hourly(t))
		log.Debug("%v incremented by %v", key, 1, org.Datastore().Context)
		if err := Increment(ctx, key, key, storeId, "", Hourly, t); err != nil {
			return err
		}

		key = storeKey(org, storeId, productsKey, daily(t))
		log.Debug("%v incremented by %v", key, 1, org.Datastore().Context)
		if err := Increment(ctx, key, key, storeId, "", Daily, t); err != nil {
			return err
		}

		key = storeKey(org, storeId, productsKey, monthly(t))
		log.Debug("%v incremented by %v", key, 1, org.Datastore().Context)
		if err := Increment(ctx, key, key, storeId, "", Monthly, t); err != nil {
			return err
		}

		key = storeKey(org, storeId, productsKey, allTime)
		log.Debug("%v incremented by %v", key, 1, org.Datastore().Context)
		if err := Increment(ctx, key, key, storeId, "", Total, t); err != nil {
			return err
		}
	}

	return nil
}

func IncrAffiliateFees(ctx context.Context, org *organization.Organization, affId string, rfl *referral.Referral) error {
	ctx = org.Namespaced(ctx)

	fee := rfl.Fee

	keyId := feesKeyId(fee.Currency)
	key := affiliateKey(org, affId, keyId, allTime)
	log.Debug("%v incremented by %v", key, fee.Amount, org.Datastore().Context)
	return IncrementBy(ctx, key, key, "", "", Total, int(fee.Amount), time.Now())
}

func IncrReferrerFees(ctx context.Context, org *organization.Organization, refId string, rfl *referral.Referral) error {
	ctx = org.Namespaced(ctx)

	fee := rfl.Fee

	keyId := feesKeyId(fee.Currency)
	key := referrerKey(org, refId, keyId, allTime)
	log.Debug("%v incremented by %v", key, fee.Amount, org.Datastore().Context)
	return IncrementBy(ctx, key, key, "", "", Total, int(fee.Amount), time.Now())
}
