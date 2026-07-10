package requests

var ValidBitcoinOrder = `
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
	"currency": "btc",
	"type": "bitcoin",
	"items": [ {
      "productSlug": "doge-shirt",
      "price": 1000,
      "quantity": 2
    } ]
  }
}`

var InvalidCurrencyBitcoinOrder = `
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
	"type": "bitcoin",
	"items": [ {
      "productSlug": "doge-shirt",
      "price": 1000,
      "quantity": 2
    } ]
  }
}`
