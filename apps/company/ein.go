package company

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// THE EIN IS WHAT MAKES A FORMED ENTITY USABLE. A certificate from a state says
// the company exists; a bank will not open an account and no card can be issued
// against it until the IRS has issued an Employer Identification Number. So the
// formation is not finished when the state files it, and treating those as one
// milestone is what leaves a customer holding a company they cannot bank.
//
// WHICH FORMS ARE OWED TURNS ON ONE FACT: whether the responsible party holds a
// US taxpayer id. With one, the IRS issues online in a sitting. Without one —
// the ordinary case for a founder outside the US — the application is a signed
// SS-4 that travels by fax or post, and the wait is measured in weeks. Asking a
// non-US founder to "just apply online" is advice that cannot be followed, and
// discovering that after formation is the expensive moment.
//
// Form 8821 is the expedite, and it is ONLY meaningful on that second path: it
// authorises us to speak to the IRS about the application, which is what lets
// anyone chase it. Selling an expedite to a founder who can file online is
// selling nothing.

// EINStatus is how far the number has got.
type EINStatus string

const (
	// EINAbsent is a formation that has not started an application.
	EINAbsent EINStatus = "absent"
	// EINFormsRequired means the application cannot be filed until the forms
	// below are signed.
	EINFormsRequired EINStatus = "forms_required"
	// EINApplied means it is with the IRS and the wait is theirs.
	EINApplied EINStatus = "applied"
	// EINIssued means the number exists.
	EINIssued EINStatus = "issued"
)

// Form is one IRS form this application owes, and why.
type Form struct {
	// Code is the IRS designation, e.g. "SS-4".
	Code string `json:"code"`
	// Name is the form's own title, so a reader need not already know the code.
	Name string `json:"name"`
	// Why states what this form is for in this application — the same form is
	// owed for different reasons on different paths.
	Why string `json:"why"`
	// Signed reports whether we hold the signature.
	Signed bool `json:"signed"`
}

// Responsible is the person the IRS holds answerable for the entity. It is an
// IRS concept and not a founder one, which is why it is stated here rather than
// widened onto Founder: the responsible party may be one founder, and the
// question the IRS asks about them is one no equity split answers.
type Responsible struct {
	// Name is their full legal name as the IRS will hold it.
	Name string `json:"name"`
	// Email reaches them for signature.
	Email string `json:"email"`
	// Country is where they reside, ISO 3166-1 alpha-2.
	Country string `json:"country"`
	// USTaxID reports that they hold an SSN or ITIN. It is a BOOLEAN on purpose:
	// the number itself is never needed here and a field that could hold it is a
	// field that will eventually be logged.
	USTaxID bool `json:"usTaxId"`
}

// EIN is the application and its state.
type EIN struct {
	// Status is how far it has got.
	Status EINStatus `json:"status"`
	// Number is the issued EIN, absent until the IRS issues it.
	Number string `json:"number,omitempty"`
	// Expedited reports that prioritised handling was asked for.
	Expedited bool `json:"expedited,omitempty"`
	// Responsible is the person the IRS holds answerable.
	Responsible Responsible `json:"responsible"`
	// NAICS is the six-digit code for what the business does. The SS-4 asks it
	// and the IRS will not process an application without one.
	NAICS string `json:"naics"`
	// Forms are the forms this application owes, with what each is for.
	Forms []Form `json:"forms,omitempty"`
	// Online reports that this application can be filed with the IRS online and
	// issued in a sitting, rather than signed and posted. It is the single fact
	// that decides how long a customer waits, so it is answered rather than
	// implied by the absence of forms.
	Online bool `json:"online"`
}

var naicsRe = regexp.MustCompile(`^[0-9]{6}$`)

// FormsFor states which forms an application owes, and why.
//
// A US-taxpayer responsible party owes NONE: that application is filed online
// and issued immediately, so returning a form for it would invent work.
func FormsFor(r Responsible, expedited bool) []Form {
	if r.USTaxID {
		return nil
	}
	forms := []Form{{
		Code: "SS-4", Name: "Application for Employer Identification Number",
		Why: "the responsible party holds no SSN or ITIN, so the application cannot be filed online and travels signed",
	}}
	if expedited {
		forms = append(forms, Form{
			Code: "8821", Name: "Tax Information Authorization",
			Why: "authorises us to speak to the IRS about this application, which is what makes chasing it possible",
		})
	}
	return forms
}

// StartEIN builds the application, or refuses and says what is missing.
//
// It REFUSES an expedite for a responsible party who can file online, rather
// than taking the fee: there is nothing to expedite when the number is issued in
// the same sitting, and charging for it would be charging for nothing.
func StartEIN(r Responsible, naics string, expedited bool) (*EIN, error) {
	switch {
	case strings.TrimSpace(r.Name) == "":
		return nil, fmt.Errorf("company: the EIN application names a responsible party")
	case strings.TrimSpace(r.Email) == "":
		return nil, fmt.Errorf("company: the responsible party needs an email to sign at")
	case len(strings.TrimSpace(r.Country)) != 2:
		return nil, fmt.Errorf("company: the responsible party's country is an ISO 3166-1 alpha-2 code, got %q", r.Country)
	case !naicsRe.MatchString(strings.TrimSpace(naics)):
		return nil, fmt.Errorf("company: NAICS is the six-digit code for what the business does, got %q — the IRS will not process an SS-4 without one", naics)
	}
	if expedited && r.USTaxID {
		return nil, fmt.Errorf("company: nothing to expedite — a responsible party with a US taxpayer id is issued an EIN online in one sitting")
	}

	e := &EIN{
		Responsible: r,
		NAICS:       strings.TrimSpace(naics),
		Expedited:   expedited,
		Online:      r.USTaxID,
		Forms:       FormsFor(r, expedited),
	}
	if len(e.Forms) == 0 {
		e.Status = EINApplied // online: nothing to sign, it goes straight in
	} else {
		e.Status = EINFormsRequired
	}
	return e, nil
}

// Ready reports that every owed form is signed, so the application can be filed.
func (e *EIN) Ready() bool {
	for _, f := range e.Forms {
		if !f.Signed {
			return false
		}
	}
	return true
}

// einIn is what a caller states to open an EIN application.
type einIn struct {
	// Responsible is the person the IRS holds answerable for the entity.
	Responsible Responsible `json:"responsible"`
	// NAICS is the six-digit code for what the business does.
	NAICS string `json:"naics"`
	// Expedited asks for prioritised handling. Only meaningful when the
	// responsible party cannot file online.
	Expedited bool `json:"expedited,omitempty"`
}

// ein opens the EIN application and answers what it owes.
//
// The answer states whether it can be filed ONLINE, because that is the fact
// deciding whether the customer waits a sitting or several weeks — and it names
// each form with what that form is for, so nobody has to already know what an
// SS-4 is to understand why they are signing one.
func (o ops) ein(_ context.Context, in *einIn) (*EIN, error) {
	if in == nil {
		return nil, fmt.Errorf("company: an EIN application states a responsible party and a NAICS code")
	}
	return StartEIN(in.Responsible, in.NAICS, in.Expedited)
}
