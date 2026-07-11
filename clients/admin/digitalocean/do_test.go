package digitalocean

import "testing"

func TestDollarsToCents(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"23.44", 2344},
		{"-40000.00", -4_000_000}, // promo credit held (negative account_balance)
		{"12.23", 1223},
		{"0", 0},
		{"", 0},
		{"  5.5 ", 550},
		{"garbage", 0},
	}
	for _, c := range cases {
		if got := int64(dollarsToCents(c.in)); got != c.want {
			t.Errorf("dollarsToCents(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}
