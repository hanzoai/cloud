package requests

var StripeRefundEvent = `
{
  "id": "evt_19E8bSCSRlllXCwP1FGOSbJu",
  "object": "event",
  "api_version": "2016-07-06",
  "created": 1478746070,
  "data": {
    "object": {
	  "id": "%s",
	  "object": "charge",
	  "amount": 2000,
	  "amount_refunded": 0,
	  "application": null,
	  "application_fee": null,
	  "balance_transaction": "txn_19DMjuCSRlllXCwPWftjwidI",
	  "captured": true,
	  "created": 1478311255,
	  "currency": "usd",
	  "customer": "cus_7TRHooO9xkO33g",
	  "description": null,
	  "destination": null,
	  "dispute": null,
	  "failure_code": null,
	  "failure_message": null,
	  "fraud_details": {
	  },
	  "invoice": "in_19CIXKCSRlllXCwPE79Sb2UB",
	  "livemode": false,
	  "metadata": {
	  },
	  "order": null,
	  "outcome": {
		"network_status": "approved_by_network",
		"reason": null,
		"risk_level": "normal",
		"seller_message": "Payment complete.",
		"type": "authorized"
	  },
	  "paid": true,
	  "receipt_email": "dev@hanzo.ai",
	  "receipt_number": null,
	  "refunded": true,
	  "refunds": {
		"object": "list",
		"data": [

		],
		"has_more": false,
		"total_count": 0,
		"url": "/v1/charges/ch_19CJUJCSRlllXCwPRi2GMdeQ/refunds"
	  },
	  "review": null,
	  "shipping": null,
	  "source": {
		"id": "card_17EUOmCSRlllXCwPpT1APWRY",
		"object": "card",
		"address_city": null,
		"address_country": null,
		"address_line1": null,
		"address_line1_check": null,
		"address_line2": null,
		"address_state": null,
		"address_zip": null,
		"address_zip_check": null,
		"brand": "Visa",
		"country": "US",
		"customer": "cus_7TRHooO9xkO33g",
		"cvc_check": null,
		"dynamic_last4": null,
		"exp_month": 12,
		"exp_year": 2042,
		"funding": "credit",
		"last4": "4242",
		"metadata": {
		},
		"name": "Fry Not Sure",
		"tokenization_method": null
	  },
	  "source_transfer": null,
	  "statement_descriptor": null,
	  "status": "succeeded"
	}
  },
  "livemode": false,
  "pending_webhooks": 0,
  "request": "req_9XD1koMYJ3TFdT",
  "type": "charge.refunded",
  "user_id": "acct_16fNBDH4ZOGOmFfW"
}
`
