//go:build unix

package main

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

// killGrace is how long a process group gets between TERM and KILL. Short: a
// sandbox is not a service being drained, and every second here is a second the
// warm pool thinks a busy box is idle.
const killGrace = 3 * time.Second

// A box runs on Linux. These functions are the whole platform-specific surface,
// split out so the rest of boxd stays portable enough to run its tests anywhere
// a developer happens to be.

// confine sets the two things that separate a child from boxd: its own process
// group, and — when configured — its own UID.
//
// THE UID IS THE ONE THAT MATTERS, and it is not obvious why, so:
//
// Linux lets a process read /proc/<pid>/environ for any process with the same
// UID. A child that shares boxd's UID can therefore read boxd's environment,
// which holds CODE_EXEC_API_KEY — and that is ONE SHARED KEY for the whole
// pool, so a box handing it to the code it runs hands that code every other
// tenant's box. Reproduced end to end: the child scanned /proc, recovered the
// key, and used it to write and read through the guarded API.
//
// Two defenses that look like they fix this and do not:
//
//   - scrubbing the key from the CHILD's env (which env() in proc.go does).
//     The child never needed it in its own env; it reads boxd's.
//   - os.Unsetenv in boxd. The kernel serves /proc/<pid>/environ from the
//     original stack block written at exec time, not from the process's
//     current environment, so the value stays readable for the life of the
//     process no matter what the runtime's copy says. Measured: after
//     os.Unsetenv, os.Getenv returns empty and /proc/self/environ still
//     contains the value.
//
// A different UID is what actually stops it — the kernel refuses the read —
// and the same separation is what stops the child reading the git credential
// file that git.go writes to /tmp during a push.
func confine(cmd *exec.Cmd, uid, gid int) {
	attr := &syscall.SysProcAttr{Setpgid: true}
	if uid > 0 {
		attr.Credential = &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}
	}
	cmd.SysProcAttr = attr
}

// What boxd creates things with. Group-writable, because a confined child runs
// as a DIFFERENT uid in the same group and has to be able to edit what boxd
// wrote — otherwise `write_file` produces a file the agent's own `sed` cannot
// touch. This is one value rather than a conditional because umask already is
// the conditional: shareWith sets 002 when there is separation, and the
// inherited 022 clears the group bit back off when there is not, reproducing
// the old 0644/0755 exactly.
const (
	dirMode  = os.FileMode(0o775)
	fileMode = os.FileMode(0o664)
)

// shareWith makes the workdir writable by both boxd and the confined child:
// owned by the child's uid/gid, setgid so everything created under it inherits
// the group, and a group-writable umask so files boxd writes stay editable by
// the child (and the reverse, since boxd keeps its own uid).
func shareWith(dir string, uid, gid int) error {
	if uid <= 0 {
		return nil
	}
	if err := os.Chown(dir, uid, gid); err != nil {
		return err
	}
	// os.ModeSetgid, NOT the octal 0o2000 a chmod(2) habit reaches for. Go's
	// FileMode is its own bit layout — setgid is 1<<21 — so os.Chmod(dir,
	// 0o2775) sets 0775 and silently DROPS the setgid bit. Observed: the
	// directory came out drwxrwxr-x, files boxd wrote were group `root` instead
	// of the child's group, and the child got permission denied on its own
	// project while every mode literal in the source looked correct.
	if err := os.Chmod(dir, os.FileMode(0o775)|os.ModeSetgid); err != nil {
		return err
	}
	syscall.Umask(0o002)
	return nil
}

// killGroup signals the child's entire process group — negative pid is the
// group form. Anything the submitted code forked dies with it, which is the
// only version of "timeout" that means anything in a sandbox.
func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	pgid := -cmd.Process.Pid
	if err := syscall.Kill(pgid, syscall.SIGTERM); err != nil {
		// The group is already gone, or we are not on a system that has one.
		// Either way the direct kill below is the honest fallback.
		return cmd.Process.Kill()
	}
	// Go's WaitDelay only reaches the direct process. The grandchildren — which
	// are the ones actually holding the pipe open — need the group KILL, so
	// schedule it here.
	time.AfterFunc(killGrace, func() { _ = syscall.Kill(pgid, syscall.SIGKILL) })
	return nil
}
