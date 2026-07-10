package requests

var ValidOrderTemplate = `
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
    "couponCodes": ["%s"],
    "items": [ {
      "productSlug": "doge-shirt",
      "price": 1000,
      "quantity": 2
    } ]
  }
}`
