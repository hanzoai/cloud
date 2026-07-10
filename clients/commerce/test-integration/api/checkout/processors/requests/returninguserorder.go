package requests

var ReturningUserOrder = `
{
  "payment": {
    "type": "stripe",
    "account": {
      "number": "4242424242424242",
      "month": "12",
      "year": "2042",
      "cvc": "123"
    }
  },
  "user": {
	"id": "%s"
  },
  "order": {
    "currency": "usd",
    "items": [ {
      "productSlug": "doge-shirt",
      "price": 1000,
      "quantity": 3
    } ]
  }
}`
