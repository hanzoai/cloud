package sandbox

// park_test.go — a sandbox with no project that asked for a disk of its own keeps
// its files across a park, comes back under the same id, is never billed for
// compute while parked, belongs to its org alone, and is retired with its disk
// once its hold is over.

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/k8s"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/kit"
	"github.com/hanzoai/cloud/sdk/meter"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/account"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/remotecommand"
	utilexec "k8s.io/client-go/util/exec"
)

// files is an exec channel over files that live where a pod's files live: on the
// disk the pod mounts, or in the pod itself when it mounts none. It speaks the two
// commands the fs verbs send (put and Read), which is all a disk has to answer.
type files struct {
	mu  sync.Mutex
	rt  *runtime
	got map[string]map[string]string // by disk (or pod), by path
}

func (f *files) stream(ctx context.Context, _, pod string, argv []string, stdin io.Reader, stdout, _ io.Writer) error {
	where := f.where(ctx, pod)
	line := argv[len(argv)-1]
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.got[where] == nil {
		f.got[where] = map[string]string{}
	}
	if _, path, ok := strings.Cut(line, "cat > "); ok {
		b, _ := io.ReadAll(stdin)
		f.got[where][strings.Trim(path, "'")] = string(b)
		return nil
	}
	if _, path, ok := strings.Cut(line, "cat -- "); ok {
		body, have := f.got[where][strings.Trim(path, "'")]
		if !have {
			return utilexec.CodeExitError{Err: errors.New("no such file"), Code: 1}
		}
		_, _ = io.WriteString(stdout, body)
	}
	return nil
}

func (f *files) tty(context.Context, string, string, []string, io.Reader, io.Writer, remotecommand.TerminalSizeQueue) error {
	return nil
}

// where is the disk the pod mounts, read off the pod the fake apiserver holds.
func (f *files) where(ctx context.Context, pod string) string {
	obj, err := f.rt.pods().Get(ctx, pod, metav1.GetOptions{})
	if err != nil {
		return "gone:" + pod
	}
	vols, _, _ := unstructured.NestedSlice(obj.Object, "spec", "volumes")
	for _, v := range vols {
		if claim, ok, _ := unstructured.NestedString(v.(map[string]any), "persistentVolumeClaim", "claimName"); ok {
			return claim
		}
	}
	return "pod:" + pod
}

// parking is a sandbox service over a fake cluster whose pods come up at once and
// whose disks hold files, with a wallet that can pay and a ledger that records.
func parking(t *testing.T) (*Service, *files, *ledger) {
	t.Helper()
	away(t)
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	s, err := New(cloud.Deps{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.State.stores.CloseAll() })
	r := fakeRuntime()
	r.startTimeout, r.execTimeout = 2*time.Second, 2*time.Second
	r.dyn.(*dynamicfake.FakeDynamicClient).PrependReactor("create", "pods",
		func(a k8stesting.Action) (bool, k8sruntime.Object, error) {
			pod := a.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured)
			_ = unstructured.SetNestedField(pod.Object, "Running", "status", "phase")
			return false, nil, nil
		})
	fs := &files{rt: r, got: map[string]map[string]string{}}
	r.str = fs
	s.State.rt = r
	books := &ledger{}
	s.State.debit = books.emit

	prior := balanceCents
	t.Cleanup(func() { balanceCents = prior })
	balanceCents = func(context.Context, string, string) (int64, error) { return 1 << 40, nil }
	return s, fs, books
}

// A scratch lease that asked for a disk writes a file, is parked — no pod, the
// row kept — and a lease naming it gives it a NEW pod under the SAME id, where the
// file still is. The disk is its own, and the size its working directory had.
func TestAParkedDiskComesBackWithItsFiles(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s, _, _ := parking(t)
		ctx := context.Background()

		m, err := Lease(s, ctx, "acme", account.Org("acme"), false, "", Spec{Class: "dev", Disk: true})
		if err != nil {
			t.Fatalf("lease: %v", err)
		}
		if m.Project != "" || m.Volume != diskName(m.ID) {
			t.Fatalf("a scratch lease that asked for a disk must own one named by it, got %+v", m)
		}
		claim, err := s.State.rt.dyn.Resource(k8s.Volumes).Namespace(s.State.rt.ns).Get(ctx, m.Volume, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("the disk was not made: %v", err)
		}
		if size, _, _ := unstructured.NestedString(claim.Object, "spec", "resources", "requests", "storage"); size != "2Gi" {
			t.Fatalf("the disk asks for %q, want the working directory's 2Gi", size)
		}
		if _, _, err := Write(s, ctx, "acme", m.ID, "notes.md", []byte("half done")); err != nil {
			t.Fatalf("write: %v", err)
		}

		parkedAs, err := Park(s, ctx, "acme", m.ID)
		if err != nil || parkedAs.Status != statusParked {
			t.Fatalf("park: %+v %v", parkedAs, err)
		}
		if _, err := s.State.rt.pods().Get(ctx, m.Pod, metav1.GetOptions{}); err == nil {
			t.Fatal("a parked sandbox still has its pod")
		}
		if _, err := Read(s, ctx, "acme", m.ID, "notes.md"); err == nil {
			t.Fatal("a parked sandbox answered a read instead of refusing it")
		}

		back, err := Lease(s, ctx, "acme", account.Org("acme"), false, "", Spec{ID: m.ID, Class: "dev", Disk: true})
		if err != nil {
			t.Fatalf("resume by lease: %v", err)
		}
		if back.ID != m.ID || back.Status != statusRunning {
			t.Fatalf("a lease naming a parked sandbox must give it back running, got %+v", back)
		}
		if back.Pod == m.Pod {
			t.Fatalf("the resumed pod reuses the name %q; a pod name is never reused", back.Pod)
		}
		e, err := Read(s, ctx, "acme", m.ID, "notes.md")
		if err != nil || string(e.Data) != "half done" {
			t.Fatalf("the file written before the park is gone: %q %v", e.Data, err)
		}
	})
}

// Parking settles compute up to the park, and a resume moves the runtime clock
// over the parked hours without billing them; the disk is billed for all of them.
func TestAParkedSandboxIsNeverBilledForCompute(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s, _, books := parking(t)
		ctx := context.Background()
		st, err := storeFor(s, "acme")
		if err != nil {
			t.Fatal(err)
		}
		day := time.Now().Unix() - 20*3600
		m := Sandbox{ID: "m_kept", Org: "acme", Kind: KindSandbox, Class: "dev", Status: statusParked,
			Volume: diskName("m_kept"), Pod: podName("m_kept"), Payer: "acme",
			MeteredAt: day, DiskMeteredAt: day, CreatedAt: day, LastUsedAt: day, ExpiresAt: day + 900}
		if err := st.Put(ctx, m); err != nil {
			t.Fatal(err)
		}
		meterRuntime(ctx, s)
		if n := books.len(); n != 0 {
			t.Fatalf("the meter billed a parked sandbox %d times", n)
		}

		if _, err := Lease(s, ctx, "acme", account.Org("acme"), false, "", Spec{ID: m.ID}); err != nil {
			t.Fatalf("resume: %v", err)
		}
		back, err := st.Get(ctx, "acme", m.ID)
		if err != nil {
			t.Fatal(err)
		}
		if back.MeteredAt < time.Now().Unix()-60 {
			t.Fatalf("the runtime clock was left at the park (%d): the first tick bills twenty parked hours as compute", back.MeteredAt)
		}
		if back.DiskMeteredAt != day {
			t.Fatalf("the resume moved the disk's clock to %d; the disk was held the whole time", back.DiskMeteredAt)
		}
		if back.ExpiresAt <= time.Now().Unix() || back.CreatedAt < time.Now().Unix()-60 {
			t.Fatalf("a resume is a new lease from now, got created %d expires %d", back.CreatedAt, back.ExpiresAt)
		}

		// The first tick after the resume: twenty hours of disk, and compute only for
		// the seconds since the resume — not for one parked hour.
		meterRuntime(ctx, s)
		var compute, disk int64
		for _, u := range books.rows {
			if strings.HasSuffix(u.Model, "/disk") {
				disk += u.AmountMicros
			} else {
				compute += u.AmountMicros
			}
		}
		if compute > rate(ctx, "dev")/60 {
			t.Fatalf("the tick after a resume billed %d µ$ of compute — the parked hours, billed as a pod", compute)
		}
		if want := 19 * storageRate(ctx) * gib(diskSize(m)); disk < want {
			t.Fatalf("the disk was held twenty hours and billed %d µ$, want at least %d", disk, want)
		}
	})
}

// A parked sandbox is its org's. Another org naming its id gets a sandbox of its
// own and leaves this one parked; parking it from another org is a 404.
func TestAnotherOrgCannotResumeOrParkASandbox(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s, _, _ := parking(t)
		ctx := context.Background()
		m, err := Lease(s, ctx, "acme", account.Org("acme"), false, "", Spec{Class: "dev", Disk: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Park(s, ctx, "acme", m.ID); err != nil {
			t.Fatal(err)
		}

		theirs, err := Lease(s, ctx, "evil", account.Org("evil"), false, "", Spec{ID: m.ID, Class: "dev"})
		if err != nil {
			t.Fatalf("lease: %v", err)
		}
		if theirs.ID == m.ID {
			t.Fatal("another org resumed acme's parked sandbox by naming it")
		}
		still, err := Get(s, ctx, "acme", m.ID)
		if err != nil || still.Status != statusParked {
			t.Fatalf("acme's sandbox is %+v (%v), want still parked", still, err)
		}
		if _, err := Park(s, ctx, "evil", m.ID); err == nil || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("another org parked acme's sandbox: %v", err)
		}
	})
}

// The reaper keeps a park for parkHold and not a minute past it — and then the
// lease is retired and the disk it owned goes with it.
func TestTheReaperRetiresAParkAfterItsHold(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s, _, _ := parking(t)
		ctx := context.Background()
		m, err := Lease(s, ctx, "acme", account.Org("acme"), false, "", Spec{Class: "dev", Disk: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Park(s, ctx, "acme", m.ID); err != nil {
			t.Fatal(err)
		}
		st, err := storeFor(s, "acme")
		if err != nil {
			t.Fatal(err)
		}
		age := func(d time.Duration) {
			t.Helper()
			row, err := st.Get(ctx, "acme", m.ID)
			if err != nil {
				t.Fatal(err)
			}
			row.LastUsedAt = time.Now().Add(-d).Unix()
			if err := st.Put(ctx, row); err != nil {
				t.Fatal(err)
			}
		}
		disk := func() bool {
			_, err := s.State.rt.dyn.Resource(k8s.Volumes).Namespace(s.State.rt.ns).Get(ctx, m.Volume, metav1.GetOptions{})
			return err == nil
		}

		// Long past every pod clock — the idle allowance, the lease, the ceiling — and
		// inside the hold: kept.
		age(parkHold - time.Minute)
		sweep(ctx, s)
		if row, err := st.Get(ctx, "acme", m.ID); err != nil || !parked(row) || !disk() {
			t.Fatalf("a park inside its hold was retired: %+v %v disk=%v", row, err, disk())
		}

		age(parkHold + time.Minute)
		sweep(ctx, s)
		if _, err := st.Get(ctx, "acme", m.ID); err == nil {
			t.Fatal("a park past its hold is still kept")
		}
		if disk() {
			t.Fatal("the lease was retired and the disk it owned was left behind")
		}
	})
}

// A park the reaper took is the short clock. An explicit pause, aged past that
// same moment, is still kept.
func TestTheReaperRetiresAFailedParkFirst(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s, _, _ := parking(t)
		ctx := context.Background()
		failed, err := Lease(s, ctx, "acme", account.Org("acme"), false, "", Spec{Class: "dev", Disk: true})
		if err != nil {
			t.Fatal(err)
		}
		kept, err := Lease(s, ctx, "acme", account.Org("acme"), false, "", Spec{Class: "dev", Disk: true})
		if err != nil {
			t.Fatal(err)
		}
		st, err := storeFor(s, "acme")
		if err != nil {
			t.Fatal(err)
		}
		row, err := st.Get(ctx, "acme", failed.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := park(s, ctx, st, row, "idle-disconnected"); err != nil {
			t.Fatal(err)
		}
		if _, err := Park(s, ctx, "acme", kept.ID); err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{failed.ID, kept.ID} {
			got, err := st.Get(ctx, "acme", id)
			if err != nil {
				t.Fatal(err)
			}
			got.LastUsedAt = time.Now().Add(-(failedParkHold + time.Minute)).Unix()
			if err := st.Put(ctx, got); err != nil {
				t.Fatal(err)
			}
		}
		sweep(ctx, s)
		if _, err := st.Get(ctx, "acme", failed.ID); err == nil {
			t.Fatal("a reaper park past the short hold is still kept")
		}
		if row, err := st.Get(ctx, "acme", kept.ID); err != nil || !parked(row) {
			t.Fatalf("an explicit pause was retired with the failed park: %+v %v", row, err)
		}
	})
}

// Ending a lease takes the disk it owned, as a retirement does; a project's disk
// outlives it (volume_test.go).
func TestEndingALeaseTakesTheDiskItOwned(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s, _, _ := parking(t)
		ctx := context.Background()
		m, err := Lease(s, ctx, "acme", account.Org("acme"), false, "", Spec{Class: "dev", Disk: true})
		if err != nil {
			t.Fatal(err)
		}
		if err := End(s, ctx, "acme", m.ID, false); err != nil {
			t.Fatal(err)
		}
		if _, err := s.State.rt.dyn.Resource(k8s.Volumes).Namespace(s.State.rt.ns).Get(ctx, m.Volume, metav1.GetOptions{}); err == nil {
			t.Fatal("the lease ended and the disk it owned was left behind")
		}
	})
}

// pods is how many sandbox pods the fake cluster holds for one sandbox.
func pods(t *testing.T, s *Service, id string) int {
	t.Helper()
	list, err := s.State.rt.pods().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, p := range list.Items {
		if p.GetLabels()[labSandbox] == id {
			n++
		}
	}
	return n
}

// Resumes that race start ONE pod between them. Each resume mints its pod a
// fresh name, so nothing in the cluster would refuse a second; the row is taken
// by compare-and-set, one resume wins, and the rest are refused — otherwise every
// extra resume is a pod the row does not name, which nothing bills, counts or stops.
func TestResumesThatRaceStartOnePod(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s, _, _ := parking(t)
		ctx := context.Background()
		m, err := Lease(s, ctx, "acme", account.Org("acme"), false, "", Spec{Class: "dev", Disk: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Park(s, ctx, "acme", m.ID); err != nil {
			t.Fatal(err)
		}

		const n = 5
		var wg sync.WaitGroup
		won := make(chan string, n)
		for range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if back, err := Lease(s, ctx, "acme", account.Org("acme"), false, "", Spec{ID: m.ID}); err == nil {
					won <- back.ID
				}
			}()
		}
		wg.Wait()
		close(won)
		wins := 0
		for id := range won {
			wins++
			if id != m.ID {
				t.Fatalf("a racing resume minted %s instead of resuming %s", id, m.ID)
			}
		}
		if wins < 1 {
			t.Fatal("no resume won")
		}
		if got := pods(t, s, m.ID); got != 1 {
			t.Fatalf("%d racing resumes left %d pods for one sandbox, want 1", n, got)
		}
		if _, err := Park(s, ctx, "acme", m.ID); err != nil {
			t.Fatal(err)
		}
		if got := pods(t, s, m.ID); got != 0 {
			t.Fatalf("parked again and %d pods are still running", got)
		}
	})
}

// A pod that did not stop is not parked: the row stays as it was, billed and
// reaped as it was, rather than answering to the day-long hold with a pod behind it.
func TestAParkWhosePodDidNotStopIsRefused(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s, _, _ := parking(t)
		ctx := context.Background()
		m, err := Lease(s, ctx, "acme", account.Org("acme"), false, "", Spec{Class: "dev", Disk: true})
		if err != nil {
			t.Fatal(err)
		}
		s.State.rt.dyn.(*dynamicfake.FakeDynamicClient).PrependReactor("delete", "pods",
			func(k8stesting.Action) (bool, k8sruntime.Object, error) {
				return true, nil, errors.New("apiserver unavailable")
			})
		if _, err := Park(s, ctx, "acme", m.ID); err == nil {
			t.Fatal("a park whose pod did not stop succeeded")
		}
		if row, err := Get(s, ctx, "acme", m.ID); err != nil || row.Status != statusRunning {
			t.Fatalf("the row is %+v (%v), want still running", row, err)
		}
	})
}

// A parked project sandbox still holds its project's disk, so a second sandbox
// on the project is refused by name — two rows would bill one disk twice — and
// the parked one resumes.
func TestAParkedProjectSandboxStillHoldsItsDisk(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s, _, _ := parking(t)
		ctx := context.Background()
		a, err := Lease(s, ctx, "acme", account.Org("acme"), false, "", Spec{Class: "dev", Project: "shop"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Park(s, ctx, "acme", a.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := Lease(s, ctx, "acme", account.Org("acme"), false, "", Spec{Class: "dev", Project: "shop"}); err == nil ||
			!strings.Contains(err.Error(), a.ID) {
			t.Fatalf("a second sandbox took a parked project's disk: %v", err)
		}
		if back, err := Lease(s, ctx, "acme", account.Org("acme"), false, "", Spec{ID: a.ID}); err != nil || back.ID != a.ID {
			t.Fatalf("the parked project sandbox did not resume: %+v %v", back, err)
		}
	})
}

// A running sandbox with a disk of its own whose time is up is PARKED by the
// reaper, not ended: its pod goes and its disk and row stay, so a resume finds its
// files. A project sandbox whose time is up is ended as before, its disk kept by
// the project.
func TestTheReaperParksAnIdleSandboxThatOwnsItsDisk(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s, _, _ := parking(t)
		ctx := context.Background()
		own, err := Lease(s, ctx, "acme", account.Org("acme"), false, "", Spec{Class: "dev", Disk: true})
		if err != nil {
			t.Fatal(err)
		}
		proj, err := Lease(s, ctx, "acme", account.Org("acme"), false, "", Spec{Class: "dev", Project: "site"})
		if err != nil {
			t.Fatal(err)
		}
		st, err := storeFor(s, "acme")
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{own.ID, proj.ID} {
			row, err := st.Get(ctx, "acme", id)
			if err != nil {
				t.Fatal(err)
			}
			row.LastUsedAt = time.Now().Add(-2 * s.State.clk.connected).Unix()
			if err := st.Put(ctx, row); err != nil {
				t.Fatal(err)
			}
		}

		sweep(ctx, s)

		row, err := st.Get(ctx, "acme", own.ID)
		if err != nil || !parked(row) {
			t.Fatalf("an idle sandbox that owns its disk was not parked: %+v %v", row, err)
		}
		if pods(t, s, own.ID) != 0 {
			t.Fatal("a parked sandbox still holds a pod")
		}
		if _, err := s.State.rt.dyn.Resource(k8s.Volumes).Namespace(s.State.rt.ns).Get(ctx, own.Volume, metav1.GetOptions{}); err != nil {
			t.Fatalf("a parked sandbox lost its disk: %v", err)
		}
		if _, err := st.Get(ctx, "acme", proj.ID); err == nil {
			t.Fatal("an idle project sandbox was kept; its disk outlives an end")
		}

		back, err := Lease(s, ctx, "acme", account.Org("acme"), false, "", Spec{ID: own.ID})
		if err != nil || back.ID != own.ID || back.Status != statusRunning {
			t.Fatalf("the parked sandbox did not resume as itself: %+v %v", back, err)
		}

	})
}

// A free lease bills nobody, so the reaper keeps no disk of its for a resume:
// it ends, and its disk with it.
func TestTheReaperEndsAFreeSandboxThatOwnsItsDisk(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s, _, _ := parking(t)
		broke(t, &client.Allowance{Plan: "free", Limit: 20, Used: 1, Window: "day"}, nil)
		ctx := context.Background()
		free, err := Lease(s, ctx, "acme", account.Org("acme"), false, "", Spec{Class: "dev", Disk: true})
		if err != nil || free.Payer != "" {
			t.Fatalf("the free lane did not lease: %+v %v", free, err)
		}
		st, err := storeFor(s, "acme")
		if err != nil {
			t.Fatal(err)
		}
		row, err := st.Get(ctx, "acme", free.ID)
		if err != nil {
			t.Fatal(err)
		}
		row.LastUsedAt = time.Now().Add(-2 * s.State.clk.connected).Unix()
		if err := st.Put(ctx, row); err != nil {
			t.Fatal(err)
		}

		sweep(ctx, s)

		if row, err := st.Get(ctx, "acme", free.ID); err == nil {
			t.Fatalf("an idle free sandbox was kept, holding a disk nobody pays for: %+v", row)
		}
		if _, err := s.State.rt.dyn.Resource(k8s.Volumes).Namespace(s.State.rt.ns).Get(ctx, free.Volume, metav1.GetOptions{}); err == nil {
			t.Fatal("an ended free sandbox left its disk behind")
		}
	})
}

// Resuming a parked desktop sandbox must not re-charge the one-shot startup fee
// (ResourceFee) that was already billed on initial lease creation.
func TestResumeParkedSandboxDoesNotRebillStartupFee(t *testing.T) {
	t.Setenv("SANDBOX_FEE_CENTS_DESKTOP", "25")
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		t.Setenv("CLOUD_RUN_DIR", kit.Sockets(t))
		cloud.ResetPlane()
		t.Cleanup(cloud.ResetPlane)
		var debits []string
		var mu sync.Mutex
		cloud.Plane().Post("/finance/record", func(_ context.Context, in *client.RecordIn) (*client.Recorded, error) {
			mu.Lock()
			debits = append(debits, in.Amount.Decimal)
			mu.Unlock()
			return &client.Recorded{Amount: in.Amount}, nil
		}, zip.WithOperationID(client.FinanceRecord))
		stop, err := cloud.ServePlane("commerce", luxlog.NewNoOpLogger())
		if err != nil {
			t.Fatalf("serve commerce plane: %v", err)
		}
		t.Cleanup(func() { _ = stop() })

		s, _, _ := parking(t)
		mc, err := meter.New(meter.Config{BaseURL: "http://127.0.0.1:1", Timeout: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		s.Bill = cloud.NewMeter(cloud.Deps{Metering: mc}, "sandbox")
		prevAllow := ledgerAllows
		t.Cleanup(func() { ledgerAllows = prevAllow })
		ledgerAllows = func(*Service, context.Context, account.Account, int64) error { return nil }
		ctx := context.Background()

		m, err := Lease(s, ctx, "acme", account.Org("acme"), false, "", Spec{Class: "desktop", Disk: true})
		if err != nil {
			t.Fatalf("lease desktop: %v", err)
		}
		if _, err := Park(s, ctx, "acme", m.ID); err != nil {
			t.Fatalf("park desktop: %v", err)
		}
		if _, err := Lease(s, ctx, "acme", account.Org("acme"), false, "", Spec{ID: m.ID, Class: "desktop", Disk: true}); err != nil {
			t.Fatalf("resume desktop: %v", err)
		}
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			n := len(debits)
			mu.Unlock()
			if n >= 1 {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		time.Sleep(50 * time.Millisecond)
		mu.Lock()
		defer mu.Unlock()
		if len(debits) != 1 || debits[0] != "0.25" {
			t.Fatalf("desktop lease+park+resume recorded startup fee debits %v, want single [0.25]", debits)
		}
	})
}
