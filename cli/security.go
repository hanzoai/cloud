package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hanzoai/cloud/clients/security/detect"
	"github.com/spf13/cobra"
)

// `hanzo security scan` is the LOCAL guardrail-at-generation: it walks a path,
// runs the SAME detect engine the /v1/security server surface uses (one engine,
// two surfaces — see clients/security/detect), and exits non-zero when a finding
// at or above the fail threshold is present. No server, no auth, no network — so
// it drops straight into a pre-commit hook, a CI step, or an agent's shell the
// moment code is written. It never prints a raw secret: findings carry the
// engine's masked preview only.

// skipDirs are never descended into — vendored / generated / VCS trees that
// would drown real findings in noise (and, for node_modules, minified blobs
// that trip entropy heuristics).
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "dist": true,
	"build": true, "out": true, ".vscode-test": true, "target": true,
	".next": true, "__pycache__": true, ".venv": true, "venv": true,
}

// maxScanFileBytes caps a single file read; a source file over this is almost
// certainly a data/blob artifact, not code a human wrote a secret into.
const maxScanFileBytes = 2 << 20 // 2 MiB

func newSecurityCmd(envOf func() *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "security",
		Short: "Code-security tools (local secret scanning)",
		// No PersistentPreRunE override: the root's runs, resolving config/creds
		// into env so envOf() (used for -o json) is non-nil. It is local-only —
		// scan requires no login or network.
	}

	var failOn string
	scan := &cobra.Command{
		Use:   "scan [path ...]",
		Short: "Scan files/directories for hardcoded secrets (default: current dir)",
		Long: "Walk each path and report hardcoded secrets using the native Hanzo\n" +
			"detection engine (the same one behind /v1/security). Exits non-zero when a\n" +
			"finding at or above --fail-on is present, so it gates a pre-commit hook or CI\n" +
			"step. Secrets are never printed — only a masked preview.",
		// We print our own findings + a clean "N secrets" error; no cobra usage
		// dump on a policy failure.
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			paths := args
			if len(paths) == 0 {
				paths = []string{"."}
			}
			floor := strings.ToLower(strings.TrimSpace(failOn))
			if floor == "" {
				floor = "low"
			}
			if floor != "none" && detect.SeverityRank(floor) == 0 {
				return fmt.Errorf("invalid --fail-on %q (want critical|high|medium|low|none)", failOn)
			}

			findings, scanned, err := scanPaths(paths)
			if err != nil {
				return err
			}

			e := envOf()
			result := scanResult{
				FilesScanned: scanned,
				Findings:     findings,
				Summary:      tally(findings),
			}
			if err := e.emit(result, func(w io.Writer) { renderScan(w, result) }); err != nil {
				return err
			}

			// Policy gate: fail when any finding is at/above the floor.
			if floor != "none" {
				bad := 0
				for _, f := range findings {
					if detect.SeverityRank(f.Severity) >= detect.SeverityRank(floor) {
						bad++
					}
				}
				if bad > 0 {
					return fmt.Errorf("%d secret(s) at or above %q severity", bad, floor)
				}
			}
			return nil
		},
	}
	scan.Flags().StringVar(&failOn, "fail-on", "low",
		"minimum severity that fails the command: critical|high|medium|low|none")

	rules := &cobra.Command{
		Use:   "rules",
		Short: "List the detection rules the scanner applies",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			e := envOf()
			rv := detect.Rules()
			return e.emit(rv, func(w io.Writer) {
				tw := newTab(w)
				fmt.Fprintln(tw, "SEVERITY\tID\tNAME")
				for _, r := range rv {
					fmt.Fprintf(tw, "%s\t%s\t%s\n", r.Severity, r.ID, r.Name)
				}
				tw.Flush()
				fmt.Fprintf(w, "\n%d rules\n", len(rv))
			})
		},
	}

	cmd.AddCommand(scan, rules)
	return cmd
}

// scanFinding is a CLI finding: the engine finding with the file path it was
// found in (the engine echoes the path it was handed, which is what we want).
type scanResult struct {
	FilesScanned int              `json:"filesScanned"`
	Findings     []detect.Finding `json:"findings"`
	Summary      scanSummary      `json:"summary"`
}

type scanSummary struct {
	Total    int `json:"total"`
	Critical int `json:"critical"`
	High     int `json:"high"`
	Medium   int `json:"medium"`
	Low      int `json:"low"`
}

func tally(fs []detect.Finding) scanSummary {
	var s scanSummary
	for _, f := range fs {
		s.Total++
		switch f.Severity {
		case detect.SeverityCritical:
			s.Critical++
		case detect.SeverityHigh:
			s.High++
		case detect.SeverityMedium:
			s.Medium++
		case detect.SeverityLow:
			s.Low++
		}
	}
	return s
}

// scanPaths walks each path and runs the engine over every readable text file,
// returning findings sorted worst-first (by severity, then path, then line).
func scanPaths(paths []string) ([]detect.Finding, int, error) {
	var findings []detect.Finding
	scanned := 0
	for _, root := range paths {
		info, err := os.Stat(root)
		if err != nil {
			return nil, 0, fmt.Errorf("stat %q: %w", root, err)
		}
		if !info.IsDir() {
			fs, ok := scanOneFile(root)
			if ok {
				scanned++
				findings = append(findings, fs...)
			}
			continue
		}
		walkErr := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil // unreadable entry — skip, don't abort the whole walk
			}
			if d.IsDir() {
				if skipDirs[d.Name()] {
					return filepath.SkipDir
				}
				return nil
			}
			if fs, ok := scanOneFile(p); ok {
				scanned++
				findings = append(findings, fs...)
			}
			return nil
		})
		if walkErr != nil {
			return nil, 0, fmt.Errorf("walk %q: %w", root, walkErr)
		}
	}
	sort.SliceStable(findings, func(i, j int) bool {
		a, b := findings[i], findings[j]
		if ra, rb := detect.SeverityRank(a.Severity), detect.SeverityRank(b.Severity); ra != rb {
			return ra > rb
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Line < b.Line
	})
	return findings, scanned, nil
}

// scanOneFile reads a file (bounded, text-only) and runs the engine. Returns
// ok=false for a file that was skipped (too big, binary, unreadable) so it is
// not counted as scanned.
func scanOneFile(path string) ([]detect.Finding, bool) {
	info, err := os.Stat(path)
	if err != nil || info.Size() > maxScanFileBytes {
		return nil, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	if isBinary(b) {
		return nil, false
	}
	return detect.ScanContent(path, string(b)), true
}

// isBinary reports whether b looks like a non-text blob — a NUL byte in the
// first 8 KiB is the same heuristic git uses. Skipping binaries avoids both
// false positives and wasted work on assets.
func isBinary(b []byte) bool {
	n := len(b)
	if n > 8192 {
		n = 8192
	}
	for i := 0; i < n; i++ {
		if b[i] == 0 {
			return true
		}
	}
	return false
}

func renderScan(w io.Writer, r scanResult) {
	if len(r.Findings) == 0 {
		fmt.Fprintf(w, "✓ no secrets found (%d files scanned)\n", r.FilesScanned)
		return
	}
	tw := newTab(w)
	fmt.Fprintln(tw, "SEVERITY\tRULE\tLOCATION\tPREVIEW")
	for _, f := range r.Findings {
		fmt.Fprintf(tw, "%s\t%s\t%s:%d\t%s\n", f.Severity, f.RuleID, f.Path, f.Line, f.Preview)
	}
	tw.Flush()
	fmt.Fprintf(w, "\n%d finding(s) in %d files  (critical=%d high=%d medium=%d low=%d)\n",
		r.Summary.Total, r.FilesScanned, r.Summary.Critical, r.Summary.High,
		r.Summary.Medium, r.Summary.Low)
}
