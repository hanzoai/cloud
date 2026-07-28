package namecom

import (
	"context"
	"net/url"
)

// Hello probes auth + connectivity: GET /v4/hello. A 2xx means the credentials are
// accepted and API access is enabled for the account; a 403 "Permission Denied" means
// the token is IP-locked or the account lacks API/reseller access.
func (c *Client) Hello(ctx context.Context) (*HelloResponse, error) {
	var out HelloResponse
	if err := c.do(ctx, "GET", "/v4/hello", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CheckAvailability checks one or more exact domain names: POST /v4/domains:checkAvailability.
// Each result carries purchasable + the wholesale first-term and renewal prices (USD).
func (c *Client) CheckAvailability(ctx context.Context, names ...string) (*SearchResponse, error) {
	var out SearchResponse
	if err := c.do(ctx, "POST", "/v4/domains:checkAvailability",
		AvailabilityRequest{DomainNames: names}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Search runs a keyword search that also suggests alternate TLDs:
// POST /v4/domains:search. tldFilter (optional) narrows the returned TLDs.
func (c *Client) Search(ctx context.Context, keyword string, tldFilter ...string) (*SearchResponse, error) {
	var out SearchResponse
	req := SearchRequest{Keyword: keyword}
	if len(tldFilter) > 0 {
		req.TLDFilter = tldFilter
	}
	if err := c.do(ctx, "POST", "/v4/domains:search", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetDomain reads one domain the reseller owns: GET /v4/domains/{domain}.
func (c *Client) GetDomain(ctx context.Context, domain string) (*Domain, error) {
	var out Domain
	if err := c.do(ctx, "GET", "/v4/domains/"+url.PathEscape(domain), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListDomains lists the domains the reseller owns: GET /v4/domains.
func (c *Client) ListDomains(ctx context.Context) (*ListDomainsResponse, error) {
	var out ListDomainsResponse
	if err := c.do(ctx, "GET", "/v4/domains", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateDomain registers a domain: POST /v4/domains. This DEBITS the reseller account
// at name.com by the wholesale price. purchasePrice caps the accepted charge (a price
// change above it is rejected). years defaults to 1.
func (c *Client) CreateDomain(ctx context.Context, req CreateDomainRequest) (*CreateDomainResponse, error) {
	if req.Years <= 0 {
		req.Years = 1
	}
	var out CreateDomainResponse
	if err := c.do(ctx, "POST", "/v4/domains", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RenewDomain renews a registered domain: POST /v4/domains/{domain}:renew.
func (c *Client) RenewDomain(ctx context.Context, domain string, req RenewDomainRequest) (*RenewDomainResponse, error) {
	if req.Years <= 0 {
		req.Years = 1
	}
	var out RenewDomainResponse
	if err := c.do(ctx, "POST", "/v4/domains/"+url.PathEscape(domain)+":renew", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SetNameservers points a registered domain at the given authoritative nameservers:
// POST /v4/domains/{domain}:setNameservers. This is the step that hands DNS control
// to Hanzo's own nameservers after registration.
func (c *Client) SetNameservers(ctx context.Context, domain string, nameservers []string) (*Domain, error) {
	var out Domain
	if err := c.do(ctx, "POST", "/v4/domains/"+url.PathEscape(domain)+":setNameservers",
		SetNameserversRequest{Nameservers: nameservers}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SetContacts updates the WHOIS contact set: POST /v4/domains/{domain}:setContacts.
func (c *Client) SetContacts(ctx context.Context, domain string, contacts Contacts) (*Domain, error) {
	var out Domain
	if err := c.do(ctx, "POST", "/v4/domains/"+url.PathEscape(domain)+":setContacts",
		SetContactsRequest{Contacts: contacts}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateTransfer starts a transfer-in of a domain the customer owns elsewhere:
// POST /v4/transfers. authCode is the EPP/auth code from the losing registrar.
func (c *Client) CreateTransfer(ctx context.Context, req TransferRequest) (*TransferResponse, error) {
	if req.Years <= 0 {
		req.Years = 1
	}
	var out TransferResponse
	if err := c.do(ctx, "POST", "/v4/transfers", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
