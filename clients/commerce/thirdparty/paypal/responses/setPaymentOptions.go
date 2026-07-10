package responses

// reference url: https://developer.paypal.com/docs/classic/api/adaptive-payments/SetPaymentOptions_API_Operation/
type SetPaymentOptionsResponse struct {
	ResponseEnvelope struct {
		Ack           string // Acknowledgement code
		Build         string // Build number, used by Paypal tech support
		CorrelationId string // Correlation identifier, used by paypal tech support
		Timestamp     string // Date stamp
		Error         string // error code
	}
	PayKey            string
	PaymentExecStatus string
}

// Example Response
// {"responseEnvelope":{"timestamp":"2015-11-03T17:14:58.757-08:00","ack":"Success","correlationId":"c7cb95e6c5ae3","build":"17820627"},"payKey":"AP-9J501673PS327884E","paymentExecStatus":"CREATED"}
