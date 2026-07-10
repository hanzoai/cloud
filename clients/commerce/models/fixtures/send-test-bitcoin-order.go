package fixtures

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/api/checkout"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/types/accounts"
	"github.com/hanzoai/cloud/clients/commerce/models/types/country"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
)

var SendTestBitcoinOrder = New("send-test-bitcoin-order", func(c *gin.Context) {
	org := Organization(c).(*organization.Organization)
	accessToken := org.MustGetTokenByName("test-published-key")

	ctx := org.Datastore().Context

	db := getNamespaceDb(c)

	u := UserCustomer(c)

	ord := order.New(db)
	ord.UserId = u.Id()
	ord.ShippingAddress.Name = "Jackson Shirts"
	ord.ShippingAddress.Line1 = "1234 Kansas Drive"
	ord.ShippingAddress.City = "Overland Park"

	ctr, _ := country.FindByISO3166_2("US")
	sd, _ := ctr.FindSubDivision("Kansas")

	ord.ShippingAddress.State = sd.Code
	ord.ShippingAddress.Country = ctr.Codes.Alpha2
	ord.ShippingAddress.PostalCode = "66212"
	ord.Type = accounts.BitcoinType

	ord.Currency = currency.BTC
	ord.Subtotal = currency.Cents(1e5)
	ord.Mode = order.ContributionMode

	ch := checkout.Authorization{
		Order: ord,
	}

	j := json.Encode(ch)

	log.Info("Sending To %s", "https://api.hanzo.ai/checkout/authorize/", c)
	log.Info("Sending Test Order: %s", j, c)

	client := &http.Client{}
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.hanzo.ai/checkout/authorize/", strings.NewReader(j))
	if err != nil {
		panic(err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", accessToken.String)

	if res, err := client.Do(req); err != nil {
		panic(err)
	} else {
		log.Info("Hanzo Test Response: %v", res, c)
	}
})
