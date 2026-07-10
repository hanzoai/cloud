package types

type Address struct {
	Name       string `json:"name,omitempty"`
	Line1      string `json:"line1,omitempty"`
	Line2      string `json:"line2,omitempty"`
	City       string `json:"city,omitempty"`
	State      string `json:"state,omitempty"`
	PostalCode string `json:"postalCode,omitempty"`
	Country    string `json:"country,omitempty"`
}

func (a Address) Line() string {
	return a.Line1 + " " + a.Line2
}

func (a Address) Empty() bool {
	return a.Line1 == "" && a.Line2 == "" && a.City == "" && a.State == "" && a.PostalCode == "" && a.Country == ""
}
