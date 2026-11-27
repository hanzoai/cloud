package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// pickStore holds hanzo/a and hanzo/z (active), with the given expiries; a past
// one with no refresh token is a session that has ended.
func pickStore(t *testing.T, aExp, zExp int64) {
	t.Helper()
	s := &IdentityStore{}
	s.Put(&Credentials{AccessToken: makeJWT(map[string]any{"jti": "a"}), Subject: "a@hanzo.ai", Owner: "hanzo", Expiry: aExp})
	s.Put(&Credentials{AccessToken: makeJWT(map[string]any{"jti": "z"}), Subject: "z@hanzo.ai", Owner: "hanzo", Expiry: zExp})
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
}

// onTerminal makes login see a terminal for the length of the test.
func onTerminal(t *testing.T) {
	t.Helper()
	was := isTerminal
	isTerminal = func(io.Reader, io.Writer) bool { return true }
	t.Cleanup(func() { isTerminal = was })
}

// deviceIAM approves every device code as the account `as` names.
func deviceIAM(t *testing.T, as *Credentials) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/iam/oauth/device":
			_ = json.NewEncoder(w).Encode(map[string]any{"device_code": "dc-1", "user_code": "WDJBMJHT",
				"verification_uri": "https://hanzo.ai/login/oauth/device", "expires_in": 60, "interval": 1})
		case "/v1/iam/oauth/token":
			tok := makeJWT(map[string]any{"email": as.Subject, "owner": as.Owner, "exp": float64(time.Now().Add(time.Hour).Unix())})
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": tok, "refresh_token": "rt", "token_type": "Bearer", "expires_in": 3600})
		default:
			t.Errorf("unexpected IAM call %s", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestPickIdentity(t *testing.T) {
	sandbox(t)
	live := time.Now().Add(time.Hour).Unix()
	pickStore(t, live, live)
	store := storedIdentities(t)
	for in, want := range map[string]string{"\n": "hanzo/z", "1\n": "hanzo/a", "2\n": "hanzo/z", "3\n": ""} {
		var out strings.Builder
		got, err := pickIdentity(strings.NewReader(in), &out, store)
		if err != nil || got != want {
			t.Fatalf("pick %q = %q, %v; want %q", in, got, err, want)
		}
		for _, line := range []string{"Sign in as:", "1) hanzo/a  a@hanzo.ai", "2) hanzo/z  z@hanzo.ai (active)", "3) Another account", "Choose [2]"} {
			if !strings.Contains(out.String(), line) {
				t.Fatalf("menu lacks %q:\n%s", line, out.String())
			}
		}
	}
	if _, err := pickIdentity(strings.NewReader(""), io.Discard, store); err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("end of input should cancel, got %v", err)
	}
	if _, err := pickIdentity(strings.NewReader("9\n"), io.Discard, store); err == nil {
		t.Fatal("an out-of-range choice was accepted")
	}
}

func TestLoginPickingALiveIdentitySwitchesToIt(t *testing.T) {
	sandbox(t)
	onTerminal(t)
	live := time.Now().Add(time.Hour).Unix()
	pickStore(t, live, live)
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { t.Errorf("switching asked IAM %s", r.URL.Path) }))
	defer srv.Close()
	out, err := runRoot(t, "1\n", "--iam-issuer", srv.URL, "login")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Switched to a@hanzo.ai @ hanzo [hanzo/a]") {
		t.Fatalf("out = %q", out)
	}
	if a := storedIdentities(t).Active; a != "hanzo/a" {
		t.Fatalf("active = %q, want hanzo/a", a)
	}
}

func TestLoginPickingAnEndedSessionSignsItInAgain(t *testing.T) {
	sandbox(t)
	onTerminal(t)
	pickStore(t, time.Now().Add(-time.Hour).Unix(), time.Now().Add(time.Hour).Unix())
	iam := deviceIAM(t, &Credentials{Subject: "a@hanzo.ai", Owner: "hanzo"})
	out, err := runRoot(t, "1\n", "--iam-issuer", iam, "login")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"The hanzo/a session has ended", "Approve as hanzo/a. If the page names another account, sign out there first.", "Logged in as a@hanzo.ai @ hanzo [hanzo/a]"} {
		if !strings.Contains(out, want) {
			t.Fatalf("out lacks %q:\n%s", want, out)
		}
	}
	if a := storedIdentities(t).Active; a != "hanzo/a" {
		t.Fatalf("active = %q, want hanzo/a", a)
	}
}

func TestLoginApprovedAsAnotherAccountIsReportedNotActivated(t *testing.T) {
	sandbox(t)
	onTerminal(t)
	pickStore(t, time.Now().Add(-time.Hour).Unix(), time.Now().Add(time.Hour).Unix())
	iam := deviceIAM(t, &Credentials{Subject: "b@hanzo.ai", Owner: "hanzo"})
	out, err := runRoot(t, "1\n", "--iam-issuer", iam, "login")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "You asked for hanzo/a, and the page approved hanzo/b: it is stored beside, and hanzo/z is still active.") {
		t.Fatalf("out = %q", out)
	}
	s := storedIdentities(t)
	if s.Active != "hanzo/z" || s.Identities["hanzo/b"] == nil {
		t.Fatalf("active = %q, held %v; want hanzo/z active and hanzo/b beside", s.Active, s.keys())
	}
}

func TestLoginOffATerminalAsksNothing(t *testing.T) {
	sandbox(t)
	live := time.Now().Add(time.Hour).Unix()
	pickStore(t, live, live)
	iam := deviceIAM(t, &Credentials{Subject: "a@hanzo.ai", Owner: "hanzo"})
	out, err := runRoot(t, "1\n", "--iam-issuer", iam, "login")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "Sign in as") || strings.Contains(out, "Approve as") {
		t.Fatalf("asked off a terminal:\n%s", out)
	}
	if a := storedIdentities(t).Active; a != "hanzo/a" {
		t.Fatalf("active = %q, want hanzo/a (the account signed in)", a)
	}
}
