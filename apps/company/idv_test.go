package company

import (
	"context"
	"errors"
	"testing"
)

// TestAMisconfiguredProviderRefusesKYCAndServesTheRest is why resolveKYC no longer
// fails the mount.
//
// Two of this app's routes read the provider. The mount check took down all ~24 for
// a secret twenty-two never touch — including /kyc/decision, the human-in-the-loop
// path that is the only route to a pass when no real provider is wired, so the
// provider's own misconfiguration removed its own remedy.
//
// The gate has to stay shut, and it does: brokenKYC mints no reference, so no
// founder gets a DecidedBy, so guardKYCVerified never opens StagePayment.
func TestAMisconfiguredProviderRefusesKYCAndServesTheRest(t *testing.T) {
	boom := errors.New("idv: key unavailable")
	p := brokenKYC{err: boom}

	ref, url, status, err := p.Start(context.Background(), "acme", Founder{Email: "a@b.c"})
	if !errors.Is(err, boom) {
		t.Errorf("Start err = %v, want the configuration error carried out", err)
	}
	if ref != "" || url != "" || status != "" {
		t.Errorf("Start = (%q,%q,%q), want empty — a reference here would set DecidedBy "+
			"and open the payment gate on a provider that verified nobody", ref, url, status)
	}

	if _, err := p.Check(context.Background(), "whatever"); !errors.Is(err, boom) {
		t.Errorf("Check err = %v, want the configuration error", err)
	}

	// NOT "manual". Manual is a legitimate configuration with a real callback path;
	// reporting it here would file a misconfiguration as a working default.
	if got := p.Name(); got == (manualKYC{}).Name() {
		t.Errorf("Name() = %q — a broken provider reported itself as the manual default", got)
	}
}
