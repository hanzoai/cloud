//go:build !windows

package cli

import (
	"os"
	"syscall"
)

// execEngine replaces the current process with hanzoai so signals (Ctrl-C) and
// the exit code flow straight through — `hanzo engine serve` becomes hanzoai.
func execEngine(bin string, args []string) error {
	return syscall.Exec(bin, args, os.Environ())
}
