package sandbox

// quota_test.go — a paying account holds the quota staff set for its org or its
// plan, counted over every paid sandbox it holds, and the default when commerce
// cannot say.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/account"
	"github.com/zap-proto/zip"
)

// paid puts n sandboxes of org paid by payer into st.
func paid(t *testing.T, st *Store, org, payer, status string, n int) {
	t.Helper()
	for i := range n {
		if err := st.Put(context.Background(), Sandbox{
			ID: fmt.Sprintf("m_%s_%s_%03d", strings.ReplaceAll(payer, "/", "_"), status, i), Org: org, Class: "exec", Status: status,
			Payer: payer, CreatedAt: 1, LastUsedAt: 1,
		}); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
}

// answering makes commerce answer each account's quota from set, and refuse with
// down while down is set; it counts the asks.
func answering(t *testing.T, set map[string]int64) (asks *int, down *error) {
	t.Helper()
	n, fail := 0, error(nil)
	prior := askQuota
	t.Cleanup(func() { askQuota = prior })
	askQuota = func(_ context.Context, org, acct string) (int64, error) {
		n++
		if fail != nil {
			return 0, fail
		}
		if q, ok := set[org+"|"+acct]; ok {
			return q, nil
		}
		return 100, nil
	}
	return &n, &fail
}

// An org's count is every paid sandbox it holds, whoever paid — a SuperAdmin's
// lease inside it included — past any page size; free leases, ended ones and
// another org's are not in it. In the signup org each person's is their own.
func TestTheQuotaCountsEveryPaidSandboxAnOrgHolds(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		st := memStore(t)
		ctx := context.Background()
		paid(t, st, "acme", "acme", "running", 240)
		paid(t, st, "acme", "admin", "pending", 7)
		paid(t, st, "acme", "acme", "ended", 5)
		paid(t, st, "acme", "", "running", 4)
		paid(t, st, "globex", "globex", "running", 9)
		if n, err := st.LivePaid(ctx, "acme", "acme"); err != nil || n != 247 {
			t.Fatalf("acme counts %d (%v), want 247: its own and the SuperAdmin's, never the free, the ended or globex's", n, err)
		}

		signup := memStore(t)
		paid(t, signup, account.SignupOrg, "hanzo/sam", "running", 3)
		paid(t, signup, account.SignupOrg, "hanzo/ada", "running", 2)
		if n, err := signup.LivePaid(ctx, account.SignupOrg, "hanzo/sam"); err != nil || n != 3 {
			t.Fatalf("sam counts %d (%v), want his own 3", n, err)
		}
		if got := counted(account.SignupOrg, account.Person(account.SignupOrg, "sam")); got != "hanzo/sam" {
			t.Fatalf("a person in the signup org counts against %q", got)
		}
		if got := counted("acme", account.Org("admin")); got != "acme" {
			t.Fatalf("a SuperAdmin leasing in acme counts against %q, want acme", got)
		}
	})
}

// A paid lease is held to the quota commerce resolves for its account: the value
// staff set refuses at it and admits under it, -1 admits past any count, and a
// free lease is never counted.
func TestAPaidLeaseIsHeldToTheQuotaStaffSet(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		_, s := owning(t)
		st, err := storeFor(s, "acme")
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		set := map[string]int64{"acme|acme": 3}
		answering(t, set)
		paid(t, st, "acme", "acme", "running", 2)
		if err := within(s, ctx, st, "acme", account.Org("acme")); err != nil {
			t.Fatalf("a lease under a quota of 3 was refused: %v", err)
		}
		paid(t, st, "acme", "acme", "pending", 1)
		err = within(s, ctx, st, "acme", account.Org("acme"))
		var he *zip.HTTPError
		if !errors.As(err, &he) || he.Status != http.StatusTooManyRequests || !strings.Contains(he.Error(), "quota of 3") {
			t.Fatalf("a lease past a quota of 3: %v, want 429 naming it", err)
		}
		if err := within(s, ctx, st, "acme", account.Account{}); err != nil {
			t.Fatalf("a free lease was counted against a paid quota: %v", err)
		}

		set["acme|acme"] = -1
		s.State.quotas = &quotas{}
		paid(t, st, "acme", "admin", "running", 150)
		if err := within(s, ctx, st, "acme", account.Org("acme")); err != nil {
			t.Fatalf("no bound refused a lease: %v", err)
		}
	})
}

// A SuperAdmin leasing inside an org pays from their own wallet, and the lease
// counts against the org's quota: with the org full, the SuperAdmin is refused
// there too.
func TestASuperAdminsLeaseInAnOrgCountsAgainstThatOrg(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, s := owning(t)
		w := reading(t)
		answering(t, map[string]int64{"acme|acme": 2})
		st, err := storeFor(s, "acme")
		if err != nil {
			t.Fatal(err)
		}
		lease := func() (int, []byte) {
			r := httptest.NewRequest(http.MethodPost, "/v1/sandbox", strings.NewReader(`{"class":"exec"}`))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("X-Org-Id", "acme")
			r.Header.Set("X-User-Owner", "admin")
			r.Header.Set("X-User-Id", "z")
			r.Header.Set("X-User-Name", "z")
			r.Header.Set("X-User-IsAdmin", "true")
			resp, err := app.Test(r, zip.TestConfig{Timeout: 30 * time.Second})
			if err != nil {
				t.Fatalf("lease: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			b, _ := io.ReadAll(resp.Body)
			return resp.StatusCode, b
		}

		paid(t, st, "acme", "acme", "running", 1)
		if code, body := lease(); code != http.StatusCreated {
			t.Fatalf("the SuperAdmin's lease under acme's quota = %d %s", code, body)
		}
		if books, _ := w.last(); books != "admin" {
			t.Fatalf("the SuperAdmin's lease was weighed on %q's books, want their own (admin)", books)
		}
		if n, err := st.LivePaid(context.Background(), "acme", "acme"); err != nil || n != 2 {
			t.Fatalf("acme holds %d (%v) after the SuperAdmin's lease, want 2", n, err)
		}
		if code, body := lease(); code != http.StatusTooManyRequests || !strings.Contains(string(body), "acme has 2") {
			t.Fatalf("a SuperAdmin past acme's quota = %d %s, want 429 on acme's count", code, body)
		}
	})
}

// When commerce cannot answer, the last answer holds, and with none the default
// does: the quota never refuses for want of an answer, and the answer is reused
// inside its TTL rather than asked per lease.
func TestTheQuotaFailsOpenToTheLastAnswerOrTheDefault(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		_, s := owning(t)
		st, err := storeFor(s, "acme")
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		asks, down := answering(t, map[string]int64{"acme|acme": 5})
		if q := s.State.quotas.of(ctx, s.Log, "acme", "acme"); q != 5 {
			t.Fatalf("quota %d, want the 5 commerce answered", q)
		}
		s.State.quotas.of(ctx, s.Log, "acme", "acme")
		if *asks != 1 {
			t.Fatalf("commerce was asked %d times inside the TTL, want once", *asks)
		}

		*down = errors.New("commerce unreachable")
		s.State.quotas.mu.Lock()
		s.State.quotas.m["acme\x00acme"] = quotaAt{n: 5, at: time.Now().Add(-time.Hour)}
		s.State.quotas.mu.Unlock()
		if q := s.State.quotas.of(ctx, s.Log, "acme", "acme"); q != 5 {
			t.Fatalf("commerce down with a last answer: %d, want the last answer 5", q)
		}

		fresh := quotas{}
		if q := fresh.of(ctx, s.Log, "globex", "globex"); q != 100 {
			t.Fatalf("commerce down with no answer: %d, want the default 100", q)
		}
		s.State.quotas = &quotas{}
		paid(t, st, "acme", "acme", "running", 99)
		if err := within(s, ctx, st, "acme", account.Org("acme")); err != nil {
			t.Fatalf("commerce down, 99 running under the default: %v", err)
		}
		paid(t, st, "acme", "acme", "pending", 1)
		if err := within(s, ctx, st, "acme", account.Org("acme")); err == nil {
			t.Fatal("commerce down, the default of 100 did not hold")
		}
	})
}

// The account reads its quota as staff set it, and can no longer set its own; the
// plane answers the org's for admin.hanzo.ai and refuses the signup org's.
func TestTheAccountReadsItsQuotaAndStaffSetIt(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, s := owning(t)
		reading(t)
		answering(t, map[string]int64{"acme|acme": 40})
		st, err := storeFor(s, "acme")
		if err != nil {
			t.Fatal(err)
		}
		paid(t, st, "acme", "acme", "running", 3)

		code, body := as(t, app, alice, http.MethodGet, "/v1/sandbox/quota", "")
		var got client.Quota
		if code != http.StatusOK || json.Unmarshal(body, &got) != nil || got != (client.Quota{Account: "acme", Running: 3, Quota: 40}) {
			t.Fatalf("GET quota = %d %s", code, body)
		}
		if code, body := as(t, app, alice, http.MethodPut, "/v1/sandbox/quota", `{"sandboxes":1000}`); code == http.StatusOK {
			t.Fatalf("the account set its own quota: %d %s", code, body)
		}

		q, err := planeQuota(zip.WithCaller(context.Background(), zip.Caller{Org: "acme"}), nil)
		if err != nil || *q != (client.Quota{Account: "acme", Running: 3, Quota: 40}) {
			t.Fatalf("plane quota = %+v, %v", q, err)
		}
		if _, err := planeQuota(zip.WithCaller(context.Background(), zip.Caller{Org: account.SignupOrg}), nil); err == nil {
			t.Fatal("the plane answered a quota for the signup org, which holds none of its own")
		}
	})
}
