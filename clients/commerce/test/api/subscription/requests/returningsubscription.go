package requests

var ReturningSubscription = `
{
  "subscription": {
	"planId": "much-shirts",
    "account": {
      "number": "4242424242424242",
      "month": "12",
      "year": "2016",
      "cvc": "123"
    }
  },
  "user": {
	"id": "%s"
  }
}`
