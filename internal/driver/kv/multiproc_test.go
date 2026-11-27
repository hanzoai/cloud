package kv

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/coord"
)

// A re-executed test binary plays one pod: COORD_TEST_POD names it, and the KV and
// the object store it shares with its siblings are named beside it.
const (
	childEnv   = "COORD_TEST_POD"
	childKV    = "COORD_TEST_KV"
	childStore = "COORD_TEST_STORE"
)

// child is one pod's root: it joins, redeems one token every pod is offered, spends
// ten requests of a window every pod shares, and then runs a loop whose every
// effect it prints with the round it held and when the effect began and ended. On
// SIGTERM it closes as a draining pod does.
func child() int {
	id := os.Getenv(childEnv)
	c, err := coord.New(coord.Config{Periods: fast, URL: os.Getenv(childKV), Deployment: "mp", Self: coord.Member{ID: id, Addr: id + ":8443"}, Instance: id + "-host", Secret: secret, Root: true})
	if err != nil {
		fmt.Println("ERR", err)
		return 1
	}
	c.SetStore(fileStore{dir: os.Getenv(childStore)})
	ctx := context.Background()
	c.Start(ctx)
	ok, err := c.Once(ctx, "link", "shared", time.Minute)
	fmt.Println("ONCE", id, ok, err == nil)
	admitted := 0
	for range 10 {
		if left, _ := c.Spend(ctx, "w", []string{"k"}, []int{1}, 15, time.Minute); left >= 0 {
			admitted++
		}
	}
	fmt.Println("ADMITTED", id, admitted)

	term := make(chan os.Signal, 1)
	signal.Notify(term, syscall.SIGTERM)
	go func() {
		<-term
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		c.Close(cctx)
		cancel()
		fmt.Println("CLOSED", id, time.Now().UnixNano())
		os.Exit(0)
	}()
	l := c.Lead("loop")
	for {
		_ = l.Do(ctx, func(ctx context.Context) {
			for ctx.Err() == nil && l.Hold(ctx) == nil {
				round := l.Round()
				start := time.Now()
				time.Sleep(20 * time.Millisecond)
				fmt.Println("FX", id, round, start.UnixNano(), time.Now().UnixNano())
			}
		})
		time.Sleep(20 * time.Millisecond)
	}
}

type fxLine struct {
	pod        string
	round      uint64
	start, end time.Time
}

// Three processes against one Valkey and one object store: the live set converges,
// a token is redeemed by one of them, a limit holds across all three, and the loop
// runs in exactly one. Killed outright, its holder's loop moves to another pod at a
// higher round, after the dead holder's term; drained, it moves at once. No two
// pods' effects ever overlap.
func TestThreePodsAgree(t *testing.T) {
	t.Parallel()
	url, _ := valkey(t)
	store := t.TempDir()
	var (
		mu     sync.Mutex
		fx     []fxLine
		other  = map[string][]string{}
		procs  = map[string]*exec.Cmd{}
		closed = map[string]time.Time{}
	)
	for _, id := range []string{"cloud-0", "cloud-1", "cloud-2"} {
		cmd := exec.Command(os.Args[0], "-test.run=^$")
		cmd.Env = append(os.Environ(), childEnv+"="+id, childKV+"="+url, childStore+"="+store)
		out, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		procs[id] = cmd
		go func() {
			s := bufio.NewScanner(out)
			for s.Scan() {
				f := strings.Fields(s.Text())
				if len(f) == 0 {
					continue
				}
				mu.Lock()
				switch f[0] {
				case "FX":
					round, _ := strconv.ParseUint(f[2], 10, 64)
					a, _ := strconv.ParseInt(f[3], 10, 64)
					b, _ := strconv.ParseInt(f[4], 10, 64)
					fx = append(fx, fxLine{f[1], round, time.Unix(0, a), time.Unix(0, b)})
				case "CLOSED":
					at, _ := strconv.ParseInt(f[2], 10, 64)
					closed[f[1]] = time.Unix(0, at)
				default:
					other[f[0]] = append(other[f[0]], strings.Join(f[1:], " "))
				}
				mu.Unlock()
			}
		}()
	}
	t.Cleanup(func() {
		for _, cmd := range procs {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	since := func(at time.Time) []fxLine {
		mu.Lock()
		defer mu.Unlock()
		var out []fxLine
		for _, l := range fx {
			if !l.start.Before(at) {
				out = append(out, l)
			}
		}
		return out
	}
	holders := func(ls []fxLine) map[string]int {
		who := map[string]int{}
		for _, l := range ls {
			who[l.pod]++
		}
		return who
	}

	reader, err := coord.New(coord.Config{Periods: fast, URL: url, Deployment: "mp", Self: coord.Member{ID: "reader"}, Secret: secret})
	if err != nil {
		t.Fatal(err)
	}
	reader.Start(context.Background())
	defer reader.Close(context.Background())
	eventually(t, 10*time.Second, "the live set never held three pods", func() bool { return len(reader.Members()) == 4 })
	eventually(t, 10*time.Second, "not every pod spent its window", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(other["ADMITTED"]) == 3
	})
	mu.Lock()
	firsts, admitted := 0, 0
	for _, l := range other["ONCE"] {
		if strings.Fields(l)[1] == "true" {
			firsts++
		}
	}
	for _, l := range other["ADMITTED"] {
		n, _ := strconv.Atoi(strings.Fields(l)[1])
		admitted += n
	}
	mu.Unlock()
	if firsts != 1 {
		t.Fatalf("the shared token was redeemed %d times; want once", firsts)
	}
	if admitted != 15 {
		t.Fatalf("three pods spending ten each against a limit of fifteen admitted %d", admitted)
	}

	// Steady state: one pod runs the loop.
	eventually(t, 10*time.Second, "nobody ran the loop", func() bool { return len(since(time.Time{})) > 0 })
	steady := time.Now()
	time.Sleep(time.Second)
	who := holders(since(steady))
	if len(who) != 1 {
		t.Fatalf("over one second the loop ran in %v; want exactly one pod", who)
	}
	var first string
	for id := range who {
		first = id
	}

	// Killed outright: nothing lets the lease go, so it moves once its object has
	// gone a term without a renewal.
	killed := time.Now()
	_ = procs[first].Process.Signal(syscall.SIGKILL)
	_ = procs[first].Wait()
	delete(procs, first)
	eventually(t, reader.Handover()+3*time.Second, "no pod took the loop of a killed holder", func() bool {
		for _, l := range since(killed) {
			if l.pod != first {
				return true
			}
		}
		return false
	})
	var second fxLine
	for _, l := range since(killed) {
		if l.pod != first {
			second = l
			break
		}
	}
	t.Logf("%s killed; %s ran the loop %v later at round %d", first, second.pod, second.start.Sub(killed).Round(time.Millisecond), second.round)

	// Drained: the holder finishes its pass and lets the lease go, and the third
	// pod starts without waiting out a term.
	time.Sleep(300 * time.Millisecond)
	_ = procs[second.pod].Process.Signal(syscall.SIGTERM)
	_ = procs[second.pod].Wait()
	delete(procs, second.pod)
	mu.Lock()
	at := closed[second.pod]
	mu.Unlock()
	if at.IsZero() {
		t.Fatalf("%s never reported closing", second.pod)
	}
	eventually(t, 3*time.Second, "no pod took the loop a draining pod let go", func() bool {
		for _, l := range since(at.Add(-term)) {
			if l.pod != first && l.pod != second.pod {
				return true
			}
		}
		return false
	})
	var third fxLine
	for _, l := range since(at.Add(-term)) {
		if l.pod != first && l.pod != second.pod {
			third = l
			break
		}
	}
	if wait := third.start.Sub(at); wait > term {
		t.Fatalf("%s waited %v after %s let the loop go; a released lease needs no wait", third.pod, wait, second.pod)
	}
	t.Logf("%s drained; %s ran the loop %v after it closed at round %d", second.pod, third.pod, third.start.Sub(at).Round(time.Millisecond), third.round)

	// No two pods acted at once, and every change of holder raised the round.
	mu.Lock()
	all := append([]fxLine(nil), fx...)
	mu.Unlock()
	rounds := map[string]map[uint64]bool{}
	for i, a := range all {
		if rounds[a.pod] == nil {
			rounds[a.pod] = map[uint64]bool{}
		}
		rounds[a.pod][a.round] = true
		for _, b := range all[i+1:] {
			if a.pod != b.pod && a.start.Before(b.end) && b.start.Before(a.end) {
				t.Fatalf("%s (round %d) and %s (round %d) acted at once", a.pod, a.round, b.pod, b.round)
			}
		}
	}
	for p, rs := range rounds {
		for q, qs := range rounds {
			if p == q {
				continue
			}
			for r := range rs {
				if qs[r] {
					t.Fatalf("%s and %s both held round %d", p, q, r)
				}
			}
		}
	}
	if second.round <= all[0].round || third.round <= second.round {
		t.Fatalf("rounds %d → %d → %d did not rise with each change of holder", all[0].round, second.round, third.round)
	}
}

// fileStore is compare-and-set over a directory every process on this machine
// shares, for the test that runs pods as processes.
type fileStore struct{ dir string }

func (s fileStore) locked(exclusive bool, f func() error) error {
	l, err := os.OpenFile(filepath.Join(s.dir, ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer l.Close()
	how := syscall.LOCK_SH
	if exclusive {
		how = syscall.LOCK_EX
	}
	if err := syscall.Flock(int(l.Fd()), how); err != nil {
		return err
	}
	defer syscall.Flock(int(l.Fd()), syscall.LOCK_UN)
	return f()
}

func (s fileStore) path(key string) string {
	return filepath.Join(s.dir, fmt.Sprintf("%x", key))
}

func (s fileStore) read(key string) ([]byte, string, error) {
	b, err := os.ReadFile(s.path(key))
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", coord.ErrMissing
	}
	if err != nil {
		return nil, "", err
	}
	v, data, _ := bytes.Cut(b, []byte("\n"))
	return data, string(v), nil
}

func (s fileStore) Get(_ context.Context, key string) (data []byte, version string, err error) {
	err = s.locked(false, func() error {
		data, version, err = s.read(key)
		return err
	})
	return data, version, err
}

func (s fileStore) PutIfVersion(_ context.Context, key string, data []byte, expect string) (version string, err error) {
	err = s.locked(true, func() error {
		_, cur, err := s.read(key)
		if err != nil && !errors.Is(err, coord.ErrMissing) {
			return err
		}
		if cur != expect {
			return coord.ErrConflict
		}
		n, _ := strconv.Atoi(cur)
		version = strconv.Itoa(n + 1)
		tmp := s.path(key) + ".tmp"
		if err := os.WriteFile(tmp, append([]byte(version+"\n"), data...), 0o600); err != nil {
			return err
		}
		return os.Rename(tmp, s.path(key))
	})
	return version, err
}

func TestMain(m *testing.M) {
	if os.Getenv(childEnv) != "" {
		os.Exit(child())
	}
	os.Exit(m.Run())
}
