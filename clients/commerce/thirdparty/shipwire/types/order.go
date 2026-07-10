package types

type OrderOptions struct {
	Service ServiceLevelCode `json:"service"`
}

type OrderRequest struct {
	ExternalID   string `json:"externalId"`
	OrderNo      string `json:"orderNo"`
	CommerceName string `json:"commerceName"`

	Options struct {
		ServiceLevelCode ServiceLevelCode `json:"serviceLevelCode"`
	} `json:"options"`

	ShipTo struct {
		Name       string `json:"name"`
		Email      string `json:"email"`
		Address1   string `json:"address1"`
		Address2   string `json:"address2"`
		City       string `json:"city"`
		State      string `json:"state"`
		PostalCode string `json:"postalCode"`
		Country    string `json:"country"`
	} `json:"shipTo"`

	Items []Item `json:"items"`
}

type Order struct {
	ID            int    `json:"id"`
	ExternalID    string `json:"externalId"`
	OrderNo       string `json:"orderNo"`
	TransactionID string `json:"transactionId"`

	CommerceName string `json:"commerceName"`

	NeedsReview      int    `json:"needsReview"`
	LastUpdatedDate  Date   `json:"lastUpdatedDate"`
	ProcessAfterDate Date   `json:"processAfterDate"`
	Status           string `json:"status"`

	PurchaseOrderID         string `json:"purchaseOrderId,omitempty"`
	PurchaseOrderExternalID string `json:"purchaseOrderExternalId,omitempty"`
	PurchaseOrderNo         string `json:"purchaseOrderNo,omitempty"`

	VendorExternalID string `json:"vendorExternalId,omitempty"`
	VendorID         string `json:"vendorId,omitempty"`
	VendorName       string `json:"vendorName,omitempty"`

	ShipFrom struct {
		ResourceLocation string `json:"resourceLocation"`
		Resource         struct {
			Company string `json:"company"`
		} `json:"resource"`
	} `json:"shipFrom"`

	ShipTo struct {
		ResourceLocation string `json:"resourceLocation,omitempty"`
		Resource         struct {
			City         string `json:"city"`
			Name         string `json:"name"`
			IsPoBox      int    `json:"isPoBox"`
			Address1     string `json:"address1"`
			Company      string `json:"company"`
			Address3     string `json:"address3"`
			IsCommercial int    `json:"isCommercial"`
			Email        string `json:"email"`
			Phone        string `json:"phone"`
			State        string `json:"state"`
			Country      string `json:"country"`
			PostalCode   string `json:"postalCode"`
			Address2     string `json:"address2"`
		} `json:"resource"`
	} `json:"shipTo"`

	Options struct {
		ResourceLocation string `json:"resourceLocation"`
		Resource         struct {
			WarehouseID                    int         `json:"warehouseId"`
			BillingType                    interface{} `json:"billingType"`
			WarehouseRegion                string      `json:"warehouseRegion"`
			Referrer                       string      `json:"referrer"`
			ForceAddress                   int         `json:"forceAddress"`
			WarehouseExternalID            interface{} `json:"warehouseExternalId"`
			ForceDuplicate                 int         `json:"forceDuplicate"`
			ServiceLevelCode               string      `json:"serviceLevelCode"`
			CarrierAccountNumber           interface{} `json:"carrierAccountNumber"`
			SameDay                        string      `json:"sameDay"`
			ThirdPartyCarrierCodeRequested interface{} `json:"thirdPartyCarrierCodeRequested"`
			WarehouseArea                  interface{} `json:"warehouseArea"`
			CarrierCode                    string      `json:"carrierCode"`
			ChannelName                    interface{} `json:"channelName"`
			TestOrder                      int         `json:"testOrder"`
		} `json:"resource"`
	} `json:"options"`

	PricingEstimate struct {
		ResourceLocation string `json:"resourceLocation"`
		Resource         struct {
			Packaging float64 `json:"packaging"`
			Total     float64 `json:"total"`
			Insurance float64 `json:"insurance"`
			Shipping  float64 `json:"shipping"`
			Handling  float64 `json:"handling"`
		} `json:"resource"`
	} `json:"pricingEstimate"`

	Pricing struct {
		ResourceLocation string `json:"resourceLocation"`
		Resource         struct {
			Packaging float64 `json:"packaging"`
			Total     float64 `json:"total"`
			Handling  float64 `json:"handling"`
			Insurance float64 `json:"insurance"`
			Shipping  float64 `json:"shipping"`
		} `json:"resource"`
	} `json:"pricing"`

	FreightSummary struct {
		ResourceLocation string `json:"resourceLocation"`
		Resource         struct {
			WeightUnit      interface{} `json:"weightUnit"`
			MeasurementType interface{} `json:"measurementType"`
			TotalWeight     string      `json:"totalWeight"`
		} `json:"resource"`
	} `json:"freightSummary"`

	Routing struct {
		ResourceLocation string `json:"resourceLocation,omitempty"`
		Resource         struct {
			DestinationLatitude  interface{} `json:"destinationLatitude"`
			DestinationLongitude interface{} `json:"destinationLongitude"`
			OriginLatitude       float64     `json:"originLatitude"`
			OriginLongitude      float64     `json:"originLongitude"`
			PhysicalWarehouseID  interface{} `json:"physicalWarehouseId"`
			WarehouseExternalID  interface{} `json:"warehouseExternalId"`
			WarehouseID          int         `json:"warehouseId"`
			WarehouseName        interface{} `json:"warehouseName"`
		} `json:"resource"`
	} `json:"routing"`

	CommercialInvoice struct {
		ResourceLocation string `json:"resourceLocation"`
	} `json:"commercialInvoice"`

	ShipwireAnywhere struct {
		ResourceLocation string `json:"resourceLocation,omitempty"`
		Resource         struct {
			Status string `json:"status"`
		} `json:"resource"`
	} `json:"shipwireAnywhere"`

	Events struct {
		ResourceLocation string `json:"resourceLocation"`
		Resource         struct {
			CancelledDate         Date `json:"cancelledDate"`
			CompletedDate         Date `json:"completedDate"`
			CreatedDate           Date `json:"createdDate"`
			ExpectedCompletedDate Date `json:"expectedCompletedDate"`
			ExpectedDate          Date `json:"expectedDate"`
			ExpectedSubmittedDate Date `json:"expectedSubmittedDate"`
			LastManualUpdateDate  Date `json:"lastManualUpdateDate"`
			PickedUpDate          Date `json:"pickedUpDate"`
			ProcessedDate         Date `json:"processedDate"`
			ReturnedDate          Date `json:"returnedDate"`
			SubmittedDate         Date `json:"submittedDate"`
		} `json:"resource"`
	} `json:"events"`

	Items struct {
		ResourceLocation string   `json:"resourceLocation"`
		Resource         Resource `json:"resource"`
	} `json:"items"`

	Pieces struct {
		ResourceLocation string   `json:"resourceLocation"`
		Resource         Resource `json:"resource"`
	} `json:"pieces"`

	ShippingLabel struct {
		ResourceLocation string   `json:"resourceLocation"`
		Resource         Resource `json:"resource"`
	} `json:"shippingLabel"`

	Trackings struct {
		ResourceLocation string   `json:"resourceLocation"`
		Resource         Resource `json:"resource"`
	} `json:"trackings"`

	Holds struct {
		ResourceLocation string   `json:"resourceLocation"`
		Resource         Resource `json:"resource"`
	} `json:"holds"`

	Returns struct {
		ResourceLocation string   `json:"resourceLocation"`
		Resource         Resource `json:"resource"`
	} `json:"returns"`

	PackingList struct {
		ResourceLocation string   `json:"resourceLocation"`
		Resource         Resource `json:"resource"`
	} `json:"packingList"`

	SplitOrders struct {
		ResourceLocation string   `json:"resourceLocation"`
		Resource         Resource `json:"resource"`
	} `json:"splitOrders"`
}
