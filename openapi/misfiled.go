package openapi

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strings"
)

// THE ADDRESS IS THE OWNER'S NAME, and this file is the ratchet that makes it so.
//
// HIP-0139 §3: every route a capability serves is under /v1/<name>, where <name>
// is the app. The woven document carries both values on every operation — the
// address as the path, the owner as x-app (which is also the tag, §4) — so
// whether they agree is a pure function of the document, measured here.
//
// misfiled.txt lists every (root, app) pair where they do not: the address root
// an operation answers at and the app that answers it. THE FILE MAY ONLY SHRINK.
// A regeneration whose document carries a pair the file does not is refused,
// naming the pair: a new address under some other capability's name is either a
// new capability (then it is a new app, HIP-0139 §1) or a misfile (then it goes
// under /v1/<app>). Adding a line is a hand edit, reviewed beside the route that
// made it necessary. A pair that stops appearing leaves the file in the same
// regeneration, so the file cannot describe a document nobody has.
//
// Three address families are exempt, each fixed by a protocol the cloud
// implements rather than by us (HIP-0139 §3.2): /.well-known (RFC 8615), the
// OpenAI- and Anthropic-compatible wire served by ai, and /v1/admin/<app> — the
// operator's view of <app>, served by <app>.

// Wire is the vendor-compatible vocabulary: the first segments every OpenAI and
// Anthropic SDK hard-codes. They are ai's addresses by standard, and only ai's.
var Wire = map[string]bool{
	"chat": true, "completions": true, "embeddings": true, "messages": true,
	"models": true, "images": true, "audio": true, "videos": true,
	"responses": true, "rerank": true,
}

// Misfiled is the sorted set of "<root> <app>" lines.
type Misfiled []string

// Misfile measures a document: every (root, app) pair HIP-0139 §3 does not
// allow, one line each, sorted.
func Misfile(d *Document) Misfiled {
	seen := map[string]bool{}
	for path, item := range d.Paths {
		for _, op := range item {
			if r, bad := misfiled(path, op.App); bad {
				seen[r+" "+op.App] = true
			}
		}
	}
	out := make(Misfiled, 0, len(seen))
	for l := range seen {
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

// root is the address an operation is filed under: /v1/<segment> inside the
// contract's namespace, /<segment> outside it. A parameter or a wildcard in
// first position is the bare namespace — /v1/{id} is filed at /v1.
func root(path string) string {
	if rest, ok := strings.CutPrefix(path, "/v1/"); ok {
		seg, _, _ := strings.Cut(rest, "/")
		if seg == "" || strings.ContainsAny(seg, ":*{}") {
			return "/v1"
		}
		return "/v1/" + seg
	}
	seg, _, _ := strings.Cut(strings.TrimPrefix(path, "/"), "/")
	return "/" + seg
}

// misfiled is the rule, asked of one operation: the root it is filed under, and
// whether that root is a defect.
func misfiled(path, app string) (string, bool) {
	r := root(path)
	switch {
	case app == "":
		return r, false // the weave stamps every operation; nothing to compare
	case r == "/v1/"+app:
		return r, false
	case strings.HasPrefix(path, "/.well-known/"):
		return r, false
	case r == "/v1/admin" && second(path) == app:
		return r, false
	case app == "ai" && Wire[strings.TrimPrefix(r, "/v1/")]:
		return r, false
	}
	return r, true
}

// second is a path's second segment under /v1/, "" when there is none.
func second(path string) string {
	rest, _ := strings.CutPrefix(path, "/v1/")
	_, tail, _ := strings.Cut(rest, "/")
	seg, _, _ := strings.Cut(tail, "/")
	return seg
}

// ReadMisfiled reads the committed ratchet. Comments and blank lines are not
// entries; an absent file is an empty ratchet, since the first regeneration is
// what writes it.
func ReadMisfiled(path string) (Misfiled, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out Misfiled
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line, _, _ := strings.Cut(sc.Text(), "#")
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	sort.Strings(out)
	return out, sc.Err()
}

// Shrink is the ratchet: the measured set may be a subset of the committed one
// and nothing else. It returns what to commit — the measured set — or refuses,
// naming every pair the document grew.
func (was Misfiled) Shrink(now Misfiled) (Misfiled, error) {
	known := make(map[string]bool, len(was))
	for _, l := range was {
		known[l] = true
	}
	var grew []string
	for _, l := range now {
		if !known[l] {
			grew = append(grew, l)
		}
	}
	if len(grew) > 0 {
		return nil, fmt.Errorf("%d address(es) answered by an app that is not named in them (HIP-0139 §3):\n  %s\n"+
			"A route belongs under /v1/<app>. Fold it there, or — if this is a new capability — make it an app.\n"+
			"If it must ship first, add the line to openapi/misfiled.txt in this commit, with the reason.",
			len(grew), strings.Join(grew, "\n  "))
	}
	return now, nil
}

// Write commits the ratchet. The header is written every time so it cannot
// drift from the rule it describes.
func (m Misfiled) Write(path string) error {
	var b strings.Builder
	b.WriteString(misfiledHeader)
	for _, l := range m {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

const misfiledHeader = `# Addresses answered by an app that is not named in them. THIS FILE MAY ONLY SHRINK.
#
# One line per (address root, app). HIP-0139 §3: every route a capability serves
# is under /v1/<app>; a line here is an address that is not yet, and each is
# closed by fold, split or rename (§7). TestNoOperationIsMisfiled refuses a pair
# this file does not carry and drops a line the document no longer has.
#
# Exempt by rule, never listed: /.well-known/* (RFC 8615), the OpenAI- and
# Anthropic-compatible wire served by ai, and /v1/admin/<app> served by <app>.
`
