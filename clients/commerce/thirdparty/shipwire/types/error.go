package types

type Error struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`

	// Ignored for now:
	// OrderNo string `json:"orderNo"`
	// ExternalId string `json:"externalId"`
	// Id int `json:"id"`
}
