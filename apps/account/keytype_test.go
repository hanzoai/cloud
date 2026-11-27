package account

import "testing"

// A KEY'S TYPE IS ITS SCOPE, BECAUSE ACCESSKEY IS NOT THE CREDENTIAL.
//
// This used to type a row from the AccessKey prefix, reasoning that the endpoints
// dispatch on the prefix so the prefix is the fact. The endpoints do — on the
// credential a HOLDER PRESENTS. AccessKey is not that credential: IAM mints both
// classes with a pk- AccessKey and puts the secret key's sk- in AccessSecret,
// which the listing masks (iam keys.MintUserKey). The rows below are the shapes
// IAM actually writes, which is what the previous table got wrong — it modelled a
// secret row as carrying an sk- AccessKey, so it never exercised a real one, and
// "prefix first" was true for every production row alike.
//
// Measured before the fix: a freshly minted SECRET key came back from
// GET /v1/account/keys typed "publishable" with its pk- half printed, and the console —
// which looks for the secret row — offered "create your Cloud API key" to a user
// who already held a working one.
func TestKeyTypeIsTheScope(t *testing.T) {
	for _, tc := range []struct {
		name string
		row  userKey
		want bool
	}{
		{
			// THE BUG, in the shape IAM writes it: this is what every secret key
			// looks like on the wire. Its holder presents the masked sk-.
			name: "a secret row carries a pk- AccessKey and is still secret",
			row:  userKey{AccessKey: "pk-live-7ad0000000", Scope: ""},
			want: false,
		},
		{
			name: "the ordinary publishable row",
			row:  userKey{AccessKey: "pk-live-3480000000", Scope: iamScopePublish},
			want: true,
		},
		{
			// THE DISAGREEMENT THAT STILL MATTERS. Calling this publishable would
			// print a confidential key's full value into a listing, so a scope may
			// never promote an sk- on its own.
			name: "an sk- scoped publish stays secret — a label cannot expose a secret",
			row:  userKey{AccessKey: "sk-live-0000000001", Scope: iamScopePublish},
			want: false,
		},
		{
			name: "an unrecognised key is secret, the safe default",
			row:  userKey{AccessKey: "something-else", Scope: "read"},
			want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := publishable(tc.row); got != tc.want {
				t.Fatalf("publishable(%q, scope=%q) = %v, want %v",
					tc.row.AccessKey, tc.row.Scope, got, tc.want)
			}
		})
	}
}
