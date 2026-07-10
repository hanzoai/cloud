package cardconnect

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/ioutil"
	"net/http"
	"time"

	// "github.com/hanzoai/cloud/clients/commerce/models"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
)

var baseUrl = "https://fts.prinpay.com:6443/cardconnect/rest" // 496160873888-CardConnect - USD - NORTH
var authCode = base64.StdEncoding.EncodeToString([]byte("testing:testing123"))

type LineItem struct {
	SKU          string // {
	Cost         int64  `json:"unitcost,string"` // "unitcost":    "450",
	Description  string `json:"description"`     // "description": "DESCRIPTION-2",
	DiscountAmnt int64  `json:"discamnt,string"` // "discamnt":    "0",
	LineNo       int    `json:"lineno,string"`   // "lineno":      "2",
	Quantity     int    `json:"quantity,string"` // "quantity":    "2000",
	UOM          string `json:"uom"`             // "uom":         "EA",
	// Material     string `json:"material"`		 // "material":    "MATERIAL-2"
	// NetAmnt      string `json:"netamnt"`         // "netamnt":     "300",
	// TaxAmnt      string `json:"taxamnt"`         // "taxamnt":     "117",
	// UPC          string `json:"upc"`             // "upc":	      "UPC-1",
} //  }

type AuthorizationReq struct { // {
	Account  string `json:"account"`        // "account":  "4111111111111111",
	AcctType string `json:"accttype"`       // "accttype": "VISA",
	Address  string `json:"address"`        // "address":  "123 MAIN STREET",
	Amount   int64  `json:"amount,string"`  // "amount":   "0",
	City     string `json:"city"`           // "city":     "anytown",
	Country  string `json:"country"`        // "country":  "US",
	Currency string `json:"currency"`       // "currency": "USD",
	CVV2     int    `json:"cvv2"`           // "cvv2":     "123",
	Ecomind  string `json:"ecomind"`        // "ecomind":  "E",
	Expiry   string `json:"expiry"`         // "expiry":   "1212",
	MerchId  int    `json:"merchid,string"` // "merchid":  "000000927996",
	Name     string `json:"name"`           // "name":     "TOM JONES",
	Email    string `json:"email"`          // "email":    "dev@hanzo.ai JONES",
	Phone    string `json:"phone"`          // "phone":    "913-777-9708",
	OrderId  string `json:"orderid"`        // "orderid":  "AB-11-9876",
	Postal   string `json:"postal"`         // "postal":   "55555",
	Region   string `json:"region"`         // "region":   "NY",
	Tokenize string `json:"tokenize"`       // "tokenize": "Y",
	// Track    interface{} `json:"track"`          // "track":    null,
	// }

	// Capture Request can be embeded to automate capture, we probably want to
	// do this.                         // {
	Capture string     `json:"capture"` // "capture":  "Y",
	Items   []LineItem `json:"items"`   // "items": [],
	// }

	// 3D Secure (optional), supposedly we'll get these values back from the 3D
	// secure shit when it's enabled.
	// SecureFlag  string `json:"secureflag"`
	// SecureValue string `json:"securevalue"`
	// SecureXid   string `json:"securexid"`
}

type AuthorizationRes struct { // {
	Account  string `json:"account"`  // "account":  "41XXXXXXXXXX1111",
	Amount   string `json:"amount"`   // "amount":   "111",
	AuthCode string `json:"authcode"` // "authcode": "046221",
	AVSRes   string `json:"avsresp"`  // "avsresp":  "9",
	CVVRes   string `json:"cvvresp"`  // "cvvresp":  "M",
	MerchId  string `json:"merchid"`  // "merchid":  "020594000000",
	Code     string `json:"respcode"` // "respcode": "00",
	ResProc  string `json:"respproc"` // "respproc": "FNOR"
	Status   string `json:"respstat"` // "respstat": "A",
	Text     string `json:"resptext"` // "resptext": "Approved",
	RetRef   string `json:"retref"`   // "retref":   "343005123105",
	Token    string `json:"token"`    // "token":    "9419786452781111",
} // }

func Authorize(ctx context.Context, order *order.Order, user *user.User) (ares AuthorizationRes, err error) {
	// Convert models.LineItem to our CardConnect specialized LineItem that
	// will serialize properly.
	items := make([]LineItem, len(order.Items))
	for i, v := range order.Items {
		items[i] = LineItem{
			Description: v.Product.Description,
			// DiscountAmnt: v.DiscountAmnt,
			// LineNo:       v.LineNo,
			Quantity: v.Quantity,
			// UOM:          v.UOM,
		}
	}

	areq := AuthorizationReq{
		// Account:  order.Account.Number,
		// AcctType: order.Account.Type,
		Address: order.BillingAddress.Line(),
		// Amount:  order.Total,
		// CVV2:     order.Account.CVV2,
		City:     order.BillingAddress.City,
		Country:  order.BillingAddress.Country,
		Currency: "USD",
		Ecomind:  "E",
		Email:    user.Email,
		// Expiry:   order.Account.Expiry,
		MerchId:  496160873888,
		Name:     user.Name(),
		OrderId:  order.Id(),
		Phone:    user.Phone,
		Postal:   order.BillingAddress.PostalCode,
		Region:   order.BillingAddress.State,
		Tokenize: "Y",
		Capture:  "Y",
		Items:    items,
	}

	client := &http.Client{Timeout: 55 * time.Second}

	jsonreq, _ := json.Marshal(areq)
	reqbuf := bytes.NewBuffer(jsonreq)
	log.Debug("%#v", areq)

	req, err := http.NewRequest("PUT", baseUrl+"/auth", reqbuf)
	req.Header.Add("Authorization", "Basic "+authCode)
	req.Header.Add("Content-Type", "application/json")

	switch res, err := client.Do(req); {
	case err != nil:
		return ares, err

	case res.StatusCode == 200:
		defer res.Body.Close()
		body, _ := ioutil.ReadAll(res.Body)
		json.Unmarshal(body, &ares)
		return ares, nil

	default:
		defer res.Body.Close()
		body, _ := ioutil.ReadAll(res.Body)
		json.Unmarshal(body, &ares)
		log.Error("%v %v", res.StatusCode, ares)
		return ares, errors.New("Invalid response from CardConnect.")
	}
}
