package requests

var ValidSubscription = `
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
    "email": "dev@hanzo.ai",
    "firstName": "Fry",
    "LastName": "Not Sure",
    "address": {
      "line1": "1 Planet Way",
      "city": "New York",
      "state": "New New York",
      "country": "United States",
      "postalCode": "55555-5555"
    }
  }
}`
