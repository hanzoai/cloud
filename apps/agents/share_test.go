package agents

import (
	"encoding/json"
	"github.com/hanzoai/cloud/sdk/oidctest"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/iamtest"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// Sharing through the real identity boundary: a verified token decides who the
// reader is, whether they are a person, and whether they administer their org —
// the three facts the share surface reads from cloud's principal.

type sharePeople struct {
	app                     *zip.App
	owner, reader, stranger string
	admin, member           string
}

func sharingApp(t *testing.T) sharePeople {
	t.Helper()
	iss := iamtest.New(t)
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	cloud.Identify(app, &cloud.Config{IAMIssuer: iamtest.Issuer, Discovery: oidctest.Discovery(iamtest.Issuer), DiscoveryClient: oidctest.Client(iamtest.Issuer, iss.URL)})
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	if err := Use(app, cloud.Deps{AI: &fakeAI{content: "x"}}); err != nil {
		t.Fatalf("Use: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(t.Context()) })
	person := func(sub, name, org, role string) string {
		return iss.Sign(t, iamtest.Claims{Sub: sub, PreferredUsername: name, Owner: org, Orgs: []map[string]any{{"org": org, "role": role}}})
	}
	return sharePeople{
		app:      app,
		owner:    person("7a1e0000-0000-4000-8000-000000000001", "alice", "acme", "member"),
		member:   person("7a1e0000-0000-4000-8000-000000000002", "bob", "acme", "member"),
		admin:    person("7a1e0000-0000-4000-8000-000000000003", "boss", "acme", "admin"),
		reader:   person("7a1e0000-0000-4000-8000-000000000004", "zed", "other", "owner"),
		stranger: person("7a1e0000-0000-4000-8000-000000000005", "quinn", "third", "owner"),
	}
}

func sendAs(t *testing.T, app *zip.App, method, path, bearer string, body any) (int, map[string]any) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = strings.NewReader(string(b))
	}
	req := httptest.NewRequest(method, path, r)
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	raw, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out
}

// share records a conversation as bearer and shares it, answering the token and
// the share's id.
func shareOne(t *testing.T, app *zip.App, bearer string) (token, id string) {
	t.Helper()
	_, rec := sendAs(t, app, http.MethodPost, "/v1/agent/chat/conversations", bearer, map[string]any{
		"messages": []map[string]string{{"role": "user", "content": "our merger terms"}, {"role": "assistant", "content": "SECRET TERMS"}},
	})
	conv, _ := rec["conversationId"].(string)
	status, made := sendAs(t, app, http.MethodPost, "/v1/agent/chat/conversations/"+conv+"/shares", bearer, nil)
	token, _ = made["token"].(string)
	if status != http.StatusCreated || token == "" {
		t.Fatalf("share: %d %v", status, made)
	}
	id, _ = made["share"].(map[string]any)["id"].(string)
	return token, id
}

// A share opens for a person's own access token and for nothing that merely
// carries a person's claims: a token IAM minted for an actor (`act`) and an
// id_token get the title alone, are not recorded, and list nothing — while
// the same person's access token reads it. (Regression: an act token counted
// as a person and read and listed the chat.)
func TestOnlyAPersonsOwnAccessTokenOpensAShare(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		iss := iamtest.New(t)
		app := zip.New(zip.Config{Logger: luxlog.New("test")})
		cloud.Identify(app, &cloud.Config{IAMIssuer: iamtest.Issuer, Discovery: oidctest.Discovery(iamtest.Issuer), DiscoveryClient: oidctest.Client(iamtest.Issuer, iss.URL)})
		t.Setenv("CLOUD_DATA_DIR", t.TempDir())
		if err := Use(app, cloud.Deps{AI: &fakeAI{content: "x"}}); err != nil {
			t.Fatalf("Use: %v", err)
		}
		t.Cleanup(func() { _ = Shutdown(t.Context()) })
		claims := func(sub, name, org string) iamtest.Claims {
			return iamtest.Claims{Sub: sub, PreferredUsername: name, Owner: org, Orgs: []map[string]any{{"org": org, "role": "member"}}}
		}
		owner := iss.Sign(t, claims("7a1e0000-0000-4000-8000-0000000000a1", "yuki", "ygroup"))
		token, id := shareOne(t, app, owner)

		mia := claims("7a1e0000-0000-4000-8000-0000000000b1", "mia", "xcorp")
		acted := mia
		acted.Act = "xcorp/robot-key"
		idToken := mia
		idToken.TokenType = "id-token"
		for name, c := range map[string]iamtest.Claims{"an act token": acted, "an id_token": idToken} {
			bearer := iss.Sign(t, c)
			st, open := sendAs(t, app, http.MethodPost, "/v1/agent/chat/shares/read", bearer, map[string]any{"token": token, "open": true})
			if st != http.StatusOK || open["full"] != false || len(open["messages"].([]any)) != 0 {
				t.Fatalf("%s opened the chat: %d %v", name, st, open)
			}
			if st, _ := sendAs(t, app, http.MethodGet, "/v1/agent/chat/shared", bearer, nil); st != http.StatusForbidden {
				t.Fatalf("%s listed chats shared with it: %d", name, st)
			}
			if st, _ := sendAs(t, app, http.MethodGet, "/v1/agent/chat/shared/"+id, bearer, nil); st != http.StatusForbidden {
				t.Fatalf("%s read the chat by id: %d", name, st)
			}
		}

		access := iss.Sign(t, mia)
		st, open := sendAs(t, app, http.MethodPost, "/v1/agent/chat/shares/read", access, map[string]any{"token": token, "open": true})
		if st != http.StatusOK || open["full"] != true || open["by"] != "yuki" {
			t.Fatalf("the person's own access token: %d %v", st, open)
		}
		if st, list := sendAs(t, app, http.MethodGet, "/v1/agent/chat/shared", access, nil); st != http.StatusOK || len(list["shared"].([]any)) != 1 {
			t.Fatalf("the person's list: %d %v", st, list)
		}

	})
}

// The owner shares; a signed-out opener gets the title alone; a person from
// another org who opens it signed in is recorded under the username IAM signed and
// finds it under the chats shared with them; a stranger with no link finds
// nothing; an org admin lists and revokes it, and it leaves the reader's list.
func TestSharingReadsWhoTheCallerIsFromTheBoundary(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		p := sharingApp(t)
		status, rec := sendAs(t, p.app, http.MethodPost, "/v1/agent/chat/conversations", p.owner, map[string]any{
			"messages": []map[string]string{{"role": "user", "content": "how do tides work"}, {"role": "assistant", "content": "THE MOON PULLS"}},
		})
		conv, _ := rec["conversationId"].(string)
		if status != http.StatusOK || conv == "" {
			t.Fatalf("record: %d %v", status, rec)
		}
		status, made := sendAs(t, p.app, http.MethodPost, "/v1/agent/chat/conversations/"+conv+"/shares", p.owner, nil)
		token, _ := made["token"].(string)
		if status != http.StatusCreated || token == "" {
			t.Fatalf("share: %d %v", status, made)
		}
		shareID, _ := made["share"].(map[string]any)["id"].(string)

		status, anon := sendAs(t, p.app, http.MethodPost, "/v1/agent/chat/shares/read", "", map[string]string{"token": token})
		if status != http.StatusOK || anon["full"] != false || len(anon["messages"].([]any)) != 0 {
			t.Fatalf("signed-out open: %d %v", status, anon)
		}

		status, read := sendAs(t, p.app, http.MethodPost, "/v1/agent/chat/shares/read", p.reader, map[string]any{"token": token, "open": true})
		if status != http.StatusOK || read["full"] != true || read["share"] != shareID {
			t.Fatalf("reader opens: %d %v", status, read)
		}
		if _, list := sendAs(t, p.app, http.MethodGet, "/v1/agent/chat/shared", p.reader, nil); len(list["shared"].([]any)) != 1 {
			t.Fatalf("reader's list: %v", list)
		}
		if _, list := sendAs(t, p.app, http.MethodGet, "/v1/agent/chat/shared", p.stranger, nil); len(list["shared"].([]any)) != 0 {
			t.Fatalf("stranger's list: %v", list)
		}
		if status, _ := sendAs(t, p.app, http.MethodGet, "/v1/agent/chat/shared/"+shareID, p.stranger, nil); status != http.StatusNotFound {
			t.Fatalf("stranger reads by id: %d", status)
		}
		_, links := sendAs(t, p.app, http.MethodGet, "/v1/agent/chat/conversations/"+conv+"/shares", p.owner, nil)
		viewers := links["shares"].([]any)[0].(map[string]any)["viewers"].([]any)
		if len(viewers) != 1 || viewers[0].(map[string]any)["name"] != "zed" {
			t.Fatalf("the owner sees viewers %v", viewers)
		}

		if status, _ := sendAs(t, p.app, http.MethodGet, "/v1/agent/chat/shares", p.member, nil); status != http.StatusForbidden {
			t.Fatalf("a member lists the org's links: %d", status)
		}
		status, orgList := sendAs(t, p.app, http.MethodGet, "/v1/agent/chat/shares", p.admin, nil)
		if status != http.StatusOK || len(orgList["shares"].([]any)) != 1 {
			t.Fatalf("the admin lists: %d %v", status, orgList)
		}
		if status, _ := sendAs(t, p.app, http.MethodDelete, "/v1/agent/chat/shares/"+shareID, p.admin, nil); status != http.StatusOK {
			t.Fatalf("the admin revokes: %d", status)
		}
		if _, list := sendAs(t, p.app, http.MethodGet, "/v1/agent/chat/shared", p.reader, nil); len(list["shared"].([]any)) != 0 {
			t.Fatalf("the reader keeps a revoked chat: %v", list)
		}

	})
}
