package billing

import (
	"errors"
	"testing"

	"github.com/hanzoai/cloud/client"
)

// The payer's name has one source, the display name on the IAM user record: the
// identity headers carry the username and the email and nothing else. A user whose
// record states none, or whom IAM cannot find, is made a Stripe customer without a
// name, and the reason is said.
func TestThePayersNameIsTheUserRecordsDisplayNameOrNothingWithAReason(t *testing.T) {
	for _, c := range []struct {
		name     string
		email    *client.Email
		err      error
		wantName string
		wantWhy  bool
	}{
		{"a display name", &client.Email{Address: "z@hanzo.ai", Name: "  Zach Kelling "}, nil, "Zach Kelling", false},
		{"a record with no display name", &client.Email{Address: "z@hanzo.ai"}, nil, "", true},
		{"a blank display name", &client.Email{Name: "   "}, nil, "", true},
		{"no user row for the subject", nil, errors.New("email: no such subject"), "", true},
		{"IAM answered nothing", nil, nil, "", true},
	} {
		name, why := nameOf(c.email, c.err)
		if name != c.wantName || (why != "") != c.wantWhy {
			t.Errorf("%s: name=%q why=%q; want name=%q, a reason=%v", c.name, name, why, c.wantName, c.wantWhy)
		}
	}
}
