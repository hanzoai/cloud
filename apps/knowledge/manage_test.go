package knowledge

import (
	azip "archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/hanzoai/cloud/sdk/oidctest"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/iamtest"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// manage_test.go holds who may change or forget a workspace file, asked through
// the real identity boundary: tokens IAM would sign, verified by cloud.Identify,
// so the subject a file is recorded under and the admin bit a gate reads are the
// ones production mints.
//
// The tests run in a customer's own org, where an admin of the org manages its
// files; TestInAHeldOrgAnAdminIsAStranger runs them in hanzo, a held org, where
// it was measured: every self-serve signup is a member there, so a brand-new
// customer listed nine of the org's files and could forget every one of them.

// heldOrg is the org the harness acts in: a customer's own org by default, and
// hanzo for the held-org test (inHeldOrg).
var heldOrg = "acme"

// inHeldOrg points the harness at hanzo for the rest of t.
func inHeldOrg(t *testing.T) {
	t.Helper()
	was := heldOrg
	heldOrg = "hanzo"
	t.Cleanup(func() { heldOrg = was })
}

// Subjects, as IAM signs them: a person's `sub` is their User.
const (
	aliceSub = "7a1e0000-0000-4000-8000-0000000000f1"
	bobSub   = "7a1e0000-0000-4000-8000-0000000000f2"
	bossSub  = "7a1e0000-0000-4000-8000-0000000000f3"
	carolSub = "7a1e0000-0000-4000-8000-0000000000f4"
	rootSub  = "7a1e0000-0000-4000-8000-0000000000f5"
)

type filePeople struct {
	app *zip.App
	mem *memObjects
	// alice uploads; bob is another member; boss administers hanzo; carol owns
	// carolco and is a plain member of hanzo; root is a SuperAdmin acting in hanzo.
	alice, bob, boss, carol, root string
}

func filesAsPeople(t *testing.T) filePeople {
	t.Helper()
	iss := iamtest.New(t)
	app, mem := mountFilesBehind(t, readerAI{fakeAI: fakeAI{dims: 1024}}, func(app *zip.App) {
		cloud.Identify(app, &cloud.Config{IAMIssuer: iamtest.Issuer, Discovery: oidctest.Discovery(iamtest.Issuer), DiscoveryClient: oidctest.Client(iamtest.Issuer, iss.URL)})
	})
	in := func(org, role string) map[string]any { return map[string]any{"org": org, "role": role} }
	person := func(sub, name, home string, orgs ...map[string]any) string {
		return iss.Sign(t, iamtest.Claims{Sub: sub, PreferredUsername: name, Owner: home, Orgs: orgs})
	}
	role := "member"
	if heldOrg == "hanzo" {
		// Platform staff may enter the org; that still grants no ownership of
		// another staff member's uploaded files. Signup members cannot enter it.
		role = "admin"
	}
	return filePeople{
		app:   app,
		mem:   mem,
		alice: person(aliceSub, "alice", heldOrg, in(heldOrg, role)),
		bob:   person(bobSub, "bob", heldOrg, in(heldOrg, role)),
		boss:  person(bossSub, "boss", heldOrg, in(heldOrg, "admin")),
		carol: person(carolSub, "carol", "carolco", in("carolco", "owner"), in(heldOrg, "member")),
		root:  person(rootSub, "z", "admin", in("admin", "owner")),
	}
}

// as sends one request in hanzo as the bearer.
func (p filePeople) as(t *testing.T, bearer, method, path string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	hr := httptest.NewRequest(method, path, r)
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Authorization", "Bearer "+bearer)
	hr.Header.Set("X-Org-Id", heldOrg)
	resp, err := p.app.Test(hr, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// upload puts an object in hanzo's drive bucket and registers it as bearer.
func (p filePeople) upload(t *testing.T, bearer, key, text string) File {
	t.Helper()
	p.mem.put(heldOrg, "drive", key, "text/markdown", []byte(text))
	code, b := p.as(t, bearer, http.MethodPost, "/v1/knowledge/files", map[string]any{"bucket": "drive", "key": key})
	if code != http.StatusOK {
		t.Fatalf("register %s: %d %s", key, code, b)
	}
	return decode[File](t, b)
}

// settle waits for a file's ingest to end, read as alice, and answers it.
func (p filePeople) settle(t *testing.T, id string) File {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		code, b := p.as(t, p.alice, http.MethodGet, "/v1/knowledge/files/"+id, nil)
		if code != http.StatusOK {
			t.Fatalf("get file %s: %d %s", id, code, b)
		}
		f := decode[File](t, b)
		if (f.Status == fileReady && f.Stage != stageEmbed) || f.Status == fileFailed || f.Status == fileStoredAs {
			return f
		}
		if time.Now().After(deadline) {
			t.Fatalf("file %s still %s at %s", id, f.Status, f.Stage)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (p filePeople) forget(t *testing.T, bearer, id string) int {
	t.Helper()
	code, _ := p.as(t, bearer, http.MethodDelete, "/v1/knowledge/files/"+id, nil)
	return code
}

func (p filePeople) visible(t *testing.T, bearer, id string) bool {
	t.Helper()
	code, b := p.as(t, bearer, http.MethodGet, "/v1/knowledge/files/"+id, nil)
	switch code {
	case http.StatusOK:
		return true
	case http.StatusNotFound:
		return false
	}
	t.Fatalf("get %s: %d %s", id, code, b)
	return false
}

// uploaderOf reads whom a file's record names as its uploader, straight from
// hanzo's store.
func uploaderOf(t *testing.T, id string) string {
	t.Helper()
	st, err := idx.storeFor(heldOrg)
	if err != nil {
		t.Fatal(err)
	}
	var who string
	if err := st.db.QueryRow(`SELECT uploader FROM files WHERE id=$1`, id).Scan(&who); err != nil {
		t.Fatalf("read the uploader of %s: %v", id, err)
	}
	return who
}

// recorded reads a file's record straight from hanzo's store.
func recorded(t *testing.T, id string) File {
	t.Helper()
	st, err := idx.storeFor(heldOrg)
	if err != nil {
		t.Fatal(err)
	}
	f, err := st.file(context.Background(), id)
	if err != nil {
		t.Fatalf("read %s: %v", id, err)
	}
	return f
}

// TestAFileIsForgottenOnlyByItsUploaderOrAnAdmin: another member reads the file
// and cannot forget it — and neither can an admin of another org who is a
// member here — while the uploader, an admin of the org and a SuperAdmin can.
func TestAFileIsForgottenOnlyByItsUploaderOrAnAdmin(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		p := filesAsPeople(t)
		mine := p.upload(t, p.alice, "alice/mine.md", "# Mine\n\nAlice's own notes.")
		byBoss := p.upload(t, p.alice, "alice/boss.md", "# Boss\n\nAn admin tidies this one.")
		byRoot := p.upload(t, p.alice, "alice/root.md", "# Root\n\nA SuperAdmin tidies this one.")

		if !p.visible(t, p.bob, mine.ID) {
			t.Fatal("another member cannot read the org's file; reads stay org-visible")
		}
		for name, bearer := range map[string]string{"another member": p.bob, "an admin of another org": p.carol} {
			if code := p.forget(t, bearer, mine.ID); code != http.StatusForbidden {
				t.Fatalf("%s forgot alice's file: %d, want 403", name, code)
			}
		}
		if !p.visible(t, p.alice, mine.ID) {
			t.Fatal("a refused forget removed the file")
		}
		if got := uploaderOf(t, mine.ID); got != aliceSub {
			t.Fatalf("uploader = %q, want alice's subject %q", got, aliceSub)
		}
		for _, c := range []struct {
			who, bearer string
			f           File
		}{{"its uploader", p.alice, mine}, {"an admin of the org", p.boss, byBoss}, {"a SuperAdmin", p.root, byRoot}} {
			if code := p.forget(t, c.bearer, c.f.ID); code != http.StatusOK {
				t.Fatalf("%s could not forget %s: %d", c.who, c.f.Key, code)
			}
			if p.visible(t, p.alice, c.f.ID) {
				t.Fatalf("%s forgot %s and it still answers", c.who, c.f.Key)
			}
		}
	})
}

// TestAFileIsReplacedOnlyByItsUploaderOrAnAdmin: registering a changed object
// again re-indexes the file, which is changing it. Another member is refused and
// the record keeps alice's version; alice and an admin re-index it, and the
// uploader stays alice whoever re-indexed.
func TestAFileIsReplacedOnlyByItsUploaderOrAnAdmin(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		p := filesAsPeople(t)
		f := p.upload(t, p.alice, "alice/plan.md", "# Plan\n\nVersion one.")
		before := recorded(t, f.ID)

		p.mem.put(heldOrg, "drive", "alice/plan.md", "text/markdown", []byte("# Plan\n\nBob's version."))
		code, b := p.as(t, p.bob, http.MethodPost, "/v1/knowledge/files", map[string]any{"bucket": "drive", "key": "alice/plan.md"})
		if code != http.StatusForbidden {
			t.Fatalf("another member re-indexed alice's file: %d %s, want 403", code, b)
		}
		if got := recorded(t, f.ID); got.etag != before.etag {
			t.Fatalf("a refused re-index changed the record: %+v", got)
		}

		for _, who := range []struct{ name, bearer, text string }{
			{"its uploader", p.alice, "# Plan\n\nVersion two."},
			{"an admin of the org", p.boss, "# Plan\n\nVersion three."},
		} {
			p.mem.put(heldOrg, "drive", "alice/plan.md", "text/markdown", []byte(who.text))
			prev := recorded(t, f.ID).etag
			if code, b := p.as(t, who.bearer, http.MethodPost, "/v1/knowledge/files", map[string]any{"bucket": "drive", "key": "alice/plan.md"}); code != http.StatusOK {
				t.Fatalf("%s could not re-index: %d %s", who.name, code, b)
			}
			if recorded(t, f.ID).etag == prev {
				t.Fatalf("%s re-indexed and the record kept the old version", who.name)
			}
			if got := uploaderOf(t, f.ID); got != aliceSub {
				t.Fatalf("after %s re-indexed, uploader = %q, want alice", who.name, got)
			}
		}

		// Registering an unchanged object is a read: anyone in the org gets the file.
		if code, b := p.as(t, p.bob, http.MethodPost, "/v1/knowledge/files", map[string]any{"bucket": "drive", "key": "alice/plan.md"}); code != http.StatusOK {
			t.Fatalf("registering an unchanged object as another member: %d %s, want 200", code, b)
		}
	})
}

// TestAFileWithNoUploaderIsTheAdminsAlone: a record written before records
// carried an uploader is nobody's. Opening the store adds the column, the old
// row reads empty, and only an admin may forget it — its real uploader included
// in the refusal, since nothing says it was theirs.
func TestAFileWithNoUploaderIsTheAdminsAlone(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		p := filesAsPeople(t)
		f := p.upload(t, p.alice, "old/report.md", "# Report\n\nFiled before uploaders were.")
		st, err := idx.storeFor(heldOrg)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.db.Exec(`ALTER TABLE files DROP COLUMN uploader`); err != nil {
			t.Fatalf("make the earlier schema: %v", err)
		}
		if _, err := openStore(st.db); err != nil { // the upgraded build opens the same file
			t.Fatal(err)
		}
		if got := uploaderOf(t, f.ID); got != "" {
			t.Fatalf("an earlier record's uploader = %q, want none", got)
		}
		for name, bearer := range map[string]string{"the person who uploaded it": p.alice, "another member": p.bob} {
			if code := p.forget(t, bearer, f.ID); code != http.StatusForbidden {
				t.Fatalf("%s forgot a file with no uploader: %d, want 403", name, code)
			}
		}
		if code := p.forget(t, p.boss, f.ID); code != http.StatusOK {
			t.Fatalf("an admin could not forget a file with no uploader: %d", code)
		}
	})
}

// TestTheRuleHoldsEveryWayIn: the same refusal and the same admission by name,
// over MCP's tools/call — which reaches the op with no route — as over REST.
func TestTheRuleHoldsEveryWayIn(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		p := filesAsPeople(t)
		byName := func(t *testing.T, bearer, id string) bool {
			t.Helper()
			frame, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
				"params": map[string]any{"name": "delete_knowledge_files_by_id", "arguments": map[string]any{"id": id}}})
			hr := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(frame))
			hr.Header.Set("Content-Type", "application/json")
			hr.Header.Set("Authorization", "Bearer "+bearer)
			hr.Header.Set("X-Org-Id", heldOrg)
			resp, err := p.app.Test(hr, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			var out struct {
				Result struct {
					IsError bool `json:"isError"`
				} `json:"result"`
				Error json.RawMessage `json:"error"`
			}
			b, _ := io.ReadAll(resp.Body)
			if err := json.Unmarshal(b, &out); err != nil {
				t.Fatalf("mcp answer %d %s: %v", resp.StatusCode, b, err)
			}
			return len(out.Error) == 0 && !out.Result.IsError
		}
		f := p.upload(t, p.alice, "ways/in.md", "# In\n\nEvery way in.")
		if byName(t, p.bob, f.ID) {
			t.Fatal("another member forgot alice's file by name over MCP")
		}
		if !p.visible(t, p.alice, f.ID) {
			t.Fatal("a refused forget by name removed the file")
		}
		if !byName(t, p.alice, f.ID) {
			t.Fatal("the uploader could not forget her file by name over MCP")
		}
		g := p.upload(t, p.alice, "ways/admin.md", "# Admin\n\nBy name, as an admin.")
		if !byName(t, p.boss, g.ID) {
			t.Fatal("an admin could not forget a file by name over MCP")
		}
	})
}

// TestAnAdminBitIsForTheOrgItWasMintedIn: a caller a sibling states — the call
// plane's dispatch, zip.Here, with no request under it — is held to the same
// rule, and an admin bit is authority only in the org it was minted for. A
// caller who administers one org, re-pointed at another, holds none there: the
// org strings are compared whole.
func TestAnAdminBitIsForTheOrgItWasMintedIn(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		p := filesAsPeople(t)
		forget := func(c zip.Caller, id string) error {
			ctx := principal.WithActing(zip.WithCaller(context.Background(), c), heldOrg)
			_, err := zip.Here[fileRef, forgotten](ctx, p.app, "delete_knowledge_files_by_id", &fileRef{ID: id})
			return err
		}
		refused := func(err error) bool {
			var he *zip.HTTPError
			return errors.As(err, &he) && he.Status == http.StatusForbidden
		}
		f := p.upload(t, p.alice, "stated/a.md", "# A\n\nStated callers.")
		for _, c := range []zip.Caller{
			{Org: "carolco", User: carolSub, OrgAdmin: true},
			{Org: "Hanzo", User: carolSub, OrgAdmin: true},
			{Org: "hanzo ", User: carolSub, OrgAdmin: true},
			{Org: "hanz", User: carolSub, OrgAdmin: true},
			{Org: heldOrg, User: bobSub},
		} {
			if err := forget(c, f.ID); !refused(err) {
				t.Fatalf("%+v forgot alice's file in %s: %v, want 403", c, heldOrg, err)
			}
		}
		if err := forget(zip.Caller{Org: heldOrg, User: aliceSub}, f.ID); err != nil {
			t.Fatalf("the uploader, stated, could not forget her file: %v", err)
		}
		g := p.upload(t, p.alice, "stated/b.md", "# B\n\nAn admin, stated.")
		if err := forget(zip.Caller{Org: heldOrg, User: bossSub, OrgAdmin: true}, g.ID); err != nil {
			t.Fatalf("an admin of hanzo, stated, could not forget a file in hanzo: %v", err)
		}
	})
}

// TestAnArchiveLeavesAnotherPersonsFileAsItIs: the files an archive holds are its
// uploader's, and its unpacking runs with nobody behind it to hold an admin
// bit. So a key the archive would write that is already another person's file
// is skipped — neither its object nor its record touched — and the rest land as
// the archive's uploader's.
func TestAnArchiveLeavesAnotherPersonsFileAsItIs(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		p := filesAsPeople(t)
		alices := p.upload(t, p.alice, "chat/bundle/docs/alpha.md", "# Alpha\n\nAlice's alpha.")
		p.settle(t, alices.ID)
		before := recorded(t, alices.ID)

		var buf bytes.Buffer
		zw := azip.NewWriter(&buf)
		for name, text := range map[string]string{
			"docs/alpha.md": "# Alpha\n\nBob's alpha.",
			"docs/beta.md":  "# Beta\n\nBob's beta.",
		} {
			w, _ := zw.Create(name)
			_, _ = w.Write([]byte(text))
		}
		_ = zw.Close()
		p.mem.put(heldOrg, "drive", "chat/bundle.zip", "application/zip", buf.Bytes())
		code, b := p.as(t, p.bob, http.MethodPost, "/v1/knowledge/files", map[string]any{"bucket": "drive", "key": "chat/bundle.zip"})
		if code != http.StatusOK {
			t.Fatalf("register bob's archive: %d %s", code, b)
		}
		if z := p.settle(t, decode[File](t, b).ID); z.Status != fileReady {
			t.Fatalf("bob's archive: %+v", z)
		}

		if obj, _ := p.mem.get(heldOrg, "drive", "chat/bundle/docs/alpha.md"); string(obj.data) != "# Alpha\n\nAlice's alpha." {
			t.Fatalf("bob's archive overwrote alice's object: %q", obj.data)
		}
		if got := recorded(t, alices.ID); got.etag != before.etag || got.Parent != "" {
			t.Fatalf("bob's archive changed alice's file: %+v", got)
		}
		if got := uploaderOf(t, alices.ID); got != aliceSub {
			t.Fatalf("alice's file now names %q", got)
		}
		beta := fileID("drive", "chat/bundle/docs/beta.md")
		p.settle(t, beta)
		if got := uploaderOf(t, beta); got != bobSub {
			t.Fatalf("a file bob's archive unpacked names %q, want bob", got)
		}
	})
}

// In a held org (hanzo, where every self-serve customer is a member) an admin of
// the org is one more stranger to what its members uploaded: it forgets neither a
// member's file nor one with no uploader. Its uploader forgets their own, and a
// SuperAdmin either.
func TestInAHeldOrgAnAdminIsAStranger(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		inHeldOrg(t)
		p := filesAsPeople(t)
		f := p.upload(t, p.alice, "alice/held.md", "# Held\n\nAlice's.")
		g := p.upload(t, p.alice, "alice/kept.md", "# Kept\n\nAlice's too.")
		if code := p.forget(t, p.boss, f.ID); code != http.StatusForbidden {
			t.Fatalf("an admin of hanzo forgot a member's file: %d, want 403", code)
		}
		if code := p.forget(t, p.bob, f.ID); code != http.StatusForbidden {
			t.Fatalf("another member forgot it: %d, want 403", code)
		}
		if code := p.forget(t, p.alice, f.ID); code != http.StatusOK {
			t.Fatalf("its uploader could not forget it: %d", code)
		}
		if code := p.forget(t, p.root, g.ID); code != http.StatusOK {
			t.Fatalf("a SuperAdmin could not forget a member's file: %d", code)
		}
	})
}

// In a held org (hanzo) a file is its uploader's to read: another member lists
// none of it, gets 404 on it by id, and the file-based search returns none of
// its passages — while a SuperAdmin reads it. In a customer's own org reads stay
// org-wide. (The owner set reads org-wide reads in hanzo were a cross-customer
// leak.)
func TestInAHeldOrgReadsAreTheUploadersOwn(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		inHeldOrg(t)
		p := filesAsPeople(t)
		f := p.upload(t, p.alice, "alice/secret.md", "# Secret\n\nAlice's confidential plan about zebras.")
		type listed struct {
			Files []File `json:"files"`
		}
		has := func(bearer string) bool {
			t.Helper()
			code, b := p.as(t, bearer, http.MethodGet, "/v1/knowledge/files", nil)
			if code != http.StatusOK {
				t.Fatalf("list: %d %s", code, b)
			}
			for _, x := range decode[listed](t, b).Files {
				if x.ID == f.ID {
					return true
				}
			}
			return false
		}
		if !has(p.alice) {
			t.Fatal("the uploader does not list her own file")
		}
		if has(p.bob) {
			t.Fatal("another member of hanzo lists the uploader's file")
		}
		if !has(p.root) {
			t.Fatal("a SuperAdmin does not list the file")
		}
		if code, _ := p.as(t, p.bob, http.MethodGet, "/v1/knowledge/files/"+f.ID, nil); code != http.StatusNotFound {
			t.Fatalf("another member opened the uploader's file: %d", code)
		}
		type searched struct {
			Passages []citedPassage `json:"passages"`
		}
		code, b := p.as(t, p.bob, http.MethodPost, "/v1/knowledge/files/search", map[string]any{"query": "zebras confidential plan"})
		if code != http.StatusOK {
			t.Fatalf("search: %d %s", code, b)
		}
		if n := len(decode[searched](t, b).Passages); n != 0 {
			t.Fatalf("another member's search returned %d passages of the uploader's file", n)
		}
	})
}
