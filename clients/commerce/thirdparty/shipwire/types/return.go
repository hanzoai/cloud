package types

type ReturnOptions struct {
	EmailCustomer        bool   `json:"email"`
	GeneratePrepaidLabel bool   `json:"prepaid"`
	Summary              string `json:"summary"`
	WarehouseRegion      string `json:"warehouseRegion"`
}

type ReturnRequest struct {
	ExternalID    string `json:"externalId"`
	OriginalOrder struct {
		ID int `json:"id"`
	} `json:"originalOrder"`
	Items   []Item `json:"items"`
	Options struct {
		GeneratePrepaidLabel int    `json:"generatePrepaidLabel"`
		EmailCustomer        int    `json:"emailCustomer"`
		WarehouseID          int    `json:"warehouseId,omitempty"`
		WarehouseExternalID  string `json:"warehouseExternalId,omitempty"`
		WarehouseRegion      string `json:"warehouseRegion,omitempty"`
	} `json:"options"`
}

type Return struct {
	ExternalID    string `json:"externalId"`
	OrderNo       string `json:"orderNo"`
	ID            int    `json:"id"`
	TransactionID string `json:"transactionId"`

	Options struct {
		ResourceLocation interface{} `json:"resourceLocation"`
		Resource         struct {
			WarehouseID         int    `json:"warehouseId"`
			WarehouseExternalID string `json:"warehouseExternalId"`
			WarehouseRegion     string `json:"warehouseRegion"`
		} `json:"resource"`
	} `json:"options"`

	ExpectedDate    Date   `json:"expectedDate"`
	CommerceName    string `json:"commerceName"`
	LastUpdatedDate Date   `json:"lastUpdatedDate"`
	Status          string `json:"status"`

	Items struct {
		ResourceLocation string `json:"resourceLocation"`
		Resource         struct {
			Offset   int         `json:"offset"`
			Total    int         `json:"total"`
			Previous interface{} `json:"previous"`
			Next     interface{} `json:"next"`
			Items    []struct {
				ResourceLocation interface{} `json:"resourceLocation"`
				Resource         struct {
					Sku               string `json:"sku"`
					Quantity          int    `json:"quantity"`
					ProductID         int    `json:"productId"`
					ProductExternalID string `json:"productExternalId"`
					OrderID           int    `json:"orderId"`
					OrderExternalID   string `json:"orderExternalId"`
					Expected          int    `json:"expected"`
					Pending           int    `json:"pending"`
					Good              int    `json:"good"`
					InReview          int    `json:"inReview"`
					Damaged           int    `json:"damaged"`
				} `json:"resource"`
			} `json:"items"`
		} `json:"resource"`
	} `json:"items"`

	Holds struct {
		ResourceLocation string `json:"resourceLocation"`
		Resource         struct {
			Offset   int           `json:"offset"`
			Total    int           `json:"total"`
			Previous interface{}   `json:"previous"`
			Next     interface{}   `json:"next"`
			Items    []interface{} `json:"items"`
		} `json:"resource"`
	} `json:"holds"`

	Trackings struct {
		ResourceLocation string `json:"resourceLocation"`
		Resource         struct {
			Offset   int           `json:"offset"`
			Total    int           `json:"total"`
			Previous interface{}   `json:"previous"`
			Next     interface{}   `json:"next"`
			Items    []interface{} `json:"items"`
		} `json:"resource"`
	} `json:"trackings"`

	Labels struct {
		ResourceLocation string `json:"resourceLocation"`
		Resource         struct {
			Offset   int         `json:"offset"`
			Total    int         `json:"total"`
			Previous interface{} `json:"previous"`
			Next     interface{} `json:"next"`
			Items    []struct {
				ResourceLocation string `json:"resourceLocation"`
				Resource         struct {
					ID              int    `json:"id"`
					OrderID         int    `json:"orderId"`
					OrderExternalID string `json:"orderExternalId"`
				} `json:"resource"`
			} `json:"items"`
		} `json:"resource"`
	} `json:"labels"`

	Routing struct {
		ResourceLocation interface{} `json:"resourceLocation"`
		Resource         struct {
			WarehouseID         int     `json:"warehouseId"`
			WarehouseExternalID string  `json:"warehouseExternalId"`
			WarehouseName       string  `json:"warehouseName"`
			OriginLongitude     float64 `json:"originLongitude"`
			OriginLatitude      float64 `json:"originLatitude"`
			WarehouseRegion     string  `json:"warehouseRegion"`
		} `json:"resource"`
	} `json:"routing"`

	Events struct {
		ResourceLocation interface{} `json:"resourceLocation"`
		Resource         struct {
			CreatedDate          Date `json:"createdDate"`
			PickedUpDate         Date `json:"pickedUpDate"`
			SubmittedDate        Date `json:"submittedDate"`
			ProcessedDate        Date `json:"processedDate"`
			CompletedDate        Date `json:"completedDate"`
			ExpectedDate         Date `json:"expectedDate"`
			DeliveredDate        Date `json:"deliveredDate"`
			CancelledDate        Date `json:"cancelledDate"`
			ReturnedDate         Date `json:"returnedDate"`
			LastManualUpdateDate Date `json:"lastManualUpdateDate"`
		} `json:"resource"`
	} `json:"events"`

	Documents interface{} `json:"documents"`

	OriginalOrder struct {
		ResourceLocation string `json:"resourceLocation"`
		Resource         Order  `json:"resource"`
	} `json:"originalOrder"`
}
