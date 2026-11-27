package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/hanzoai/cloud/sdk/oidctest"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/iamtest"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// inbox_test.go holds the inbox to one rule across the real identity boundary: a
// message is the person's who said it to the bot — the Hanzo account their chat
// identity is linked to — and nobody else's in the org, its admins included. A
// message that names no such person is nobody's: a SuperAdmin alone reads it, and
// that read is on the audit trail.
//
// Tokens are signed by a test IAM issuer and validated by cloud.Identify, and the
// trail is installed as every serving program installs it (cloud.Witness), so no
// caller here states its own identity.

const (
	subAna   = "7a1e0000-0000-4000-8000-0000000000c1"
	subBen   = "7a1e0000-0000-4000-8000-0000000000c2"
	subBoss  = "7a1e0000-0000-4000-8000-0000000000c3"
	subSuper = "7a1e0000-0000-4000-8000-0000000000c4"
)

type inboxRig struct {
	app *zip.App
	rec *audit.Recorder
	iss *iamtest.Issuer0
	st  *store
}

func inboxApp(t *testing.T) *inboxRig {
	t.Helper()
	iss := iamtest.New(t)
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	cloud.Identify(app, &cloud.Config{IAMIssuer: iamtest.Issuer, Discovery: oidctest.Discovery(iamtest.Issuer), DiscoveryClient: oidctest.Client(iamtest.Issuer, iss.URL)})
	rec, err := audit.Open(t.TempDir(), "audit", nil)
	if err != nil {
		t.Fatalf("audit.Open: %v", err)
	}
	t.Cleanup(func() { _ = rec.Close() })
	cloud.Witness(app, rec)
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	t.Setenv("CLOUD_DOMAIN", "api.hanzo.ai")
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(context.Background()) })
	return &inboxRig{app: app, rec: rec, iss: iss, st: mounted.Load().State.store}
}

// person is a token for someone whose home org is org, in the given role.
func (r *inboxRig) person(t *testing.T, sub, name, org, role string) string {
	t.Helper()
	return r.iss.Sign(t, iamtest.Claims{Sub: sub, PreferredUsername: name, Owner: org,
		Orgs: []map[string]any{{"org": org, "role": role}}})
}

// superAdmin is a person whose home org is the reserved admin org.
func (r *inboxRig) superAdmin(t *testing.T) string {
	t.Helper()
	return r.person(t, subSuper, "z", "admin", "owner")
}

// do sends one request as bearer, acting in org.
func (r *inboxRig) do(t *testing.T, method, path, bearer, org string, body []byte) (int, []byte) {
	t.Helper()
	rq := httptest.NewRequest(method, path, bytes.NewReader(body))
	if body != nil {
		rq.Header.Set("Content-Type", "application/json")
	}
	rq.Header.Set("Authorization", "Bearer "+bearer)
	rq.Header.Set("X-Org-Id", org)
	resp, err := r.app.Test(rq, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// seed stores the rows every test reads: two of Ana's (a DM and a remark in a
// shared channel), one of Ben's, and two whose sender is linked to nobody.
func (r *inboxRig) seed(t *testing.T, org string) {
	t.Helper()
	now := time.Now().Unix()
	for i, row := range []inboxRow{
		{SenderUser: subAna, ChannelID: "D-ANA", ChannelKind: ChannelDM, Sender: "U-ANA", Text: org + ": ana's DM"},
		{SenderUser: subAna, ChannelID: "C-ALL", ChannelKind: ChannelGroup, Sender: "U-ANA", Text: org + ": ana in #all"},
		{SenderUser: subBen, ChannelID: "D-BEN", ChannelKind: ChannelDM, Sender: "U-BEN", Text: org + ": ben's DM"},
		{ChannelID: "D-UNK", ChannelKind: ChannelDM, Sender: "U-UNK", Text: org + ": unlinked DM"},
		{ChannelID: "C-ALL", ChannelKind: ChannelGroup, Sender: "U-UNK", Text: org + ": unlinked in #all"},
	} {
		row.Org, row.Transport, row.Account, row.CreatedAt = org, "slack", "t-"+org, now
		row.EventKey = org + "-" + string(rune('a'+i))
		if err := r.st.insertInbox(context.Background(), row); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
}

// stored is every row ingest wrote for org, read straight off the table: what the
// gate let in, apart from who may read it.
func stored(ctx context.Context, st *store, org string) ([]inboxRow, error) {
	rows, err := st.db.QueryContext(ctx, `SELECT `+inboxColumns+` FROM channel_inbox WHERE org = $1 ORDER BY id`, org)
	if err != nil {
		return nil, err
	}
	return scanInbox(rows)
}

// inboxTexts is what one caller's inbox lists, sorted.
func inboxTexts(t *testing.T, raw []byte) []string {
	t.Helper()
	var page struct {
		Messages []struct {
			Text string `json:"text"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatalf("inbox json: %v (%s)", err, raw)
	}
	out := []string{}
	for _, m := range page.Messages {
		out = append(out, m.Text)
	}
	slices.Sort(out)
	return out
}

func sorted(v ...string) []string {
	out := append([]string{}, v...)
	slices.Sort(out)
	return out
}

// Two members of one org, in a customer's own org and in the held org every
// self-serve customer joins: each lists exactly their own messages, the admin of
// a customer's org lists every row of it (its bots are theirs) while the admin
// of the held org lists none of either's, a token that names no stable subject lists nothing, and only a
// SuperAdmin lists the messages no person is recorded on. Every org's rows are in
// the one store, so each org also proves the other's rows stay out.
func TestInboxIsThePersonsOwn(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := inboxApp(t)
		for _, org := range []string{"acme", "hanzo"} {
			r.seed(t, org)
		}
		for _, org := range []string{"acme", "hanzo"} {
			ana := r.person(t, subAna, "ana", org, "member")
			ben := r.person(t, subBen, "ben", org, "member")
			boss := r.person(t, subBoss, "boss", org, "admin")
			ghost := r.iss.Sign(t, iamtest.Claims{PreferredUsername: "ghost", Owner: org,
				Orgs: []map[string]any{{"org": org, "role": "member"}}})

			// The admin is an admin: the boundary minted the bit the mutation gate reads.
			put := []byte(`{"transport":"slack","dmPolicy":"members"}`)
			if code, body := r.do(t, http.MethodPut, "/v1/transport/allowlist", boss, org, put); code != http.StatusOK {
				t.Fatalf("%s: the org admin's allowlist PUT answered %d (%s)", org, code, body)
			}
			if code, _ := r.do(t, http.MethodPut, "/v1/transport/allowlist", ben, org, put); code != http.StatusForbidden {
				t.Fatalf("%s: a member's allowlist PUT answered %d, want 403", org, code)
			}

			for _, c := range []struct {
				who, bearer string
				want        []string
			}{
				{"ana", ana, sorted(org+": ana's DM", org+": ana in #all")},
				{"ben", ben, sorted(org + ": ben's DM")},
				{"the org admin", boss, adminSees(org)},
				{"a token with no subject", ghost, sorted()},
				{"a SuperAdmin", r.superAdmin(t), sorted(org+": unlinked DM", org+": unlinked in #all")},
			} {
				code, body := r.do(t, http.MethodGet, "/v1/transport/inbox", c.bearer, org, nil)
				if org == "hanzo" && (c.who == "ana" || c.who == "ben" || c.who == "a token with no subject") {
					if code != http.StatusForbidden {
						t.Fatalf("a plain platform-org member read an inbox: %d %s", code, body)
					}
					continue
				}
				if code != http.StatusOK {
					t.Fatalf("%s: %s's inbox answered %d (%s)", org, c.who, code, body)
				}
				if got := inboxTexts(t, body); !slices.Equal(got, c.want) {
					t.Errorf("%s: %s's inbox listed %q, want %q", org, c.who, got, c.want)
				}
			}
		}

	})
}

// The cursor pages the caller's own rows: a page ends at the last row it listed,
// and paging on from it skips everyone else's rows rather than surfacing them.
func TestInboxCursorPagesTheCallersOwnRows(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := inboxApp(t)
		r.seed(t, "hanzo")
		ana := r.person(t, subAna, "ana", "hanzo", "admin")
		type page struct {
			Messages []struct {
				ID   int64  `json:"id"`
				Text string `json:"text"`
			} `json:"messages"`
			Cursor int64 `json:"cursor"`
		}
		read := func(path string) page {
			code, body := r.do(t, http.MethodGet, path, ana, "hanzo", nil)
			if code != http.StatusOK {
				t.Fatalf("GET %s: %d (%s)", path, code, body)
			}
			var p page
			decodeJSON(t, body, &p)
			return p
		}
		first := read("/v1/transport/inbox?limit=1")
		if len(first.Messages) != 1 || first.Messages[0].Text != "hanzo: ana's DM" || first.Cursor != first.Messages[0].ID {
			t.Fatalf("first page = %+v, want ana's DM with the cursor at its id", first)
		}
		second := read("/v1/transport/inbox?since=" + strconv.FormatInt(first.Cursor, 10))
		if len(second.Messages) != 1 || second.Messages[0].Text != "hanzo: ana in #all" || second.Cursor != second.Messages[0].ID {
			t.Fatalf("second page = %+v, want ana's remark in #all and nothing of anyone else's", second)
		}
		last := read("/v1/transport/inbox?since=" + strconv.FormatInt(second.Cursor, 10))
		if len(last.Messages) != 0 || last.Cursor != second.Cursor {
			t.Fatalf("last page = %+v, want empty at cursor %d", last, second.Cursor)
		}

	})
}

// The rule is the operation's, so the MCP door answers what REST answers: a member
// reaching the inbox as a tool lists their own messages and nobody else's.
func TestInboxRuleHoldsOverMCP(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := inboxApp(t)
		r.seed(t, "hanzo")
		call := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_transport_inbox","arguments":{}}}`)
		for _, c := range []struct {
			who, bearer string
			want        []string
		}{
			{"ben", r.person(t, subBen, "ben", "hanzo", "admin"), sorted("hanzo: ben's DM")},
			{"the org admin", r.person(t, subBoss, "boss", "hanzo", "admin"), sorted()},
		} {
			code, raw := r.do(t, http.MethodPost, "/mcp", c.bearer, "hanzo", call)
			if code != http.StatusOK {
				t.Fatalf("tools/call as %s: %d %s", c.who, code, raw)
			}
			var env struct {
				Result struct {
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
					IsError bool `json:"isError"`
				} `json:"result"`
			}
			if err := json.Unmarshal(raw, &env); err != nil || env.Result.IsError || len(env.Result.Content) == 0 {
				t.Fatalf("tools/call as %s: no result: %s", c.who, raw)
			}
			if got := inboxTexts(t, []byte(env.Result.Content[0].Text)); !slices.Equal(got, c.want) {
				t.Errorf("tools/call as %s listed %q, want %q", c.who, got, c.want)
			}
		}

	})
}

// A SuperAdmin's read of the inbox is on the trail, recorded by the audit
// middleware for the authority the request carried; a member's read of their own
// inbox is an ordinary read and is not.
func TestSuperAdminInboxReadIsAudited(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := inboxApp(t)
		r.seed(t, "hanzo")
		ctx := context.Background()
		reads := func() (super, other int) {
			rows, _, err := r.rec.Query(ctx, audit.Filter{Org: "hanzo", Limit: 1000})
			if err != nil {
				t.Fatalf("audit query: %v", err)
			}
			for _, row := range rows {
				if row.Method != http.MethodGet || row.Path != "/v1/transport/inbox" {
					continue
				}
				if row.Auth.IsAdmin {
					super++
				} else {
					other++
				}
			}
			return super, other
		}
		if code, body := r.do(t, http.MethodGet, "/v1/transport/inbox", r.person(t, subBen, "ben", "hanzo", "admin"), "hanzo", nil); code != http.StatusOK {
			t.Fatalf("member inbox: %d (%s)", code, body)
		}
		if code, body := r.do(t, http.MethodGet, "/v1/transport/inbox", r.superAdmin(t), "hanzo", nil); code != http.StatusOK {
			t.Fatalf("SuperAdmin inbox: %d (%s)", code, body)
		}
		if super, other := reads(); super != 1 || other != 0 {
			t.Fatalf("trail holds %d SuperAdmin and %d other inbox reads, want 1 and 0", super, other)
		}

	})
}

// Pending pairing requests and the allowlist name the people who message the org's
// bots. They are the org admin's to govern, as the writes beside them are, so a
// member of the org reads neither.
func TestPairingAndAllowlistAreForOrgAdmins(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := inboxApp(t)
		ben := r.person(t, subBen, "ben", "hanzo", "member")
		boss := r.person(t, subBoss, "boss", "hanzo", "admin")
		for _, path := range []string{"/v1/transport/pairing", "/v1/transport/allowlist?transport=slack"} {
			if code, body := r.do(t, http.MethodGet, path, ben, "hanzo", nil); code != http.StatusForbidden {
				t.Errorf("a member's GET %s answered %d (%s), want 403", path, code, body)
			}
			if code, body := r.do(t, http.MethodGet, path, boss, "hanzo", nil); code != http.StatusOK {
				t.Errorf("the org admin's GET %s answered %d (%s), want 200", path, code, body)
			}
			if code, body := r.do(t, http.MethodGet, path, r.superAdmin(t), "hanzo", nil); code != http.StatusOK {
				t.Errorf("a SuperAdmin's GET %s answered %d (%s), want 200", path, code, body)
			}
		}

	})
}

// adminSees is what an org's admin lists: every row of a customer's own org, and
// in a held org nothing but their own (they sent none here).
func adminSees(org string) []string {
	if org == "hanzo" {
		return sorted()
	}
	return sorted(org+": ana's DM", org+": ana in #all", org+": ben's DM", org+": unlinked DM", org+": unlinked in #all")
}
