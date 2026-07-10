package mixin

type ErrorMessage struct {
	ErrorCode    string
	ErrorMessage string
}

type JsonResponse struct {
	Meta struct {
		RetrievedAt   string
		ExecutionTime int64
		CallsUsed     int64
	}

	Errors []ErrorMessage
}
