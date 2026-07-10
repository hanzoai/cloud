package types

type Tracking struct {
	ID              int    `json:"id"`
	OrderID         int    `json:"orderId"`
	PieceID         int    `json:"pieceId"`
	OrderExternalID string `json:"orderExternalId"`
	Carrier         string `json:"carrier"`
	Url             string `json:"url"`

	Summary     string `json:"summary"`
	SummaryDate Date   `json:"summaryDate"`

	Tracking    string `json:"tracking"`
	TrackedDate Date   `json:"trackedDate"`

	LabelCreatedDate Date `json:"labelCreatedDate"`

	FirstScanRegion     string `json:"firstScanRegion"`
	FirstScanPostalCode string `json:"firstScanPostalCode"`
	FirstScanCountry    string `json:"firstScanCountry"`
	FirstScanDate       Date   `json:"firstScanDate"`

	DeliveryCity       string `json:"deliveryCity"`
	DeliveryRegion     string `json:"deliveryRegion"`
	DeliveryPostalCode string `json:"DeliveryPostalCode"`
	DeliveryCountry    string `json:"DeliveryCountry"`
	DeliveredDate      Date   `json:"deliveredDate"`
}
