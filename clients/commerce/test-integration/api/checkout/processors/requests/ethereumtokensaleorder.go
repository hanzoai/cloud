package requests

var ValidEthereumTokenSaleOrder = `
{
  "tokenSale": {
  	"passphrase": "123456"
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
    "currency": "eth",
	"type": "ethereum",
	"tokenSaleId": "%s"
  }
}`

var InvalidEthereumNoTokenSaleIdTokenSaleOrder = `
{
  "tokenSale": {
  	"passphrase": "123456"
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
    "currency": "eth",
	"type": "ethereum"
  }
}`

var InvalidEthereumPassphraseTokenSaleOrder = `
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
	"tokenSaleId": "%s"
  }
}`
