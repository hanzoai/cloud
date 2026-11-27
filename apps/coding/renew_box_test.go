package coding

// renew_test.go — a run's model credential is short-lived and renewed by the host
// (key.go). The clock is a fake one the test advances, IAM is a server the test
// scripts, and the person's standing is the park's own mayAct, so each test says one
// thing about the renewal: that it is made before the credential ends, that a person
// removed is ended and one IAM cannot be asked about is waited for, that no run waits
// on another, and that nothing is renewed after the run is over.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/client"
)

func TestARenewalTheBoxDidNotTakeIsNotAdopted(t *testing.T) {
	t.Run("the box said so", func(t *testing.T) {
		r := newRenewRig(t)
		r.app.run("acme", "alice", "sess_1")
		b := r.key("acme", "alice", "sess_1")
		first := b.current()
		r.mu.Lock()
		r.plantErr = fmt.Errorf("%w: the exec exited 1", errNotLanded)
		r.mu.Unlock()

		r.clock.tick(credLife - credMargin)
		eventually(t, "the renewal is tried and refused by the box", func() bool { return len(r.iam.revokedTokens()) == 1 })
		if b.current() != first {
			t.Fatal("a token the box never took became the one in use")
		}
		if got := r.iam.revokedTokens(); !slices.Equal(got, []string{jwtWith("ai:inference", "tok-2")}) {
			t.Fatalf("revoked %v, want the unused token", got)
		}
		eventually(t, "a retry is set", func() bool { return len(r.clock.pending()) == 1 })
		r.mu.Lock()
		r.plantErr = nil
		r.mu.Unlock()
		r.clock.tick(retryAfter(1) + retryAfter(1)/4)
		eventually(t, "the renewal lands", func() bool { return b.current() != first })
	})
	t.Run("the answer was lost", func(t *testing.T) {
		r := newRenewRig(t)
		r.app.run("acme", "alice", "sess_1")
		b := r.key("acme", "alice", "sess_1")
		first := b.current()
		r.mu.Lock()
		r.plantErr = errors.New("context deadline exceeded")
		r.mu.Unlock()

		r.clock.tick(credLife - credMargin)
		eventually(t, "a retry is set", func() bool { return len(r.clock.pending()) == 1 && len(r.iam.asks()) == 2 })
		lost := jwtWith("ai:inference", "tok-2")
		if b.current() != first {
			t.Fatal("a token the box may not have taken became the one in use")
		}
		if got := r.iam.revokedTokens(); len(got) != 0 {
			t.Fatalf("revoked %v: the harness may be reading that token from its file", got)
		}
		if known := b.known(); !slices.Contains(known, lost) {
			t.Fatalf("the token that may be in the box is not blinded: %v", known)
		}

		// The retry lands another, which replaces the file's: the lost one is revoked then.
		r.mu.Lock()
		r.plantErr = nil
		r.mu.Unlock()
		r.clock.tick(retryAfter(1) + retryAfter(1)/4)
		eventually(t, "the renewal lands", func() bool { return b.current() != first })
		landed := jwtWith("ai:inference", "tok-3")
		if b.current() != landed {
			t.Fatalf("the token in use is %q, want the one that landed", b.current())
		}
		eventually(t, "the lost token is revoked", func() bool { return slices.Contains(r.iam.revokedTokens(), lost) })
		if slices.Contains(r.iam.revokedTokens(), landed) {
			t.Fatal("the token that landed was revoked")
		}
	})
	t.Run("and the run ends before one lands", func(t *testing.T) {
		r := newRenewRig(t)
		r.app.run("acme", "alice", "sess_1")
		b := r.key("acme", "alice", "sess_1")
		r.mu.Lock()
		r.plantErr = errors.New("context deadline exceeded")
		r.mu.Unlock()
		r.clock.tick(credLife - credMargin)
		eventually(t, "a retry is set", func() bool { return len(r.clock.pending()) == 1 && len(r.iam.asks()) == 2 })
		b.close()
		got := r.iam.revokedTokens()
		if !slices.Contains(got, jwtWith("ai:inference", "tok-2")) || !slices.Contains(got, jwtWith("ai:inference", "tok-1")) {
			t.Fatalf("revoked %v at the end, want the token in use and the one that may have been in the box", got)
		}
	})
}

// A plant is known not to have landed only when the exec said it failed; a transport that gave up
// after the exec may have run is not known. The classification is the runner's: the box's answer.
//
// MUTATION: return errNotLanded for every error and a lost answer is read as a refusal.
func TestAPlantKnowsWhenTheFileWasNotWritten(t *testing.T) {
	var fail bool
	p := &pod{answer: func(argv []string) client.Ran {
		if len(argv) > 2 && strings.HasPrefix(argv[2], "IFS= read -r k; ") && fail {
			return client.Ran{ExitCode: 1}
		}
		return client.Ran{}
	}}
	servePod(t, p)
	plant := plantAt("sbx_1", "/tmp/hanzo-run/model.sess_1")
	if err := plant(t.Context(), "tok"); err != nil {
		t.Fatalf("a plant the box took: %v", err)
	}
	fail = true
	if err := plant(t.Context(), "tok"); !errors.Is(err, errNotLanded) {
		t.Fatalf("a plant whose exec failed: %v, want it known not to have landed", err)
	}
	fail = false
	p.refuse = func(argv []string) error { return errors.New("context deadline exceeded") }
	if err := plant(t.Context(), "tok"); err == nil || errors.Is(err, errNotLanded) {
		t.Fatalf("a plant whose answer was lost: %v, want an error that does not say it did not land", err)
	}
}

// A run that goes on after a restart (a parked run launched again, a resumed one) holds a
// credential of its own, minted afresh: nothing of the flight that died is carried, so a
// restart can neither lengthen nor lose a credential.
//
// MUTATION: reuse the earlier bearer for the same session and the relaunch spends a token the
// dead flight revoked.
func TestARunThatGoesOnAfterARestartMintsAFreshCredential(t *testing.T) {
	r := newRenewRig(t)
	r.app.run("acme", "alice", "sess_1")
	old := r.key("acme", "alice", "sess_1")
	dead := old.current()
	old.close() // the pod went away: its flight ended

	again := r.key("acme", "alice", "sess_1")
	if again.current() == dead || again.current() == "" {
		t.Fatalf("the relaunched run holds %q, want a credential minted for it", again.current())
	}
	if due := r.clock.pending(); len(due) != 1 {
		t.Fatalf("timers %v after a relaunch, want the new run's alone", due)
	}
	if got := r.iam.revokedTokens(); !slices.Equal(got, []string{dead}) {
		t.Fatalf("revoked %v, want only the flight that ended", got)
	}
}

// A burst of runs — started together, or all asking again after IAM was down — does not ask IAM
// in the same instant: each timer is spread.
//
// MUTATION: take the spread out of arm and retry and every run is due at the same moment.
func TestARenewalBurstIsSpread(t *testing.T) {
	r := newRenewRig(t)
	const runs = 24
	for i := range runs {
		r.app.run("acme", "alice", "sess_"+strconv.Itoa(i))
		r.key("acme", "alice", "sess_"+strconv.Itoa(i))
	}
	due := r.clock.pending()
	if len(due) != runs {
		t.Fatalf("%d timers for %d runs", len(due), runs)
	}
	if spread := due[len(due)-1] - due[0]; spread < time.Minute {
		t.Fatalf("%d runs started together are due within %v of each other, want them spread across the quarter margin", runs, spread)
	}
	if due[0] < credLife-credMargin-credMargin/4 || due[len(due)-1] > credLife-credMargin {
		t.Fatalf("due between %v and %v, want within the quarter margin before the margin", due[0], due[len(due)-1])
	}

	// IAM goes down for all of them: their retries are spread too.
	r.iam.setAnswer(func(n int, _ url.Values) int {
		if n > runs {
			return http.StatusServiceUnavailable
		}
		return 0
	})
	r.clock.tick(credLife - credMargin)
	eventually(t, "every run is retrying", func() bool { return len(r.clock.pending()) == runs })
	retries := r.clock.pending()
	if spread := retries[len(retries)-1] - retries[0]; spread < time.Second {
		t.Fatalf("%d retries are due within %v of each other, want them spread", runs, spread)
	}
}

// Nothing is spent on a person unrecorded. The first mint is on the audit trail before the
// credential reaches a box, and a process that keeps no trail refuses the run and revokes the
// token it asked for: as item two's relaunch refuses.
//
// MUTATION: ignore the trail's refusal and a run holds a credential as a person with no record.
func TestAProcessWithNoAuditTrailMintsNothingThatStays(t *testing.T) {
	r := newRenewRig(t)
	flights.keep(nil)
	_, err := key(t.Context(), loan{org: "acme", person: "alice", run: "sess_1", ttl: 3600, plant: r.plant, halt: r.halt})
	if !errors.Is(err, errNoTrail) {
		t.Fatalf("key answered %v, want the process refused for keeping no audit trail", err)
	}
	if got := r.iam.revokedTokens(); len(got) != 1 {
		t.Fatalf("revoked %v, want the token IAM issued revoked unseen", got)
	}
	if due := r.clock.pending(); len(due) != 0 {
		t.Fatalf("timers %v for a run that was refused", due)
	}

	// A trail that will not take the act is no trail either.
	l := ledgerOf(t)
	r.iam.trail.refuse(errors.New("the trail is down"))
	l.trail = r.iam.trail
	flights.keep(l)
	if _, err := key(t.Context(), loan{org: "acme", person: "alice", run: "sess_1", ttl: 3600, plant: r.plant, halt: r.halt}); err == nil {
		t.Fatal("a mint the trail would not take was let through")
	}
}

// The trail holds the first mint and, at the end, how many renewals the run made: not one row
// for each.
//
// MUTATION: write a row for every renewal and the trail grows with the length of a run.
func TestTheTrailHoldsTheMintAndTheCountOfRenewals(t *testing.T) {
	r := newRenewRig(t)
	r.app.run("acme", "alice", "sess_1")
	b := r.key("acme", "alice", "sess_1")
	for range 3 {
		before := b.current()
		r.clock.tick(credLife - credMargin)
		eventually(t, "a renewal", func() bool { return b.current() != before })
	}
	b.close()

	got := actions(r.iam.trail)
	if !slices.Equal(got, []string{"coding.run.mint:success", "coding.run.end:success"}) {
		t.Fatalf("the trail holds %v, want the mint and the end and no row for the %d renewals between", got, 3)
	}
	recs := r.iam.trail.records()
	for _, rec := range recs {
		if rec.Actor.Sub != "alice" || rec.Actor.Org != "acme" || rec.Resource.ID != "sess_1" || rec.Auth.Method != "delegated" {
			t.Fatalf("a row is %+v, want alice's, in acme, of her session, by delegation", rec)
		}
	}
	if !strings.Contains(string(recs[1].After), `"renewals":3`) {
		t.Fatalf("the end says %s, want three renewals", recs[1].After)
	}
	if !strings.Contains(string(recs[0].After), `"renews":true`) {
		t.Fatalf("the mint says %s, want that it renews", recs[0].After)
	}
}

// The run's bound is the reaper's: the same figure, from the same function, so a session the
// reaper would end and a run that has run out of time are one moment; a run may be asked for
// more than a day and takes a day; a run may be asked for five hours and is given five.
//
// MUTATION: bound a run at four hours again and a five-hour run is cut at four.
func TestTheLongestARunMayTakeIsTheLongestASessionMayLive(t *testing.T) {
	if ceiling() != agents.MaxLife() {
		t.Fatalf("a run may take %v and a session may live %v", ceiling(), agents.MaxLife())
	}
	if budget(0) != agents.MaxLife() {
		t.Fatalf("a run that names no timeout is given %v, want as long as any run may take", budget(0))
	}
	if got := budget(5 * 3600); got != 5*time.Hour {
		t.Fatalf("a five-hour run is given %v, not cut at the four hours IAM delegates for", got)
	}
	if got := budget(30 * 86400); got != agents.MaxLife() {
		t.Fatalf("a run asked for thirty days is given %v, want %v", got, agents.MaxLife())
	}
	t.Setenv("AGENTS_MAX_LIFE_SEC", "7200")
	if ceiling() != 2*time.Hour || budget(0) != 2*time.Hour || budget(3*3600) != 2*time.Hour || budget(600) != 10*time.Minute {
		t.Fatalf("with a session allowed two hours: ceiling %v, default %v, three hours asked %v", ceiling(), budget(0), budget(3*3600))
	}
	// The run's own context is that long and no longer, and so is the time it may be extended to.
	ctx, cancel, clk := runContext("acme", 0, ceiling())
	defer cancel()
	if d, ok := ctx.Deadline(); !ok || time.Until(d) > 2*time.Hour+time.Minute {
		t.Fatalf("the run's context ends in %v, want within the ceiling", time.Until(d))
	}
	if _, err := clk.extend(time.Now(), time.Hour); err == nil {
		t.Fatal("a run held for as long as a session may live was extended")
	}
	// Item two: a run goes on by itself for as long as a session may live from its first park.
	if (park{created: time.Now().Add(-119 * time.Minute).UnixMilli()}).stale(time.Now()) ||
		!(park{created: time.Now().Add(-121 * time.Minute).UnixMilli()}).stale(time.Now()) {
		t.Fatal("a park's age and a run's ceiling are not the one figure")
	}
}

// The credential reaches the harness only on stdin and in a file only the run's user reads, never on a
// command line; and the file is written whole and renamed into place, never over a newer token. The
// scripts are run by a real shell.
//
// MUTATION: write the file in place and a reader sees half a token; let a later turn's wrapper write it
// and a renewal is put back; or print the token in argv.
func TestTheCredentialFileIsWrittenAtomicallyAndNeverAnArgument(t *testing.T) {
	const tok = "eyJhbGciOiJSUzI1NiJ9.THE-FIRST-TOKEN.sig"
	home := t.TempDir()
	file := home + "/run/model.sess_1"
	inner := []string{"sh", "-c", `printf '%s|%s|' "$HANZO_API_KEY" "$HANZO_RUN"; env | grep -c '^k=' || true`}
	argv := planted(file, inner)
	for i, a := range argv {
		if strings.Contains(a, tok) {
			t.Fatalf("argv[%d] carries the credential: %q", i, a)
		}
	}
	out, err := runShell(t, home, tok+"\nrun_ticket\n", argv...)
	if err != nil {
		t.Fatalf("the wrapper: %v: %s", err, out)
	}
	if !strings.HasPrefix(out, tok+"|run_ticket|0") {
		t.Fatalf("the harness saw %q, want the credential and the ticket in its environment and no k", out)
	}
	if got := readFile(t, file); got != tok {
		t.Fatalf("the file holds %q, want the token exactly, with no newline", got)
	}
	if mode := permsOf(t, file); mode != 0o600 {
		t.Fatalf("the file is mode %o, want 600", mode)
	}
	if left := globIn(t, home+"/run", "model.sess_1.*"); len(left) != 0 {
		t.Fatalf("temporary files left behind: %v", left)
	}

	// A renewal is the same write, from its own exec, and is a replacement of the file and not a
	// rewrite of it: a harness that reads it as it is written reads the token before or the token
	// after, never half of one.
	before, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	const next = "eyJhbGciOiJSUzI1NiJ9.THE-NEXT-TOKEN.sig"
	renew := []string{"sh", "-c", `IFS= read -r k; f=$1; ` + put, "sh", file}
	if out, err := runShell(t, home, next+"\n", renew...); err != nil {
		t.Fatalf("the renewal exec: %v: %s", err, out)
	}
	if got := readFile(t, file); got != next {
		t.Fatalf("after a renewal the file holds %q, want %q", got, next)
	}
	if after, err := os.Stat(file); err != nil || os.SameFile(before, after) {
		t.Fatalf("the renewal rewrote the file in place (%v): a reader could see half a token", err)
	}
	if left := globIn(t, home+"/run", "model.sess_1.*"); len(left) != 0 {
		t.Fatalf("temporary files left behind: %v", left)
	}

	// A turn that begins after the renewal holds the token it was handed before it, and leaves the
	// newer one in the file alone; one whose file is gone (a pod replaced between turns) writes it.
	later := lodged(file, inner)
	if out, err := runShell(t, home, tok+"\nrun_ticket\n", later...); err != nil {
		t.Fatalf("the later turn: %v: %s", err, out)
	}
	if got := readFile(t, file); got != next {
		t.Fatalf("a later turn put %q back over the renewal %q", got, next)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if out, err := runShell(t, home, tok+"\nrun_ticket\n", later...); err != nil {
		t.Fatalf("the turn after a pod was replaced: %v: %s", err, out)
	}
	if got := readFile(t, file); got != tok {
		t.Fatalf("a turn whose file was gone left %q, want its token", got)
	}
	// A flight's first turn writes whatever is there: the file of a flight that died is not its own.
	if out, err := runShell(t, home, next+"\nrun_ticket\n", argv...); err != nil {
		t.Fatalf("the first turn of the next flight: %v: %s", err, out)
	}
	if got := readFile(t, file); got != next {
		t.Fatalf("a flight's first turn left %q over its own token %q", got, next)
	}

	// dev's credential command answers the file, and its own answer when there is none.
	fake := t.TempDir()
	writeExec(t, fake+"/dev", "#!/bin/sh\necho dev-token-answer\n")
	if out, _ := runShellPath(t, home, fake, "", "sh", "-c", readKey, "sh", file); out != next {
		t.Fatalf("the credential command answered %q, want the file's %q", out, next)
	}
	if out, _ := runShellPath(t, home, fake, "", "sh", "-c", readKey, "sh", home+"/none"); strings.TrimSpace(out) != "dev-token-answer" {
		t.Fatalf("with no file the credential command answered %q, want dev's own", out)
	}

	// The file is the run's own: another run's, or an earlier one's, is another name.
	if modelPath("sess_1") == modelPath("sess_2") || !strings.HasPrefix(modelPath("../../etc/passwd"), "/tmp/hanzo-run/model.") ||
		strings.Contains(modelPath("../../etc/passwd")[len("/tmp/hanzo-run/"):], "/") {
		t.Fatalf("a run's file is %q / %q", modelPath("sess_1"), modelPath("../../etc/passwd"))
	}
}

// dev's credential command is pointed at that file ahead of the task (and of a resume), and the
// harness is asked for it again before a renewal's margin can pass.
func TestDevsCredentialCommandIsPointedAtTheFileAheadOfTheTask(t *testing.T) {
	const file = "/tmp/hanzo-run/model.sess_1"
	dev := devExec("", "enso-pro", "", nil)
	for _, argv := range [][]string{append(slices.Clone(dev), "--", "-"), resumeFor("11111111-1111-1111-1111-111111111111", "", "enso-pro", "")} {
		got := refreshing(file, argv)
		joined := strings.Join(got, " ")
		for _, want := range []string{"model_providers.hanzo.auth.command=\"sh\"", "model_providers.hanzo.auth.args=[", file, "refresh_interval_ms=600000", "timeout_ms=30000"} {
			if !strings.Contains(joined, want) {
				t.Errorf("the argv lacks %q: %s", want, joined)
			}
		}
		end := slices.Index(got, "--")
		if at := slices.Index(got, "resume"); at >= 0 {
			end = at
		}
		if i := slices.Index(got, `model_providers.hanzo.auth.command="sh"`); i < 0 || i > end {
			t.Errorf("the flags are not ahead of the task: %v", got)
		}
		if got[len(got)-1] != "-" {
			t.Errorf("the task's place on stdin moved: %v", got)
		}
	}
	if askEvery >= credMargin {
		t.Fatalf("the harness asks every %v, and a credential is renewed %v before it ends: a renewal could be missed", askEvery, credMargin)
	}
}

func runShell(t *testing.T, home, stdin string, argv ...string) (string, error) {
	t.Helper()
	return runShellPath(t, home, "", stdin, argv...)
}

// runShellPath runs argv as the box would: a small environment, dev's home in home, and bin ahead on the path.
func runShellPath(t *testing.T, home, bin, stdin string, argv ...string) (string, error) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), argv[0], argv[1:]...)
	path := "/usr/bin:/bin"
	if bin != "" {
		path = bin + ":" + path
	}
	cmd.Env = []string{"PATH=" + path, "HOME=" + home, "DEV_HOME=" + home}
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func permsOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return fi.Mode().Perm()
}

func globIn(t *testing.T, dir, pattern string) []string {
	t.Helper()
	got, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	return got
}

func writeExec(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// The credential file goes with the run: taken out of the box when the run ends, once, on a context of its
// own (the run's is done), and a box that will not answer does not stop the tokens being revoked.
//
// MUTATION: leave the erase out of close and a sandbox kept for a follow-up holds a token's file.
func TestAnEndedRunTakesItsCredentialFileOutOfItsBox(t *testing.T) {
	r := newRenewRig(t)
	r.app.run("acme", "alice", "sess_1")
	var mu sync.Mutex
	var erased int
	var live bool
	b, err := key(t.Context(), loan{org: "acme", person: "alice", run: "sess_1", ttl: 24 * 3600, plant: r.plant, halt: r.halt,
		forget: func(ctx context.Context) error {
			mu.Lock()
			defer mu.Unlock()
			erased++
			live = ctx.Err() == nil
			return errors.New("the sandbox is gone")
		}})
	if err != nil {
		t.Fatal(err)
	}
	first := b.current()
	b.close()
	b.close() // twice is harmless
	mu.Lock()
	defer mu.Unlock()
	if erased != 1 || !live {
		t.Fatalf("the file was erased %d times, on a live context = %v, want once on one", erased, live)
	}
	if got := r.iam.revokedTokens(); !slices.Equal(got, []string{first}) {
		t.Fatalf("revoked %v: a box that would not answer must not leave the token live", got)
	}
}
