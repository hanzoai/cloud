package requests

var InvalidOrderBadCard = `
{
  "payment": {
    "type": "stripe",
    "account": {
      "number": "2424242424242424",
      "month": "12",
      "year": "2042",
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
  },
  "order": {
    "currency": "usd",
    "items": [ {
      "productSlug": "doge-shirt",
      "price": 1000,
      "quantity": 2
    } ]
  }
}`
