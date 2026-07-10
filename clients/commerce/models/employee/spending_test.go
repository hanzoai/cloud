package employee

import (
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
)

func TestRemainingSpendCents(t *testing.T) {
	cases := []struct {
		name      string
		committed currency.Cents
		limit     currency.Cents
		want      currency.Cents
	}{
		{"unlimited returns sentinel", 5000, 0, Unlimited},
		{"unlimited ignores committed", 999999, 0, Unlimited},
		{"under limit leaves remainder", 3000, 10000, 7000},
		{"at limit leaves zero", 10000, 10000, 0},
		{"over limit goes negative", 12000, 10000, -2000},
		{"nothing committed leaves full limit", 0, 10000, 10000},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := RemainingSpendCents(c.committed, c.limit); got != c.want {
				t.Fatalf("RemainingSpendCents(%d, %d) = %d, want %d", c.committed, c.limit, got, c.want)
			}
		})
	}
}

func TestWithinLimit(t *testing.T) {
	cases := []struct {
		name      string
		committed currency.Cents
		requested currency.Cents
		limit     currency.Cents
		want      bool
	}{
		{"unlimited always within", 999999, 999999, 0, true},
		{"under limit accepts", 3000, 2000, 10000, true},
		{"exact-limit boundary accepts", 6000, 4000, 10000, true},
		{"at-limit rejects further spend", 10000, 1, 10000, false},
		{"over limit rejects", 8000, 5000, 10000, false},
		{"zero request at boundary accepts", 10000, 0, 10000, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := WithinLimit(c.committed, c.requested, c.limit); got != c.want {
				t.Fatalf("WithinLimit(%d, %d, %d) = %v, want %v", c.committed, c.requested, c.limit, got, c.want)
			}
		})
	}
}
