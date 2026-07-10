package account

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/paymentmethod"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/paymentmethods/plaid"
	"github.com/hanzoai/cloud/clients/commerce/types/integration"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/json/http"

	. "encoding/json"

	. "github.com/hanzoai/cloud/clients/commerce/thirdparty/paymentmethods"
)

type CreateReq struct {
	PublicToken string     `json:"publicToken"`
	AccountId   string     `json:"accountId"`
	Name        string     `json:"name"`
	Metadata    RawMessage `json:"metadata"`
}

func createPaymentMethod(c *gin.Context) {
	usr := middleware.GetUser(c)
	org := middleware.GetOrganization(c)

	req := &CreateReq{}

	// Decode response body to create new user
	if err := json.Decode(c.Request.Body, req); err != nil {
		http.Fail(c, 400, "Failed decode request body", err)
		return
	}

	var (
		pm             PaymentMethod
		externalUserId string
	)

	t := c.Params.ByName("paymentmethodtype")
	switch t {
	case "plaid":
		in := org.Integrations.FindByType(integration.PlaidType)
		if in == nil {
			http.Fail(c, 500, "Missing plaid credentials: "+t, ErrorMissingCredentials)
			return
		}
		// TODO: We need to redo the customer id/account object stuff
		externalUserId = usr.Accounts.StripeAccount.CustomerId
		pm = plaid.New(org.Context(), in.Plaid.ClientId, in.Plaid.Secret, in.Plaid.PublicKey, plaid.SandboxEnvironment)
	default:
		http.Fail(c, 500, "Invalid payment type: "+t, ErrorInvalidPaymentMethod)
		return
	}

	out, err := pm.GetPayToken(PaymentMethodParams{
		VerifierToken:  req.PublicToken,
		VerifierId:     req.AccountId,
		ExternalUserId: externalUserId,
		Metadata:       req.Metadata,
	})
	if err != nil {
		http.Fail(c, 500, "Error while creating paykey for: "+t, err)
		return
	}

	p := paymentmethod.New(usr.Datastore())
	p.UserId = usr.Id()
	p.CustomerId = usr.Id()
	p.Name = req.Name
	p.ProviderRef = out.PayToken
	p.ProviderType = string(out.Type)
	p.Type = string(out.Type)

	if err := p.Create(); err != nil {
		http.Fail(c, 400, "Failed to add payment method", err)
		return
	}

	http.Render(c, 201, p)
}
