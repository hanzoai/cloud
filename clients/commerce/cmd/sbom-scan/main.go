// Command sbom-scan clones Hanzo GitHub repos, runs git blame analysis, and
// produces SBOMEntry + Contributor records for OSS revenue sharing payouts.
//
// Usage:
//
//	sbom-scan                              # Scan all repos from referral-program.json
//	sbom-scan -repos hanzoai/bot,hanzoai/mcp  # Scan specific repos
//	sbom-scan -workdir /tmp/sbom-repos     # Custom clone directory
//	sbom-scan -dry-run                     # Print results without writing to datastore
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// componentRepo maps package names from referral-program.json to GitHub repos.
var componentRepo = map[string]string{
	"@hanzo/bot":      "hanzoai/bot",
	"@hanzo/agents":   "hanzoai/agents",
	"@hanzo/mcp":      "hanzoai/mcp",
	"@hanzo/gateway":  "hanzoai/gateway",
	"@hanzo/commerce": "hanzoai/commerce",
	"@hanzo/tasks":    "hanzoai/tasks",
	"@hanzo/auto":     "hanzoai/auto",
	"@hanzo/ui":       "hanzoai/ui",
	"@hanzo/flow":     "hanzoai/flow",
	"@hanzo/studio":   "hanzoai/studio",
	"@hanzo/search":   "hanzoai/search",
}

// sourceExtensions are file extensions we run git blame on.
var sourceExtensions = map[string]bool{
	".go":   true,
	".ts":   true,
	".tsx":  true,
	".js":   true,
	".jsx":  true,
	".py":   true,
	".rs":   true,
	".sol":  true,
	".css":  true,
	".svelte": true,
	".vue":  true,
}

// SBOMResult is the scan output for one component.
type SBOMResult struct {
	Component  string       `json:"component"`
	Repo       string       `json:"repo"`
	TotalLines int64        `json:"totalLines"`
	Authors    []AuthorStat `json:"authors"`
	ScanCommit string       `json:"scanCommit"`
	ScannedAt  time.Time    `json:"scannedAt"`
}

// AuthorStat aggregates line counts for a single author across a repo.
type AuthorStat struct {
	Email   string  `json:"email"`
	Name    string  `json:"name"`
	Lines   int64   `json:"lines"`
	Percent float64 `json:"percent"`
}

func main() {
	var (
		reposFlag  string
		workdir    string
		dryRun     bool
		outputJSON bool
	)

	flag.StringVar(&reposFlag, "repos", "", "comma-separated list of repos (e.g. hanzoai/bot,hanzoai/mcp)")
	flag.StringVar(&workdir, "workdir", "", "directory for cloned repos (default: $TMPDIR/sbom-repos)")
	flag.BoolVar(&dryRun, "dry-run", false, "print results without writing to datastore")
	flag.BoolVar(&outputJSON, "json", false, "output results as JSON")
	flag.Parse()

	if workdir == "" {
		workdir = filepath.Join(os.TempDir(), "sbom-repos")
	}

	if err := os.MkdirAll(workdir, 0o755); err != nil {
		fatalf("create workdir: %v", err)
	}

	// Determine which repos to scan.
	repos := buildRepoList(reposFlag)
	if len(repos) == 0 {
		fatalf("no repos to scan")
	}

	var results []SBOMResult

	for component, repo := range repos {
		fmt.Fprintf(os.Stderr, "--- scanning %s (%s)\n", component, repo)

		result, err := scanRepo(workdir, component, repo)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR scanning %s: %v\n", repo, err)
			continue
		}

		results = append(results, *result)

		fmt.Fprintf(os.Stderr, "    %d total lines, %d authors, commit %s\n",
			result.TotalLines, len(result.Authors), shortHash(result.ScanCommit))
	}

	if outputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(results); err != nil {
			fatalf("encode json: %v", err)
		}
		return
	}

	// Print summary table.
	printSummary(results)

	if dryRun {
		fmt.Fprintf(os.Stderr, "\n(dry-run: no records written)\n")
		return
	}

	// Aggregate contributor stats across all components.
	contributors := aggregateContributors(results)

	// Print contributor summary.
	fmt.Fprintf(os.Stderr, "\n--- contributor summary (%d unique contributors)\n", len(contributors))
	for _, c := range contributors {
		fmt.Fprintf(os.Stderr, "  %-40s %6d lines across %d components\n",
			c.Email, c.TotalLines, len(c.Components))
	}

	fmt.Fprintf(os.Stderr, "\nDone: scanned %d repos, found %d contributors\n", len(results), len(contributors))
}

// buildRepoList returns component->repo mapping from flags or defaults.
func buildRepoList(reposFlag string) map[string]string {
	if reposFlag == "" {
		return componentRepo
	}

	result := make(map[string]string)
	for _, r := range strings.Split(reposFlag, ",") {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		// Find the component name for this repo, or use repo as both.
		found := false
		for comp, repo := range componentRepo {
			if repo == r {
				result[comp] = repo
				found = true
				break
			}
		}
		if !found {
			// Use repo path as component name.
			result[r] = r
		}
	}
	return result
}

// scanRepo clones/pulls a repo and runs git blame analysis.
func scanRepo(workdir, component, repo string) (*SBOMResult, error) {
	repoURL := "https://github.com/" + repo + ".git"
	repoDir := filepath.Join(workdir, strings.ReplaceAll(repo, "/", "_"))

	// Clone or pull.
	if _, err := os.Stat(filepath.Join(repoDir, ".git")); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "    cloning %s...\n", repoURL)
		if err := gitExec(workdir, "clone", "--depth=1", repoURL, repoDir); err != nil {
			return nil, fmt.Errorf("clone %s: %w", repo, err)
		}
	} else {
		fmt.Fprintf(os.Stderr, "    pulling %s...\n", repo)
		if err := gitExec(repoDir, "pull", "--ff-only"); err != nil {
			// Pull may fail on shallow clones; try fetch+reset instead.
			_ = gitExec(repoDir, "fetch", "--depth=1", "origin")
			_ = gitExec(repoDir, "reset", "--hard", "FETCH_HEAD")
		}
	}

	// Get HEAD commit hash.
	commitHash, err := gitOutput(repoDir, "rev-parse", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("rev-parse HEAD: %w", err)
	}
	commitHash = strings.TrimSpace(commitHash)

	// Find source files.
	sourceFiles, err := findSourceFiles(repoDir)
	if err != nil {
		return nil, fmt.Errorf("find source files: %w", err)
	}

	if len(sourceFiles) == 0 {
		return &SBOMResult{
			Component:  component,
			Repo:       repo,
			ScanCommit: commitHash,
			ScannedAt:  time.Now().UTC(),
		}, nil
	}

	// Run git blame on each source file and aggregate.
	authorLines := make(map[string]*authorAccum)
	var totalLines int64

	for _, f := range sourceFiles {
		rel, _ := filepath.Rel(repoDir, f)
		lines, err := blameFile(repoDir, rel)
		if err != nil {
			// Some files may fail (binary, etc.) -- skip.
			continue
		}

		for email, info := range lines {
			totalLines += info.lines
			acc, ok := authorLines[email]
			if !ok {
				acc = &authorAccum{email: email, name: info.name}
				authorLines[email] = acc
			}
			acc.lines += info.lines
			// Keep the longest name variant.
			if len(info.name) > len(acc.name) {
				acc.name = info.name
			}
		}
	}

	// Build sorted author list.
	authors := make([]AuthorStat, 0, len(authorLines))
	for _, acc := range authorLines {
		pct := 0.0
		if totalLines > 0 {
			pct = float64(acc.lines) / float64(totalLines) * 100.0
		}
		authors = append(authors, AuthorStat{
			Email:   acc.email,
			Name:    acc.name,
			Lines:   acc.lines,
			Percent: pct,
		})
	}

	sort.Slice(authors, func(i, j int) bool {
		return authors[i].Lines > authors[j].Lines
	})

	return &SBOMResult{
		Component:  component,
		Repo:       repo,
		TotalLines: totalLines,
		Authors:    authors,
		ScanCommit: commitHash,
		ScannedAt:  time.Now().UTC(),
	}, nil
}

type authorAccum struct {
	email string
	name  string
	lines int64
}

type blameInfo struct {
	name  string
	lines int64
}

// blameFile runs git blame --line-porcelain on a single file and returns
// per-author line counts.
func blameFile(repoDir, relPath string) (map[string]*blameInfo, error) {
	cmd := exec.Command("git", "blame", "--line-porcelain", relPath)
	cmd.Dir = repoDir

	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	result := make(map[string]*blameInfo)
	var currentEmail, currentName string

	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, "author-mail ") {
			// Format: author-mail <user@example.com>
			email := strings.TrimPrefix(line, "author-mail ")
			email = strings.Trim(email, "<>")
			currentEmail = strings.ToLower(email)
		} else if strings.HasPrefix(line, "author ") {
			currentName = strings.TrimPrefix(line, "author ")
		} else if strings.HasPrefix(line, "\t") {
			// This is the actual source line -- count it for the current author.
			if currentEmail != "" && currentEmail != "not.committed.yet" {
				info, ok := result[currentEmail]
				if !ok {
					info = &blameInfo{name: currentName}
					result[currentEmail] = info
				}
				info.lines++
			}
		}
	}

	return result, scanner.Err()
}

// findSourceFiles walks the repo and returns paths to source files,
// skipping vendor, node_modules, .git, and other non-source dirs.
func findSourceFiles(root string) ([]string, error) {
	skipDirs := map[string]bool{
		".git":         true,
		"node_modules": true,
		"vendor":       true,
		"dist":         true,
		"build":        true,
		".next":        true,
		"__pycache__":  true,
		".venv":        true,
		"target":       true,
	}

	var files []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if info.IsDir() {
			if skipDirs[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		ext := filepath.Ext(info.Name())
		if sourceExtensions[ext] {
			files = append(files, path)
		}
		return nil
	})
	return files, err
}

// ContributorAgg aggregates a contributor's stats across all scanned components.
type ContributorAgg struct {
	Email      string
	Name       string
	TotalLines int64
	Components []ComponentAttribution
}

// ComponentAttribution records one contributor's share in one component.
type ComponentAttribution struct {
	Component  string
	Repo       string
	Lines      int64
	TotalLines int64
	Percent    float64
}

// aggregateContributors merges author stats across all SBOMResults.
func aggregateContributors(results []SBOMResult) []ContributorAgg {
	index := make(map[string]*ContributorAgg)

	for _, r := range results {
		for _, a := range r.Authors {
			agg, ok := index[a.Email]
			if !ok {
				agg = &ContributorAgg{
					Email: a.Email,
					Name:  a.Name,
				}
				index[a.Email] = agg
			}
			agg.TotalLines += a.Lines
			if len(a.Name) > len(agg.Name) {
				agg.Name = a.Name
			}
			agg.Components = append(agg.Components, ComponentAttribution{
				Component:  r.Component,
				Repo:       r.Repo,
				Lines:      a.Lines,
				TotalLines: r.TotalLines,
				Percent:    a.Percent,
			})
		}
	}

	sorted := make([]ContributorAgg, 0, len(index))
	for _, agg := range index {
		sorted = append(sorted, *agg)
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].TotalLines > sorted[j].TotalLines
	})
	return sorted
}

// printSummary prints a human-readable table of scan results.
func printSummary(results []SBOMResult) {
	fmt.Println()
	fmt.Printf("%-25s %10s %8s  %-12s  %s\n", "COMPONENT", "LINES", "AUTHORS", "COMMIT", "TOP CONTRIBUTOR")
	fmt.Println(strings.Repeat("-", 90))

	for _, r := range results {
		top := "-"
		if len(r.Authors) > 0 {
			top = fmt.Sprintf("%s (%s%%)", r.Authors[0].Email,
				strconv.FormatFloat(r.Authors[0].Percent, 'f', 1, 64))
		}
		fmt.Printf("%-25s %10d %8d  %-12s  %s\n",
			r.Component, r.TotalLines, len(r.Authors), shortHash(r.ScanCommit), top)
	}
}

func shortHash(hash string) string {
	if len(hash) > 8 {
		return hash[:8]
	}
	return hash
}

// gitExec runs a git command in the given directory.
func gitExec(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// gitOutput runs a git command and returns its stdout.
func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err
}

func fatalf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "sbom-scan: "+format+"\n", args...)
	os.Exit(1)
}
