package git

import (
	"context"
	"regexp"
	"sort"
	"strings"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/hanzoai/cloud"
)

// ci.go makes an IMPORTED repo build on git.hanzo.ai's native runners: it commits
// .hanzo/workflows/ when the repo has none, either by SYNCING the repo's own
// .github/workflows/* (retargeting runs-on to the Hanzo runner, so the repo's real
// CI intent carries over) or, when the repo has no GitHub CI, by generating ONE
// language-detected default build/test workflow.
//
// The commit rides corePush (push.go) — the SAME client-less-push primitive the
// hanzo.app builder uses — so it fires the EXACT push-landed event a real push does.
// That single event drives every downstream reactor with no extra wiring: the code
// index folds the repo in (index_on_push.go) and any configured outbound mirror
// receives the branch (mirror_out.go). So "inject CI on import" and "index on import"
// are ONE mechanism, not two.
//
// Idempotent: a .hanzo/workflows/<name> already present is left untouched, so a
// re-import never clobbers a workflow the repo (or a prior import) already carries.

// nativeRunnerLabel is the runner label git.hanzo.ai's native CI (arcd) advertises;
// every generated/synced workflow targets it so a job schedules on a Hanzo runner.
const nativeRunnerLabel = "hanzo-build-linux-amd64"

// nativeWorkflowDir is git.hanzo.ai's native-CI workflow home; githubWorkflowDir is
// GitHub's. Import syncs the latter into the former (one direction), so an imported
// repo builds on Hanzo runners without touching its GitHub CI.
const (
	nativeWorkflowDir = ".hanzo/workflows"
	githubWorkflowDir = ".github/workflows"
)

// maxWorkflowBytes bounds a single synced workflow blob — a CI YAML is small; a
// blob larger than this is skipped rather than pulled into memory.
const maxWorkflowBytes = 256 << 10

// ensureNativeCI guarantees the repo has native CI, committing .hanzo/workflows/
// when absent. Returns the workflow files it wrote (nil ⇒ every wanted workflow was
// already present, so nothing was committed — the caller then drives indexing
// itself, since no push fired). Best-effort by contract: the import is not failed by
// a CI-injection error, but a hard storage error is returned so the caller can log it.
func ensureNativeCI(s *cloud.Service[state], ctx context.Context, org, project, name string) ([]string, error) {
	repo, err := openRepoForIndex(s, org, project, name)
	if err != nil {
		return nil, err
	}
	branch := headBranch(repo)

	// Read the imported tip's tree (empty repo ⇒ nil tree ⇒ generate the default).
	tree := tipTree(repo)
	sources := githubWorkflows(tree)
	present := nativeWorkflowsPresent(tree)

	// Decide the .hanzo/workflows/ set: sync each GitHub workflow, or one default.
	want := map[string]string{}
	if len(sources) > 0 {
		for fname, content := range sources {
			want[fname] = syncWorkflow(fname, content)
		}
	} else {
		want["ci.yml"] = defaultWorkflow(detectLang(tree), branch)
	}

	var files []pushFile
	var written []string
	for fname, content := range want {
		if present[fname] {
			continue // already native — never clobber
		}
		files = append(files, pushFile{Path: nativeWorkflowDir + "/" + fname, Content: content})
		written = append(written, fname)
	}
	if len(files) == 0 {
		return nil, nil
	}
	sort.Strings(written)

	if _, _, err := corePush(s, ctx, org, project, name, pushReq{
		Branch:  branch,
		Message: "ci: native CI on git.hanzo.ai (" + strings.Join(written, ", ") + ")",
		Files:   files,
	}); err != nil {
		return nil, err
	}
	return written, nil
}

// headBranch resolves the repo's default-branch SHORT name from its HEAD symref
// (initBare/mirror-in points HEAD at the default), falling back to the package
// default. This is the branch the index reactor treats as canonical, so CI must land
// on it.
func headBranch(repo *gogit.Repository) string {
	if ref, err := repo.Reference(plumbing.HEAD, false); err == nil {
		if b := ref.Target().Short(); b != "" {
			return b
		}
	}
	return defaultBranchName
}

// tipTree returns the repo's HEAD commit tree, or nil for an empty repo (no commits
// yet) — the caller then generates a default workflow with no sources to sync.
func tipTree(repo *gogit.Repository) *object.Tree {
	h, err := repo.Head()
	if err != nil {
		return nil
	}
	commit, err := repo.CommitObject(h.Hash())
	if err != nil {
		return nil
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil
	}
	return tree
}

// githubWorkflows returns the repo's .github/workflows/*.{yml,yaml} as name→content,
// bounded per file. A missing dir yields an empty map (no sources to sync).
func githubWorkflows(tree *object.Tree) map[string]string {
	out := map[string]string{}
	if tree == nil {
		return out
	}
	wf, err := tree.Tree(githubWorkflowDir)
	if err != nil {
		return out
	}
	for _, ent := range wf.Entries {
		if ent.Mode == filemode.Dir || !isYAMLName(ent.Name) {
			continue
		}
		f, err := wf.File(ent.Name)
		if err != nil || f.Size > maxWorkflowBytes {
			continue
		}
		content, err := f.Contents()
		if err != nil {
			continue
		}
		out[ent.Name] = content
	}
	return out
}

// nativeWorkflowsPresent returns the set of .hanzo/workflows/ filenames the repo
// already carries, so ensureNativeCI never clobbers an existing native workflow.
func nativeWorkflowsPresent(tree *object.Tree) map[string]bool {
	out := map[string]bool{}
	if tree == nil {
		return out
	}
	hw, err := tree.Tree(nativeWorkflowDir)
	if err != nil {
		return out
	}
	for _, ent := range hw.Entries {
		if ent.Mode != filemode.Dir {
			out[ent.Name] = true
		}
	}
	return out
}

func isYAMLName(name string) bool {
	return strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".yaml")
}

// runsOnRE matches a `runs-on:` line that has an INLINE value (ubuntu-latest,
// [ubuntu-latest], ${{ matrix.os }}), preserving indentation. A bare `runs-on:`
// (block-sequence form) has no inline value and is deliberately left untouched — the
// common inline forms are retargeted; the rare block form is documented as a manual
// edit rather than risk producing invalid YAML.
var runsOnRE = regexp.MustCompile(`(?m)^([ \t]*)runs-on:[ \t]*\S.*$`)

// syncWorkflow adapts a GitHub Actions workflow to git.hanzo.ai's native runner:
// every inline runs-on is retargeted to the Hanzo runner label. The rest of the
// workflow (Actions syntax the native runner shares) is preserved verbatim, with a
// provenance header noting it is now the repo's own CI to edit.
func syncWorkflow(name, content string) string {
	retargeted := runsOnRE.ReplaceAllString(content, "${1}runs-on: ["+nativeRunnerLabel+"]")
	return "# Synced from " + githubWorkflowDir + "/" + name + " on import to git.hanzo.ai.\n" +
		"# runs-on retargeted to the native Hanzo runner — this is now your CI; edit freely.\n" +
		retargeted
}

// detectLang picks the repo's primary toolchain from well-known root manifests so a
// generated default workflow builds+tests the right way. "" ⇒ no manifest matched
// (a generic placeholder workflow is generated).
func detectLang(tree *object.Tree) string {
	if tree == nil {
		return ""
	}
	has := func(p string) bool { _, err := tree.File(p); return err == nil }
	switch {
	case has("go.mod"):
		return "go"
	case has("Cargo.toml"):
		return "rust"
	case has("package.json"):
		return "node"
	case has("pyproject.toml"), has("requirements.txt"), has("setup.py"):
		return "python"
	default:
		return ""
	}
}

// defaultWorkflow generates one native build/test workflow for lang, triggered on a
// push to branch (and manual dispatch). It is intentionally minimal + valid Actions
// YAML the native runner accepts; the repo owner edits it into their real pipeline.
func defaultWorkflow(lang, branch string) string {
	var b strings.Builder
	b.WriteString("name: CI\n")
	b.WriteString("# Generated by git.hanzo.ai import — native CI on the Hanzo runners.\n")
	b.WriteString("# Edit freely; this is your repo's own build/test pipeline.\n")
	b.WriteString("on:\n")
	b.WriteString("  push:\n    branches: [" + branch + "]\n")
	b.WriteString("  workflow_dispatch: {}\n")
	b.WriteString("jobs:\n")
	b.WriteString("  build:\n")
	b.WriteString("    runs-on: [" + nativeRunnerLabel + "]\n")
	b.WriteString("    steps:\n")
	b.WriteString("      - uses: actions/checkout@v4\n")
	b.WriteString(langSteps(lang))
	return b.String()
}

// langSteps returns the toolchain-specific build/test steps (6-space indented under
// steps:) for a generated default workflow.
func langSteps(lang string) string {
	switch lang {
	case "go":
		return "" +
			"      - uses: actions/setup-go@v5\n" +
			"        with:\n          go-version: stable\n" +
			"      - run: go build ./...\n" +
			"      - run: go test ./...\n"
	case "node":
		return "" +
			"      - uses: actions/setup-node@v4\n" +
			"        with:\n          node-version: '22'\n" +
			"      - run: npm ci\n" +
			"      - run: npm run build --if-present\n" +
			"      - run: npm test --if-present\n"
	case "rust":
		return "" +
			"      - run: cargo build --all --locked\n" +
			"      - run: cargo test --all\n"
	case "python":
		return "" +
			"      - uses: actions/setup-python@v5\n" +
			"        with:\n          python-version: '3.12'\n" +
			"      - run: pip install -e . || pip install -r requirements.txt\n" +
			"      - run: pytest\n"
	default:
		return "      - run: echo \"Add build/test steps for this repo in " + nativeWorkflowDir + "/ci.yml\"\n"
	}
}

// indexImported drives the code index (and any outbound mirror) for a repo whose
// import committed NO new native workflow — so no push fired to carry it. It emits the
// one push-landed event the index reactor consumes, keyed to the current tip. A no-op
// for an empty repo (no tip to index). This is the ONLY path that emits directly;
// when ensureNativeCI committed CI, corePush already emitted (no double-fire).
func indexImported(s *cloud.Service[state], ctx context.Context, org, project, name string) {
	repo, err := openRepoForIndex(s, org, project, name)
	if err != nil {
		return
	}
	h, err := repo.Head()
	if err != nil {
		return // empty repo — nothing to index
	}
	cloud.EmitLifecycle(context.WithoutCancel(ctx), cloud.LifecycleEvent{
		Kind:   cloud.LifecyclePushLanded,
		Org:    org,
		Project: project,
		Repo:   name,
		Branch: headBranch(repo),
		After:  h.Hash().String(),
	})
}
