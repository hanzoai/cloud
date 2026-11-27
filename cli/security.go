package cli

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/security/detect"
	"github.com/spf13/cobra"
)

// `hanzo security scan` is the LOCAL + CI guardrail: it walks working-tree paths,
// staged diffs (`--staged`), or full/range git commit history (`--git`,
// `--since-commit`, `--branch`) using the SAME detect engine the /v1/security
// server surface uses (one engine, two surfaces — see apps/security/detect),
// supports opt-in read-only credential verification (`--verify`), SHA-256
// fingerprint allowlists (`.hanzo-security-ignore`), GitHub Actions annotations
// (`--github-actions`), and redacted telemetry into Hanzo Insights (`--insights`).
// It never prints, logs, or transmits a raw secret.

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
const (
	maxScanFileBytes  = 2 << 20 // 2 MiB
	defaultIgnoreFile = ".hanzo-security-ignore"
	verifyProbeTimout = 5 * time.Second
)

// verifyEndpoints holds the read-only identity endpoints `--verify` probes.
// Package-level so tests can point probes at local httptest servers.
var verifyEndpoints = struct {
	github       string
	huggingface  string
	digitalocean string
	slack        string
	hanzo        string
}{
	github:       "https://api.github.com/user",
	huggingface:  "https://huggingface.co/api/whoami-v2",
	digitalocean: "https://api.digitalocean.com/v2/account",
	slack:        "https://slack.com/api/auth.test",
	hanzo:        "https://api.hanzo.ai/v1/iam/userinfo",
}

type scanOpts struct {
	failOn        string
	gitHistory    bool
	sinceCommit   string
	branch        string
	staged        bool
	verify        bool
	onlyVerified  bool
	ignoreFile    string
	githubActions bool
	insights      bool
	report        bool
}

func newSecurityCmd(envOf func() *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "security",
		Short: "Code-security tools (local + git-history secret scanning & Insights)",
	}

	var opts scanOpts
	scan := &cobra.Command{
		Use:   "scan [path ...]",
		Short: "Scan files, staged changes, or git history for hardcoded secrets",
		Long: "Scan working-tree paths, staged diffs (--staged), or git commit history\n" +
			"(--git --since-commit <sha> --branch <ref>) using the native Hanzo detection\n" +
			"engine (the same catalog behind /v1/security). Supports SHA-256 allowlists\n" +
			"(.hanzo-security-ignore), opt-in read-only credential verification (--verify),\n" +
			"GitHub Actions annotations (--github-actions), and redacted Hanzo Insights\n" +
			"telemetry (--insights). Raw secrets are never printed or transmitted.",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			start := time.Now()
			paths := args
			if len(paths) == 0 {
				paths = []string{"."}
			}
			floor := strings.ToLower(strings.TrimSpace(opts.failOn))
			if floor == "" {
				floor = "low"
			}
			if floor != "none" && detect.SeverityRank(floor) == 0 {
				return fmt.Errorf("invalid --fail-on %q (want critical|high|medium|low|none)", opts.failOn)
			}
			if opts.onlyVerified {
				opts.verify = true
			}
			if opts.sinceCommit != "" || opts.branch != "" {
				opts.gitHistory = true
			}
			if opts.gitHistory && opts.staged {
				return fmt.Errorf("cannot combine --git and --staged")
			}

			ignored, err := loadIgnoreFingerprints(opts.ignoreFile, paths)
			if err != nil {
				return err
			}

			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			var verifier func(*detect.Finding, string)
			if opts.verify {
				verifier = newLiveVerifier(ctx)
			}

			var (
				findings       []detect.Finding
				filesScanned   int
				commitsScanned int
				mode           = "files"
			)
			switch {
			case opts.staged:
				mode = "staged"
				findings, filesScanned, err = scanGitStaged(ctx, paths[0], verifier)
			case opts.gitHistory:
				mode = "git"
				findings, filesScanned, commitsScanned, err = scanGitHistory(ctx, paths[0], opts.sinceCommit, opts.branch, verifier)
			default:
				findings, filesScanned, err = scanPathsWithVerifier(paths, verifier)
			}
			if err != nil {
				return err
			}

			// Filter allowlisted SHA-256 fingerprints and optional --only-verified gate.
			filtered := make([]detect.Finding, 0, len(findings))
			for _, f := range findings {
				if ignored[strings.ToLower(f.Fingerprint)] {
					continue
				}
				if opts.onlyVerified && (f.Verified == nil || !*f.Verified) {
					continue
				}
				filtered = append(filtered, f)
			}
			findings = filtered

			durMs := time.Since(start).Milliseconds()
			e := envOf()
			result := scanResult{
				FilesScanned:   filesScanned,
				CommitsScanned: commitsScanned,
				Findings:       findings,
				Summary:        tally(findings),
			}

			if opts.githubActions {
				emitGitHubAnnotations(cmd.OutOrStdout(), findings)
			}
			if err := e.emit(result, func(w io.Writer) { renderScan(w, result) }); err != nil {
				return err
			}

			if opts.insights || opts.report || strings.TrimSpace(os.Getenv("HANZO_INSIGHTS_API_KEY")) != "" {
				emitSecurityInsights(ctx, cmd.ErrOrStderr(), e, paths[0], mode, opts, result, durMs)
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
	scan.Flags().StringVar(&opts.failOn, "fail-on", "low",
		"minimum severity that fails the command: critical|high|medium|low|none")
	scan.Flags().BoolVar(&opts.gitHistory, "git", false,
		"scan git commit history patches instead of working-tree files")
	scan.Flags().StringVar(&opts.sinceCommit, "since-commit", "",
		"base commit SHA for --git diff range (all-zero SHA scans reachable history)")
	scan.Flags().StringVar(&opts.branch, "branch", "",
		"git branch or ref to scan in --git mode (default HEAD)")
	scan.Flags().BoolVar(&opts.staged, "staged", false,
		"scan staged git changes (git diff --cached) for pre-commit hooks")
	scan.Flags().BoolVar(&opts.verify, "verify", false,
		"run opt-in read-only credential probes to check whether detected tokens are live")
	scan.Flags().BoolVar(&opts.onlyVerified, "only-verified", false,
		"report and fail only on credentials confirmed live by --verify")
	scan.Flags().StringVar(&opts.ignoreFile, "ignore-file", "",
		"path to SHA-256 fingerprint allowlist file (default .hanzo-security-ignore if present)")
	scan.Flags().BoolVar(&opts.githubActions, "github-actions", false,
		"emit GitHub Actions ::error workflow annotations (masked preview + sha256 only)")
	scan.Flags().BoolVar(&opts.insights, "insights", false,
		"emit redacted security.scan.completed telemetry to Hanzo Insights")
	scan.Flags().BoolVar(&opts.report, "report", false,
		"report redacted scan summary to Hanzo Insights / Cloud Security")

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

type scanResult struct {
	FilesScanned   int              `json:"filesScanned"`
	CommitsScanned int              `json:"commitsScanned,omitempty"`
	Findings       []detect.Finding `json:"findings"`
	Summary        scanSummary      `json:"summary"`
}

type scanSummary struct {
	Total    int `json:"total"`
	Critical int `json:"critical"`
	High     int `json:"high"`
	Medium   int `json:"medium"`
	Low      int `json:"low"`
	Verified int `json:"verified,omitempty"`
}

func tally(fs []detect.Finding) scanSummary {
	var s scanSummary
	for _, f := range fs {
		s.Total++
		if f.Verified != nil && *f.Verified {
			s.Verified++
		}
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

func scanPaths(paths []string) ([]detect.Finding, int, error) {
	return scanPathsWithVerifier(paths, nil)
}

// scanPathsWithVerifier walks each path and runs the engine over every readable
// text file, returning findings sorted worst-first (by severity, then path, then line).
func scanPathsWithVerifier(paths []string, verifier func(*detect.Finding, string)) ([]detect.Finding, int, error) {
	var findings []detect.Finding
	scanned := 0
	for _, root := range paths {
		info, err := os.Stat(root)
		if err != nil {
			return nil, 0, fmt.Errorf("stat %q: %w", root, err)
		}
		if !info.IsDir() {
			fs, ok := scanOneFile(root, verifier)
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
			if filepath.Base(p) == defaultIgnoreFile {
				return nil
			}
			if fs, ok := scanOneFile(p, verifier); ok {
				scanned++
				findings = append(findings, fs...)
			}
			return nil
		})
		if walkErr != nil {
			return nil, 0, fmt.Errorf("walk %q: %w", root, walkErr)
		}
	}
	sortSecurityFindings(findings)
	return findings, scanned, nil
}

func sortSecurityFindings(findings []detect.Finding) {
	sort.SliceStable(findings, func(i, j int) bool {
		a, b := findings[i], findings[j]
		if ra, rb := detect.SeverityRank(a.Severity), detect.SeverityRank(b.Severity); ra != rb {
			return ra > rb
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Commit < b.Commit
	})
}

// scanOneFile reads a file (bounded, text-only) and runs the engine. Returns
// ok=false for a file that was skipped (too big, binary, unreadable) so it is
// not counted as scanned.
func scanOneFile(path string, verifier func(*detect.Finding, string)) ([]detect.Finding, bool) {
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
	return detect.ScanMatches(path, string(b), verifier), true
}

func isBinary(b []byte) bool {
	n := min(len(b), 8192)
	for i := range n {
		if b[i] == 0 {
			return true
		}
	}
	return false
}

// ---- Git history & staged diff scanning ----

var hunkHeaderRE = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

func isAllZeroSHA(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return true
	}
	for i := 0; i < len(s); i++ {
		if s[i] != '0' {
			return false
		}
	}
	return true
}

func shouldSkipRelPath(rel string) bool {
	if rel == "" || rel == "/dev/null" {
		return true
	}
	if filepath.Base(rel) == defaultIgnoreFile {
		return true
	}
	for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
		if skipDirs[seg] {
			return true
		}
	}
	return false
}

func gitCommitExists(ctx context.Context, repoDir, rev string) bool {
	c := exec.CommandContext(ctx, "git", "-C", repoDir, "cat-file", "-e", rev+"^{commit}")
	return c.Run() == nil
}

func scanGitHistory(ctx context.Context, repoDir, sinceCommit, branch string, verifier func(*detect.Finding, string)) ([]detect.Finding, int, int, error) {
	ref := cmp.Or(strings.TrimSpace(branch), "HEAD")
	args := []string{"-C", repoDir, "log", "--root", "--format=COMMIT %H", "-p", "--no-color", "--no-ext-diff", "-U0"}
	if !isAllZeroSHA(sinceCommit) {
		since := strings.TrimSpace(sinceCommit)
		if gitCommitExists(ctx, repoDir, since) {
			args = append(args, since+".."+ref)
		} else {
			// Shallow clone or force-push where sinceCommit is unreachable: scan recent reachable history safely.
			args = append(args, "-n", "50", ref)
		}
	} else {
		args = append(args, ref)
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	out, err := cmd.Output()
	if err != nil {
		return nil, 0, 0, fmt.Errorf("git log in %q: %w", repoDir, err)
	}
	findings, files, commits := parseUnifiedDiffStream(bytes.NewReader(out), "", verifier)
	return findings, files, commits, nil
}

func scanGitStaged(ctx context.Context, repoDir string, verifier func(*detect.Finding, string)) ([]detect.Finding, int, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", repoDir, "diff", "--cached", "--no-color", "--no-ext-diff", "-U0")
	out, err := cmd.Output()
	if err != nil {
		return nil, 0, fmt.Errorf("git diff --cached in %q: %w", repoDir, err)
	}
	findings, files, _ := parseUnifiedDiffStream(bytes.NewReader(out), "staged", verifier)
	return findings, files, nil
}

func parseUnifiedDiffStream(r io.Reader, defaultCommit string, verifier func(*detect.Finding, string)) ([]detect.Finding, int, int) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), maxScanFileBytes)

	curCommit := defaultCommit
	curFile := ""
	skipFile := true
	curLine := 0

	commits := 0
	filesSeen := map[string]bool{}
	dedup := map[string]bool{}
	var findings []detect.Finding

	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "COMMIT "):
			curCommit = strings.TrimSpace(strings.TrimPrefix(line, "COMMIT "))
			curFile = ""
			skipFile = true
			commits++
		case strings.HasPrefix(line, "diff --git "):
			curFile = ""
			skipFile = true
		case strings.HasPrefix(line, "+++ "):
			rawPath := strings.TrimSpace(strings.TrimPrefix(line, "+++ "))
			rawPath = strings.TrimPrefix(rawPath, "b/")
			if shouldSkipRelPath(rawPath) {
				curFile = ""
				skipFile = true
			} else {
				curFile = rawPath
				skipFile = false
				filesSeen[curFile] = true
			}
		case strings.HasPrefix(line, "@@ "):
			if m := hunkHeaderRE.FindStringSubmatch(line); len(m) == 2 {
				curLine, _ = strconv.Atoi(m[1])
			}
		case !skipFile && strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			added := line[1:]
			matches := detect.ScanMatches(curFile, added, verifier)
			for _, f := range matches {
				f.Line = curLine
				f.Commit = curCommit
				key := f.RuleID + "\x00" + f.Path + "\x00" + strconv.Itoa(f.Line) + "\x00" + f.Fingerprint + "\x00" + f.Commit
				if !dedup[key] {
					dedup[key] = true
					findings = append(findings, f)
				}
			}
			curLine++
		case !skipFile && strings.HasPrefix(line, " "):
			curLine++
		}
	}
	sortSecurityFindings(findings)
	return findings, len(filesSeen), commits
}

// ---- Allowlist (.hanzo-security-ignore) ----

func loadIgnoreFingerprints(explicit string, paths []string) (map[string]bool, error) {
	out := map[string]bool{}
	readOne := func(p string, required bool) error {
		b, err := os.ReadFile(p)
		if err != nil {
			if !required && os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("read ignore file %q: %w", p, err)
		}
		sc := bufio.NewScanner(bytes.NewReader(b))
		for sc.Scan() {
			line := sc.Text()
			if i := strings.IndexByte(line, '#'); i >= 0 {
				line = line[:i]
			}
			tok := strings.ToLower(strings.TrimSpace(line))
			if tok == "" {
				continue
			}
			tok = strings.TrimPrefix(tok, "sha256:")
			if len(tok) == 64 {
				if _, err := hex.DecodeString(tok); err == nil {
					out[tok] = true
				}
			}
		}
		return nil
	}

	if strings.TrimSpace(explicit) != "" {
		if err := readOne(explicit, true); err != nil {
			return nil, err
		}
		return out, nil
	}
	_ = readOne(defaultIgnoreFile, false)
	for _, p := range paths {
		info, err := os.Stat(p)
		if err == nil && info.IsDir() {
			_ = readOne(filepath.Join(p, defaultIgnoreFile), false)
		}
	}
	return out, nil
}

// ---- Opt-in read-only credential verification (--verify) ----

func newLiveVerifier(ctx context.Context) func(*detect.Finding, string) {
	cl := &http.Client{Timeout: verifyProbeTimout}
	cache := map[string]*bool{}
	return func(f *detect.Finding, rawSecret string) {
		if v, seen := cache[f.Fingerprint]; seen {
			f.Verified = v
			return
		}
		res := probeCredentialReadOnly(ctx, cl, f.RuleID, rawSecret)
		cache[f.Fingerprint] = res
		f.Verified = res
	}
}

func probeCredentialReadOnly(ctx context.Context, cl *http.Client, ruleID, secret string) *bool {
	pctx, cancel := context.WithTimeout(ctx, verifyProbeTimout)
	defer cancel()

	bearerStatus := func(method, url string) *bool {
		req, err := http.NewRequestWithContext(pctx, method, url, nil)
		if err != nil {
			return nil
		}
		req.Header.Set("Authorization", "Bearer "+secret)
		req.Header.Set("User-Agent", "hanzo-security-verify/"+Version)
		resp, err := cl.Do(req)
		if err != nil {
			return nil
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		switch {
		case resp.StatusCode == http.StatusOK:
			v := true
			return &v
		case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
			v := false
			return &v
		default:
			return nil
		}
	}

	switch ruleID {
	case "github-token", "github-pat-fine":
		return bearerStatus(http.MethodGet, verifyEndpoints.github)
	case "huggingface-token":
		return bearerStatus(http.MethodGet, verifyEndpoints.huggingface)
	case "digitalocean-token":
		if strings.HasPrefix(secret, "dop_v1_") || strings.HasPrefix(secret, "doo_v1_") {
			return bearerStatus(http.MethodGet, verifyEndpoints.digitalocean)
		}
		return nil
	case "hanzo-api-key":
		return bearerStatus(http.MethodGet, verifyEndpoints.hanzo)
	case "slack-token":
		req, err := http.NewRequestWithContext(pctx, http.MethodPost, verifyEndpoints.slack, nil)
		if err != nil {
			return nil
		}
		req.Header.Set("Authorization", "Bearer "+secret)
		req.Header.Set("User-Agent", "hanzo-security-verify/"+Version)
		resp, err := cl.Do(req)
		if err != nil {
			return nil
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		var sr struct {
			OK bool `json:"ok"`
		}
		if json.Unmarshal(raw, &sr) == nil {
			v := sr.OK
			return &v
		}
		return nil
	default:
		return nil
	}
}

// ---- GitHub Actions annotations & Hanzo Insights telemetry ----

func emitGitHubAnnotations(w io.Writer, findings []detect.Finding) {
	for _, f := range findings {
		commitPart := ""
		if f.Commit != "" {
			commitPart = ", commit=" + f.Commit
		}
		fmt.Fprintf(w, "::error file=%s,line=%d,title=Hanzo Security (%s)::%s secret detected (preview=%s, sha256=%s%s)\n",
			f.Path, f.Line, f.RuleID, f.Severity, f.Preview, f.Fingerprint, commitPart)
	}
}

func gitRevParse(ctx context.Context, dir string, args ...string) string {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func emitSecurityInsights(ctx context.Context, errw io.Writer, e *Env, rootDir, mode string, opts scanOpts, res scanResult, durMs int64) {
	tctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	host := strings.TrimRight(cmp.Or(os.Getenv("HANZO_INSIGHTS_HOST"), "https://api.hanzo.ai"), "/")
	apiKey := strings.TrimSpace(os.Getenv("HANZO_INSIGHTS_API_KEY"))
	repo := cmp.Or(os.Getenv("GITHUB_REPOSITORY"), filepath.Base(filepath.Clean(rootDir)))
	headSHA := gitRevParse(tctx, rootDir, "rev-parse", "HEAD")
	branch := cmp.Or(opts.branch, os.Getenv("GITHUB_REF_NAME"), gitRevParse(tctx, rootDir, "rev-parse", "--abbrev-ref", "HEAD"))
	distinctID := cmp.Or(orgOf(e), repo, "hanzo-security")
	if distinctID == "?" {
		distinctID = cmp.Or(repo, "hanzo-security")
	}

	props := map[string]any{
		"repo":            repo,
		"branch":          branch,
		"since_commit":    opts.sinceCommit,
		"head_commit":     headSHA,
		"mode":            mode,
		"files_scanned":   res.FilesScanned,
		"commits_scanned": res.CommitsScanned,
		"findings_total":  res.Summary.Total,
		"critical":        res.Summary.Critical,
		"high":            res.Summary.High,
		"medium":          res.Summary.Medium,
		"low":             res.Summary.Low,
		"verified_count":  res.Summary.Verified,
		"duration_ms":     durMs,
	}
	payload := map[string]any{
		"event":       "security.scan.completed",
		"distinct_id": distinctID,
		"properties":  props,
	}
	if apiKey != "" {
		payload["api_key"] = apiKey
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	req, err := http.NewRequestWithContext(tctx, http.MethodPost, host+"/v1/capture", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey == "" && e != nil {
		if tok, terr := e.ensureToken(tctx); terr == nil && tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	}
	cl := &http.Client{Timeout: 5 * time.Second}
	resp, err := cl.Do(req)
	if err != nil {
		if opts.insights || opts.report {
			fmt.Fprintf(errw, "insights report warning: %v\n", err)
		}
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
}

func renderScan(w io.Writer, r scanResult) {
	if len(r.Findings) == 0 {
		if r.CommitsScanned > 0 {
			fmt.Fprintf(w, "✓ no secrets found (%d files across %d commits scanned)\n", r.FilesScanned, r.CommitsScanned)
		} else {
			fmt.Fprintf(w, "✓ no secrets found (%d files scanned)\n", r.FilesScanned)
		}
		return
	}
	tw := newTab(w)
	fmt.Fprintln(tw, "SEVERITY\tRULE\tLOCATION\tPREVIEW")
	for _, f := range r.Findings {
		loc := fmt.Sprintf("%s:%d", f.Path, f.Line)
		if f.Commit != "" {
			short := f.Commit
			if len(short) > 12 {
				short = short[:12]
			}
			loc = fmt.Sprintf("%s:%d@%s", f.Path, f.Line, short)
		}
		preview := f.Preview
		if f.Verified != nil && *f.Verified {
			preview += " [VERIFIED]"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", f.Severity, f.RuleID, loc, preview)
	}
	tw.Flush()
	fmt.Fprintf(w, "\n%d finding(s) in %d files  (critical=%d high=%d medium=%d low=%d)\n",
		r.Summary.Total, r.FilesScanned, r.Summary.Critical, r.Summary.High,
		r.Summary.Medium, r.Summary.Low)
}
