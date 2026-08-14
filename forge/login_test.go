package forge

// login_test.go pins the forge's own username rule.
//
// The rule is not ours: this deployment registers through OIDC with
// oauth2_client.USERNAME=email, so the forge's login is the address run through
// its NormalizeUserName. These cases are taken from that function
// (models/user/user.go) so a drift shows up here rather than as "unknown actor"
// against a live forge.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLogin_IsTheEmailLocalPartFoldedTheForgesWay(t *testing.T) {
	for _, tc := range []struct{ email, want string }{
		// The case that shipped broken: a real user, and the login the forge holds.
		{"a@hanzo.ai", "a"},
		{"z@hanzo.ai", "z"},
		{"first.last@hanzo.ai", "first.last"},
		// Already a bare login (no @) — a no-op, which is what makes the fallback
		// path safe to pass through here too.
		{"a", "a"},
		// Only the FIRST @ splits.
		{"a@b@hanzo.ai", "a"},
		// Diacritics fold to ASCII; Æ expands rather than vanishing.
		{"café@hanzo.ai", "cafe"},
		{"Æon@hanzo.ai", "AEon"},
		// Quotes and accents are deleted, joining what is either side.
		{"o'brien@hanzo.ai", "obrien"},
		{"a`b@hanzo.ai", "ab"},
		// Whitespace and ~ + become a hyphen.
		{"a b@hanzo.ai", "a-b"},
		{"a+tag@hanzo.ai", "a-tag"},
		{"a~b@hanzo.ai", "a-b"},
		// Nothing to derive is EMPTY, never a guess: the caller refuses on it.
		{"", ""},
		{"   ", ""},
		{"@hanzo.ai", ""},
	} {
		if got := Login(tc.email); got != tc.want {
			t.Errorf("Login(%q) = %q, want %q", tc.email, got, tc.want)
		}
	}
}

// Case is LEFT ALONE. The forge stores the name as given and enforces
// uniqueness on a lowercased copy, and Sudo resolves case-insensitively
// (models/user/user.go GetUserByName on lower_name) — so folding case here
// would be a transformation the forge does not make, for no gain.
func TestLogin_DoesNotFoldCase(t *testing.T) {
	if got := Login("Zoe@hanzo.ai"); got != "Zoe" {
		t.Fatalf("Login = %q, want the address's own case", got)
	}
}

// ── ownership, which is what makes a derived login an identity ───────────────

// userStub is a forge that answers /users/{login} from a table of login→email.
func userStub(t *testing.T, users map[string]string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		login := strings.TrimPrefix(r.URL.Path, "/v1/users/")
		email, ok := users[login]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"login": login, "email": email})
	}))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, "machine-token-value")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// THE COLLISION, WHICH IS THE WHOLE POINT.
//
// Two DIFFERENT addresses derive ONE login, because the derivation drops the
// domain. On this deployment every self-serve signup lands in the same org as
// the staff, so the stranger reaching this code is a stranger — and without the
// ownership check they would act as the colleague whose local part they chose.
func TestLoginFor_RefusesALoginDerivedFromSomebodyElsesAddress(t *testing.T) {
	c := userStub(t, map[string]string{"z": "z@hanzo.ai"})

	// Both derive `z`. Only one of them IS z.
	if got := Login("z@attacker.example"); got != Login("z@hanzo.ai") {
		t.Fatalf("the fixture does not collide: %q vs %q", got, Login("z@hanzo.ai"))
	}

	_, err := c.LoginFor(context.Background(), "z@attacker.example")
	if !errors.Is(err, ErrNotYours) {
		t.Fatalf("a stranger was given a colleague's forge identity: err=%v", err)
	}

	// And the person who owns it still resolves — a control that refuses
	// everybody is an outage, not a control.
	got, err := c.LoginFor(context.Background(), "z@hanzo.ai")
	if err != nil || got != "z" {
		t.Fatalf("the owner was refused their own login: %q, %v", got, err)
	}
}

// Case and surrounding space do not decide an ownership question.
func TestLoginFor_ComparesTheAddressCaseInsensitively(t *testing.T) {
	c := userStub(t, map[string]string{"z": "Z@Hanzo.AI"})
	got, err := c.LoginFor(context.Background(), "  z@hanzo.ai  ")
	if err != nil || got != "z" {
		t.Fatalf("a case difference refused the owner: %q, %v", got, err)
	}
}

// A login nobody holds is an unknown actor, not a mismatch — the two are
// different facts and only one of them is about a person.
func TestLoginFor_AnAbsentUserIsAnUnknownActor(t *testing.T) {
	c := userStub(t, map[string]string{})
	if _, err := c.LoginFor(context.Background(), "nobody@hanzo.ai"); !errors.Is(err, ErrUnknownActor) {
		t.Fatalf("want ErrUnknownActor, got %v", err)
	}
}

// A forge that hides the address answers a PLACEHOLDER (services/convert
// toUser), which can never equal a real caller's address — so a deployment whose
// machine credential lost its administrator rights refuses everyone rather than
// admitting anyone.
func TestLoginFor_APlaceholderAddressRefuses(t *testing.T) {
	c := userStub(t, map[string]string{"z": "z@noreply.git.hanzo.ai"})
	if _, err := c.LoginFor(context.Background(), "z@hanzo.ai"); !errors.Is(err, ErrNotYours) {
		t.Fatalf("a hidden address was accepted as a match: %v", err)
	}
}

// Nothing to derive is a refusal, never a lookup of the empty login.
func TestLoginFor_NoAddressIsARefusal(t *testing.T) {
	c := userStub(t, map[string]string{"": "anything"})
	if _, err := c.LoginFor(context.Background(), ""); err == nil {
		t.Fatal("an empty address resolved to a forge identity")
	}
}
