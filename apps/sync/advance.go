package sync

import (
	"github.com/hanzoai/cloud/internal/environ"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// advance.go moves ONE ref from one git host to another, and refuses to move it
// any way but forward. It is the whole safety property of this client.
//
// # The invariant, and where it is enforced
//
// The forge is CANONICAL. An inbound sync may only fast-forward it: if the
// upstream's tip is not a descendant of what the forge already holds, the two
// have diverged, and the answer is a CONFLICT with the forge untouched — never
// an overwrite. That rule cost nothing to state and everything to get wrong,
// because getting it wrong is silent: a force-push that eats a colleague's
// commits leaves a repository that looks fine.
//
// It used to be enforced by `git fetch` INTO a local bare repo with a
// non-forcing refspec. The store is the forge now, so the same enforcement moves
// to the other end of the same protocol: `git push` with a non-forcing refspec.
// git refuses, client-side, to SEND an update that is not a fast-forward
// (remote.c set_ref_status_for_push: the destination must be a descendant, or
// under refs/tags/ it must not exist at all, unless the refspec is forced), and
// the forge's receive-pack refuses to APPLY one whose old value is not what we
// said it was. Neither refusal is ours to reason around:
//
//   - there is no '+' anywhere in this file, and no --force, --mirror or
//     --force-with-lease. That is a property you can grep for, and a test does;
//   - a rejection is read from git's own words (nonFFRE) and turned into a
//     Conflict, so a divergence is REPORTED rather than resolved.
//
// # Why it primes from the destination first
//
// Before pushing, the scratch repository fetches the DESTINATION's current tip.
// Two reasons, and the second is the one that matters:
//
//   - the objects it brings are most of what the push would otherwise send, and
//     they come from the forge (same cluster) rather than from the upstream
//     (the public internet);
//   - git can only call an update "non-fast-forward" if it HOLDS the old object
//     to compare against. Without it the same divergence is rejected as "fetch
//     first", which is still a rejection and still a Conflict — but the precise
//     answer is worth one cheap fetch.
//
// # Deletions
//
// Nothing here removes a ref from a REMOTE. A refspec that names a source ref
// can only create or update, and no path pushes an empty source, so a branch
// that disappears upstream simply stays on the forge. That is deliberate: this
// client is not allowed to take anything away from the canonical store, and the
// way to guarantee that is to have no code path that could.
//
// The one delete this file does make is [work.drop], on a scratch ref in the
// transit repository — a private copy, not a store, and the delete is what lets
// every refspec here be non-forcing.

// outcome is what one advance did. It is the retired ffResult, unchanged in
// shape, because the three answers are the same three answers.
type outcome struct {
	// Applied — the destination moved forward to After.
	Applied bool
	// NoOp — nothing to do: the tips were already equal (the loop echo), or the
	// source does not have the ref at all.
	NoOp bool
	// Absent — a refinement of NoOp: the SOURCE does not have the ref. The
	// destination keeps whatever it holds (nothing here removes a ref), and
	// nothing was learned about whether the two ends agree.
	Absent bool
	// Conflict — the destination has commits the source does not, so the update
	// is not a fast-forward. THE DESTINATION WAS NOT CHANGED.
	Conflict bool
	// Before and After are the destination's tip on either side of the attempt.
	// On a Conflict and a NoOp they are equal, which is the point.
	Before, After string
	// Detail is the human reason for a Conflict or a skip.
	Detail string
}

// reason is what a caller records about an advance: the conflict's detail, or ""
// when there is none — so a clean advance and a resolved conflict are the SAME
// write, and there is no way to record one without clearing the other.
//
// ok is FALSE when the advance learned nothing and there is nothing to write. A
// ref the source no longer has is the case, and it is not the same as agreement:
// an upstream that deletes a branch it had diverged on would otherwise clear the
// conflict it never resolved — the console goes green while the forge still
// holds the split history. Silence about a ref nobody offered is the honest
// record.
func (o outcome) reason() (string, bool) {
	if o.Absent {
		return "", false
	}
	if o.Conflict {
		return o.Detail, true
	}
	return "", true
}

// nonFFRE matches git's refusal to move a ref backwards or sideways.
//
// Lifted verbatim from the retired fetch path, because the words are the same
// words: a push that is not a fast-forward prints
// "! [rejected] <src> -> <dst> (non-fast-forward)"; one whose old object we do
// not hold prints "(fetch first)"; one onto an existing tag prints
// "(already exists)" — all three carry "[rejected]", all three leave the
// destination as it was, and all three are the divergence this returns as a
// Conflict rather than as an error. LC_ALL=C (baseGitEnv) is what keeps those
// words in English.
var nonFFRE = regexp.MustCompile(`(?i)\[rejected\]|non-fast-forward|would clobber existing tag`)

// refRE bounds a ref this client will write. It is the retired push path's rule:
// under refs/heads/ or refs/tags/, no traversal, no escape from the namespace.
// A source ref that fails it is skipped rather than sanitized — a name we cannot
// state exactly is a name we do not write.
var refRE = regexp.MustCompile(`^refs/(heads|tags)/[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)

// agentRefPrefix is the branch namespace a coding run pushes into.
//
// It is excluded from every sync, in both directions, structurally. A run's
// branch is machine output under a credential scoped to one repository; letting
// an upstream create or move one would let whoever controls that upstream place
// code where the platform's own tooling looks for a run's work. The retired
// mirror made the same exclusion with a negative refspec, for the same reason.
const agentRefPrefix = "refs/heads/agent/"

// syncable reports whether this client may carry ref.
func syncable(ref string) bool {
	return refRE.MatchString(ref) && !strings.HasPrefix(ref, agentRefPrefix)
}

// remote is one end of an advance: where it is, and what opens it.
type remote struct {
	URL  string
	Cred gitCred
}

// pushTimeout is the ceiling on one push (transfer included).
// GIT_MIRROR_OUT_TIMEOUT (seconds) overrides; default 300s.
func pushTimeout() time.Duration {
	if v := environ.Or("GIT_MIRROR_OUT_TIMEOUT", ""); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs >= 1 {
			return time.Duration(secs) * time.Second
		}
	}
	return 300 * time.Second
}

// waitDelay bounds how long cmd.Wait blocks after a context expires before it
// force-closes the subprocess pipes. A stalled remote leaves git's network
// helper child holding the stderr pipe, and killing the parent alone does not
// reap it — without this, a deadline would not actually bound anything.
const waitDelay = 5 * time.Second

// ── the transit repository ───────────────────────────────────────────────────

// work is the bare repository one (org, repo)'s objects pass through.
//
// It is a CACHE and never a store. It holds no truth — the forge does — serves
// nobody, and can be deleted at any moment; the next advance rebuilds whatever
// it needs. Its whole job is to make the next advance a DELTA.
//
// That is the difference between this and the object plane it replaces, and it
// is the reason it is kept rather than made fresh per event. A scratch
// repository created per webhook holds no objects, so every push would refetch
// the branch's whole history from the forge and send the whole history back —
// pack-objects on a repository-sized input, on the forge, once per event. Held,
// the two fetches and the push are all deltas.
//
// ONE CALLER AT A TIME per repository: [openTransit] hands it out through a
// per-repository gate, so two advances can never write the same scratch ref. The
// gate is taken BEFORE any pack slot, always in that order, so the two can never
// deadlock against each other.
type work struct {
	dir     string
	release func()
}

// transit holds one gate per (org, repo): a one-slot channel rather than a
// mutex, so a caller whose context is already cancelled gives up instead of
// queueing behind a transfer that may take minutes. The same discipline as the
// pack pool, for the same reason.
//
// The map only grows, bounded by the number of repositories this deployment
// syncs, and a channel is a pointer.
var transit struct {
	sync.Mutex
	per map[string]chan struct{}
}

// openTransit opens the transit repository for one (org, repo) and takes its
// lock. The caller MUST call close.
//
// parent is the app's own data directory: a repository in transit is as large as
// the repository, and a multi-gigabyte one in a tmpfs is a pod eviction. An empty
// parent falls back to the OS temp dir, which is what a test gets.
func openTransit(ctx context.Context, parent, org, repo string) (*work, error) {
	key := org + "/" + repo
	transit.Lock()
	if transit.per == nil {
		transit.per = map[string]chan struct{}{}
	}
	gate, ok := transit.per[key]
	if !ok {
		gate = make(chan struct{}, 1)
		transit.per[key] = gate
	}
	transit.Unlock()
	select {
	case gate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	release := func() { <-gate }

	if parent == "" {
		parent = os.TempDir()
	}
	dir := filepath.Join(parent, "transit", org, repo+".git")
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		release()
		return nil, fmt.Errorf("transit dir: %w", err)
	}
	// init is idempotent on an existing repository, which is what makes "open or
	// create" one call rather than a stat and a branch.
	cmd, err := gitCmd(ctx, nil, nil, "init", "--bare", "--quiet", dir)
	if err != nil {
		release()
		return nil, err
	}
	stderr := &cappedBuffer{cap: stderrCap}
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		release()
		return nil, fmt.Errorf("git init: %w: %s", err, sanitizeGitErr(stderr.String()))
	}
	return &work{dir: dir, release: release}, nil
}

// close releases the repository for the next caller. It does NOT delete it: the
// objects are the cache, and throwing them away is what this design exists to
// stop doing.
func (w *work) close() {
	if w != nil && w.release != nil {
		w.release()
		w.release = nil
	}
}

// scratch names the local ref an advance parks a remote's tip at.
//
// DETERMINISTIC, one per (side, ref), so the objects behind the last advance of
// that ref stay reachable and the next one negotiates against them. refs/transit
// is a namespace no repository uses for anything, and the ref name arrives whole
// (refs/transit/src/refs/heads/main) so two refs cannot spell one scratch name.
func scratch(side, ref string) string { return "refs/transit/" + side + "/" + ref }

// ── the advance ──────────────────────────────────────────────────────────────

// advance makes to's ref equal from's ref, IF that is a fast-forward.
//
// before is to's current tip and want is from's, both already read (see [refs]).
// They are arguments rather than reads because the caller that syncs a whole
// repository has them for every ref from one advertisement each, and re-reading
// them per ref is one round trip per ref to learn what it already knows.
//
//	Applied   the destination moved forward
//	NoOp      the tips were equal, or the source has no such ref
//	Conflict  git refused: the destination has commits the source does not
//
// A transport or credential failure is an ERROR, not a Conflict — and the
// destination is unchanged in that case too, because a push that did not
// complete moves nothing.
func (w *work) advance(ctx context.Context, from, to remote, ref, before, want string) (outcome, error) {
	if want == "" {
		return outcome{NoOp: true, Absent: true, Before: before, After: before,
			Detail: "the source does not have " + ref}, nil
	}
	if before == want {
		// ALREADY OURS. This is the loop echo — the ref we just sent the other way
		// coming back — and it costs nothing to answer, which is why it is answered
		// before anything is fetched.
		return outcome{NoOp: true, Before: before, After: before}, nil
	}

	// The destination's own tip, for the objects and for the ancestry. A failure
	// here is a failure: proceeding without it would still be safe (the push
	// refuses either way) but it would report the wrong reason, and a wrong reason
	// is what sends somebody looking in the wrong place.
	if before != "" {
		if err := w.fetch(ctx, to, ref, scratch("dst", ref)); err != nil {
			return outcome{}, fmt.Errorf("read %s from the destination: %w", ref, err)
		}
		// WHAT THE DESTINATION ACTUALLY HOLDS, not what its advertisement said. The
		// caller read that advertisement once for a whole repository and the
		// destination moves on its own; this value is what a deployment diffs FROM,
		// so a stale one describes a change that never happened.
		held, err := w.oid(ctx, scratch("dst", ref))
		if err != nil {
			return outcome{}, err
		}
		before = held
	}
	src := scratch("src", ref)
	if err := w.fetch(ctx, from, ref, src); err != nil {
		return outcome{}, fmt.Errorf("fetch %s: %w", ref, err)
	}
	// WHAT WE ACTUALLY FETCHED, not what the advertisement said a moment ago. The
	// two differ whenever the source moved in between, and this value is the one a
	// deployment checks out — reporting the advertised tip would build a commit
	// other than the one that landed.
	landed, err := w.oid(ctx, src)
	if err != nil {
		return outcome{}, err
	}
	if landed == before {
		// The two ends agree after all — the advertisements were read a moment apart
		// and something moved in between. Pushing here would earn git's "Everything
		// up-to-date", which exits zero and moves nothing, and reporting THAT as an
		// advance hands push-to-deploy an empty diff to build.
		return outcome{NoOp: true, Before: before, After: before}, nil
	}

	rejected, err := w.push(ctx, to, src, ref)
	switch {
	case err != nil:
		return outcome{}, err
	case rejected != "":
		return outcome{
			Conflict: true, Before: before, After: before,
			Detail: ref + " has diverged: the receiving side has commits the source " +
				"does not, so a fast-forward is not possible and nothing was changed",
		}, nil
	}
	return outcome{Applied: true, Before: before, After: landed}, nil
}

// oid reads what a local ref points at. A ref the fetch just wrote is always
// there, so an unreadable one is a real failure rather than an absence.
func (w *work) oid(ctx context.Context, ref string) (string, error) {
	cmd, err := gitCmd(ctx, nil, nil, "--git-dir="+w.dir, "rev-parse", "--verify", "--quiet", ref)
	if err != nil {
		return "", err
	}
	var out bytes.Buffer
	stderr := &cappedBuffer{cap: stderrCap}
	cmd.Stdout, cmd.Stderr = &out, stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("read %s: %w: %s", ref, err, sanitizeGitErr(stderr.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// fetch brings ONE ref from r into the transit repository at the local name
// `into`.
//
// It DROPS that local ref first, so the fetch is always a create and the refspec
// needs no '+'. That matters twice: a remote may legitimately rewrite its own
// history (this is a scratch copy of it, not a canonical ref, so there is
// nothing to protect), and this file stays free of any forcing refspec, which is
// the property [work.push] rests on and a test greps for.
//
// The dropped ref's objects are NOT lost: they stay in the repository, which is
// the cache the next fetch negotiates against, and git prunes them in its own
// time once nothing reaches them.
//
// --no-tags because this is a fetch for ONE ref: git otherwise auto-follows tags
// pointing into the history it just downloaded, and a tag sharing a name with a
// branch then resolves onto the ref this very refspec created.
func (w *work) fetch(ctx context.Context, r remote, ref, into string) error {
	if err := w.drop(ctx, into); err != nil {
		return err
	}
	cmd, err := gitCmd(ctx, &r, nil,
		"--git-dir="+w.dir, "fetch", "--no-write-fetch-head", "--no-tags",
		r.URL, ref+":"+into)
	if err != nil {
		return err
	}
	stderr := &cappedBuffer{cap: stderrCap}
	cmd.Stderr, cmd.WaitDelay = stderr, waitDelay
	if err := withPackSlot(ctx, cmd.Run); err != nil {
		return fmt.Errorf("git fetch: %w: %s", err, sanitizeGitErr(stderr.String()))
	}
	return nil
}

// drop removes a scratch ref if it is there, so the next fetch into it is a
// create. Deleting a ref that does not exist is not an error.
func (w *work) drop(ctx context.Context, ref string) error {
	cmd, err := gitCmd(ctx, nil, nil, "--git-dir="+w.dir, "update-ref", "-d", ref)
	if err != nil {
		return err
	}
	stderr := &cappedBuffer{cap: stderrCap}
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("clear %s: %w: %s", ref, err, sanitizeGitErr(stderr.String()))
	}
	return nil
}

// push sends the object at scratch ref `src` to `ref` on r.
//
// THE REFSPEC HAS NO LEADING '+', so git will not send an update that is not a
// fast-forward, and the forge's receive-pack will not apply one whose old value
// has moved since we looked. Both refusals leave the destination exactly as it
// was.
//
// It returns the REJECTION TEXT when git refused (the caller's Conflict), an
// error for anything else, and "" with a nil error when the ref moved.
func (w *work) push(ctx context.Context, r remote, src, ref string) (string, error) {
	// A hard ceiling of its own: a stalled forge must not hold a pack slot for the
	// caller's whole budget, and a low-speed abort catches the connection that is
	// open but going nowhere.
	ctx, cancel := context.WithTimeout(ctx, pushTimeout())
	defer cancel()
	cmd, err := gitCmd(ctx, &r, []string{
		"http.lowSpeedLimit=1000", // under 1 KB/s ...
		"http.lowSpeedTime=30",    // ... for 30s, and git gives up
	}, "--git-dir="+w.dir, "push", r.URL, src+":"+ref)
	if err != nil {
		return "", err
	}
	stderr := &cappedBuffer{cap: stderrCap}
	cmd.Stderr, cmd.WaitDelay = stderr, waitDelay
	if err := withPackSlot(ctx, cmd.Run); err != nil {
		msg := sanitizeGitErr(stderr.String())
		if nonFFRE.MatchString(msg) {
			return msg, nil
		}
		return "", fmt.Errorf("git push: %w: %s", err, msg)
	}
	return "", nil
}

// ── reading a remote's refs ──────────────────────────────────────────────────

// refs lists what r advertises: every full ref name to the object it names, plus
// HEAD's symref target (the default branch).
//
// ONE round trip per remote per sync, and the map is what makes an advance cheap
// — the caller learns every tip on both sides before it fetches anything, so a
// ref that has not moved costs nothing at all. The advertisement is bounded by
// REF COUNT rather than by repository size, so it is safe to buffer.
//
// only narrows it to named refs, which is what the single-ref webhook path asks
// for: one full ref name, matched exactly by the server. It is the same function
// either way, so there is one place that knows how an advertisement is read.
func refs(ctx context.Context, r remote, only ...string) (map[string]string, string, error) {
	cmd, err := gitCmd(ctx, &r, nil, append([]string{"ls-remote", "--symref", r.URL}, only...)...)
	if err != nil {
		return nil, "", err
	}
	var out bytes.Buffer
	stderr := &cappedBuffer{cap: stderrCap}
	cmd.Stdout, cmd.Stderr, cmd.WaitDelay = &out, stderr, waitDelay
	if err := cmd.Run(); err != nil {
		return nil, "", fmt.Errorf("ls-remote: %w: %s", err, sanitizeGitErr(stderr.String()))
	}
	tips, head := map[string]string{}, ""
	for _, line := range strings.Split(out.String(), "\n") {
		// "ref: refs/heads/main\tHEAD" — the default-branch symref.
		if rest, ok := strings.CutPrefix(line, "ref: "); ok {
			if tab := strings.IndexByte(rest, '\t'); tab > 0 && strings.TrimSpace(rest[tab+1:]) == "HEAD" {
				head = strings.TrimSpace(rest[:tab])
			}
			continue
		}
		// "<oid>\t<ref>".
		tab := strings.IndexByte(line, '\t')
		if tab <= 0 {
			continue
		}
		name := strings.TrimSpace(line[tab+1:])
		// THE PEELED ENTRY IS DROPPED. An annotated tag is advertised twice: once
		// as the tag object and once as "<tag>^{}" resolving to the commit it
		// wraps. Keeping the second would overwrite the first, and the sync would
		// then push a COMMIT to a ref the source holds a TAG at — the same name
		// pointing at a different kind of object on the two hosts.
		if strings.HasSuffix(name, "^{}") {
			continue
		}
		if !syncable(name) {
			continue
		}
		tips[name] = strings.TrimSpace(line[:tab])
	}
	return tips, head, nil
}
