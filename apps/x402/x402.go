// SPDX-License-Identifier: Apache-2.0 OR MIT

// Package x402 charges per request over HTTP 402 for the routes an operator prices.
//
// It is the x402 version 2 mechanism of github.com/hanzoai/commerce/payment/x402
// mounted on the router: a priced request without a payment is answered 402 with
// the terms, one with a payment is verified before the route runs, and the payment
// settles once the route has answered with a success. Nothing is priced until the
// operator says so: there is no default token, payee, network or price, and a
// deployment with no configuration mounts nothing and serves every route as it did.
//
// The operator gives three things (Config): the assets a payer may pay in, the
// price of each route, and the facilitator that verifies and settles. Load reads
// them from a JSON file.
//
//	{
//	  "facilitator": "https://facilitator.example",
//	  "assets": [{"network":"eip155:8453","address":"0x…","name":"…","version":"2",
//	              "decimals":6,"payTo":"0x…","maxTimeoutSeconds":60}],
//	  "prices": {"GET /v1/data": {"amount":"0.01"}, "POST /v1/run/*": {"amount":"0.25"}}
//	}
//
// A price key is a method and a path. A path ending in /* prices everything under
// that prefix, and the longest match wins. Paths match the way the router does, not
// literally: case is folded, a trailing slash and "." or ".." segments are ignored,
// so /V1/Data/ cannot reach the handler of /v1/data without paying for it. A HEAD
// request is priced as GET.
//
// Prices are charged for a route's success: a handler that returns an error or
// answers 4xx or 5xx settles nothing. Routes that fail by rendering an error and
// returning nil are still seen by their status, but a priced route should be a typed
// op, whose failures are returned errors.
//
// Surface (typed op, /v1 only):
//
//	GET /v1/x402/prices   every priced route and the exact terms for paying it
package x402

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	protocol "github.com/hanzoai/commerce/payment/x402"
)

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// Config is what an operator gives the rail. Nothing in it has a default.
type Config struct {
	// Assets are the ways a payer may pay: a token on a network, the payee, the
	// token's EIP-712 domain and decimals. Every price is quoted in each of them.
	Assets []protocol.Asset
	// Prices maps a method and path ("GET /v1/data", or "POST /v1/run/*" for a
	// prefix) to its price in whole units of every asset.
	Prices map[string]protocol.Price
	// Facilitator verifies and settles payments. A host that settles on its own
	// books passes its own; Load builds one that speaks to a facilitator by URL.
	Facilitator protocol.Facilitator
	// Facilitators configures two or more independent facilitators with automatic
	// failover or round-robin routing. When non-empty and Facilitator is nil, Mount
	// composes them into a MultiFacilitator with zero mandatory Hanzo or Circle dependency.
	Facilitators []NamedFacilitator
	// RoutingStrategy selects how MultiFacilitator chooses among Facilitators
	// ("failover" or "round_robin"). Defaults to "failover".
	RoutingStrategy RoutingStrategy
	// QuoteSigner, when non-nil, signs exact price quotes bound to the request body
	// hash and rejects tampered or expired quotes prior to inference.
	QuoteSigner QuoteSigner
	// NonceStore, when non-nil, enforces persistent/concurrent ERC-3009 replay
	// protection and Idempotency-Key deduplication.
	NonceStore NonceReplayStore
	// Ledger, when non-nil, records hash-chained provider-signed receipts,
	// failed-inference voids, and facilitator reversal reconciliations.
	Ledger ReceiptLedger
	// ReversalVerifier, when non-nil, authenticates or independently verifies
	// reversal events submitted to POST /v1/x402/reconcile before mutating Ledger.
	ReversalVerifier func(ctx context.Context, ev ReversalEvent) error
	// FacilitatorSigners maps facilitator IDs (case-insensitive) to their trusted
	// secp256k1 signer addresses so POST /v1/x402/reconcile can cryptographically
	// authenticate facilitator-signed ReversalEvents.
	FacilitatorSigners map[string]string
	// Now overrides wall clock time for deterministic testing.
	Now func() time.Time
}

// facilitatorEntry is one independent facilitator entry in the operator JSON file.
type facilitatorEntry struct {
	ID       string `json:"id"`
	Operator string `json:"operator,omitempty"`
	URL      string `json:"url"`
}

// file is the JSON an operator writes.
type file struct {
	Facilitator     string                    `json:"facilitator,omitempty"`
	Facilitators    []facilitatorEntry        `json:"facilitators,omitempty"`
	RoutingStrategy RoutingStrategy           `json:"routingStrategy,omitempty"`
	Assets          []protocol.Asset          `json:"assets"`
	Prices          map[string]protocol.Price `json:"prices"`
}

// Load reads an operator's JSON configuration. A field it does not know is an error:
// a misspelled key must not leave a route unpriced. Facilitators are reached at
// the URLs named, without credentials; a facilitator that needs them is wired by
// the host with its own Config.Facilitator or Config.Facilitators.
func Load(r io.Reader) (Config, error) {
	var f file
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return Config{}, fmt.Errorf("x402: config: %w", err)
	}
	if dec.More() {
		return Config{}, errors.New("x402: config: trailing data after the document")
	}
	cfg := Config{
		Assets:          f.Assets,
		Prices:          f.Prices,
		RoutingStrategy: f.RoutingStrategy,
	}
	if len(f.Facilitators) > 0 {
		named := make([]NamedFacilitator, 0, len(f.Facilitators))
		for _, fe := range f.Facilitators {
			if !strings.HasPrefix(fe.URL, "https://") && !strings.HasPrefix(fe.URL, "http://") {
				return Config{}, fmt.Errorf("x402: config: facilitator %q url must be an http(s) URL", fe.ID)
			}
			id := strings.TrimSpace(fe.ID)
			if id == "" {
				return Config{}, errors.New("x402: config: every facilitator entry requires an id")
			}
			op := strings.TrimSpace(fe.Operator)
			if op == "" {
				op = "independent"
			}
			named = append(named, NamedFacilitator{
				ID:          id,
				Operator:    op,
				Endpoint:    fe.URL,
				Facilitator: protocol.Remote{URL: fe.URL},
			})
		}
		mf, err := NewMultiFacilitator(f.RoutingStrategy, named...)
		if err != nil {
			return Config{}, fmt.Errorf("x402: config: %w", err)
		}
		cfg.Facilitators = named
		cfg.Facilitator = mf
		return cfg, nil
	}
	if !strings.HasPrefix(f.Facilitator, "https://") && !strings.HasPrefix(f.Facilitator, "http://") {
		return Config{}, errors.New("x402: config: facilitator must be an http(s) URL")
	}
	cfg.Facilitator = protocol.Remote{URL: f.Facilitator}
	return cfg, nil
}

// Mount charges for the configured routes. It must be mounted before the routes it
// prices: the payment check wraps the handlers registered after it. An invalid
// configuration is an error, so a deployment never starts with a price it did not
// understand.
func Mount(app *zip.App, log luxlog.Logger, cfg Config) error {
	if app == nil {
		return errors.New("x402: nil app")
	}
	var multi *MultiFacilitator
	fac := cfg.Facilitator
	if fac == nil && len(cfg.Facilitators) > 0 {
		var err error
		multi, err = NewMultiFacilitator(cfg.RoutingStrategy, cfg.Facilitators...)
		if err != nil {
			return err
		}
		fac = multi
	} else if mf, ok := fac.(*MultiFacilitator); ok {
		multi = mf
	}
	if fac == nil {
		return errors.New("x402: no facilitator")
	}
	if len(cfg.Prices) == 0 {
		return errors.New("x402: no prices: nothing to charge for")
	}
	table, err := protocol.NewTable(cfg.Assets...)
	if err != nil {
		return err
	}
	for key, price := range cfg.Prices {
		method, p, ok := strings.Cut(key, " ")
		if !ok || !strings.HasPrefix(p, "/") || method != strings.ToUpper(method) || method == "" {
			return fmt.Errorf("x402: price key %q is not a method and a path, like \"GET /v1/data\"", key)
		}
		if err := table.Set(method+" "+canonical(p), price); err != nil {
			return err
		}
	}

	enhanced := cfg.QuoteSigner != nil || cfg.NonceStore != nil || cfg.Ledger != nil || multi != nil
	if enhanced {
		nonceStore := cfg.NonceStore
		if nonceStore == nil {
			nonceStore = NewMemoryNonceStore()
		}
		guard := &SettlementGuard{
			Table:       table,
			Facilitator: fac,
			QuoteSigner: cfg.QuoteSigner,
			NonceStore:  nonceStore,
			Ledger:      cfg.Ledger,
			Now:         cfg.Now,
		}
		app.Use(guard.ZipPaywall(pricedKey(table)))
	} else {
		gate := &protocol.Gate{Table: table, Facilitator: fac, Now: cfg.Now}
		app.Use(protocol.Paywall(gate, pricedKey(table)))
	}

	var facSigners map[string]string
	if len(cfg.FacilitatorSigners) > 0 {
		facSigners = make(map[string]string, len(cfg.FacilitatorSigners))
		for k, v := range cfg.FacilitatorSigners {
			facSigners[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
		}
	}

	o := ops{
		table:              table,
		quoteSigner:        cfg.QuoteSigner,
		ledger:             cfg.Ledger,
		multi:              multi,
		reversalVerifier:   cfg.ReversalVerifier,
		facilitatorSigners: facSigners,
	}
	g := app.Group("/v1/x402")
	g.Get("/prices", o.prices)
	if enhanced {
		g.Post("/quote", o.quote)
		g.Get("/receipts", o.receipts)
		g.Get("/receipts/export", o.exportReceipts)
		g.Post("/reconcile", o.reconcile)
		g.Get("/facilitators", o.facilitators)
	}

	log.Info("x402 mounted", "priced", len(cfg.Prices), "assets", len(cfg.Assets))
	return nil
}

// canonical is a path as the router reads it: lower case, cleaned of repeated
// slashes, "." and ".." segments and a trailing slash.
func canonical(p string) string { return strings.ToLower(path.Clean("/" + p)) }

// pricedKey names the table entry that prices a request: the exact method and
// canonical path, else the longest "/*" prefix that contains it, else the empty key,
// which is not priced.
func pricedKey(t *protocol.Table) func(method, p string) string {
	return func(method, p string) string {
		p = canonical(p)
		if exact := method + " " + p; t.Priced(exact) {
			return exact
		}
		for p != "/" {
			p = path.Dir(p)
			prefix := p
			if prefix != "/" {
				prefix += "/"
			}
			if key := method + " " + prefix + "*"; t.Priced(key) {
				return key
			}
		}
		return ""
	}
}

// ops binds the typed ops. A typed handler takes no service parameter, so the table
// arrives as a receiver.
type ops struct {
	table              *protocol.Table
	quoteSigner        QuoteSigner
	ledger             ReceiptLedger
	multi              *MultiFacilitator
	reversalVerifier   func(ctx context.Context, ev ReversalEvent) error
	facilitatorSigners map[string]string
}

// noIn is the input of an op that takes nothing.
type noIn struct{}

// priceTable is every priced route.
type priceTable struct {
	// X402Version is the protocol version the terms are stated in.
	X402Version int `json:"x402Version"`
	// Prices is one row per priced route, ordered by key; never null.
	Prices []protocol.Row `json:"prices"`
}

// prices lists every priced route with the exact terms for paying it, so a client
// can sign before it is refused: the price in whole units, and for each asset the
// network, token, payee, amount in atomic units and the token's signing domain. A
// payment is made on the route itself, in the PAYMENT-SIGNATURE header, and the
// receipt comes back in PAYMENT-RESPONSE; nothing is paid here. It needs no
// credential, so a client can price a route before it has an account.
func (o ops) prices(_ context.Context, _ *noIn) (*priceTable, error) {
	return &priceTable{X402Version: protocol.Version, Prices: o.table.Listing()}, nil
}

type quoteIn struct {
	Resource        string `json:"resource"`
	Model           string `json:"model,omitempty"`
	MaxTokens       int    `json:"maxTokens,omitempty"`
	RequestBodyHash string `json:"requestBodyHash"`
	Network         string `json:"network,omitempty"`
}

type quoteOut struct {
	InterfaceVersion string           `json:"interfaceVersion"`
	Quote            SignedPriceQuote `json:"quote"`
}

func (o ops) quote(ctx context.Context, in *quoteIn) (*quoteOut, error) {
	if o.quoteSigner == nil {
		return nil, zip.Errorf(503, "x402: quote signer not configured")
	}
	if in == nil || strings.TrimSpace(in.Resource) == "" || strings.TrimSpace(in.RequestBodyHash) == "" {
		return nil, zip.ErrBadRequest("x402: resource and requestBodyHash are required")
	}
	method, p, ok := strings.Cut(strings.TrimSpace(in.Resource), " ")
	if !ok {
		return nil, zip.ErrBadRequest("x402: resource must be \"METHOD /path\"")
	}
	key := pricedKey(o.table)(strings.ToUpper(method), p)
	if key == "" {
		return nil, zip.ErrNotFound("x402: resource is not priced")
	}
	accepts, _, ok := o.table.Quote(key)
	if !ok || len(accepts) == 0 {
		return nil, zip.ErrNotFound("x402: no payment requirements for resource")
	}
	chosen := accepts[0]
	if in.Network != "" {
		matched := false
		for _, a := range accepts {
			if strings.EqualFold(a.Network, in.Network) {
				chosen = a
				matched = true
				break
			}
		}
		if !matched {
			return nil, zip.ErrBadRequest("x402: requested network is not offered for this resource")
		}
	}
	q, err := o.quoteSigner.SignQuote(ctx, QuoteRequest{
		Resource:        key,
		Model:           in.Model,
		MaxTokens:       in.MaxTokens,
		RequestBodyHash: in.RequestBodyHash,
		Network:         chosen.Network,
		Asset:           chosen.Asset,
		AmountAtomic:    chosen.Amount,
		PayTo:           chosen.PayTo,
		TTLSeconds:      chosen.MaxTimeoutSeconds,
	})
	if err != nil {
		return nil, zip.ErrInternal(err.Error())
	}
	return &quoteOut{InterfaceVersion: InterfaceVersion, Quote: q}, nil
}

type receiptsIn struct {
	FacilitatorID string        `query:"facilitatorId"`
	Status        ReceiptStatus `query:"status"`
	Payer         string        `query:"payer"`
}

type receiptsOut struct {
	InterfaceVersion string    `json:"interfaceVersion"`
	Count            int       `json:"count"`
	Receipts         []Receipt `json:"receipts"`
}

func (o ops) receipts(ctx context.Context, in *receiptsIn) (*receiptsOut, error) {
	if o.ledger == nil {
		return nil, zip.Errorf(503, "x402: receipt ledger not configured")
	}
	var filter ReceiptFilter
	if in != nil {
		filter = ReceiptFilter{
			FacilitatorID: in.FacilitatorID,
			Status:        in.Status,
			Payer:         in.Payer,
		}
	}
	list := o.ledger.List(ctx, filter)
	if !zip.CallerOf(ctx).Admin {
		for i := range list {
			list[i].IdempotencyKey = ""
		}
	}
	return &receiptsOut{
		InterfaceVersion: InterfaceVersion,
		Count:            len(list),
		Receipts:         list,
	}, nil
}

type exportOut struct {
	InterfaceVersion string `json:"interfaceVersion"`
	Format           string `json:"format"`
	JSONL            string `json:"jsonl"`
}

func (o ops) exportReceipts(_ context.Context, _ *noIn) (*exportOut, error) {
	if o.ledger == nil {
		return nil, zip.Errorf(503, "x402: receipt ledger not configured")
	}
	var buf bytes.Buffer
	if err := o.ledger.ExportJSONL(&buf); err != nil {
		return nil, zip.ErrInternal(err.Error())
	}
	return &exportOut{
		InterfaceVersion: InterfaceVersion,
		Format:           "jsonl",
		JSONL:            buf.String(),
	}, nil
}

type reconcileOut struct {
	InterfaceVersion string  `json:"interfaceVersion"`
	Receipt          Receipt `json:"receipt"`
}

func (o ops) reconcile(ctx context.Context, in *ReversalEvent) (*reconcileOut, error) {
	if o.ledger == nil {
		return nil, zip.Errorf(503, "x402: receipt ledger not configured")
	}
	if in == nil {
		return nil, zip.ErrBadRequest("x402: reversal event is required")
	}
	if o.reversalVerifier != nil {
		if err := o.reversalVerifier(ctx, *in); err != nil {
			return nil, zip.ErrForbidden(err.Error())
		}
	} else if signerAddr, ok := o.facilitatorSigners[strings.ToLower(strings.TrimSpace(in.FacilitatorID))]; ok && signerAddr != "" {
		if err := VerifyReversalEventSignature(*in, signerAddr); err != nil {
			return nil, zip.ErrForbidden(err.Error())
		}
	} else if !zip.CallerOf(ctx).Admin {
		return nil, zip.ErrForbidden("x402: reversal reconciliation requires operator authorization or trusted facilitator signature")
	}
	rec, err := o.ledger.ReconcileReversal(ctx, *in)
	if err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	return &reconcileOut{
		InterfaceVersion: InterfaceVersion,
		Receipt:          rec,
	}, nil
}

type facilitatorsOut struct {
	InterfaceVersion string             `json:"interfaceVersion"`
	Failovers        int64              `json:"failovers"`
	Migrations       int64              `json:"migrations"`
	Facilitators     []FacilitatorStats `json:"facilitators"`
}

func (o ops) facilitators(_ context.Context, _ *noIn) (*facilitatorsOut, error) {
	if o.multi == nil {
		return &facilitatorsOut{
			InterfaceVersion: InterfaceVersion,
			Facilitators:     []FacilitatorStats{},
		}, nil
	}
	return &facilitatorsOut{
		InterfaceVersion: InterfaceVersion,
		Failovers:        o.multi.TotalFailovers(),
		Migrations:       o.multi.Migrations(),
		Facilitators:     o.multi.Stats(),
	}, nil
}
