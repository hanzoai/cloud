package domain

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/hanzoai/cloud/apps/domain/namecom"
	"github.com/hanzoai/cloud/internal/environ"
)

// name.com Core API v4 — the wholesale registrar this deployment resells by default.
//
// Everything name.com is in this file: the credential names, the environment slug,
// and the mapping between name.com's wire structs and the vocabulary registrar.go
// declares. The orchestration in register.go names none of it.
//
// A SECOND registrar is a second file exactly like this one.
func init() { register("namecom", func() Registrar { return newNamecom() }) }

// namecomRegistrar adapts *namecom.Client to Registrar.
type namecomRegistrar struct {
	c   *namecom.Client
	env string
}

func newNamecom() *namecomRegistrar {
	// The environment is EXPLICIT and fail-safe: only "prod" hits the live, billable
	// registrar; anything else (including unset) is the sandbox.
	env := environ.Or("NAMECOM_ENV", "test")
	return &namecomRegistrar{
		c: namecom.New(
			strings.TrimSpace(os.Getenv("NAMECOM_USER")),
			strings.TrimSpace(os.Getenv("NAMECOM_TOKEN")),
			env, nil,
		),
		env: env,
	}
}

func (n *namecomRegistrar) ID() string       { return "namecom" }
func (n *namecomRegistrar) Env() string      { return n.env }
func (n *namecomRegistrar) Needs() []string  { return []string{"NAMECOM_USER", "NAMECOM_TOKEN"} }
func (n *namecomRegistrar) Configured() bool { return n.c.Configured() }

func (n *namecomRegistrar) Reach(ctx context.Context) error {
	_, err := n.c.Hello(ctx)
	return refusal(err)
}

func (n *namecomRegistrar) Available(ctx context.Context, names ...string) ([]SearchResult, error) {
	resp, err := n.c.CheckAvailability(ctx, names...)
	if err != nil {
		return nil, refusal(err)
	}
	return quotes(resp), nil
}

func (n *namecomRegistrar) Search(ctx context.Context, keyword string, tld ...string) ([]SearchResult, error) {
	resp, err := n.c.Search(ctx, keyword, tld...)
	if err != nil {
		return nil, refusal(err)
	}
	return quotes(resp), nil
}

func (n *namecomRegistrar) Create(ctx context.Context, req CreateRequest) (*Registration, error) {
	out, err := n.c.CreateDomain(ctx, namecom.CreateDomainRequest{
		Domain: namecom.DomainInput{
			DomainName:  req.Domain,
			Nameservers: req.Nameservers,
			Contacts:    contactsTo(req.Contacts),
		},
		PurchasePrice: req.Price,
		Years:         req.Years,
	})
	if err != nil {
		return nil, refusal(err)
	}
	return registration(req.Domain, out.Domain, out.Order), nil
}

func (n *namecomRegistrar) Renew(ctx context.Context, req RenewRequest) (*Registration, error) {
	out, err := n.c.RenewDomain(ctx, req.Domain, namecom.RenewDomainRequest{
		PurchasePrice: req.Price,
		Years:         req.Years,
	})
	if err != nil {
		return nil, refusal(err)
	}
	return registration(req.Domain, out.Domain, out.Order), nil
}

func (n *namecomRegistrar) Transfer(ctx context.Context, req TransferRequest) (*Registration, error) {
	out, err := n.c.CreateTransfer(ctx, namecom.TransferRequest{
		DomainName:    req.Domain,
		AuthCode:      req.Auth,
		PurchasePrice: req.Price,
		Years:         req.Years,
	})
	if err != nil {
		return nil, refusal(err)
	}
	return &Registration{Domain: req.Domain, Order: out.Order}, nil
}

// ── wire → vocabulary ────────────────────────────────────────────────────────────

// quotes maps a name.com search/availability response to the quotes the orchestration
// prices. Prices are USD, exactly as name.com states them.
func quotes(resp *namecom.SearchResponse) []SearchResult {
	out := make([]SearchResult, 0, len(resp.Results))
	for _, r := range resp.Results {
		out = append(out, SearchResult{
			Domain:    r.DomainName,
			TLD:       r.TLD,
			Available: r.Purchasable,
			Premium:   r.Premium,
			Price:     r.PurchasePrice,
			Renewal:   r.RenewalPrice,
		})
	}
	return out
}

// registration maps a name.com domain record to the order result. name.com omits the
// domain block on some answers; the name the order was placed for is the fallback, so
// a Registration always says which name it is about.
func registration(name string, d *namecom.Domain, order int64) *Registration {
	reg := &Registration{Domain: name, Order: order}
	if d != nil {
		if d.DomainName != "" {
			reg.Domain = d.DomainName
		}
		reg.Nameservers = d.Nameservers
		reg.ExpiresAt = d.ExpireDate
	}
	return reg
}

// contactsTo maps the WHOIS contact set onto name.com's wire shape. nil stays nil,
// which is how the reseller account's default contacts are asked for.
func contactsTo(c *Contacts) *namecom.Contacts {
	if c == nil {
		return nil
	}
	return &namecom.Contacts{
		Registrant: registrantTo(c.Registrant),
		Admin:      registrantTo(c.Admin),
		Tech:       registrantTo(c.Tech),
		Billing:    registrantTo(c.Billing),
	}
}

func registrantTo(r *Registrant) *namecom.Registrant {
	if r == nil {
		return nil
	}
	return &namecom.Registrant{
		FirstName: r.FirstName, LastName: r.LastName, Company: r.Company,
		Address1: r.Address1, Address2: r.Address2, City: r.City, State: r.State,
		Zip: r.Zip, Country: r.Country, Phone: r.Phone, Fax: r.Fax, Email: r.Email,
	}
}

// refusal turns a name.com non-2xx into the vendor-free Refusal the HTTP adapter maps
// to a status. Anything else (a dial failure, a decode) travels as itself.
func refusal(err error) error {
	if err == nil {
		return nil
	}
	if apiErr, ok := errors.AsType[*namecom.APIError](err); ok {
		return &Refusal{Status: apiErr.Status, Message: apiErr.Message}
	}
	return err
}
