package crm

// The model is data, so the tests are about the data: the engine accepts every
// fixture, every Link resolves inside the module, and the module registers.

import (
	"testing"

	"github.com/hanzoai/doctype"
)

// Validate is what the engine runs at install, so a fixture that fails here fails
// for a customer the moment they turn the product on.
func TestEveryDocTypeIsValid(t *testing.T) {
	for _, dt := range DocTypes() {
		if err := dt.Validate(); err != nil {
			t.Errorf("%s: %v", dt.Name, err)
		}
		if dt.Module != Module {
			t.Errorf("%s carries module %q, want %q — the refusal resolves a document's product through this field", dt.Name, dt.Module, Module)
		}
	}
}

// A Link to a name nothing declares is accepted at define time and fails at the
// first write, so the model must be closed over itself.
func TestEveryLinkResolves(t *testing.T) {
	declared := map[string]bool{}
	for _, dt := range DocTypes() {
		declared[dt.Name] = true
	}
	for _, dt := range DocTypes() {
		for _, f := range dt.Fields {
			if f.Fieldtype != doctype.FieldLink {
				continue
			}
			if !declared[f.Options] {
				t.Errorf("%s.%s links to %q, which this module does not declare", dt.Name, f.Fieldname, f.Options)
			}
		}
	}
}

// Registration at init is the whole activation: unregistered fixtures make
// POST /modules/crm/install answer for a module it cannot create.
func TestModuleIsRegistered(t *testing.T) {
	fixtures := doctype.Fixtures(Module)
	if len(fixtures) != len(DocTypes()) {
		t.Fatalf("registry holds %d fixtures for %q, DocTypes() returns %d",
			len(fixtures), Module, len(DocTypes()))
	}
	got := map[string]bool{}
	for _, dt := range fixtures {
		got[dt.Name] = true
	}
	for _, want := range []string{dtCompany, dtContact, dtOpportunity, dtApplication} {
		if !got[want] {
			t.Errorf("%q is not in the registered module", want)
		}
	}
}

// The engine enforces a Select's closed set at write, so a Select with no options
// accepts anything.
func TestEverySelectIsClosed(t *testing.T) {
	for _, dt := range DocTypes() {
		for _, f := range dt.Fields {
			if f.Fieldtype == doctype.FieldSelect && f.Options == "" {
				t.Errorf("%s.%s is a Select with no options — it accepts any string", dt.Name, f.Fieldname)
			}
		}
	}
}
