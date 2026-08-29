package domain

import (
	"reflect"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
)

// A second registrar is a file: a type, its methods, and this one line. Nothing in
// registrar.go, domain.go, register.go or mount.go changes to admit it — which is
// what the tests below assert.
func init() { register("mock", func() Registrar { return &mockReg{configured: true} }) }

// A deployment that names no registrar resells name.com, which is what every
// existing deployment already does.
func TestNamecomIsTheDefaultRegistrar(t *testing.T) {
	reg, err := registrarFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if reg.ID() != "namecom" {
		t.Fatalf("default registrar = %q, want namecom", reg.ID())
	}
	if got := reg.Needs(); len(got) != 2 || got[0] != "NAMECOM_USER" {
		t.Fatalf("Needs = %v, want the name.com credentials it reads", got)
	}
}

// DOMAIN_REGISTRAR selects one, and the subsystem builds on it end to end: the
// health probe reads the registrar's own id, environment and credential names, so
// nothing on the wire says "name.com" for a deployment that resells someone else.
func TestASecondRegistrarIsOneFile(t *testing.T) {
	t.Setenv("DOMAIN_REGISTRAR", "mock")
	st, err := buildState(cloud.NewBase(cloud.Deps{}, "domain"))
	if err != nil {
		t.Fatalf("buildState: %v", err)
	}
	if st.reg.ID() != "mock" {
		t.Fatalf("registrar = %q, want mock", st.reg.ID())
	}

	got, err := ops{s: &cloud.Service[state]{State: st}}.health(t.Context(), &noIn{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Registrar != "mock" || got.Env != "test" {
		t.Fatalf("health = registrar %q env %q, want the selected registrar's own", got.Registrar, got.Env)
	}
	if got.Status != "ok" || !got.Reachable {
		t.Fatalf("health = %+v, want ok/reachable from a configured registrar", got)
	}
}

// An unconfigured registrar names ITS OWN credentials, not name.com's.
func TestHealthNamesTheRegistrarsOwnCredentials(t *testing.T) {
	st := state{svc: NewService(&mockReg{}, &mockBill{balance: -1}, &mockZones{}, NewMemStore(), Config{}), reg: &mockReg{}}
	got, err := ops{s: &cloud.Service[state]{State: st}}.health(t.Context(), &noIn{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Error, "MOCK_USER/MOCK_TOKEN") {
		t.Fatalf("error = %q, want the registrar's own credential names", got.Error)
	}
}

// A name no file registered is refused at MOUNT, so a deployment configured for a
// registrar that does not exist fails to start rather than 503ing every purchase.
func TestAnUnknownRegistrarIsRefusedAtUse(t *testing.T) {
	t.Setenv("DOMAIN_REGISTRAR", "carrier-pigeon")
	_, err := buildState(cloud.NewBase(cloud.Deps{}, "domain"))
	if err == nil {
		t.Fatal("buildState accepted an unregistered registrar name")
	}
	if !strings.Contains(err.Error(), "carrier-pigeon") || !strings.Contains(err.Error(), "namecom") {
		t.Fatalf("error = %q, want it to name the miss and what IS registered", err)
	}
}

// The contract names NO VENDOR, structurally: not one method of Registrar takes or
// returns a type from a registrar's own package. That is the property the interface
// lacked while it spoke namecom.SearchResponse and namecom.CreateDomainRequest — it
// was name.com wearing an interface, and no second registrar could satisfy it
// without speaking name.com's JSON. Asserted over the type rather than by reading
// it, so a vendor type reintroduced anywhere in the contract goes red.
func TestTheRegistrarContractNamesNoVendor(t *testing.T) {
	rt := reflect.TypeOf((*Registrar)(nil)).Elem()
	self := reflect.TypeOf(SearchResult{}).PkgPath() // this package
	var vendor func(reflect.Type) string
	vendor = func(t reflect.Type) string {
		switch t.Kind() {
		case reflect.Ptr, reflect.Slice, reflect.Array, reflect.Chan:
			return vendor(t.Elem())
		case reflect.Map:
			if v := vendor(t.Key()); v != "" {
				return v
			}
			return vendor(t.Elem())
		}
		if p := t.PkgPath(); p != "" && p != self && !strings.HasPrefix(p, "context") {
			return p
		}
		return ""
	}
	for i := range rt.NumMethod() {
		m := rt.Method(i)
		for j := range m.Type.NumIn() {
			if p := vendor(m.Type.In(j)); p != "" {
				t.Fatalf("Registrar.%s takes %s — the vendor shape is back in the contract", m.Name, p)
			}
		}
		for j := range m.Type.NumOut() {
			if p := vendor(m.Type.Out(j)); p != "" {
				t.Fatalf("Registrar.%s returns %s — the vendor shape is back in the contract", m.Name, p)
			}
		}
	}
}
