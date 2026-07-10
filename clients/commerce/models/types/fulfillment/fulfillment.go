package fulfillment

import (
	"time"

	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
)

type Status string
type Type string

const (
	Pending   Status = "pending"
	Processed Status = "processed"
	Canceled  Status = "canceled"
	Completed Status = "completed"
	Delivered Status = "delivered"
	Returned  Status = "returned"
	Submitted Status = "submitted"
	Held      Status = "held"
	Tracked   Status = "tracked"
)

const (
	Shipwire    Type = "shipwire"
	ShipStation Type = "shipstation"
	Manual      Type = "manual"
)

type Tracking struct {
	Number     string    `json:"number,omitempty"`
	ExternalId string    `json:"externalId,omitempty"`
	Url        string    `json:"url,omitempty"`
	CreatedAt  time.Time `json:"createdAt,omitempty"`

	Carrier string `json:"carrier,omitempty"`

	Summary   string    `json:"summary,omitempty"`
	SummaryAt time.Time `json:"summaryAt,omitempty"`

	LabelCreatedAt time.Time `json:"labelCreatedAt,omitempty"`

	FirstScanRegion     string    `json:"firstScanRegion,omitempty"`
	FirstScanPostalCode string    `json:"firstScanPostalCode,omitempty"`
	FirstScanCountry    string    `json:"firstScanCountry,omitempty"`
	FirstScanAt         time.Time `json:"firstScanAt,omitempty"`

	DeliveryCity       string    `json:"deliveryCity,omitempty"`
	DeliveryRegion     string    `json:"deliveryRegion,omitempty"`
	DeliveryPostalCode string    `json:"deliveryPostalCode,omitempty"`
	DeliveryCountry    string    `json:"deliveryCountry,omitempty"`
	DeliveredAt        time.Time `json:"deliveredAt,omitempty"`
}

type Hold struct {
	Type        string    `json:"type"`
	Description string    `json:"description"`
	ExternalId  string    `json:"externalId"`
	AppliedAt   time.Time `json:"appliedAt"`
}

type Fulfillment struct {
	Type       Type   `json:"type"`
	Status     Status `json:"status"`
	ExternalId string `json:"externalId,omitempty"`

	Service         string         `json:"service"`
	Carrier         string         `json:"carrier"`
	SameDay         string         `json:"sameDay,omitempty"`
	Pricing         currency.Cents `json:"pricing,omitempty"`
	PricingEstimate currency.Cents `json:"pricingEstimate,omitempty"`

	// When was the order created
	CreatedAt time.Time `json:"createdAt,omitempty"`
	// When was the order picked up
	PickedUpAt time.Time `json:"pickedUpAt"`
	// When was the order submitted to the warehouse
	SubmittedAt time.Time `json:"submittedAt"`
	// When was the order processed by the warehouse
	ProcessedAt time.Time `json:"processedAt"`
	// When was order processing completed
	CompletedAt time.Time `json:"completedAt"`
	// When was the package expected to be delivered
	ExpectedAt time.Time `json:"expectedAt"`
	// When was the package cancelled
	CancelledAt time.Time `json:"cancelledAt"`
	// When was the package returned
	ReturnedAt time.Time `json:"returnedAt"`

	ExpectedSubmittedAt time.Time `json:"expectedSubmittedAt"`
	ExpectedCompletedAt time.Time `json:"expectedCompletedAt"`
	LastManualUpdateAt  time.Time `json:"lastManualUpdateAt"`

	Holds     []Hold     `json:"holds,omitempty"`
	Trackings []Tracking `json:"trackings,omitempty"`

	WarehouseId     string `json:"warehouseId,omitempty"`
	WarehouseRegion string `json:"warehouseRegion,omitempty"`
}
