package author

import (
	"context"
	"errors"
	"testing"

	"github.com/hanzoai/cloud"
)

// The defect, on the money path: AccrueForOrg returned a bare int, so "no author
// was owed anything" and "authors is not in this process" were both 0. authors
// ships as its own binary and the accrual walk runs in affiliates, so the second
// case is the one that happens — the royalty leg of every sweep latched nothing
// and reported a completed accrual of zero, with no error channel to say so.
//
// Absence is ErrNoPeer now. A royalty that silently does not accrue is the one
// outcome this seam must not be able to express.
func TestAccrueAbsentIsAnErrorNotAZero(t *testing.T) {
	prev := mounted
	mounted = nil
	t.Cleanup(func() { mounted = prev })

	n, err := AccrueForOrg(context.Background(), "acme", 5000, "2026-08", 1)
	if err == nil {
		t.Fatal("unmounted AccrueForOrg returned no error — royalties accruing " +
			"zero forever is not a successful sweep")
	}
	if !errors.Is(err, cloud.ErrNoPeer) {
		t.Errorf("AccrueForOrg err = %v, want ErrNoPeer", err)
	}
	if n != 0 {
		t.Errorf("AccrueForOrg n = %d, want 0 alongside the error", n)
	}
}
