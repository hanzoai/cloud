package share

// person_test.go pins that one person holds one tunnel account in a held org,
// whichever credential they call with: their sign-in and their API key are
// handed one account, an account an API key of theirs was handed before keeps
// working from both and is rotated with theirs, and an admin's own accounts are
// rotated whatever becomes of the org's.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/iam"
	"github.com/hanzoai/cloud/sdk/kit"
	"github.com/zap-proto/zip"
)

// people serves IAM's person read: each subject in rows is answered its row, and
// any other is no such subject.
func people(t *testing.T, rows map[string]iam.Self) {
	t.Helper()
	t.Setenv("ZIP_RUNTIME_DIR", kit.Sockets(t))
	a := zip.New(zip.Config{AppName: "iam", DisableStartupMessage: true})
	a.Post("/iam/self", func(ctx context.Context, _ *struct{}) (*iam.Self, error) {
		p, ok := rows[zip.CallerOf(ctx).User]
		if !ok {
			return nil, zip.ErrUnauthorized("no such subject")
		}
		return &p, nil
	}, zip.WithOperationID(iam.IAMSelf))
	at := zip.SocketPath("iam")
	go func() { _ = a.Listen(at) }()
	t.Cleanup(func() { _ = a.Shutdown() })
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if c, err := net.Dial("unix", at); err == nil {
			_ = c.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never accepted", at)
		}
	}
}

// accounts is a controller holding some accounts, each with its token and the
// environments it enabled; it provisions on a create and rotates what it holds.
type accounts struct {
	held     map[holding]string
	envs     map[string]int // per token
	failOrg  bool           // a held org's own account cannot be rotated
	created  []holding
	rotated  []holding
	reminted int
}

func (a *accounts) configured() bool { return true }

func (a *accounts) token(_ context.Context, h holding, create bool) (string, error) {
	if tok, ok := a.held[h]; ok {
		return tok, nil
	}
	if !create {
		return "", errNoAccount
	}
	a.created = append(a.created, h)
	a.held[h] = "tok-new"
	return "tok-new", nil
}

func (a *accounts) rotate(_ context.Context, h holding) (int, error) {
	a.rotated = append(a.rotated, h)
	if a.failOrg && h.person == "" {
		return 0, errors.New("controller unreachable")
	}
	tok, ok := a.held[h]
	if !ok {
		return 0, errNoAccount
	}
	a.reminted++
	a.held[h] = tok + "-next"
	return a.envs[tok], nil
}

func (a *accounts) overview(_ context.Context, tok string) (overviewResp, error) {
	envs := []map[string]any{}
	for i := range a.envs[tok] {
		envs = append(envs, map[string]any{
			"environment": map[string]string{"zId": fmt.Sprint(tok, "-env-", i)},
			"shares":      []map[string]string{{"token": fmt.Sprint(tok, "-share-", i)}},
		})
	}
	raw, _ := json.Marshal(map[string]any{"environments": envs})
	var ov overviewResp
	return ov, json.Unmarshal(raw, &ov)
}

// request drives one request as user, an admin of org when admin.
func request(t *testing.T, app *zip.App, method, path, org, user string, admin bool) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("X-Org-Id", org)
	req.Header.Set("X-User-Id", user)
	if admin {
		req.Header.Set("X-User-IsOrgAdmin", "true")
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, body
}

// ONE PERSON, ONE TUNNEL ACCOUNT. Ivy signs in as her IAM row's id and calls with
// an API key whose subject is her row; both resolve to her, so both are handed
// the one account keyed on her id. The account her API key was handed before —
// keyed on her row — follows it, and nobody else's derivation reaches either.
func TestOnePersonHoldsOneTunnelAccount(t *testing.T) {
	ivy := iam.Self{ID: "uuid-ivy", Row: "hanzo/ivy"}
	people(t, map[string]iam.Self{"uuid-ivy": ivy, "hanzo/ivy": ivy, "uuid-jo": {ID: "uuid-jo", Row: "hanzo/jo"}})
	signed := zip.WithCaller(context.Background(), zip.Caller{Org: "hanzo", User: "uuid-ivy"})
	keyed := zip.WithCaller(context.Background(), zip.Caller{Org: "hanzo", User: "hanzo/ivy"})
	a, err := holders(signed, "hanzo")
	if err != nil {
		t.Fatal(err)
	}
	b, err := holders(keyed, "hanzo")
	if err != nil {
		t.Fatal(err)
	}
	want := []holding{held("hanzo", "uuid-ivy"), held("hanzo", "hanzo/ivy")}
	if !slices.Equal(a, want) || !slices.Equal(b, want) {
		t.Fatalf("signed in holds %v and by API key %v, want both %v", a, b, want)
	}
	jo, err := holders(zip.WithCaller(context.Background(), zip.Caller{Org: "hanzo", User: "uuid-jo"}), "hanzo")
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range jo {
		if slices.Contains(want, h) {
			t.Fatalf("a stranger holds %v, one of ivy's", h)
		}
	}
	// An API key IAM cannot say the person of is refused, never keyed on its
	// name, which is the split this closes.
	if _, err := holders(zip.WithCaller(context.Background(), zip.Caller{Org: "hanzo", User: "hanzo/ghost"}), "hanzo"); err == nil {
		t.Fatal("an API key whose person IAM did not say was handed an account")
	}
}

// AN ACCOUNT AN API KEY WAS HANDED BEFORE IS CARRIED ACROSS. Kai's only account
// was provisioned when her API key asked, keyed on her row. Enabled signed in or
// by key, she is handed that account and no second one is made; the console
// lists its tunnels; and a rotation from the console ends it, so nothing she
// was handed stays live.
func TestAnAccountAnAPIKeyWasHandedIsCarriedAcross(t *testing.T) {
	kai := iam.Self{ID: "uuid-kai", Row: "hanzo/kai"}
	people(t, map[string]iam.Self{"uuid-kai": kai, "hanzo/kai": kai})
	old := held("hanzo", "hanzo/kai")
	f := &accounts{held: map[holding]string{old: "tok-key"}, envs: map[string]int{"tok-key": 2}}
	app := mountShare(t, f)

	for _, user := range []string{"uuid-kai", "hanzo/kai"} {
		code, body := request(t, app, http.MethodPost, "/v1/share/enable", "hanzo", user, false)
		var out enableResp
		if err := json.Unmarshal(body, &out); code != http.StatusOK || err != nil || out.AccountToken != "tok-key" {
			t.Fatalf("enable as %s = %d %s, want the account her key was handed", user, code, body)
		}
		code, body = request(t, app, http.MethodGet, "/v1/share", "hanzo", user, false)
		if code != http.StatusOK || strings.Count(string(body), `"token":"tok-key-share-`) != 2 {
			t.Fatalf("list as %s = %d %s, want the key account's two tunnels", user, code, body)
		}
	}
	if len(f.created) != 0 {
		t.Fatalf("a person with an account was provisioned another: %v", f.created)
	}

	code, body := request(t, app, http.MethodPost, "/v1/share/rotate", "hanzo", "uuid-kai", false)
	var out rotated
	if err := json.Unmarshal(body, &out); code != http.StatusOK || err != nil || out.Ended != 2 || out.Pending != "" {
		t.Fatalf("rotate signed in = %d %s, want the key account's two environments ended", code, body)
	}
	if want := []holding{held("hanzo", "uuid-kai"), old}; !slices.Equal(f.rotated, want) || f.reminted != 1 {
		t.Fatalf("rotated %v (%d reminted), want %v with the key account reminted", f.rotated, f.reminted, want)
	}
	if f.held[old] == "tok-key" {
		t.Fatal("the token her key was handed is still the account's")
	}
}

// A PERSON WITH NO ACCOUNT IS PROVISIONED ONE, under their id, from either
// credential, and the second credential is handed the same.
func TestAPersonsFirstAccountIsKeyedOnTheirID(t *testing.T) {
	lu := iam.Self{ID: "uuid-lu", Row: "hanzo/lu"}
	people(t, map[string]iam.Self{"uuid-lu": lu, "hanzo/lu": lu})
	f := &accounts{held: map[holding]string{}, envs: map[string]int{}}
	app := mountShare(t, f)
	for _, user := range []string{"hanzo/lu", "uuid-lu"} {
		if code, body := request(t, app, http.MethodPost, "/v1/share/enable", "hanzo", user, false); code != http.StatusOK || !strings.Contains(string(body), "tok-new") {
			t.Fatalf("enable as %s = %d %s", user, code, body)
		}
	}
	if want := []holding{held("hanzo", "uuid-lu")}; !slices.Equal(f.created, want) {
		t.Fatalf("provisioned %v, want one account under her id", f.created)
	}
}

// AN ADMIN ALWAYS ROTATES THEIR OWN. In a held org an admin's own accounts are
// rotated first and the org's own after; when the org's cannot be rotated the
// answer says so, and their own are rotated still.
func TestAnAdminsOwnAccountIsRotatedWhateverTheOrgs(t *testing.T) {
	mo := iam.Self{ID: "uuid-mo", Row: "hanzo/mo"}
	people(t, map[string]iam.Self{"uuid-mo": mo})
	own := held("hanzo", "uuid-mo")
	f := &accounts{held: map[holding]string{own: "tok-mo"}, envs: map[string]int{"tok-mo": 1}, failOrg: true}
	app := mountShare(t, f)
	code, body := request(t, app, http.MethodPost, "/v1/share/rotate", "hanzo", "uuid-mo", true)
	var out rotated
	if err := json.Unmarshal(body, &out); code != http.StatusOK || err != nil || out.Ended != 1 || !strings.Contains(out.Pending, "not rotated") {
		t.Fatalf("rotate = %d %s, want her own rotated and the org's said pending", code, body)
	}
	if want := []holding{own, held("hanzo", "hanzo/mo"), {org: "hanzo"}}; !slices.Equal(f.rotated, want) {
		t.Fatalf("rotated %v, want %v: her own first", f.rotated, want)
	}
}
