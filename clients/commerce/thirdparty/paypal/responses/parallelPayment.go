package responses

type Error struct {
	ErrorId   string
	Domain    string
	Subdomain string
	Severity  string
	Category  string
	Message   string
}

type ParallelPaymentResponse struct {
	PayKey            string
	PaymentExecStatus string
	Error             []Error
}

type PaymentDetailsResponse struct {
}
