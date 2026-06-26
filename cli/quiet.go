package cli

import "os"

// realStdout is the process's true stdout, captured at this package's init
// before the server-graph dependencies (iam/beego, kms) run their own init()
// functions — several of which emit warnings to stdout (e.g. the IAM registry
// signing-key loader). To keep the CLI's stdout machine-readable (so
// `hanzo apps list -o json | jq` is never corrupted by a dependency's startup
// chatter), this init redirects stdout to stderr for the duration of process
// initialization; RestoreStdout puts the real stdout back before any command
// writes a byte.
//
// This is best-effort: it only helps when this package initializes before the
// noisy dependency (cmd/hanzo imports cli first) AND that dependency reads the
// os.Stdout variable at log time rather than capturing it earlier. main always
// calls RestoreStdout, so correctness never depends on the redirect taking.
var realStdout = os.Stdout

func init() { os.Stdout = os.Stderr }

// RestoreStdout restores the real process stdout. cmd/hanzo calls this as its
// first statement so every command writes to the genuine stdout.
func RestoreStdout() { os.Stdout = realStdout }
