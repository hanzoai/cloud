// tip.go — the commit a git deploy builds.
//
// A deploy that names no commit builds the commit its branch points at. The tip is read once,
// before the build starts, and the commit it names is the image tag, the recorded commit and the
// ref the build checks out, so all three are the same bytes and none is a name that moves.

package platform

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

// tipTimeout bounds the one network read a deploy makes before it starts a build.
const tipTimeout = 10 * time.Second

// tipFunc answers the head commit of ref in repoURL, or an error when it cannot.
type tipFunc func(ctx context.Context, repoURL, ref string) (string, error)

var fullSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// lsRemoteTip reads a public repository's branch head with `git ls-remote`. repoURL has
// already passed validateRepoURL (https, an allowlisted host, no credentials), and no
// credential is offered here: a private repository answers an error and the deploy keeps
// the branch name, as it did before.
func lsRemoteTip(ctx context.Context, repoURL, ref string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, tipTimeout)
	defer cancel()
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=true")
	out, err := runGit(ctx, "", env, "ls-remote", "--", repoURL, "refs/heads/"+ref)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[1] == "refs/heads/"+ref && fullSHA.MatchString(f[0]) {
			return f[0], nil
		}
	}
	return "", fmt.Errorf("%s has no branch %q", repoURL, ref)
}

// deployRef is the ref a git deploy builds: the commit it names, else the head commit of
// its branch when that can be read, else the branch. A ref that is already a full commit
// is kept as it is.
func deployRef(s *state, ctx context.Context, repoURL, commit, branch string) (string, error) {
	ref := strings.TrimSpace(commit)
	if ref == "" {
		ref = strings.TrimSpace(branch)
	}
	if ref == "" {
		ref = "main"
	}
	if fullSHA.MatchString(ref) || s.tip == nil {
		return ref, nil
	}
	sha, err := s.tip(ctx, repoURL, ref)
	if err != nil {
		return ref, err
	}
	return sha, nil
}
