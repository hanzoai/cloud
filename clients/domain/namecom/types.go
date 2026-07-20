package namecom

// The request/response shapes below mirror the name.com Core API v4 JSON. Only the
// fields Hanzo Domains uses are modeled; unknown fields are ignored on decode.

// Contact is a WHOIS/registration contact. name.com requires registrant/admin/tech/
// billing contacts on register; missing ones default to the reseller account.
type Contact struct {
	FirstName string `json:"firstName,omitempty"`
	LastName  string `json:"lastName,omitempty"`
	Company   string `json:"companyName,omitempty"`
	Address1  string `json:"address1,omitempty"`
	Address2  string `json:"address2,omitempty"`
	City      string `json:"city,omitempty"`
	State     string `json:"state,omitempty"`
	Zip       string `json:"zip,omitempty"`
	Country   string `json:"country,omitempty"` // ISO-3166 alpha-2, e.g. "US"
	Phone     string `json:"phone,omitempty"`   // +NN.NNNNNNN
	Fax       string `json:"fax,omitempty"`
	Email     string `json:"email,omitempty"`
}

// Contacts is the four-role contact set for a domain.
type Contacts struct {
	Registrant *Contact `json:"registrant,omitempty"`
	Admin      *Contact `json:"admin,omitempty"`
	Tech       *Contact `json:"tech,omitempty"`
	Billing    *Contact `json:"billing,omitempty"`
}

// Domain is a domain record as name.com returns it (get/create/renew/setNameservers).
type Domain struct {
	DomainName   string    `json:"domainName"`
	Nameservers  []string  `json:"nameservers,omitempty"`
	Contacts     *Contacts `json:"contacts,omitempty"`
	Locked       bool      `json:"locked,omitempty"`
	AutorenewOn  bool      `json:"autorenewEnabled,omitempty"`
	ExpireDate   string    `json:"expireDate,omitempty"`   // RFC3339
	CreateDate   string    `json:"createDate,omitempty"`   // RFC3339
	RenewalPrice float64   `json:"renewalPrice,omitempty"` // USD
	PrivacyOn    bool      `json:"privacyEnabled,omitempty"`
}

// AvailabilityRequest is the body of POST /v4/domains:checkAvailability.
type AvailabilityRequest struct {
	DomainNames []string `json:"domainNames"`
}

// SearchRequest is the body of POST /v4/domains:search — a keyword search that also
// suggests alternate TLDs. tldFilter narrows to specific TLDs (e.g. ["ai","com"]).
type SearchRequest struct {
	Keyword   string   `json:"keyword"`
	TLDFilter []string `json:"tldFilter,omitempty"`
	Timeout   int      `json:"timeout,omitempty"` // ms; name.com caps the search
}

// SearchResult is one candidate in a search / availability response. Prices are USD.
type SearchResult struct {
	DomainName    string  `json:"domainName"`
	SLD           string  `json:"sld,omitempty"`
	TLD           string  `json:"tld,omitempty"`
	Purchasable   bool    `json:"purchasable"`
	Premium       bool    `json:"premium,omitempty"`
	PurchasePrice float64 `json:"purchasePrice,omitempty"` // first-term registration, USD
	PurchaseType  string  `json:"purchaseType,omitempty"`  // "registration" | "renewal" | ...
	RenewalPrice  float64 `json:"renewalPrice,omitempty"`  // USD
	Transferable  bool    `json:"transferable,omitempty"`
}

// SearchResponse wraps the results for both search and checkAvailability.
type SearchResponse struct {
	Results []SearchResult `json:"results"`
}

// CreateDomainRequest is the body of POST /v4/domains (register). purchasePrice is
// the price the caller EXPECTS to pay (the wholesale quote from availability); name.com
// rejects a registration whose real price exceeds it — a guard against a price change
// between quote and buy. years defaults to 1.
type CreateDomainRequest struct {
	Domain          DomainInput       `json:"domain"`
	PurchasePrice   float64           `json:"purchasePrice,omitempty"`
	Years           int               `json:"years,omitempty"`
	TLDRequirements map[string]string `json:"tldRequirements,omitempty"`
}

// DomainInput is the domain sub-object of a create request.
type DomainInput struct {
	DomainName  string    `json:"domainName"`
	Nameservers []string  `json:"nameservers,omitempty"`
	Contacts    *Contacts `json:"contacts,omitempty"`
	PrivacyOn   bool      `json:"privacyEnabled,omitempty"`
}

// CreateDomainResponse is the register result: the created domain plus what name.com
// actually charged the reseller account (order + totalPaid, USD).
type CreateDomainResponse struct {
	Domain    *Domain `json:"domain"`
	Order     int64   `json:"order,omitempty"`
	TotalPaid float64 `json:"totalPaid,omitempty"`
}

// RenewDomainRequest is the body of POST /v4/domains/{domain}:renew.
type RenewDomainRequest struct {
	PurchasePrice float64 `json:"purchasePrice,omitempty"`
	Years         int     `json:"years,omitempty"`
}

// RenewDomainResponse is the renew result.
type RenewDomainResponse struct {
	Domain    *Domain `json:"domain"`
	Order     int64   `json:"order,omitempty"`
	TotalPaid float64 `json:"totalPaid,omitempty"`
}

// SetNameserversRequest is the body of POST /v4/domains/{domain}:setNameservers —
// this is how a registered domain is pointed at Hanzo's authoritative nameservers.
type SetNameserversRequest struct {
	Nameservers []string `json:"nameservers"`
}

// SetContactsRequest is the body of POST /v4/domains/{domain}:setContacts.
type SetContactsRequest struct {
	Contacts Contacts `json:"contacts"`
}

// ListDomainsResponse is the body of GET /v4/domains.
type ListDomainsResponse struct {
	Domains  []Domain `json:"domains"`
	NextPage int      `json:"nextPage,omitempty"`
}

// TransferRequest is the body of POST /v4/transfers (transfer a domain IN to Hanzo).
type TransferRequest struct {
	DomainName    string  `json:"domainName"`
	AuthCode      string  `json:"authCode"`
	PurchasePrice float64 `json:"purchasePrice,omitempty"`
	Years         int     `json:"years,omitempty"`
}

// Transfer is a transfer record as name.com returns it.
type Transfer struct {
	DomainName string `json:"domainName"`
	Email      string `json:"email,omitempty"`
	Status     string `json:"status,omitempty"`
}

// TransferResponse is the create-transfer result.
type TransferResponse struct {
	Transfer  *Transfer `json:"transfer"`
	Order     int64     `json:"order,omitempty"`
	TotalPaid float64   `json:"totalPaid,omitempty"`
}

// HelloResponse is the body of GET /v4/hello — the auth/health probe.
type HelloResponse struct {
	ServerName string `json:"serverName,omitempty"`
	Motd       string `json:"motd,omitempty"`
	Username   string `json:"username,omitempty"`
}
