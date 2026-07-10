package requests

var ValidOrderCoupon = `
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
	"couponCodes": ["such-coupon"],
    "items": [ {
      "productSlug": "doge-shirt",
      "price": 1000,
      "quantity": 2
    } ]
  }
}`
