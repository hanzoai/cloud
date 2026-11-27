package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
)

// THE POD'S PID 1 REAPS WHAT ITS COMMANDS ORPHAN (hanzo-inc/cloud#553). Run as
// PID 1 of a pid namespace of its own, as a pod's container is, the reaper is
// handed a process whose parent exits at once; when that orphan ends it is gone
// from `ps`, not left `Z`. `sleep infinity` reaped nothing, and a harness killed
// mid `make test` left `[make] <defunct>` for the life of the pod.
//
// MUTATION: make the reaper `exec sleep infinity` and the orphan stays a zombie.
func TestThePodsFirstProcessReapsOrphans(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		if exec.Command("unshare", "--user", "--map-root-user", "--fork", "--pid", "--mount-proc", "true").Run() != nil ||
			exec.Command("ps", "-o", "pid=", "-p", "1").Run() != nil {
			t.Skip("this host cannot start a pid namespace of its own, or has no ps")
		}
		seen := filepath.Join(t.TempDir(), "ps")
		probe := `sh -c "sleep 0.2 & exit 0"; (sleep 1; ps -eo stat=,comm= >"$1.part" && mv "$1.part" "$1") & ` + reaper
		cmd := exec.Command("unshare", "--user", "--map-root-user", "--fork", "--pid", "--mount-proc", "sh", "-c", probe, "sh", seen)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		// PID 1 of a namespace ignores what it has no handler for; KILL ends it.
		t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); _ = cmd.Wait() })
		var got []byte
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
			if b, err := os.ReadFile(seen); err == nil {
				got = b
				break
			}
		}
		if got == nil {
			t.Fatal("the probe never listed the namespace's processes")
		}
		for _, line := range strings.Split(strings.TrimSpace(string(got)), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "Z") {
				t.Fatalf("an orphan was left a zombie under the pod's first process:\n%s", got)
			}
		}
	})
}
