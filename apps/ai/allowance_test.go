// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ai

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/tenant"
	aictl "github.com/hanzoai/ai/controllers"
	aiobject "github.com/hanzoai/ai/object"
	"github.com/zap-proto/zip"
)

// A FREE CALL IS COUNTED AS THE GATE ADMITS IT, FOR THE PERSON WHO ASKED.
//
// Asking is taking: the gate's hook spends one of the person's calls before any model
// is reached (the ai module's SpentFunc), so a loop retrying a failing model spends
// its own allowance and calls in flight cannot all slip past the last unit. The record of a
// served call is money alone.

// The recorder settles money and nothing else; a call the plan or a free cap covered
// draws no wallet at all.
func TestARecordIsMoneyAlone(t *testing.T) {
	for _, c := range []struct {
		name  string
		event aiobject.UsageEvent
		paid  bool
	}{
		{"a priced call debits", aiobject.UsageEvent{Namespace: "acme", Subject: "acme", USD: "0.00132"}, true},
		{"a cash-only call debits", aiobject.UsageEvent{Namespace: "acme", Subject: "acme", USD: "0.042", Cash: true}, true},
		{"a covered call draws nothing", aiobject.UsageEvent{Namespace: "acme", Subject: "acme", USD: "0.021", Plan: true}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			var got []aiobject.UsageEvent
			settle := record(nil, func(_ context.Context, u aiobject.UsageEvent) error { got = append(got, u); return nil })
			if err := settle(context.Background(), c.event); err != nil {
				t.Fatal(err)
			}
			if (len(got) == 1) != c.paid || (c.paid && got[0].Cash != c.event.Cash) {
				t.Fatalf("debits %+v, want paid=%v carrying cash=%v", got, c.paid, c.event.Cash)
			}
		})
	}
}

// THE POSITION IS STRUCTURAL, AND THIS IS WHAT HOLDS IT THERE. The one take in this
// file is admit, and admit is what the gate asks before a call. A future edit that
// moves the count back behind the answer has to move it out of admit.
func TestTheGateCountsAtAdmission(t *testing.T) {
	src, err := os.ReadFile("ai.go")
	if err != nil {
		t.Fatalf("read ai.go: %v", err)
	}
	var code []string
	for line := range strings.SplitSeq(string(src), "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		code = append(code, line)
	}
	body := strings.Join(code, "\n")
	if n := strings.Count(body, "allowancepeer.AllowanceTake"); n != 1 {
		t.Fatalf("allowancepeer.AllowanceTake is called %d times; exactly one call site, admit, may count", n)
	}
	if !strings.Contains(body[strings.Index(body, "func admit("):], "allowancepeer.AllowanceTake") {
		t.Error("the one take must live in admit")
	}
	if !strings.Contains(body, "aiobject.SetSpent(spent(admit, give))") {
		t.Error("the gate's allowance hook must be admit, or a free call is never counted")
	}
	if strings.Contains(body, "allowancepeer.AllowanceRead") {
		t.Error("the gate reads the allowance without counting it — the ceiling would never stop a loop")
	}
}

// The count is the PERSON's and the ceiling the wallet's: a member of a pooled org is
// counted alone and held to the plan the pool pays for, a key counts against the user
// it was minted for, a member acting in another org is counted at home, and the
// public lane counts its visitor.
func TestTheCountIsThePersonsAndTheCeilingTheWallets(t *testing.T) {
	ctx := zip.WithCaller(context.Background(), zip.Caller{Owner: "acme", Org: "acme", User: "0b6f", Name: "bob"})
	if org, in := allowanceIn(ctx, "acme", "acme"); org != "acme" || in.Subject != "acme/bob" || in.Payer != "acme" {
		t.Fatalf("a pooled member: %s %+v, want counted as acme/bob under the acme wallet", org, in)
	}
	away := zip.WithCaller(context.Background(), zip.Caller{Org: "webby-ai", Owner: "joshuafl369", User: "8e4e1ba8"})
	if org, in := allowanceIn(away, "webby-ai", "webby-ai"); org != "webby-ai" || in.Subject != "joshuafl369/8e4e1ba8" {
		t.Fatalf("a member acting in another org: %s %+v, want counted at their home org", org, in)
	}
	if org, in := allowanceIn(ctx, "v-1", tenant.Public); org != tenant.Public || in.Subject != "v-1" || in.Payer != "" {
		t.Fatalf("the public lane: %s %+v, want the visitor alone", org, in)
	}
	if org, in := allowanceIn(context.Background(), "acme/ann", "acme"); org != "acme" || in.Subject != "acme/ann" || in.Payer != "" {
		t.Fatalf("no caller on the context: %s %+v, want the wallet's own count", org, in)
	}
	// A sibling's completion on the deployment's token, for a person in the signup
	// org: held to that person's wallet there, never the deployment's.
	bought := zip.WithCaller(context.Background(), zip.Caller{Owner: "admin", Org: "admin", User: "hanzo/ann"})
	if org, in := allowanceIn(bought, "admin", "admin"); org != "hanzo" || in.Subject != "hanzo/ann" || in.Payer != "hanzo/ann" {
		t.Fatalf("a bought completion: %s %+v, want hanzo/ann counted under their own wallet in hanzo", org, in)
	}
	// A member whose caller User is "<home>/<name>" acting in a paying org: held to
	// that org's wallet, never redirected to their unfunded personal wallet in home.
	member := zip.WithCaller(context.Background(), zip.Caller{Owner: "hanzo", Org: "acme", User: "hanzo/ann"})
	if org, in := allowanceIn(member, "acme", "acme"); org != "acme" || in.Subject != "hanzo/ann" || in.Payer != "acme" {
		t.Fatalf("a member acting in a paying org: %s %+v, want hanzo/ann counted under acme's wallet", org, in)
	}
}

// THE GATE HEARS WHICH WINDOW REFUSED AND WHEN IT REFILLS, so the refusal a free
// caller reads names the hour, the session or the day and the instant it opens
// again. And WHO FAILS OPEN is decided here: an unanswerable ask about the public
// lane is spent, one about a tenant we can name is an error the gate lets through.
func TestTheGateHearsTheStandingAndWhoFailsOpen(t *testing.T) {
	resets := time.Date(2026, 9, 28, 16, 0, 0, 0, time.UTC)
	read := spent(func(context.Context, string, string) (*client.Allowance, error) {
		return &client.Allowance{Plan: "free", Window: "hour", Limit: 10, Used: 10, Spent: true, Resets: resets.Unix()}, nil
	}, nil)
	st, err := read(context.Background(), "acme/ann", "acme")
	if err != nil || !st.Spent || st.Window != "hour" || st.Limit != 10 || st.Used != 10 || !st.Resets.Equal(resets) {
		t.Fatalf("standing = %+v, %v; want spent on this hour's 10, refilling at %s", st, err, resets)
	}
	reopens := time.Date(2026, 11, 3, 2, 40, 13, 0, time.UTC)
	session := spent(func(context.Context, string, string) (*client.Allowance, error) {
		return &client.Allowance{Plan: "free", Window: "session", Limit: 3, Used: 3, Spent: true, Resets: reopens.Unix()}, nil
	}, nil)
	if st, err := session(context.Background(), "acme/ann", "acme"); err != nil || !st.Spent || st.Window != "session" || !st.Resets.Equal(reopens) {
		t.Fatalf("standing = %+v, %v; want spent on the session, reopening at %s", st, err, reopens)
	}

	down := spent(func(context.Context, string, string) (*client.Allowance, error) {
		return nil, errors.New("allowance is rolling")
	}, nil)
	if st, err := down(context.Background(), "v-1", tenant.Public); err != nil || !st.Spent {
		t.Fatalf("public lane with no counter: %+v, %v; want spent — a stranger gets no benefit of the doubt", st, err)
	}
	if st, err := down(context.Background(), "acme/ann", "acme"); err == nil || st.Spent {
		t.Fatalf("named tenant with no counter: %+v, %v; want the error, which the gate lets through", st, err)
	}
}

// THE POOL CROSSES AS ITS STATE. No count of accounts leaves this process, and a
// refill is stated only where it is the pool's: exhausted, or busy for less than
// the minute busy promises.
func TestThePoolCrossesAsItsState(t *testing.T) {
	now := time.Date(2026, 9, 28, 15, 0, 0, 0, time.UTC)
	midnight := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		in   aictl.Pool
		want client.Pool
	}{
		{aictl.Pool{State: aictl.PoolAvailable, Keys: 3, Ready: 3}, client.Pool{State: "available"}},
		{aictl.Pool{State: aictl.PoolBusy, Keys: 3, Ready: 2, Resets: midnight}, client.Pool{State: "busy"}},
		{aictl.Pool{State: aictl.PoolBusy, Keys: 3, Ready: 0, Resets: now.Add(40 * time.Second)}, client.Pool{State: "busy", Resets: now.Add(40 * time.Second).Unix()}},
		{aictl.Pool{State: aictl.PoolExhausted, Keys: 3, Ready: 0, Resets: midnight}, client.Pool{State: "exhausted", Resets: midnight.Unix()}},
	} {
		if got := poolOf(c.in, now); *got != c.want {
			t.Errorf("poolOf(%+v) = %+v, want %+v", c.in, *got, c.want)
		}
	}
}

// A call admitted against a ceiling carries the give that hands its unit back, naming
// the subject, when it was taken and the session its take answered; a refused or
// unbounded call took nothing and carries none.
func TestAnAdmittedCallCarriesItsGive(t *testing.T) {
	var gave []string
	var when time.Time
	var session int64
	back := func(_ context.Context, subject, namespace string, at time.Time, start int64) {
		gave = append(gave, subject+"@"+namespace)
		when, session = at, start
	}
	answer := func(a *client.Allowance) aiobject.Standing {
		st, err := spent(func(context.Context, string, string) (*client.Allowance, error) { return a, nil }, back)(context.Background(), "acme/ann", "acme")
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	before := time.Now()
	st := answer(&client.Allowance{Window: "session", Limit: 3, Used: 1, Start: 1793625553})
	if st.Release == nil {
		t.Fatal("an admitted call carries no give")
	}
	st.Release()
	if len(gave) != 1 || gave[0] != "acme/ann@acme" || when.Before(before.Add(-time.Second)) || session != 1793625553 {
		t.Fatalf("gave %v at %v to the session of %d, want the one the take answered", gave, when, session)
	}
	if st := answer(&client.Allowance{Window: "session", Limit: 3, Used: 3, Spent: true}); st.Release != nil {
		t.Error("a refused call carries a give")
	}
	if st := answer(&client.Allowance{}); st.Release != nil {
		t.Error("an unbounded call carries a give")
	}
}
