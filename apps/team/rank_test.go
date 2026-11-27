package team

import "testing"

// TestRankAfterIsLexoRanksGenNext pins the ranks the Team client itself writes
// when it appends: middle() for a first child, then ceil + 8 in base 36, with
// the bucket and the trailing colon kept, so the client can parse every rank
// this writes and place the next sibling after it.
func TestRankAfterIsLexoRanksGenNext(t *testing.T) {
	for _, tc := range []struct{ last, want string }{
		{"", "0|hzzzzz:"},
		{"garbage", "0|hzzzzz:"},
		{"0|hzzzzz:", "0|i00007:"},
		{"0|i00007:", "0|i0000f:"},
		{"0|i00007:i", "0|i0000g:"},
		{"1|000000:", "1|000008:"},
		{"0|zzzzzz:", "0|zzzzzz:"},
	} {
		if got := rankAfter(tc.last); got != tc.want {
			t.Errorf("rankAfter(%q) = %q, want %q", tc.last, got, tc.want)
		}
		if tc.last != "" && tc.last != "garbage" && tc.last != "0|zzzzzz:" && rankAfter(tc.last) <= tc.last {
			t.Errorf("rankAfter(%q) does not sort after it", tc.last)
		}
	}
}
