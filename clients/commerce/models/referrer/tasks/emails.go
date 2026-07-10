package tasks

import (
	"context"
	"strconv"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/delay"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	// mandrill "github.com/hanzoai/cloud/clients/commerce/thirdparty/mandrill/tasks"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/woopra"
)

// Fire webhooks
var SendUserEmail = delay.Func("referrer-send-user-email", func(ctx context.Context, orgId string, templateName string, usrId string) {
	db := datastore.New(ctx)
	org := organization.New(db)
	if err := org.GetById(orgId); err != nil {
		log.Error("Could not get organization '%s', %s", orgId, err, ctx)
		return
	}

	// User Welcome stuff
	if !org.Email.Enabled {
		return
	}

	nsCtx := org.Namespaced(ctx)
	nsDb := datastore.New(nsCtx)
	usr := user.New(nsDb)
	if err := usr.GetById(usrId); err != nil {
		log.Error("Could not get user '%s'", usrId, ctx)
		return
	}

	// // From
	// from := org.Email.From

	// // To
	// toEmail := usr.Email
	// toName := usr.Name()

	// // Create Merge Vars
	// vars := map[string]interface{}{
	// 	"user": map[string]interface{}{
	// 		"firstname": usr.FirstName,
	// 		"lastname":  usr.LastName,
	// 	},
	// 	"USER_FIRSTNAME": usr.FirstName,
	// 	"USER_LASTNAME":  usr.LastName,
	// }

	// Send Email
	// mandrill.SendTemplate(ctx, templateName, org.Mandrill.APIKey, toEmail, toName, fromEmail, fromName, "", vars)
})

var SendWoopraEvent = delay.Func("referrer-send-woopra-event", func(ctx context.Context, orgId, domain, usrId, id, kind string) {
	db := datastore.New(ctx)
	org := organization.New(db)

	if err := org.GetById(orgId); err != nil {
		log.Error("Could not get organization '%s', %s", orgId, err, ctx)
		return
	}

	nsCtx := org.Namespaced(ctx)
	nsDb := datastore.New(nsCtx)
	usr := user.New(nsDb)
	if err := usr.GetById(usrId); err != nil {
		log.Panic("Could not get referring user '%s'", usrId, ctx)
		return
	}

	if err := usr.LoadReferrals(); err != nil {
		log.Panic("Could not load referring user's referrals '%s'", usrId, ctx)
		return
	}

	usrId2 := ""
	orderId := ""
	orderNumber := ""

	switch kind {
	case "order":
		log.Debug("Loading order referrent")
		ord := order.New(nsDb)
		orderId = id
		if err := ord.GetById(id); err != nil {
			log.Panic("Could not get referrent user '%s'", usrId, ctx)
			return
		}
		usrId2 = ord.UserId
		orderNumber = strconv.Itoa(ord.Number)
	case "user":
		log.Debug("Loading user referrent")
		usrId2 = id
	default:
		log.Panic("unknown kind %s", kind, ctx)
		return
	}

	usr2 := user.New(nsDb)
	if err := usr2.GetById(usrId2); err != nil {
		log.Panic("Could not get referrent user '%s'", usrId, ctx)
		return
	}

	wt, _ := woopra.NewTracker(map[string]string{

		// `host` is domain as registered in Woopra, it identifies which
		// project environment to receive the tracking request
		"host": domain,

		// In milliseconds, defaults to 30000 (equivalent to 30 seconds)
		// after which the event will expire and the visit will be marked
		// as offline.
		"timeout": "30000",
	})

	revokedReferrals := 0
	for _, v := range usr.Referrals {
		if v.Revoked {
			revokedReferrals += 1
		}
	}

	values := make(map[string]string)
	values["first_name"] = usr.FirstName
	values["last_name"] = usr.LastName
	values["city"] = usr.ShippingAddress.City
	values["country"] = usr.ShippingAddress.Country
	values["referred_by"] = usr.ReferrerId
	values["referrals"] = strconv.Itoa(len(usr.Referrals))
	values["active_referrals"] = strconv.Itoa(len(usr.Referrals) - revokedReferrals)
	values["revoked_referrals"] = strconv.Itoa(revokedReferrals)

	person := woopra.Person{
		Id:     usr.Id(),
		Name:   usr.Name(),
		Email:  usr.Email,
		Values: values,
	}

	// identifying current visitor in Woopra
	ident := wt.Identify(
		ctx,
		person,
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_11_6) AppleWebKit/601.7.7 (KHTML, like Gecko) Version/9.1.2 Safari/601.7.7",
	)

	// Tracking custom event in Woopra. Each event can has additional data
	ident.Track(
		"New Referral", // event name
		map[string]string{ // custom data
			"referred_id":           usr2.Id(),
			"referred_name":         usr2.Name(),
			"referred_email":        usr2.Email,
			"referred_order_id":     orderId,
			"referred_order_number": orderNumber,
		})

	// it's possible to send only visitor's data to Woopra, without sending
	// any custom event and/or data
	ident.Push()
})
