package fixtures

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/models/form"
	"github.com/hanzoai/cloud/clients/commerce/models/types/thankyou"
)

var Form = New("form", func(c *gin.Context) *form.Form {
	db := getNamespaceDb(c)

	f := form.New(db)

	f.Name = "Such Tees Newsletter"
	f.SendWelcome = true
	f.Type = "signup"

	f.EmailList.Id = "cc383800a7"
	f.EmailList.Enabled = true

	f.ThankYou.Type = thankyou.Redirect
	f.ThankYou.Url = "http://suchtees.com/thanks/"
	f.Facebook.Id = "6031480185266"
	f.Facebook.Value = "0.00"
	f.Facebook.Currency = "USD"

	f.Google.Category = "Subscription"
	f.Google.Name = "Newsletter Sign-up"

	f.MustPut()

	return f
})
