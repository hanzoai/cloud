package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeDelegate installs a script as the fabric CLI and points resolution at it
// through the documented override, so no test touches a real installation.
func fakeDelegate(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "hanzo-node")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HANZO_FABRIC_CLI", p)
	return p
}

// TestVersionOutputContract pins the whole of it: STDOUT is one line and only
// ever the answer, and the delegate is a stderr WARNING raised only when there
// is something to act on. The delegate used to print on stdout unconditionally
// — burying the answer under an internal detail, and handing delegateVersion (a
// first-line, last-token parser) a second line to trip over.
func TestVersionOutputContract(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*testing.T) string // returns the delegate path, "" for none
		warn  []string                // stderr substrings; nil demands silence
	}{
		{
			name: "no delegate is silent",
			setup: func(t *testing.T) string {
				t.Setenv("HANZO_FABRIC_CLI", "")
				t.Setenv("PATH", t.TempDir()) // nothing resolvable
				return ""
			},
		},
		{
			// Ours carries the `v`, the delegate's does not. Same build: silence.
			name:  "agreeing delegate is silent",
			setup: func(t *testing.T) string { return fakeDelegate(t, "echo hanzo 1.234.5") },
		},
		{
			name:  "stale delegate warns with both versions",
			setup: func(t *testing.T) string { return fakeDelegate(t, "echo hanzo 1.9.18") },
			warn:  []string{"stale delegate", "1.9.18", "1.234.5", "hanzo-node"},
		},
		{
			name:  "unreadable delegate warns",
			setup: func(t *testing.T) string { return fakeDelegate(t, "exit 1") },
			warn:  []string{"would not report a version"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func(v string) { Version = v }(Version)
			Version = "v1.234.5"
			path := tc.setup(t)

			var out, errOut bytes.Buffer
			root := newRootCmd()
			root.SetOut(&out)
			root.SetErr(&errOut)
			root.SetArgs([]string{"version"})
			if err := root.Execute(); err != nil {
				t.Fatalf("version: %v", err)
			}

			if out.String() != "hanzo v1.234.5\n" {
				t.Errorf("stdout = %q, want exactly %q", out.String(), "hanzo v1.234.5\n")
			}
			if tc.warn == nil {
				if errOut.Len() != 0 {
					t.Errorf("stderr = %q, want silence", errOut.String())
				}
				return
			}
			for _, want := range append(tc.warn, path) {
				if !strings.Contains(errOut.String(), want) {
					t.Errorf("stderr = %q, want it to mention %q", errOut.String(), want)
				}
			}
			if !strings.HasPrefix(errOut.String(), "warning:") {
				t.Errorf("stderr = %q, want it to lead with a warning", errOut.String())
			}
		})
	}
}
