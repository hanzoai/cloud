// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"testing"

	commercebilling "github.com/hanzoai/commerce/api/billing"
	"github.com/hanzoai/commerce/models/organization"

	"github.com/hanzoai/cloud/client"
)

// The reference a payer is shown is commerce's to render, and the admin console reads
// a bank statement's reference back to the account with client.WireReference. The two
// must be one function, or a wire that arrived carrying exactly what the customer was
// told would resolve to nobody, or to somebody else. This holds them equal over the
// shapes a billing key takes: an org, a member of the signup org, and the characters a
// slug or a username may carry.
func TestWireReferenceIsCommerces(t *testing.T) {
	recv := &organization.Organization{}
	// A placeholder account number: the rendering refuses an org with no account at
	// all, and this test reads only the reference.
	recv.Wire.AccountNumber = "0000"
	for _, payer := range []string{
		"acme", "hanzo/z", "hanzo/grundy.solomun_7", "acme-co", "Acme/Bob", "o'neil/x+y@z", "lux/ä",
	} {
		w, err := commercebilling.GetWireInstructions(recv, payer)
		if err != nil {
			t.Fatalf("%s: commerce rendered no instructions: %v", payer, err)
		}
		if got := client.WireReference(payer); got != w.Reference || got != w.Memo {
			t.Errorf("%s: client.WireReference = %q, commerce prints reference %q memo %q", payer, got, w.Reference, w.Memo)
		}
	}
}
