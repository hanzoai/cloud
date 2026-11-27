package forge

// change.go answers what a branch CHANGED: the commits it carries that its base
// does not, and the net change it makes, file by file. They are the two things a
// reviewer reads, before a pull request exists and after.
//
// Both are ONE comparison on the forge — GET /repos/{o}/{r}/compare/{base}...{head}
// — asked two ways: as JSON for the commits, and as the raw unified diff
// (?output=diff) for the files. Neither of the forge's other file lists will do.
// The JSON's own per-commit files are not the NET change — a file added by one
// commit and deleted by the next is in both and in neither — and a pull request's
// file list exists only once there is a pull request, and carries no patch. The
// three-dot diff is the net change against the merge base by definition, so it is
// read once and split here into one entry per file.
//
// # What is bounded
//
// Both are read for a PERSON LOOKING at a change, and each answers what fits in a
// page rather than refusing what does not. A diff is a stream the forge writes and
// this process reads, so the bounds sit where a bad one would cost: [maxDiff] on
// the bytes read at all and [maxChanges] on the files listed, and [maxPatch] and
// [maxPatches] on the patch text KEPT. The last two cut a patch and mark it
// Truncated and never drop a file. The first two cut the CHANGE: the files read
// so far are listed, the one being read when the budget ran out is marked
// Truncated, and Changes says the list is not the whole change — a list cut short
// is only wrong when it is presented as complete. [maxCommits] bounds the commits
// the same way, keeping the newest.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// maxPatch is the most of ONE file's patch kept. Past it the patch ends at the
	// last whole line that fits and the file is marked Truncated.
	maxPatch = 64 << 10
	// maxPatches is the most patch text kept across every file of one comparison.
	// A file past it is listed with its counts and no patch, marked Truncated.
	maxPatches = 1 << 20
	// maxDiff bounds the diff read at all — eight times the patch text a reader is
	// handed, so the counts of a change several times larger than what is shown
	// are still whole. Past it the change is cut, not refused.
	maxDiff = 8 << 20
	// maxChanges is the most files one change lists. A change past it is cut.
	maxChanges = 3000
	// maxCommits is the most commits one comparison lists, the newest.
	maxCommits = 250
)

// Commit is one commit of a comparison, as a reader of the change is shown it.
type Commit struct {
	SHA string
	// Message is the whole message; a caller showing a line takes the first.
	Message string
	// Author is the commit author's name as git recorded it, not a forge login:
	// a run's commits are authored by its harness, which has no account.
	Author string
	// Date is the author date. Zero when the forge sent none it could parse.
	Date time.Time
}

// Change is one file a comparison touches.
type Change struct {
	Path string
	// From is where a renamed file was; empty for every other status.
	From string
	// Status is added, modified, deleted or renamed. A mode change alone is
	// modified; a copy, which a plain diff does not detect, is added.
	Status string
	// Additions and Deletions count the patch's added and removed lines, over the
	// whole patch whether or not all of it was kept — except in the file a cut
	// change stopped in, where they count the part read.
	Additions, Deletions int
	// Patch is the file's hunks, from its first "@@": empty for a binary file and
	// for a rename or a mode change that moved no line.
	Patch string
	// Truncated marks a Patch cut at [maxPatch] or [maxPatches], and the file a
	// change was cut in at [maxDiff].
	Truncated bool
}

// Compare is the commits head carries that base does not, newest first, read as
// this client's actor. Both are refs the forge resolves: a branch, a tag or a
// commit, slashed or not.
//
// At most [maxCommits] are listed; more reports that head carries more than that,
// and the list is then the newest of them. The forge sends the whole list, so it
// is read an element at a time and left unread past the bound.
//
// A ref the forge does not have is 404, which on a sudoed call surfaces as
// [ErrUnknownActor] — a caller that must tell "no such branch" from "cannot see
// this repository" reads the repository first.
func (c *Client) Compare(ctx context.Context, owner, repo, base, head string) (commits []Commit, more bool, err error) {
	path, err := comparePath(owner, repo, base, head)
	if err != nil {
		return nil, false, err
	}
	// files and verification OFF: the commits are wanted, not each one's diff or
	// its signature check, and the files come from the diff below.
	q := url.Values{"files": {"false"}, "verification": {"false"}}
	resp, err := c.open(ctx, path, q, "application/json")
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	commits, more, err = commitsOf(io.LimitReader(resp.Body, maxBody), maxCommits)
	if err != nil {
		return nil, false, fmt.Errorf("forge: decode %s: %w", path, err)
	}
	return commits, more, nil
}

// commitsOf reads the commits of a comparison, at most max of them, and reports
// whether there were more. Every other member of the object is skipped whole.
func commitsOf(r io.Reader, max int) ([]Commit, bool, error) {
	dec := json.NewDecoder(r)
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, false, fmt.Errorf("a comparison is an object: %v", err)
	}
	out := []Commit{}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return nil, false, err
		}
		if key != "commits" {
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return nil, false, err
			}
			continue
		}
		t, err := dec.Token()
		if err != nil {
			return nil, false, err
		}
		if t == nil {
			continue // "commits": null is none
		}
		if t != json.Delim('[') {
			return nil, false, fmt.Errorf("commits is not a list")
		}
		for dec.More() {
			if len(out) == max {
				return out, true, nil
			}
			var x struct {
				SHA    string `json:"sha"`
				Commit struct {
					Message string `json:"message"`
					Author  struct {
						Name string `json:"name"`
						Date string `json:"date"`
					} `json:"author"`
				} `json:"commit"`
			}
			if err := dec.Decode(&x); err != nil {
				return nil, false, err
			}
			at, _ := time.Parse(time.RFC3339, x.Commit.Author.Date)
			out = append(out, Commit{SHA: x.SHA, Message: x.Commit.Message, Author: x.Commit.Author.Name, Date: at})
		}
		if _, err := dec.Token(); err != nil {
			return nil, false, err
		}
	}
	return out, false, nil
}

// Changes is the net change head makes against its merge base with base, one
// entry per file in the order the diff lists them, read as this client's actor.
//
// more reports a change larger than one read answers — past [maxDiff] bytes of
// diff or [maxChanges] files — and the list is then the files read before the
// bound, the last of them marked Truncated. It is a cut, never a refusal: a
// reader is shown what fits and told there is more.
func (c *Client) Changes(ctx context.Context, owner, repo, base, head string) (changes []Change, more bool, err error) {
	path, err := comparePath(owner, repo, base, head)
	if err != nil {
		return nil, false, err
	}
	resp, err := c.open(ctx, path, url.Values{"output": {"diff"}}, "text/plain")
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	changes, more, err = split(resp.Body)
	if err != nil {
		return nil, false, fmt.Errorf("forge: diff %s...%s on %s/%s: %w", base, head, owner, repo, err)
	}
	return changes, more, nil
}

// comparePath is the comparison route for base...head. The refs travel
// UNESCAPED because the route is a wildcard the forge splits on "...", which
// [validRef] already guarantees neither ref contains.
func comparePath(owner, repo, base, head string) (string, error) {
	if err := validSegment(owner); err != nil {
		return "", err
	}
	if err := validSegment(repo); err != nil {
		return "", fmt.Errorf("forge: repo: %w", err)
	}
	for _, ref := range []string{base, head} {
		if err := validRef(ref); err != nil {
			return "", err
		}
	}
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/compare/" + base + "..." + head, nil
}

// split reads a unified git diff into one Change per file, and reports whether
// it stopped at a bound ([maxDiff] bytes, [maxChanges] files) before the diff
// ended.
//
// It reads a LINE AT A TIME without holding one: a line longer than the buffer
// arrives in pieces, so a minified bundle's single line costs the buffer and not
// its length. Only the start of a line decides what it is — a file header is
// short, and every hunk line is known by its first byte.
func split(r io.Reader) ([]Change, bool, error) {
	lim := &io.LimitedReader{R: r, N: maxDiff}
	br := bufio.NewReaderSize(lim, 64<<10)

	var (
		out   []Change
		cur   *Change
		patch []byte
		from  string // the header's old path, for a file the diff deletes
		hunks bool   // cur is past its headers and into its hunks
		cut   bool   // cur's patch was cut; nothing more of it is kept
		kept  int    // patch bytes kept by the files before cur
		keep  bool   // the line being read belongs in cur's patch
		mark  int    // len(patch) when that line began
		start = true // the next piece begins a line
	)
	finish := func() {
		if cur == nil {
			return
		}
		cur.Patch = string(patch)
		kept += len(patch)
		out = append(out, *cur)
		cur, patch, from, hunks, cut = nil, nil, "", false, false
	}
	for {
		piece, err := br.ReadSlice('\n')
		if len(piece) > 0 {
			if start {
				keep = false
				line := bytes.TrimSuffix(piece, []byte("\n"))
				switch {
				case bytes.HasPrefix(line, []byte("diff --git ")):
					finish()
					if len(out) >= maxChanges {
						return out, true, nil
					}
					old, path := header(string(line[len("diff --git "):]))
					cur, from = &Change{Path: path, Status: "modified"}, old
				case cur == nil:
					// Nothing precedes the first header in a git diff; anything that
					// did is not a file's and is not read as one.
				case hunks:
					switch {
					case bytes.HasPrefix(line, []byte("+")):
						cur.Additions++
					case bytes.HasPrefix(line, []byte("-")):
						cur.Deletions++
					}
					keep = true
				default:
					keep = extended(cur, &from, &hunks, string(line))
				}
				if keep && !cut {
					mark = len(patch)
				}
			}
			if keep && !cut {
				if len(patch)+len(piece) > maxPatch || kept+len(patch)+len(piece) > maxPatches {
					// Cut at the last whole line, so what is kept is a patch a reader
					// can read and not a line stopped halfway.
					patch, cut, cur.Truncated = patch[:mark], true, true
				} else {
					patch = append(patch, piece...)
				}
			}
			start = piece[len(piece)-1] == '\n'
		}
		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			// The same accounting [unpack] keeps: having spent the budget is not
			// having reached the end. The file being read is cut there, and the
			// change says it is not whole rather than reading as if it were.
			spent := lim.N <= 0
			if spent && cur != nil {
				cur.Truncated = true
			}
			finish()
			if out == nil {
				out = []Change{}
			}
			return out, spent, nil
		case err != nil:
			return nil, false, fmt.Errorf("read: %w", err)
		}
	}
}

// extended reads one line between a file's `diff --git` header and its first
// hunk — the lines that say what happened to the file rather than how — and
// reports whether it opens the hunks, which is the first line a patch keeps.
func extended(cur *Change, from *string, hunks *bool, line string) bool {
	switch {
	case strings.HasPrefix(line, "@@"):
		*hunks = true
		return true
	case strings.HasPrefix(line, "new file mode "), strings.HasPrefix(line, "copy to "):
		cur.Status = "added"
		if p, ok := strings.CutPrefix(line, "copy to "); ok {
			cur.Path = unquote(p)
		}
	case strings.HasPrefix(line, "deleted file mode "):
		cur.Status = "deleted"
	case strings.HasPrefix(line, "rename from "):
		cur.Status, cur.From = "renamed", unquote(line[len("rename from "):])
	case strings.HasPrefix(line, "rename to "):
		cur.Status, cur.Path = "renamed", unquote(line[len("rename to "):])
	case strings.HasPrefix(line, "--- "):
		// git ends a name that holds a space with a tab, so the name can be read
		// back out of the line; the tab is not part of it.
		if p := strings.TrimSuffix(line[len("--- "):], "\t"); p != "/dev/null" {
			*from = side(unquote(p), "a/")
		}
	case strings.HasPrefix(line, "+++ "):
		if p := strings.TrimSuffix(line[len("+++ "):], "\t"); p == "/dev/null" {
			cur.Status, cur.Path = "deleted", *from
		} else if cur.Status != "renamed" {
			cur.Path = side(unquote(p), "b/")
		}
	}
	return false
}

// header reads the two paths of a `diff --git a/<old> b/<new>` line.
//
// A name git had to quote is quoted and read as such. An unquoted one may hold a
// space, which makes the line ambiguous in general and not in the case that
// matters: a file that was not renamed names itself twice, so the line is
// "a/P b/P" and P is its first half. A rename that is ambiguous here is read
// again from its `rename from` and `rename to` lines, which are not.
func header(s string) (old, path string) {
	if strings.HasPrefix(s, `"`) {
		a, rest := quoted(s)
		b := strings.TrimPrefix(rest, " ")
		if strings.HasPrefix(b, `"`) {
			b, _ = quoted(b)
		}
		return side(a, "a/"), side(b, "b/")
	}
	if n := len(s) - len("a/ b/"); n > 0 && n%2 == 0 {
		p := s[2 : 2+n/2]
		if s == "a/"+p+" b/"+p {
			return p, p
		}
	}
	a, b, ok := strings.Cut(s, " b/")
	if !ok {
		return s, s
	}
	if strings.HasPrefix(b, `"`) {
		b, _ = quoted(b)
	}
	return side(a, "a/"), b
}

// quoted reads the C-quoted name s begins with, and what follows it.
func quoted(s string) (name, rest string) {
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			return unquote(s[:i+1]), s[i+1:]
		}
	}
	return s, ""
}

// unquote reads a name git quoted — "a/na\303\257ve \"x\"" — and returns any
// other name as it is. git's escapes are a subset of Go's, so strconv reads them;
// its octal escapes are bytes, which is what a non-ASCII name is.
func unquote(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		if u, err := strconv.Unquote(s); err == nil {
			return u
		}
	}
	return s
}

// side strips the a/ or b/ git puts before each side's name.
func side(s, prefix string) string { return strings.TrimPrefix(s, prefix) }
