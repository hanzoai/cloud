package train

// money_test.go — a job Hanzo runs is metered by the clock at every report and
// billed by the device-second inside what is held for it: caps and balances stop
// it, by cancel, before it runs past what is held, a crash is charged through its
// last report, an ended job releases what it held, a create its wallet cannot
// fund is refused, and storage is billed by the GB-month for as long as it is
// kept.

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/audit"
)

// hanzoJob is a job Hanzo's executor runs — it names no machines — bounded by
// resources, and by a budget of $10,000 when resources name none: every job
// Hanzo runs takes one.
func hanzoJob(resources map[string]any) map[string]any {
	j := job()
	if _, ok := resources["budget"]; !ok {
		resources["budget"] = 1_000_000
	}
	j["resources"] = resources
	return j
}

// executor is the rig's Hanzo executor holding one task: it claims under the
// platform credential and reports acting in the task's org.
type executor struct {
	r                *rig
	org, job, task   string
	lease            string
	devices, held    int64
	report_, devName string
}

// claimPlatform claims as Hanzo's executor on devices and answers the task held,
// or nil when there was none to hold (204).
func (r *rig) claimPlatform(devices ...string) *executor {
	r.t.Helper()
	code, a := r.as("POST", "/v1/train/jobs/claim", "hanzo", true, platformClaim(devices...))
	if code == 204 {
		return nil
	}
	if code != 200 {
		r.t.Fatalf("platform claim: %d %v", code, a)
	}
	tk := a["task"].(map[string]any)
	x := &executor{r: r, org: str(a, "org"), job: str(tk, "job"), task: str(tk, "id"), lease: str(tk, "lease"), devName: str(tk, "device")}
	x.devices = int64(tk["devices"].(float64))
	x.held = int64(tk["held"].(float64))
	x.report_ = str(tk, "report")
	return x
}

// claimed is claimPlatform for a test that needs the claim answered.
func (r *rig) claimed(devices ...string) *executor {
	r.t.Helper()
	x := r.claimPlatform(devices...)
	if x == nil {
		r.t.Fatal("platform claim: no task answered")
	}
	return x
}

// report sends the task's events; the cloud meters the task by its clock.
func (x *executor) report(events ...map[string]any) map[string]any {
	x.r.t.Helper()
	if events == nil {
		events = []map[string]any{}
	}
	out := x.r.must(200, "POST", "/v1/train/jobs/"+x.job+"/events", x.org,
		map[string]any{"task": x.task, "lease": x.lease, "events": events})
	if h, ok := out["held"].(float64); ok {
		x.held = int64(h)
	}
	return out
}

// store registers an output, puts its bytes and reports it stored.
func (x *executor) store(kind, sum string, size int64) {
	x.r.t.Helper()
	x.r.must(200, "POST", "/v1/train/jobs/"+x.job+"/artifacts", x.org,
		map[string]any{"task": x.task, "lease": x.lease, "kind": kind, "sha256": sum, "size": size})
	x.r.objs.store(x.org+"/"+sum, sum)
	x.report(map[string]any{"type": "artifact", "sha256": sum})
}

// gpu is what n device-seconds of the kai GPU class cost at the compiled floor:
// the rig has no meter authority beside it.
func gpu(n int64) int64 { return cost(gpuHourFloor, n) }

// owed is what the payer still has held beyond what it was charged.
func (r *rig) owed(org string) int64 {
	r.t.Helper()
	n, err := r.store(org).outstanding(context.Background(), "org:"+org)
	if err != nil {
		r.t.Fatal(err)
	}
	return n
}

// until reports every interval until an answer says cancel, and answers the
// seconds since the claim at which it did, with that answer.
func (x *executor) until(c *clock, limit int64) (int64, map[string]any) {
	x.r.t.Helper()
	for s := reportEvery; s <= limit; s += reportEvery {
		c.tick(reportEvery)
		if out := x.report(); out["cancel"] == true {
			return s, out
		}
	}
	x.r.t.Fatalf("no cancel within %d seconds", limit)
	return 0, nil
}

// stoppedAt asserts a job ended stopped for reason, metered at seconds and
// charged exactly what they cost, holding nothing.
func (r *rig) stoppedAt(id, reason string, seconds int64) {
	r.t.Helper()
	g := r.must(200, "GET", "/v1/train/jobs/"+id, "acme", nil)
	usage := g["usage"].(map[string]any)
	if str(g, "status") != stopped || str(g, "error") != reason || int64(usage["seconds"].(float64)) != seconds || usage["held"].(float64) != 0 {
		r.t.Fatalf("job: %v", g)
	}
	if got := r.books.spent("compute"); got != gpu(seconds) {
		r.t.Fatalf("charged %d µ$, want exactly %d for %d metered seconds", got, gpu(seconds), seconds)
	}
	if r.owed("acme") != 0 {
		r.t.Fatalf("a stopped job still holds %d µ$", r.owed("acme"))
	}
}

func TestAJobPastItsBudgetIsCancelledInsideItAndChargedWhatRan(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := mount(t, "")
		c := pin(t)
		j := r.must(200, "POST", "/v1/train/jobs", "acme", hanzoJob(map[string]any{"budget": 50}))
		x := r.claimed("cuda")
		if x.org != "acme" || x.job != str(j, "id") || x.devices != 1 || x.held != windowSeconds || x.report_ != "20" || x.devName != "cuda" {
			t.Fatalf("claim: %+v", x)
		}
		at, out := x.until(c, 3600)
		if str(out, "status") != stopped || x.held-at >= stopBelow || at > x.held {
			t.Fatalf("cancelled at %d with %d held: %v", at, x.held, out)
		}
		r.stoppedAt(x.job, stopBudget, at)
		if got := r.books.spent("compute"); got > 50*10_000 {
			t.Fatalf("charged %d µ$ against a 50¢ budget", got)
		}
		for _, d := range r.books.of("compute") {
			if d.payer != "org:acme" || d.usage.Model != "kai-gpu-hour" || !strings.HasPrefix(d.usage.Ref, "trc_"+x.task+"_") {
				t.Fatalf("debit: %+v", d)
			}
		}
		// Its executor reports again before it has killed its trainer: told to cancel,
		// and charged nothing more.
		c.tick(reportEvery)
		if out := x.report(); out["cancel"] != true || str(out, "status") != stopped {
			t.Fatalf("a report after the stop: %v", out)
		}
		r.stoppedAt(x.job, stopBudget, at)
	})
}

func TestAJobIsCancelledBeforeItsMaxSecondsAndNeverChargedPastThem(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := mount(t, "")
		c := pin(t)
		r.must(200, "POST", "/v1/train/jobs", "acme", hanzoJob(map[string]any{"max_seconds": 400}))
		x := r.claimed("cuda")
		at, _ := x.until(c, 3600)
		if x.held != 400 || at > 400 || 400-at >= stopBelow {
			t.Fatalf("cancelled at %d, held %d: max_seconds 400", at, x.held)
		}
		r.stoppedAt(x.job, stopSeconds, at)
		// An executor that ran on past the cancel is charged nothing for it.
		c.tick(10_000)
		if code, _ := r.as("POST", "/v1/train/jobs/"+x.job+"/events", x.org, true, map[string]any{"task": x.task, "lease": x.lease, "events": []any{}}); code != 200 {
			t.Fatalf("a late report: %d", code)
		}
		r.stoppedAt(x.job, stopSeconds, at)
	})
}

func TestMaxSecondsStopsAJobOnTheOrgsOwnMachinesUncharged(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := mount(t, "")
		c := pin(t)
		j := job("dgx")
		j["resources"].(map[string]any)["max_seconds"] = 200
		id := str(r.must(200, "POST", "/v1/train/jobs", "acme", j), "id")
		tk := r.must(200, "POST", "/v1/train/jobs/claim", "acme", claimBody("dgx"))["task"].(map[string]any)
		if tk["held"].(float64) != 200 {
			t.Fatalf("held: %v", tk)
		}
		var at int64
		for s := reportEvery; at == 0 && s <= 400; s += reportEvery {
			c.tick(reportEvery)
			out := r.must(200, "POST", "/v1/train/jobs/"+id+"/events", "acme", map[string]any{"task": str(tk, "id"), "lease": str(tk, "lease"), "events": []any{}})
			if out["cancel"] == true {
				at = s
			}
		}
		if at == 0 || at > 200 || 200-at >= stopBelow {
			t.Fatalf("cancelled at %d against max_seconds 200", at)
		}
		g := r.must(200, "GET", "/v1/train/jobs/"+id, "acme", nil)
		if str(g, "status") != stopped || str(g, "error") != stopSeconds || int64(g["usage"].(map[string]any)["seconds"].(float64)) != at {
			t.Fatalf("job: %v", g)
		}
		if n := len(r.books.debits); n != 0 || len(r.books.asked) != 0 {
			t.Fatalf("the org's own machine was priced: %d debits, %v asked", n, r.books.asked)
		}
	})
}

func TestEachReportIsMeteredByTheClockOnEveryDevice(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := mount(t, "")
		c := pin(t)
		r.must(200, "POST", "/v1/train/jobs", "acme", hanzoJob(map[string]any{}))
		x := r.claimed("cuda", "cuda")
		if x.devices != 2 || x.held != 2*windowSeconds {
			t.Fatalf("claim: %+v", x)
		}
		c.tick(reportEvery)
		x.report()
		if got := r.books.spent("compute"); got != gpu(2*reportEvery) {
			t.Fatalf("charged %d µ$ for %d s on two devices, want %d", got, reportEvery, gpu(2*reportEvery))
		}
		// The same instant again adds nothing.
		x.report()
		if got := r.books.spent("compute"); got != gpu(2*reportEvery) {
			t.Fatalf("a resend was charged: %d µ$", got)
		}
		// A report late past what is held is charged what is held, then the next
		// window is held.
		c.tick(windowSeconds)
		x.report()
		if got := r.books.spent("compute"); got != gpu(2*windowSeconds) {
			t.Fatalf("a late report charged %d µ$, want %d: what was held", got, gpu(2*windowSeconds))
		}
		if x.held != 4*windowSeconds {
			t.Fatalf("held %d after the next window, want %d", x.held, 4*windowSeconds)
		}
		g := r.must(200, "GET", "/v1/train/jobs/"+x.job, "acme", nil)
		if u := g["usage"].(map[string]any); int64(u["seconds"].(float64)) != 2*windowSeconds || int64(u["held"].(float64)) != 2*windowSeconds {
			t.Fatalf("usage: %v", u)
		}
	})
}

func TestABalanceThatRunsOutMidJobStopsItInsideWhatIsHeld(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := mount(t, "")
		c := pin(t)
		r.books.balance = 10_000_000
		r.must(200, "POST", "/v1/train/jobs", "acme", hanzoJob(map[string]any{}))
		x := r.claimed("cuda")
		for range 6 {
			c.tick(reportEvery)
			x.report()
		}
		// Another surface spends the wallet down to what the job already holds.
		r.books.mu.Lock()
		r.books.balance = r.books.spentLocked("") + gpu(windowSeconds) - gpu(6*reportEvery) + 1
		r.books.mu.Unlock()
		at, _ := x.until(c, 3600)
		at += 6 * reportEvery
		if x.held != windowSeconds || windowSeconds-at >= stopBelow {
			t.Fatalf("cancelled at %d, held %d", at, x.held)
		}
		r.stoppedAt(x.job, stopFunds, at)
		if got := r.books.spent(""); got > r.books.balance {
			t.Fatalf("the wallet was overrun: %d spent of %d", got, r.books.balance)
		}
	})
}

func TestABalanceThatCannotBeReadStopsAJobOnlyOnceItsHoldRunsDown(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := mount(t, "")
		c := pin(t)
		r.must(200, "POST", "/v1/train/jobs", "acme", hanzoJob(map[string]any{}))
		x := r.claimed("cuda")
		r.books.unread = true
		for s := reportEvery; windowSeconds-s >= stopBelow; s += reportEvery {
			c.tick(reportEvery)
			if out := x.report(); out["cancel"] == true {
				t.Fatalf("cancelled at %d on a moment's outage: %v", s, out)
			}
		}
		c.tick(reportEvery)
		if out := x.report(); out["cancel"] != true {
			t.Fatalf("not cancelled with less than %d s held: %v", stopBelow, out)
		}
		r.stoppedAt(x.job, stopUnread, windowSeconds-stopBelow+reportEvery)
	})
}

func TestACrashIsChargedThroughItsLastReport(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := mount(t, "")
		c := pin(t)
		r.must(200, "POST", "/v1/train/jobs", "acme", hanzoJob(map[string]any{}))
		x := r.claimed("cuda")
		c.tick(reportEvery)
		x.report()
		c.tick(reportEvery)
		x.report()
		ran := 2 * reportEvery
		c.tick(300)
		if got, want := r.owed("acme"), gpu(windowSeconds)-gpu(ran); got != want {
			t.Fatalf("held while its lease lives: %d µ$, want %d", got, want)
		}
		c.tick(r.st.ttl)
		g := r.must(200, "GET", "/v1/train/jobs/"+x.job, "acme", nil)
		if str(g, "status") != failed || !strings.HasPrefix(str(g, "error"), "executor_lost") || g["usage"].(map[string]any)["held"].(float64) != 0 {
			t.Fatalf("job: %v", g)
		}
		if got := r.books.spent("compute"); got != gpu(ran) {
			t.Fatalf("charged %d µ$, want %d for the %d seconds its reports metered", got, gpu(ran), ran)
		}
		if r.owed("acme") != 0 {
			t.Fatalf("a lapsed lease still holds %d µ$", r.owed("acme"))
		}
		if code, m := r.as("POST", "/v1/train/jobs/"+x.job+"/events", "acme", true, map[string]any{"task": x.task, "lease": x.lease, "events": []any{}}); code != 409 || str(m, "code") != "lease_lost" {
			t.Fatalf("a report after the lapse: %d %v", code, m)
		}
		if got := r.books.spent("compute"); got != gpu(ran) {
			t.Fatalf("a late report was charged: %d µ$", got)
		}
	})
}

func TestAnEndedJobReleasesWhatItHeld(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		sum := strings.Repeat("ab", 32)
		res := map[string]any{"type": "result", "verdict": "rejected", "base": map[string]any{"sha256": baseSHA},
			"outputs": []any{map[string]any{"kind": "capability", "sha256": sum}}}
		for _, c := range []struct {
			name, want string
			end        func(r *rig, x *executor)
		}{
			{"failed", failed, func(r *rig, x *executor) {
				x.report(map[string]any{"type": "status", "status": "failed", "error": "nan loss"})
			}},
			{"rejected", rejected, func(r *rig, x *executor) {
				x.store("capability", sum, 1024)
				x.report(res)
			}},
			{"cancelled", cancelled, func(r *rig, x *executor) {
				x.report()
				r.must(200, "POST", "/v1/train/jobs/"+x.job+"/cancel", "acme", nil)
				if out := x.report(); out["cancel"] != true {
					t.Fatalf("cancel was not said: %v", out)
				}
			}},
		} {
			t.Run(c.name, func(t *testing.T) {
				r := mount(t, "")
				clk := pin(t)
				r.must(200, "POST", "/v1/train/jobs", "acme", hanzoJob(map[string]any{}))
				x := r.claimed("cuda")
				if r.owed("acme") != gpu(windowSeconds) {
					t.Fatalf("held at claim: %d", r.owed("acme"))
				}
				clk.tick(reportEvery)
				c.end(r, x)
				g := r.must(200, "GET", "/v1/train/jobs/"+x.job, "acme", nil)
				if str(g, "status") != c.want || r.owed("acme") != 0 {
					t.Fatalf("%s: %v, still holding %d µ$", c.name, g, r.owed("acme"))
				}
				if got := r.books.spent("compute"); got != gpu(reportEvery) {
					t.Fatalf("charged %d µ$, want %d", got, gpu(reportEvery))
				}
			})
		}
	})
}

func TestACreateItsWalletCannotHoldIsRefused(t *testing.T) {
	for _, c := range []struct {
		name   string
		set    func(b *books)
		status int
		code   string
	}{
		{"out of funds", func(b *books) { b.balance = gpu(windowSeconds) - 1 }, 402, "insufficient_balance"},
		{"a spend cap", func(b *books) { b.capped = true }, 402, "spend_cap_exceeded"},
		{"an unreadable balance", func(b *books) { b.unread = true }, 503, "balance_unavailable"},
	} {
		t.Run(c.name, func(t *testing.T) {
			sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
				r := mount(t, "")
				c.set(r.books)
				code, m := r.do("POST", "/v1/train/jobs", "acme", hanzoJob(map[string]any{}))
				e, _ := m["error"].(map[string]any)
				if code != c.status || str(e, "code") != c.code {
					t.Fatalf("create: %d %v", code, m)
				}
				if rs, _ := r.store("acme").jobs(context.Background(), "", 10); len(rs) != 0 {
					t.Fatalf("a refused create stored a job: %v", rs)
				}
				// The org's own machines are its hardware: nothing to hold.
				if code, _ := r.do("POST", "/v1/train/jobs", "acme", job("dgx")); code != 200 {
					t.Fatalf("a job on the org's machine: %d", code)
				}
			})
		})
	}
}

func TestAFirstWindowIsSizedByTheJobsOwnBounds(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := mount(t, "")
		r.books.balance = 50_000
		if code, m := r.do("POST", "/v1/train/jobs", "acme", hanzoJob(map[string]any{"budget": 5})); code != 200 {
			t.Fatalf("a 5¢ budget on a 5¢ wallet: %d %v", code, m)
		}
		if code, m := r.do("POST", "/v1/train/jobs", "acme", hanzoJob(map[string]any{"max_seconds": 30})); code != 200 {
			t.Fatalf("30 seconds on a wallet that holds 30: %d %v", code, m)
		}
		if code, _ := r.do("POST", "/v1/train/jobs", "acme", hanzoJob(map[string]any{})); code != 402 {
			t.Fatalf("a whole window on a wallet that holds 30 seconds: %d", code)
		}
	})
}

func TestARefusedEventStillChargesTheSecondsItMetered(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := mount(t, "")
		c := pin(t)
		r.must(200, "POST", "/v1/train/jobs", "acme", hanzoJob(map[string]any{}))
		x := r.claimed("cuda")
		c.tick(reportEvery)
		code, _ := r.as("POST", "/v1/train/jobs/"+x.job+"/events", x.org, true,
			map[string]any{"task": x.task, "lease": x.lease, "events": []any{map[string]any{"type": "nonsense"}}})
		if code != 400 {
			t.Fatalf("a nonsense event: %d", code)
		}
		if got := r.books.spent("compute"); got != gpu(reportEvery) {
			t.Fatalf("charged %d µ$, want %d: the seconds were metered", got, gpu(reportEvery))
		}
		x.report()
		if got := r.books.spent("compute"); got != gpu(reportEvery) {
			t.Fatalf("the same seconds were charged again: %d µ$", got)
		}
	})
}

func TestAWalletThatHoldsOneDeviceRunsOnOne(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := mount(t, "")
		pin(t)
		r.books.balance = gpu(windowSeconds) + 1
		r.must(200, "POST", "/v1/train/jobs", "acme", hanzoJob(map[string]any{}))
		x := r.claimed("cuda", "cuda", "cuda", "cuda")
		if x.devices != 1 || x.held != windowSeconds {
			t.Fatalf("claim on four devices with one device's window: %+v", x)
		}
	})
}

func TestAPlatformClaimIsTheSuperAdminsAndAudited(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := mount(t, "")
		pin(t)
		id := str(r.must(200, "POST", "/v1/train/jobs", "acme", hanzoJob(map[string]any{})), "id")
		if code, _ := r.do("POST", "/v1/train/jobs/claim", "acme", platformClaim()); code != 403 {
			t.Fatalf("a platform claim by an org's principal: %d", code)
		}
		if code, _ := r.do("POST", "/v1/train/jobs/claim", "acme", claimBody("dgx")); code != 204 {
			t.Fatalf("an org's machine was handed Hanzo's job: %d", code)
		}
		x := r.claimed("cuda")
		if x.job != id {
			t.Fatalf("claimed %s, want %s", x.job, id)
		}
		if code, _ := r.do("POST", "/v1/train/jobs/"+id+"/events", "acme", map[string]any{"task": x.task, "lease": x.lease, "events": []any{}}); code != 403 {
			t.Fatalf("a report on Hanzo's task without the platform credential: %d", code)
		}
		rows, _, err := r.trail.Query(context.Background(), audit.Filter{Action: "train.claim"})
		if err != nil || len(rows) != 1 || rows[0].Resource.ID != "acme/"+id {
			t.Fatalf("audit: %v %v", rows, err)
		}
		var after map[string]any
		_ = json.Unmarshal(rows[0].After, &after)
		if after["payer"] != "org:acme" || after["held"].(float64) != float64(windowSeconds) {
			t.Fatalf("audited: %v", after)
		}
	})
}

func TestAClaimMatchesTheJobsDevices(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := mount(t, "")
		j := job("dgx")
		j["resources"].(map[string]any)["devices"] = []string{"cuda"}
		r.must(200, "POST", "/v1/train/jobs", "acme", j)
		if code, _ := r.do("POST", "/v1/train/jobs/claim", "acme", claimBody("dgx")); code != 204 {
			t.Fatalf("a cuda job was handed to a cpu machine: %d", code)
		}
		body := claimBody("dgx")
		body["devices"] = []string{"cpu", "cuda", "cuda"}
		tk := r.must(200, "POST", "/v1/train/jobs/claim", "acme", body)["task"].(map[string]any)
		if str(tk, "device") != "cuda" || tk["devices"].(float64) != 2 {
			t.Fatalf("claim: %v", tk)
		}
	})
}

func TestEveryStoredObjectIsBilledByTheGBMonthForAsLongAsItIsKept(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := mount(t, "")
		c := pin(t)
		r.st.retain = 0
		o := ops{s: r.svc}
		ctx := context.Background()
		const kept, dropped = int64(10e9), int64(5e9)
		// An upload, kept for a job to read.
		data := strings.Repeat("da", 32)
		r.must(200, "POST", "/v1/train/artifacts", "acme", map[string]any{"kind": "dataset", "sha256": data, "size": dropped})
		r.objs.store("acme/"+data, data)
		j := job("dgx")
		j["dataset"] = map[string]any{"uri": "artifact:" + data}
		id := str(r.must(200, "POST", "/v1/train/jobs", "acme", j), "id")
		// A succeeded job's capability, and a rejected job's: both are kept, both billed.
		run := func(id, sum, verdict string) {
			tk := r.must(200, "POST", "/v1/train/jobs/claim", "acme", claimBody("dgx"))["task"].(map[string]any)
			path := "/v1/train/jobs/" + id
			r.must(200, "POST", path+"/artifacts", "acme", map[string]any{"task": str(tk, "id"), "lease": str(tk, "lease"), "kind": "capability", "sha256": sum, "size": kept})
			r.objs.store("acme/"+sum, sum)
			res := map[string]any{"type": "result", "verdict": verdict, "base": map[string]any{"sha256": baseSHA}, "outputs": []any{map[string]any{"kind": "capability", "sha256": sum}}}
			r.must(200, "POST", path+"/events", "acme", map[string]any{"task": str(tk, "id"), "lease": str(tk, "lease"), "events": []any{map[string]any{"type": "artifact", "sha256": sum}, res}})
		}
		run(id, strings.Repeat("aa", 32), "accepted")
		other := str(r.must(200, "POST", "/v1/train/jobs", "acme", job("dgx")), "id")
		run(other, strings.Repeat("bb", 32), "rejected")
		if g := r.must(200, "GET", "/v1/train/jobs/"+other, "acme", nil); str(g, "status") != rejected {
			t.Fatalf("other: %v", g)
		}
		rate := hourMicros(2*kept+dropped, storageFloorMicros)
		c.tick(3600)
		o.sweep(ctx, c.now)
		if got := r.books.spent("storage"); got != cost(rate, 3600) {
			t.Fatalf("an hour of %d bytes: %d µ$, want %d", 2*kept+dropped, got, cost(rate, 3600))
		}
		// A month on, the same bytes are a month of them.
		c.tick(30 * 24 * 3600)
		o.sweep(ctx, c.now)
		month := cost(rate, 3600) + cost(rate, 30*24*3600)
		if got := r.books.spent("storage"); got != month {
			t.Fatalf("a month on: %d µ$, want %d", got, month)
		}
		if per := float64(cost(rate, 30*24*3600)) / float64(2*kept+dropped) * 1e9; math.Abs(per-storageFloorMicros) > storageFloorMicros*0.001 {
			t.Fatalf("a GB-month cost %.1f µ$, want %d", per, storageFloorMicros)
		}
		// Deleted, the upload is no longer billed.
		r.must(200, "DELETE", "/v1/train/artifacts/"+data, "acme", nil)
		c.tick(3600)
		o.sweep(ctx, c.now)
		if got := r.books.spent("storage") - month; got != cost(hourMicros(2*kept, storageFloorMicros), 3600) {
			t.Fatalf("after the delete: %d µ$ for the hour, want %d", got, cost(hourMicros(2*kept, storageFloorMicros), 3600))
		}
		for _, d := range r.books.of("storage") {
			if d.payer != "org:acme" || d.usage.Model != storageMeter {
				t.Fatalf("debit: %+v", d)
			}
		}
	})
}

// TestTheComputeFloorIsTheCatalogs pins the GPU device-hour floor to the "GPU
// Basic" row @hanzo/plans publishes: one H100 for an hour.
//
// MUTATION: change gpuHourFloor to 348_000 ($3.48 read as dimes) and this fails
// naming both numbers.

// TestTheStorageFloorIsTheCatalogs pins the GB-month floor to the object-storage
// row @hanzo/plans publishes.
//
// MUTATION: change storageFloorMicros to 2_000 ($0.02 read as cents) and this
// fails naming both numbers.

func TestAJobWhosePayersMoneyIsHeldWaitsAndRunsOnceItIsFree(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := mount(t, "")
		c := pin(t)
		r.books.balance = gpu(windowSeconds) + 1
		r.must(200, "POST", "/v1/train/jobs", "acme", hanzoJob(map[string]any{}))
		c.tick(1)
		second := str(r.must(200, "POST", "/v1/train/jobs", "acme", hanzoJob(map[string]any{})), "id")
		x := r.claimed("cuda")
		if y := r.claimPlatform("cuda"); y != nil {
			t.Fatalf("a second window was held on money the first holds: %+v", y)
		}
		if g := r.must(200, "GET", "/v1/train/jobs/"+second, "acme", nil); str(g, "status") != queued {
			t.Fatalf("a job waiting on its payer's money was ended: %v", g)
		}
		c.tick(reportEvery)
		x.report(map[string]any{"type": "status", "status": "failed", "error": "done with it"})
		r.books.mu.Lock()
		r.books.balance += gpu(reportEvery)
		r.books.mu.Unlock()
		if y := r.claimed("cuda"); y.job != second {
			t.Fatalf("claimed %s, want the waiting job %s", y.job, second)
		}
	})
}
