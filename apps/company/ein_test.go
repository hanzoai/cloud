package company

import (
	"strings"
	"testing"
)

// THE FACT THAT DECIDES EVERYTHING. A responsible party with a US taxpayer id is
// issued an EIN online in one sitting; without one the application is signed and
// posted and the wait is weeks. Getting this backwards tells a founder abroad to
// "apply online", which is advice they cannot follow.
func TestEIN_USTaxpayerFilesOnlineAndOwesNoForms(t *testing.T) {
	e, err := StartEIN(Responsible{Name: "Ada", Email: "ada@acme.com", Country: "US", USTaxID: true}, "541511", false)
	if err != nil {
		t.Fatal(err)
	}
	if !e.Online {
		t.Error("a US-taxpayer responsible party files online")
	}
	if len(e.Forms) != 0 {
		t.Errorf("invented %d form(s) for an online application", len(e.Forms))
	}
	if e.Status != EINApplied {
		t.Errorf("status %q — an online application with nothing to sign goes straight in", e.Status)
	}
}

func TestEIN_NonUSOwesASignedSS4(t *testing.T) {
	e, err := StartEIN(Responsible{Name: "Kenji", Email: "k@acme.com", Country: "JP"}, "541511", false)
	if err != nil {
		t.Fatal(err)
	}
	if e.Online {
		t.Error("a responsible party with no US taxpayer id cannot file online")
	}
	if len(e.Forms) != 1 || e.Forms[0].Code != "SS-4" {
		t.Fatalf("forms = %+v, want exactly SS-4", e.Forms)
	}
	if e.Forms[0].Why == "" {
		t.Error("a form must say what it is for; nobody should have to already know what an SS-4 is")
	}
	if e.Status != EINFormsRequired {
		t.Errorf("status %q, want forms_required", e.Status)
	}
}

// 8821 is the expedite and it only exists on the posted path.
func TestEIN_ExpediteAddsThe8821(t *testing.T) {
	e, err := StartEIN(Responsible{Name: "Kenji", Email: "k@acme.com", Country: "JP"}, "541511", true)
	if err != nil {
		t.Fatal(err)
	}
	var has8821 bool
	for _, f := range e.Forms {
		if f.Code == "8821" {
			has8821 = true
		}
	}
	if !has8821 {
		t.Fatalf("an expedited non-US application owes an 8821, got %+v", e.Forms)
	}
}

// SELLING AN EXPEDITE FOR AN ONLINE APPLICATION IS SELLING NOTHING. There is
// nothing to prioritise when the number is issued in the same sitting.
func TestEIN_RefusesToSellAnExpediteThatCannotHelp(t *testing.T) {
	_, err := StartEIN(Responsible{Name: "Ada", Email: "ada@acme.com", Country: "US", USTaxID: true}, "541511", true)
	if err == nil {
		t.Fatal("took an expedite fee for an application issued online in one sitting")
	}
	if !strings.Contains(err.Error(), "expedite") {
		t.Fatalf("the refusal must say why, got: %v", err)
	}
}

// The IRS will not process an SS-4 without the business activity, so an
// application missing it is refused here rather than weeks later by them.
func TestEIN_NAICSIsRequiredAndSixDigits(t *testing.T) {
	for _, bad := range []string{"", "5415", "54151x", "5415111"} {
		if _, err := StartEIN(Responsible{Name: "A", Email: "a@b.c", Country: "US", USTaxID: true}, bad, false); err == nil {
			t.Errorf("accepted NAICS %q", bad)
		}
	}
	if _, err := StartEIN(Responsible{Name: "A", Email: "a@b.c", Country: "US", USTaxID: true}, "541511", false); err != nil {
		t.Errorf("rejected a valid six-digit NAICS: %v", err)
	}
}

func TestEIN_ResponsiblePartyIsRequired(t *testing.T) {
	cases := []Responsible{
		{Email: "a@b.c", Country: "US"},
		{Name: "A", Country: "US"},
		{Name: "A", Email: "a@b.c"},
		{Name: "A", Email: "a@b.c", Country: "USA"},
	}
	for _, r := range cases {
		if _, err := StartEIN(r, "541511", false); err == nil {
			t.Errorf("accepted an incomplete responsible party: %+v", r)
		}
	}
}

// Ready is the gate on filing: every owed signature held.
func TestEIN_ReadyOnlyWhenEveryFormIsSigned(t *testing.T) {
	e, err := StartEIN(Responsible{Name: "Kenji", Email: "k@acme.com", Country: "JP"}, "541511", true)
	if err != nil {
		t.Fatal(err)
	}
	if e.Ready() {
		t.Fatal("ready with nothing signed")
	}
	e.Forms[0].Signed = true
	if e.Ready() {
		t.Fatal("ready with the 8821 unsigned")
	}
	e.Forms[1].Signed = true
	if !e.Ready() {
		t.Fatal("every form signed and still not ready")
	}
}
