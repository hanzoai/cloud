package counter

import (
	"context"
	"strconv"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/cache"
)

type currencyValue map[currency.Type]int
type currencyValues map[currency.Type][]int

type DashboardData struct {
	TotalSales       currencyValue
	TotalOrders      int
	TotalUsers       int
	TotalSubscribers int

	DailySales       currencyValues
	DailyOrders      []int
	DailyUsers       []int
	DailySubscribers []int

	// DailyStoreSales  [](map[currency.Type]int64)
	// DailyStoreOrders [](map[currency.Type]int64)
}

func GetDashboardData(ctx context.Context, t Period, date time.Time, tzOffset int, org *organization.Organization) (DashboardData, error) {
	ctx = org.Namespaced(ctx)

	loc := time.FixedZone("utc +"+strconv.Itoa(tzOffset), tzOffset)

	data := DashboardData{}
	dashboardKey := org.Name + sep + string(t) + sep + loc.String() + sep
	switch t {
	case Monthly:
		d := time.Date(date.Year(), date.Month(), 1, 0, 0, 0, 0, loc)
		dashboardKey += strconv.FormatInt(d.Unix(), 10)
	case Weekly:
		weekday := int(date.Weekday())
		d := time.Date(date.Year(), date.Month(), (7-weekday)+date.Day(), 0, 0, 0, 0, loc)
		dashboardKey += strconv.FormatInt(d.Unix(), 10)
	case Daily:
		d := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, loc)
		dashboardKey += strconv.FormatInt(d.Unix(), 10)
	}

	log.Debug("Counter memcache lookup for key: %v", dashboardKey)

	if _, err := cache.Gob.Get(ctx, dashboardKey, &data); err == nil {
		log.Debug("Counter memcache hit for key: %v", dashboardKey)
		return data, nil
	}

	log.Debug("Counter memcache miss for key: %v", dashboardKey)
	data.TotalSales = make(currencyValue)

	var (
		newDate time.Time
		oldDate time.Time
		buckets int64
	)

	switch t {
	case Monthly:
		year := date.Year()
		month := date.Month()
		oldDate = time.Date(year, month, 1, 0, 0, 0, 0, date.Location())
		newDate = time.Date(year, month+1, 1, 0, 0, 0, 0, date.Location())

		// 0th day of month is last day of previous month
		buckets = int64(time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day())

	case Weekly:
		weekday := int(date.Weekday())
		newDate = time.Date(date.Year(), date.Month(), (7-weekday)+date.Day(), 0, 0, 0, 0, date.Location())
		oldDate = time.Date(date.Year(), date.Month(), (7-weekday)+date.Day()-7, 0, 0, 0, 0, date.Location())

		buckets = 7

	case Daily:
		newDate = time.Date(date.Year(), date.Month(), date.Day(), 23, 59, 0, 0, loc)
		oldDate = time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, loc)

		buckets = 24
	}

	log.Debug("Period %s from %s to %s", t, oldDate, newDate, ctx)

	currencies, err := Members(ctx, setName(org, currencySetKey))
	if err != nil {
		log.Error("Counter Error: %v", err)
		return data, err
	}

	users, err := Count(ctx, userKey(org, allTime))
	if err != nil {
		log.Error("Counter Error: %v", err)
		return data, err
	}

	data.TotalUsers = users

	subs, err := Count(ctx, subKey(org, mailinglistAllKey, allTime))

	if err != nil {
		log.Error("Counter Error: %v", err)
		return data, err
	}

	data.TotalSubscribers = subs

	orders, err := Count(ctx, totalKey(org, ordersKey, allTime))

	if err != nil {
		log.Error("Counter Error: %v", err)
		return data, err
	}

	data.TotalOrders = orders
	data.DailySales = make(currencyValues)
	data.DailySales[currency.USD] = make([]int, buckets)

	// Default initialization
	data.TotalSales[currency.USD] = 0
	data.DailyOrders = make([]int, buckets)
	data.DailyUsers = make([]int, buckets)
	data.DailySubscribers = make([]int, buckets)

	for _, cr := range currencies {
		cur := currency.Type(cr)

		keyId := salesKeyId(cur)
		sales, err := Count(ctx, totalKey(org, keyId, allTime))
		if err != nil {
			log.Error("Counter Error: %v", err)
			return data, err
		}

		data.TotalSales[cur] = sales

		if data.DailySales[cur] == nil {
			data.DailySales[cur] = make([]int, buckets)
		}

		data.DailyOrders = make([]int, buckets)
		data.DailyUsers = make([]int, buckets)
		data.DailySubscribers = make([]int, buckets)

		currentDate := oldDate
		startDate := currentDate
		for currentDate.Before(newDate) {
			var (
				i  int
				tf TimeFunc
			)
			if t == Daily {
				i = currentDate.Hour() - startDate.Hour()
				tf = hourly
			} else {
				i = currentDate.Day() - startDate.Day()
				if currentDate.Month() != startDate.Month() {
					i += time.Date(currentDate.Year(), currentDate.Month(), 0, 0, 0, 0, 0, time.UTC).Day()
				}
				tf = daily
			}

			sales, err := Count(ctx, totalKey(org, keyId, tf(currentDate)))
			if err != nil {
				log.Error("Counter Error: %v", err)
				return data, err
			}

			data.DailySales[cur][i] += sales

			orders, err := Count(ctx, totalKey(org, ordersKey, tf(currentDate)))
			if err != nil {
				log.Error("Counter Error: %v", err)
				return data, err
			}

			data.DailyOrders[i] += orders

			users, err := Count(ctx, userKey(org, tf(currentDate)))
			if err != nil {
				log.Error("Counter Error: %v", err)
				return data, err
			}

			data.DailyUsers[i] += users

			subs, err := Count(ctx, subKey(org, mailinglistAllKey, tf(currentDate)))
			if err != nil {
				log.Error("Counter Error: %v", err)
				return data, err
			}

			data.DailyUsers[i] += subs

			if t == Daily {
				currentDate = currentDate.Add(time.Hour)
			} else {
				currentDate = currentDate.Add(time.Hour * 24)
			}
		}
	}

	expiration := 15 * time.Minute

	item := &cache.Item{
		Key:        dashboardKey,
		Object:     data,
		Expiration: expiration,
	}

	cache.Gob.Set(ctx, item)

	return data, nil
}
