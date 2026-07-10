package types

type RateRequest struct {
	Options struct {
		Currency              string `json:"currency"`
		CanSplit              int    `json:"canSplit"`
		WarehouseArea         string `json:"warehouseArea"`
		ChannelName           string `json:"channelName"`
		ExpectedShipDate      string `json:"expectedShipDate"`
		HighAccuracyEstimates int    `json:"highAccuracyEstimates"`
		ReturnAllRates        int    `json:"returnAllRates"`
	} `json:"options"`

	Order struct {
		ShipTo struct {
			Address1     string `json:"address1"`
			Address2     string `json:"address2"`
			Address3     string `json:"address3"`
			City         string `json:"city"`
			PostalCode   string `json:"postalCode"`
			State        string `json:"state"`
			Country      string `json:"country"`
			IsCommercial int    `json:"isCommercial"`
			IsPoBox      int    `json:"isPoBox"`
		} `json:"shipTo"`
		Items []Item `json:"items"`
	} `json:"order"`
}

type RateResponse struct {
	Status           int      `json:"status"`
	Message          string   `json:"message"`
	Warnings         struct{} `json:"warnings"`
	Errors           struct{} `json:"errors"`
	ResourceLocation string   `json:"resourceLocation"`
	Resource         []Rates  `json:"resource"`
}

type Rates struct {
	WarehouseName       string      `json:"warehouseName"`
	WarehouseID         int         `json:"warehouseId"`
	WarehouseExternalID string      `json:"warehouseExternalId"`
	VendorID            interface{} `json:"vendorId"`
	VendorExternalID    interface{} `json:"vendorExternalId"`
	VendorName          interface{} `json:"vendorName"`

	ShipTo struct {
		Email        string `json:"email"`
		Name         string `json:"name"`
		Company      string `json:"company"`
		Address1     string `json:"address1"`
		Address2     string `json:"address2"`
		Address3     string `json:"address3"`
		City         string `json:"city"`
		State        string `json:"state"`
		PostalCode   string `json:"postalCode"`
		Country      string `json:"country"`
		Phone        string `json:"phone"`
		IsCommercial int    `json:"isCommercial"`
		IsPoBox      int    `json:"isPoBox"`
	} `json:"shipTo"`

	Pieces []struct {
		Length struct {
			Amount float64 `json:"amount"`
			Units  string  `json:"units"`
		} `json:"length"`
		Width struct {
			Amount float64 `json:"amount"`
			Units  string  `json:"units"`
		} `json:"width"`
		Height struct {
			Amount float64 `json:"amount"`
			Units  string  `json:"units"`
		} `json:"height"`
		Weight struct {
			Amount float64 `json:"amount"`
			Units  string  `json:"units"`
			Type   string  `json:"type"`
		} `json:"weight"`
		Subweights []struct {
			Amount float64 `json:"amount"`
			Units  string  `json:"units"`
			Type   string  `json:"type"`
		} `json:"subweights"`
		Contents []struct {
			Sku      string `json:"sku"`
			Quantity int    `json:"quantity"`
		} `json:"contents"`
	} `json:"pieces"`

	ShippingOptions []struct {
		Carrier struct {
			Code        string   `json:"code"`
			Name        string   `json:"name"`
			Description string   `json:"description"`
			Properties  []string `json:"properties"`
		} `json:"carrier"`
		Cost struct {
			Currency         string  `json:"currency"`
			Type             string  `json:"type"`
			Name             string  `json:"name"`
			Amount           float64 `json:"amount"`
			Converted        bool    `json:"converted"`
			OriginalAmount   float64 `json:"originalAmount"`
			OriginalCurrency string  `json:"originalCurrency"`
		} `json:"cost"`
		Subtotals []struct {
			Currency         string  `json:"currency"`
			Type             string  `json:"type"`
			Name             string  `json:"name"`
			Amount           float64 `json:"amount"`
			Converted        bool    `json:"converted"`
			OriginalAmount   float64 `json:"originalAmount"`
			OriginalCurrency string  `json:"originalCurrency"`
		} `json:"subtotals"`
		ExpectedShipDate        Date   `json:"expectedShipDate"`
		ExpectedDeliveryMinDate Date   `json:"expectedDeliveryMinDate"`
		ExpectedDeliveryMaxDate Date   `json:"expectedDeliveryMaxDate"`
		ServiceLevel            string `json:"serviceLevel"`
	} `json:"shippingOptions"`

	RecommendedShippingOptionsIndex struct {
		GD   int `json:"GD"`
		TwoD int `json:"2D"`
		OneD int `json:"1D"`
	} `json:"recommendedShippingOptionsIndex"`
}
