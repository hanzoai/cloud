package requests

var ValidEthereumOrder = `
{
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
  },
  "order": {
	"currency": "eth",
	"type": "ethereum",
	"items": [ {
      "productSlug": "doge-shirt",
      "price": 1000,
      "quantity": 2
    } ]
  }
}`

var InvalidCurrencyEthereumOrder = `
{
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
  },
  "order": {
	"currency": "usd",
	"type": "ethereum",
	"items": [ {
      "productSlug": "doge-shirt",
      "price": 1000,
      "quantity": 2
    } ]
  }
}`
