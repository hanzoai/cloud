// Git, with the credential in the body and nowhere else.
//
// The rule apps/coding/task.go states — "the per-org agent git credential
// travels in the request BODY (never a URL, never argv, never a log)" — is
// stated there and ENFORCED here, because here is where it would actually leak.
// Three ways it usually does, and what this file does instead:
//
//	https://user:token@host/…      → in the remote URL, so `git remote -v`,
//	                                 .git/config and every error message carry it
//	-c http.extraHeader=…          → in argv, so /proc/<pid>/cmdline carries it to
//	                                 any process in the box, which is running
//	                                 submitted code by definition
//	GIT_ASKPASS / env              → inherited by every child git spawns
//
// What we do: write it to a 0600 file outside the project, point ONE git
// invocation at that file through credential.helper, and remove it. The file
// PATH is in argv; the token never is.
package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/hanzoai/cloud/apps/sandbox/wire"

	"github.com/zap-proto/zip"
)

// withCredential runs fn with a git credential store holding cred, and removes
// it afterwards no matter how fn ends. An empty credential is not an error —
// public clones are ordinary — it just runs fn with no extra config.
func withCredential(remote string, cred wire.Credential, fn func(cfg []string) error) error {
	if cred.Token == "" {
		return fn(nil)
	}
	u, err := url.Parse(remote)
	if err != nil || u.Host == "" {
		return fmt.Errorf("git: remote %q is not a URL", remote)
	}
	user := cred.Username
	if user == "" {
		user = "x-access-token"
	}
	// The store format is one URL per line with the credential embedded. It is
	// written outside the project so a `git add -A` cannot commit it and the
	// agent cannot read it back as a project file.
	f, err := os.CreateTemp("/tmp", "gitcred-")
	if err != nil {
		return fmt.Errorf("git: credential store: %w", err)
	}
	path := f.Name()
	defer func() { _ = os.Remove(path) }()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return fmt.Errorf("git: credential store mode: %w", err)
	}
	line := fmt.Sprintf("%s://%s:%s@%s\n", u.Scheme, url.QueryEscape(user), url.QueryEscape(cred.Token), u.Host)
	if _, err := f.WriteString(line); err != nil {
		_ = f.Close()
		return fmt.Errorf("git: credential store: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("git: credential store: %w", err)
	}
	return fn([]string{"-c", "credential.helper=store --file=" + path})
}

func (b *box) gitClone(c *zip.Ctx) error {
	var req wire.CloneRequest
	if err := c.Bind(&req); err != nil {
		return zip.Errorf(http.StatusBadRequest, "%s", "body: "+err.Error())
	}
	if strings.TrimSpace(req.URL) == "" {
		return zip.Errorf(http.StatusBadRequest, "%s", "url required")
	}
	dir := req.Dir
	if dir == "" {
		dir = "/repo"
	}
	abs, ok := b.resolve(dir)
	if !ok || abs == b.workdir {
		return zip.Errorf(http.StatusBadRequest, "%s", "dir escapes the project")
	}

	// The whole point of the per-project volume is that the checkout survives a
	// suspend. So an existing checkout is FETCHED, not re-cloned: that is the
	// difference between a resume costing a `git fetch` and costing a cold clone
	// plus a cold install.
	err := withCredential(req.URL, req.Credential, func(cfg []string) error {
		ref := firstNonEmpty(req.Ref, req.Branch)
		if _, serr := os.Stat(filepath.Join(abs, ".git")); serr == nil {
			if e := b.git(c.Context(), abs, cfg, "fetch", "--all", "--prune"); e != nil {
				return e
			}
			if ref == "" {
				return nil
			}
			return b.git(c.Context(), abs, cfg, "checkout", "--force", ref)
		}
		args := append([]string{"clone"}, "--", req.URL, abs)
		if req.Depth > 0 {
			args = append([]string{"clone", fmt.Sprintf("--depth=%d", req.Depth), "--"}, req.URL, abs)
		}
		if e := b.git(c.Context(), b.workdir, cfg, args...); e != nil {
			return e
		}
		if ref == "" {
			return nil
		}
		return b.git(c.Context(), abs, cfg, "checkout", "--force", ref)
	})
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "%s", err.Error())
	}
	head, _ := b.gitOut(c.Context(), abs, nil, "rev-parse", "HEAD")
	return c.JSON(http.StatusOK, wire.CloneResult{Head: strings.TrimSpace(head), Dir: b.relOf(abs)})
}

func (b *box) gitPush(c *zip.Ctx) error {
	var req wire.PushRequest
	if err := c.Bind(&req); err != nil {
		return zip.Errorf(http.StatusBadRequest, "%s", "body: "+err.Error())
	}
	if strings.TrimSpace(req.Branch) == "" {
		return zip.Errorf(http.StatusBadRequest, "%s", "branch required")
	}
	repo, ok := b.resolve("/repo")
	if !ok {
		return zip.Errorf(http.StatusInternalServerError, "%s", "no repo")
	}

	// Nothing changed is a SUCCESSFUL push of nothing, not an error: an agent
	// that decided the task needed no edit is a legitimate outcome, and
	// reporting it as a failure sends the caller looking for a broken box.
	status, _ := b.gitOut(c.Context(), repo, nil, "status", "--porcelain")
	if strings.TrimSpace(status) == "" {
		return c.JSON(http.StatusOK, wire.PushResult{Changed: false})
	}

	msg := req.Message
	if strings.TrimSpace(msg) == "" {
		msg = "agent: " + req.Branch
	}
	remote, _ := b.gitOut(c.Context(), repo, nil, "remote", "get-url", "origin")
	err := withCredential(strings.TrimSpace(remote), req.Credential, func(cfg []string) error {
		for _, args := range [][]string{
			{"checkout", "-B", req.Branch},
			{"add", "-A"},
			{"-c", "user.email=agent@hanzo.ai", "-c", "user.name=Hanzo Agent", "commit", "-m", msg},
			{"push", "--set-upstream", "origin", req.Branch},
		} {
			if e := b.git(c.Context(), repo, cfg, args...); e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "%s", err.Error())
	}
	sha, _ := b.gitOut(c.Context(), repo, nil, "rev-parse", "HEAD")
	stat, _ := b.gitOut(c.Context(), repo, nil, "show", "--stat", "--oneline", "HEAD")
	return c.JSON(http.StatusOK, wire.PushResult{
		CommitSha: strings.TrimSpace(sha), Diffstat: strings.TrimSpace(stat), Changed: true,
	})
}

// git runs one git command through the SAME exec path everything else uses, so
// the timeout ceiling and the environment scrubbing are not restated here.
func (b *box) git(ctx context.Context, dir string, cfg []string, args ...string) error {
	out, err := b.gitOut(ctx, dir, cfg, args...)
	if err != nil {
		return fmt.Errorf("git %s: %s", args[0], strings.TrimSpace(out))
	}
	return nil
}

func (b *box) gitOut(ctx context.Context, dir string, cfg []string, args ...string) (string, error) {
	argv := append([]string{"git"}, append(cfg, args...)...)
	res, err := b.runIn(ctx, dir, wire.ExecRequest{
		Argv: argv, TimeoutSec: envInt("BOX_GIT_TIMEOUT_SEC", 900),
		// Never prompt: a box has no terminal, and a git that asks for a password
		// hangs until the timeout instead of failing in a millisecond.
		Env: map[string]string{"GIT_TERMINAL_PROMPT": "0", "GIT_ADVICE": "0"},
	})
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return res.Stdout + res.Stderr, fmt.Errorf("exit %d", res.ExitCode)
	}
	return res.Stdout, nil
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			return x
		}
	}
	return ""
}
