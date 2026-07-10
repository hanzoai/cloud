package requests

var ReturningUserOrderNewCard = `
{
  "payment": {
    "type": "stripe",
    "account": {
      "number": "6011111111111117",
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
