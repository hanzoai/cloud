package s3_test

// tenancy_test.go is the cross-org table: every address /v1/s3 serves, asked by
// every kind of caller, through the real identity boundary (tokens IAM would
// sign, verified by cloud.Identify), against an object store that records what it
// is asked.
//
// It holds one property rather than a status per cell. A caller REACHES an org's
// namespace — the store is asked about a bucket under that org's prefix, or the
// answer carries that prefix, one of its names or its bytes — exactly when it may.
// A status alone cannot say this: a caller whose selection of another org the
// boundary discards is answered 200 from its own org, and a 403 says nothing about
// whether the store was asked first. So every cell is judged by what the store saw
// and what came back, and every cell an authorized caller fills must reach, so the
// table cannot pass by every surface being broken.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/hanzoai/cloud/sdk/oidctest"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/provisioning"
	"github.com/hanzoai/cloud/internal/iamtest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// surface is one way in: an address over REST, or an operation by name over MCP.
type surface struct {
	name         string
	method, path string
	body         string
	kind         string         // Content-Type of a non-JSON body
	tool         string         // an MCP tools/call name, in place of the route
	args         map[string]any // its arguments
	// administers marks the surfaces only an admin may use: making and removing a
	// bucket. Every member of a customer's org uses the rest.
	administers bool
}

// surfaces is every operation /v1/s3 serves but its probe, each aimed at the
// victim's bucket "vault" and object "secret.txt": names a caller can guess.
var surfaces = []surface{
	{name: "list buckets", method: http.MethodGet, path: "/v1/s3/buckets"},
	{name: "create a bucket", method: http.MethodPost, path: "/v1/s3/buckets", body: `{"name":"annex"}`, administers: true},
	{name: "delete a bucket", method: http.MethodDelete, path: "/v1/s3/buckets/vault", administers: true},
	{name: "list objects", method: http.MethodGet, path: "/v1/s3/buckets/vault/objects?recursive=true"},
	{name: "presign an upload", method: http.MethodPost, path: "/v1/s3/buckets/vault/objects", body: `{"key":"secret.txt"}`},
	{name: "presign a download", method: http.MethodGet, path: "/v1/s3/buckets/vault/objects/secret.txt"},
	{name: "delete an object", method: http.MethodDelete, path: "/v1/s3/buckets/vault/objects/secret.txt"},
	{name: "write bytes", method: http.MethodPut, path: "/v1/s3/buckets/vault/blob/secret.txt", body: "OVERWRITTEN", kind: "text/plain"},
	{name: "read bytes", method: http.MethodGet, path: "/v1/s3/buckets/vault/blob/secret.txt"},
	{name: "start a multipart upload", method: http.MethodPost, path: "/v1/s3/buckets/vault/uploads", body: `{"key":"big.bin"}`},
	{name: "mint part URLs", method: http.MethodPost, path: "/v1/s3/buckets/vault/uploads/u1/parts", body: `{"key":"big.bin","parts":[1]}`},
	{name: "list stored parts", method: http.MethodGet, path: "/v1/s3/buckets/vault/uploads/u1?key=big.bin"},
	{name: "complete an upload", method: http.MethodPost, path: "/v1/s3/buckets/vault/uploads/u1/complete", body: `{"key":"big.bin"}`},
	{name: "abandon an upload", method: http.MethodDelete, path: "/v1/s3/buckets/vault/uploads/u1?key=big.bin"},
	{name: "list buckets by name", tool: "get_s3_buckets", args: map[string]any{}},
	{name: "delete an object by name", tool: "delete_s3_buckets_by_bucket_objects_by_wildcard1", args: map[string]any{"bucket": "vault", "key": "secret.txt"}},
	{name: "delete a bucket by name", tool: "delete_s3_buckets_by_bucket", args: map[string]any{"bucket": "vault"}, administers: true},
}

// verdict is what a caller is owed on a surface.
type verdict int

const (
	// reaches: the caller acts in the victim's org and the operation addresses it.
	reaches verdict = iota
	// refused: the caller is answered the status given, and the store is asked
	// nothing at all.
	refused
	// elsewhere: the caller acts in its OWN org. Whatever it is answered, the store
	// is asked about no namespace but that one, and nothing of the victim's comes
	// back.
	elsewhere
)

// caller is one kind of principal, aimed at the victim's org.
type caller struct {
	name   string
	bearer string // "" sends no credential
	forge  bool   // send the headers a SuperAdmin's request carries, with no credential
	own    string // the org an elsewhere caller acts in
	// owed is the verdict on s; status is the refusal's when it is refused.
	owed   func(s surface) verdict
	status int
}

// victim is one org's namespace as seeded, and what would betray it.
type victim struct {
	org     string
	prefix  string   // every physical bucket of the org starts with this
	secrets []string // a bucket name, a key and the bytes no caller names
	seed    contents
}

func seedVictim(org string) victim {
	pfx := provisioning.BucketPrefix(org)
	ledger := "ledger-" + strings.ReplaceAll(org, "_", "-")
	private := "private/payroll-" + org + ".csv"
	bytesOf := "BYTES-OF-" + org + "-ONLY"
	at := time.Unix(1_790_000_000, 0)
	return victim{
		org:     org,
		prefix:  pfx,
		secrets: []string{ledger, "payroll-" + org, bytesOf},
		seed: contents{
			buckets: map[string]time.Time{
				provisioning.BucketName(org, "vault"): at,
				provisioning.BucketName(org, ledger):  at,
			},
			objects: map[string][]byte{
				provisioning.BucketName(org, "vault") + "/secret.txt": []byte(bytesOf),
				provisioning.BucketName(org, "vault") + "/" + private: []byte(bytesOf),
			},
		},
	}
}

// reached reports whether a call touched v's namespace: what the store was asked,
// and what the caller was answered.
func (v victim) reached(asked []string, body string) (bool, string) {
	for _, r := range asked {
		if strings.Contains(r, v.prefix) {
			return true, "the store was asked " + r
		}
	}
	if strings.Contains(body, v.prefix) {
		return true, "the answer carries the org's namespace"
	}
	for _, s := range v.secrets {
		if strings.Contains(body, s) {
			return true, "the answer carries " + s
		}
	}
	return false, ""
}

// table is one app behind the identity boundary, its store, and the callers.
type table struct {
	app   *zip.App
	store *bucketStore
}

func newTable(t *testing.T) (table, *iamtest.Issuer0) {
	t.Helper()
	bs, addr := newBucketStore(t)
	iss := iamtest.New(t)
	app := newAppBehind(t, addr, addr, func(app *zip.App) {
		cloud.Identify(app, &cloud.Config{IAMIssuer: iamtest.Issuer, Discovery: oidctest.Discovery(iamtest.Issuer), DiscoveryClient: oidctest.Client(iamtest.Issuer, iss.URL)})
	})
	return table{app: app, store: bs}, iss
}

// call sends s as c, selecting org, and answers the status, the body, and what
// the store was asked meanwhile. Over MCP the status is the envelope's and a
// refusal is an error result inside it, so there the status answered is 0 for
// an error and 200 for an answer.
func (tb table) call(t *testing.T, c caller, org string, s surface) (int, string, []string) {
	t.Helper()
	tb.store.requests()
	var req *http.Request
	if s.tool != "" {
		frame, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": s.tool, "arguments": s.args}})
		req = httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(frame))
		req.Header.Set("Content-Type", "application/json")
	} else {
		var r io.Reader
		if s.body != "" {
			r = strings.NewReader(s.body)
		}
		req = httptest.NewRequest(s.method, s.path, r)
		switch {
		case s.kind != "":
			req.Header.Set("Content-Type", s.kind)
		case s.body != "":
			req.Header.Set("Content-Type", "application/json")
		}
	}
	if c.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearer)
	}
	if c.forge {
		req.Header.Set("X-User-Id", "7a1e0000-0000-4000-8000-0000000000f0")
		req.Header.Set("X-User-IsAdmin", "true")
		req.Header.Set("X-User-IsOrgAdmin", "true")
		req.Header.Set("X-User-Owner", "admin")
	}
	req.Header.Set("X-Org-Id", org)
	resp, err := tb.app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("%s %s: %v", c.name, s.name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	status := resp.StatusCode
	if s.tool != "" && status == http.StatusOK && !answered(string(b)) {
		status = 0
	}
	return status, string(b), tb.store.requests()
}

// TestNoCallerReachesAnotherOrgsBuckets is the table, in a customer's org (acme),
// in an org the platform runs (hanzo), and in a funded org the platform holds by
// name and its customer runs (adnexus).
func TestNoCallerReachesAnotherOrgsBuckets(t *testing.T) {
	for _, org := range []string{"acme", "hanzo", "adnexus"} {
		t.Run(org, func(t *testing.T) {
			tb, iss := newTable(t)
			v := seedVictim(org)
			in := func(org, role string) map[string]any { return map[string]any{"org": org, "role": role} }
			person := func(n int, name, home string, orgs ...map[string]any) string {
				return iss.Sign(t, iamtest.Claims{Sub: fmt.Sprintf("7a1e0000-0000-4000-8000-%012d", n),
					PreferredUsername: name, Owner: home, Orgs: orgs})
			}
			held := org != "acme"
			always := func(w verdict) func(surface) verdict { return func(surface) verdict { return w } }
			member := func(s surface) verdict {
				if held || s.administers {
					return refused
				}
				return reaches
			}
			callers := []caller{
				{name: "an admin of the org", bearer: person(1, "ann", org, in(org, "owner")), owed: always(reaches)},
				{name: "a member of the org", bearer: person(2, "bob", org, in(org, "member")), owed: member, status: http.StatusForbidden},
				{name: "a SuperAdmin", bearer: person(3, "z", "admin", in("admin", "owner")), owed: always(reaches)},
				{name: "an admin of another org", bearer: person(4, "carol", "carolco", in("carolco", "owner")), own: "carolco", owed: always(elsewhere)},
				{name: "a member of another org", bearer: person(5, "dave", "carolco", in("carolco", "member")), own: "carolco", owed: always(elsewhere)},
				{name: "nobody", owed: always(refused), status: http.StatusUnauthorized},
				{name: "a forged SuperAdmin", forge: true, owed: always(refused), status: http.StatusUnauthorized},
			}
			// An admin of another org who holds a plain membership of the victim: in a
			// customer's org that makes her a member, and the member row above is her.
			// In an org the platform runs the membership opens nothing, so she acts in
			// her own; in a funded org it opens, and the held org's rule refuses her.
			switch org {
			case "hanzo":
				callers = append(callers, caller{name: "an admin of another org holding a plain membership",
					bearer: person(6, "eve", "evilco", in("evilco", "owner"), in(org, "member")), own: "evilco", owed: always(elsewhere)})
			case "adnexus":
				callers = append(callers, caller{name: "an admin of another org holding a plain membership",
					bearer: person(6, "eve", "evilco", in("evilco", "owner"), in(org, "member")), owed: always(refused), status: http.StatusForbidden})
			}

			for _, c := range callers {
				for _, s := range surfaces {
					tb.store.hold(v.seed)
					status, body, asked := tb.call(t, c, org, s)
					hit, why := v.reached(asked, body)
					switch c.owed(s) {
					case reaches:
						if !hit {
							t.Errorf("%s: %s did not reach the org's namespace (%d %s; the store was asked %v)", c.name, s.name, status, body, asked)
						}
						if status == http.StatusUnauthorized || status == http.StatusForbidden {
							t.Errorf("%s: %s = %d %s, want it admitted", c.name, s.name, status, body)
						}
					case refused:
						if (s.tool == "" && status != c.status) || (s.tool != "" && status != 0) {
							t.Errorf("%s: %s = %d %s, want %d", c.name, s.name, status, body, c.status)
						}
						if len(asked) > 0 {
							t.Errorf("%s: %s was refused after the store was asked %v", c.name, s.name, asked)
						}
						if hit {
							t.Errorf("%s: %s reached the org's namespace: %s", c.name, s.name, why)
						}
					case elsewhere:
						if hit {
							t.Errorf("%s: %s reached %s's namespace: %s (%d)", c.name, s.name, org, why, status)
						}
						mine := provisioning.BucketPrefix(c.own)
						for _, r := range asked {
							if path := strings.TrimPrefix(strings.SplitN(r, " ", 2)[1], "/"); path != "" && !strings.HasPrefix(path, mine) {
								t.Errorf("%s: %s asked the store %q, outside its own org %s", c.name, s.name, r, c.own)
							}
						}
						// Worth nothing unless the caller really lands at home: by the same
						// address, its listing asks the store about its own org's bucket.
						if s.name == "list objects" && !askedUnder(asked, mine) {
							t.Errorf("%s: %s asked the store %v, not about its own org %s", c.name, s.name, asked, c.own)
						}
					}
					if after := tb.store.contents(); c.owed(s) != reaches && !same(after, v.seed, v.prefix) {
						t.Errorf("%s: %s changed %s's namespace", c.name, s.name, org)
					}
				}
			}

		})
	}
}

// same reports whether a and b hold the same buckets and objects under prefix.
func same(a, b contents, prefix string) bool {
	pick := func(c contents) (map[string]bool, map[string]string) {
		bk, ob := map[string]bool{}, map[string]string{}
		for k := range c.buckets {
			if strings.HasPrefix(k, prefix) {
				bk[k] = true
			}
		}
		for k, v := range c.objects {
			if strings.HasPrefix(k, prefix) {
				ob[k] = string(v)
			}
		}
		return bk, ob
	}
	ab, ao := pick(a)
	bb, bo := pick(b)
	if len(ab) != len(bb) || len(ao) != len(bo) {
		return false
	}
	for k := range ab {
		if !bb[k] {
			return false
		}
	}
	for k, v := range ao {
		if bo[k] != v {
			return false
		}
	}
	return true
}

func askedUnder(asked []string, prefix string) bool {
	for _, r := range asked {
		if strings.Contains(r, " /"+prefix) {
			return true
		}
	}
	return false
}
