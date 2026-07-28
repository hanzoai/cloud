package namecom

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// mockServer stands in for name.com v4: it asserts Basic auth + records the path/body
// of each request, and replies with the canned JSON the handler registers.
func mockServer(t *testing.T, wantUser, wantToken string, routes map[string]func(*http.Request) (int, any)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != wantUser || p != wantToken {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(APIError{Message: "Permission Denied"})
			return
		}
		key := r.Method + " " + r.URL.Path
		h, ok := routes[key]
		if !ok {
			t.Errorf("unexpected request %s", key)
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(APIError{Message: "route not mocked: " + key})
			return
		}
		code, body := h(r)
		w.WriteHeader(code)
		if body != nil {
			_ = json.NewEncoder(w).Encode(body)
		}
	}))
}

func TestBaseFor(t *testing.T) {
	if got := BaseFor("prod"); got != BaseProd {
		t.Fatalf("prod → %s, want %s", got, BaseProd)
	}
	// Anything non-prod must resolve to the sandbox — an unset env can never hit live.
	for _, env := range []string{"", "test", "dev", "testnet", "typo", "TEST"} {
		if got := BaseFor(env); got != BaseTest {
			t.Fatalf("env %q → %s, want sandbox %s", env, got, BaseTest)
		}
	}
}

func TestCheckAvailability(t *testing.T) {
	srv := mockServer(t, "u", "tok", map[string]func(*http.Request) (int, any){
		"POST /v4/domains:checkAvailability": func(r *http.Request) (int, any) {
			var req AvailabilityRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			if len(req.DomainNames) != 1 || req.DomainNames[0] != "acme.ai" {
				t.Errorf("body domainNames = %v", req.DomainNames)
			}
			return 200, SearchResponse{Results: []SearchResult{{
				DomainName: "acme.ai", Purchasable: true, PurchasePrice: 55.99, RenewalPrice: 55.99, TLD: "ai",
			}}}
		},
	})
	defer srv.Close()

	c := NewWithBase("u", "tok", srv.URL, nil)
	res, err := c.CheckAvailability(context.Background(), "acme.ai")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Results) != 1 || !res.Results[0].Purchasable || res.Results[0].PurchasePrice != 55.99 {
		t.Fatalf("unexpected result: %+v", res.Results)
	}
}

func TestAuthFailureSurfacesAPIError(t *testing.T) {
	srv := mockServer(t, "right", "right", nil)
	defer srv.Close()
	c := NewWithBase("wrong", "creds", srv.URL, nil)
	_, err := c.Hello(context.Background())
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("want *APIError, got %T: %v", err, err)
	}
	if apiErr.Status != http.StatusUnauthorized || apiErr.Message != "Permission Denied" {
		t.Fatalf("unexpected APIError: %+v", apiErr)
	}
}

func TestCreateDomainSendsPriceCapAndYears(t *testing.T) {
	srv := mockServer(t, "u", "tok", map[string]func(*http.Request) (int, any){
		"POST /v4/domains": func(r *http.Request) (int, any) {
			var req CreateDomainRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req.Domain.DomainName != "acme.ai" {
				t.Errorf("domainName = %q", req.Domain.DomainName)
			}
			if req.PurchasePrice != 55.99 {
				t.Errorf("purchasePrice cap = %v, want 55.99", req.PurchasePrice)
			}
			if req.Years != 1 {
				t.Errorf("years = %d, want default 1", req.Years)
			}
			if len(req.Domain.Nameservers) != 2 {
				t.Errorf("nameservers = %v", req.Domain.Nameservers)
			}
			return 200, CreateDomainResponse{
				Domain:    &Domain{DomainName: "acme.ai", Nameservers: req.Domain.Nameservers},
				Order:     42,
				TotalPaid: 55.99,
			}
		},
	})
	defer srv.Close()

	c := NewWithBase("u", "tok", srv.URL, nil)
	res, err := c.CreateDomain(context.Background(), CreateDomainRequest{
		Domain:        DomainInput{DomainName: "acme.ai", Nameservers: []string{"ns1.hanzo.ai", "ns2.hanzo.ai"}},
		PurchasePrice: 55.99,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.TotalPaid != 55.99 || res.Order != 42 {
		t.Fatalf("unexpected create response: %+v", res)
	}
}

func TestSetNameserversPathAndBody(t *testing.T) {
	srv := mockServer(t, "u", "tok", map[string]func(*http.Request) (int, any){
		"POST /v4/domains/acme.ai:setNameservers": func(r *http.Request) (int, any) {
			var req SetNameserversRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			if len(req.Nameservers) != 2 || req.Nameservers[0] != "ns1.hanzo.ai" {
				t.Errorf("nameservers = %v", req.Nameservers)
			}
			return 200, Domain{DomainName: "acme.ai", Nameservers: req.Nameservers}
		},
	})
	defer srv.Close()

	c := NewWithBase("u", "tok", srv.URL, nil)
	d, err := c.SetNameservers(context.Background(), "acme.ai", []string{"ns1.hanzo.ai", "ns2.hanzo.ai"})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Nameservers) != 2 {
		t.Fatalf("unexpected: %+v", d)
	}
}

func TestRenewDefaultsYears(t *testing.T) {
	srv := mockServer(t, "u", "tok", map[string]func(*http.Request) (int, any){
		"POST /v4/domains/acme.ai:renew": func(r *http.Request) (int, any) {
			var req RenewDomainRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req.Years != 1 {
				t.Errorf("years = %d, want 1", req.Years)
			}
			return 200, RenewDomainResponse{Domain: &Domain{DomainName: "acme.ai"}, TotalPaid: 55.99}
		},
	})
	defer srv.Close()
	c := NewWithBase("u", "tok", srv.URL, nil)
	if _, err := c.RenewDomain(context.Background(), "acme.ai", RenewDomainRequest{}); err != nil {
		t.Fatal(err)
	}
}
