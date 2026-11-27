package coding

// idempotency.go — a request that carries an id happens once.
//
// The memory of it is the ledger (ledger.go), in the database every pod shares, so a
// retry that lands on another pod, or after a restart, is the same request. This file
// is how an op asks for it: one function, one field (`requestId`), one scope per op.

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

const (
	// idempotencyTTL is how long a request's outcome is kept. A client retries within
	// minutes; a day is a wide margin.
	idempotencyTTL              = 24 * time.Hour
	maxRequestIDLen             = 256
	maxDeveloperInstructionsLen = 48 << 10
)

// requestID is the id a caller gave, trimmed. One longer than maxRequestIDLen is refused,
// never cut: two ids that differ only past the cut would be one request, and a cut can
// land inside a rune.
func requestID(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if len(s) > maxRequestIDLen {
		return "", zip.ErrBadRequest(fmt.Sprintf("coding: a request id is at most %d bytes", maxRequestIDLen))
	}
	return s, nil
}

// printVersion is the version of the recipe a fingerprint is made by: what is hashed and
// how a start is normalized. It is written into the fingerprint ("v1:<hash>"). Change the
// recipe and raise it: a request settled under an older recipe matches its retry on the
// request id alone (samePrint), so changing it cannot refuse a retry across a deploy.
const printVersion = 1

// printOf is the fingerprint of a request's body: its version and a hash of its JSON with
// the id left out, which is normal by construction (a struct's fields in their order, no
// spacing). A duplicate that carries another body is another request under a reused id.
func printOf(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return "v" + strconv.Itoa(printVersion) + ":" + hex.EncodeToString(sum[:])
}

// samePrint says whether a request kept under print stored is the one print got asks
// for. A fingerprint made by an older recipe (one with no version is the oldest) cannot be
// compared with this one's, so it matches on the request id alone, which the caller has
// already matched; the same version matches only on the same body.
func samePrint(stored, got string) bool {
	if stored == "" || got == "" {
		return true
	}
	version := 0
	if rest, ok := strings.CutPrefix(stored, "v"); ok {
		if n, _, ok := strings.Cut(rest, ":"); ok {
			if v, err := strconv.Atoi(n); err == nil {
				version = v
			}
		}
	}
	return version < printVersion || stored == got
}

func truncateUTF8(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	s = s[:limit]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// requested runs fn at most once for the request id names, whoever asks again and
// wherever: the first attempt runs it and a duplicate answers what it answered. scope
// names the op (and the run, for an op on one: "steer/<session>"), and print is the
// fingerprint of the body: a duplicate whose body differs is another request under a
// reused id, and is refused 422.
//
// A request id that cannot be kept is refused 400, never dropped: one for a caller that
// names no person, or an op that names no run, would be run twice by a retry. And a
// request that names an id while this process records none is refused, never run on a
// guess: it may be the second of two.
func requested[T any](ctx context.Context, l *ledger, org, subject, scope, id, print string, fn func(context.Context) (T, error)) (T, error) {
	var zero T
	id, err := requestID(id)
	if err != nil {
		return zero, err
	}
	if id == "" {
		return fn(ctx)
	}
	org, subject = strings.TrimSpace(org), strings.TrimSpace(subject)
	if scope == "" || org == "" || subject == "" {
		return zero, zip.ErrBadRequest("coding: a request id needs the run it is about and a credential that names the person; send it without one")
	}
	if l == nil {
		return zero, zip.Errorf(http.StatusServiceUnavailable, "coding: this process records no requests, so a request id cannot be honoured; try again")
	}
	return once(ctx, l, request{org: org, subject: subject, scope: scope, id: id, print: print}, func(c *claim) (T, error) {
		return fn(withClaim(ctx, c))
	})
}

// scoped names a request of op on one run: "steer/<session>". A request that names no
// run is scoped to none.
func scoped(op, session string) string {
	if session = strings.TrimSpace(session); session == "" {
		return ""
	}
	return op + "/" + session
}

// asker is who an op on a run is asked by: the caller's org, and the subject the
// boundary attested. A credential that names no subject asks as nobody, and its
// request is run as asked.
func asker(ctx context.Context) (org, subject string) {
	org = strings.TrimSpace(cloud.Who(ctx).Org)
	if sub, ok := principal.SubjectFrom(ctx); ok {
		subject = sub
	}
	return org, subject
}

// filed names the request in ctx for a callee that files a row for it, so a replay files
// none: who asked, what, and the id. Empty for a request nobody asked twice.
func filed(ctx context.Context) string {
	c := claimOf(ctx)
	if c == nil {
		return ""
	}
	return c.k.subject + "\x1f" + c.k.scope + "\x1f" + c.k.id
}

// fenced answers 409 when the request in ctx has been taken over by another attempt. An
// op whose effect cannot be undone asks it just before making it: a claimer that was
// retaken (it was slow, not dead) makes nothing the retaker will make again. A request
// nobody asked twice has no claim to lose.
func fenced(ctx context.Context) error {
	c := claimOf(ctx)
	if c == nil {
		return nil
	}
	switch err := c.Fence(ctx); {
	case err == nil:
		return nil
	case errors.Is(err, errClaim):
		return zip.ErrConflict("coding: another attempt at this request now owns it; ask again for its answer")
	default:
		return unavailable(err)
	}
}

// normalized is a start as start reads it: the aliases folded (task into prompt,
// instructions into developerInstructions), text trimmed and cut where start cuts it, a
// branch written as it will be, and mode, model and effort as the run will carry them. Two
// bodies start reads as one request are fingerprinted as one, so a retry spelled another way
// is a retry. A base given and a base left to default from `after` are two requests, because
// start reads them differently (a base given is kept where one defaulted is replaced), so
// they are not folded. What the request does not say is not filled from the person's
// settings: those can change between a request and its retry.
func normalized(in client.CodingStartIn) client.CodingStartIn {
	in.RequestID = ""
	in.Prompt, in.Task = cmp.Or(strings.TrimSpace(in.Prompt), strings.TrimSpace(in.Task)), ""
	if len(in.Prompt) > maxPromptLen {
		in.Prompt = in.Prompt[:maxPromptLen]
	}
	in.DeveloperInstructions = truncateUTF8(cmp.Or(strings.TrimSpace(in.DeveloperInstructions), strings.TrimSpace(in.Instructions)), maxDeveloperInstructionsLen)
	in.Instructions = ""
	in.Repo, in.Tool, in.TargetID = strings.TrimSpace(in.Repo), strings.TrimSpace(in.Tool), strings.TrimSpace(in.TargetID)
	in.Project, in.AgentRef, in.After = strings.TrimSpace(in.Project), strings.TrimSpace(in.AgentRef), strings.TrimSpace(in.After)
	in.Conversation, in.Model, in.Effort = strings.TrimSpace(in.Conversation), strings.TrimSpace(in.Model), strings.TrimSpace(in.Effort)
	in.Base = strings.TrimSpace(in.Base)
	if b, err := AgentBranch(in.Branch); err == nil {
		in.Branch = b
	}
	if issue, err := Closing(in.Issue); err == nil {
		in.Issue = client.Issue(issue)
	}
	if mode, model, effort, err := checkAsk(in.Tool, in.TargetID, in.Mode, in.Model, in.Effort); err == nil {
		in.Mode, in.Model, in.Effort = mode, model, effort
	}
	return in
}
