package network

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/secret"
)

// codeShape is an invite code as issued: HNZ- and ten characters of Crockford base32
// (no I, L, O or U), in two groups of five.
var codeShape = regexp.MustCompile(`^HNZ-[0-9A-HJKMNP-TV-Z]{5}-[0-9A-HJKMNP-TV-Z]{5}$`)

const (
	yes = `{"valid":true}`
	no  = `{"valid":false}`

	// The codes the tests seed, in the one spelling a code has.
	live    = "HNZ-ABCD2-3EFGH"
	revoked = "HNZ-RVKD5-6PQRS"
	lapsed  = "HNZ-EXPR7-8TVWX"
)

// miss is the i-th code that was never issued.
func miss(i int) string { return fmt.Sprintf("HNZ-%05d-ZZZZZ", i) }

// from is the i-th caller, as a public address (the edge ignores a private one).
func from(i int) string { return fmt.Sprintf("45.%d.%d.%d", i/65536%256, i/256%256, i%256) }

// TestVerifyAcceptsEverySpellingOfAnIssuedCode: a person copies a code from a mail
// and types it back as they read it. Case, hyphens or none, spaces, and the
// characters Crockford folds (I and L for 1, O for 0) all find the invite.
func TestVerifyAcceptsEverySpellingOfAnIssuedCode(t *testing.T) {
	in := mountIntake(t)
	in.seed(t, "HNZ-01ABC-DEFGH", nil)
	in.seed(t, "HNZ-7K9Q2-MXZ4P", nil)

	for i, spelling := range []string{
		"HNZ-01ABC-DEFGH", "hnz-01abc-defgh", "HNZ01ABCDEFGH", "hnz01abcdefgh", "  HNZ-01ABC-DEFGH\n",
		"HNZ 01ABC DEFGH", "hnz-01abc defgh", "HNZ-01ABC-DEFGH ", "H N Z - 0 1 A B C - D E F G H", "HNZ-01-ABCDE-FGH",
		"HNZ-OIABC-DEFGH", "hnz-oiabc-defgh", "HNZ-OlABC-DEFGH", "HNZOIABCDEFGH", // O for 0, I and l for 1
		"HNZ-7K9Q2-MXZ4P", "hnz-7k9q2-mxz4p", "hnz7k9q2mxz4p",
	} {
		a := in.verify(t, spelling, from(10+i))
		if a.code != http.StatusOK || a.body != yes {
			t.Errorf("%q: %d %s, want 200 %s", spelling, a.code, a.body, yes)
		}
	}
}

// TestVerifyAnswersAlikeWhateverIsWrongWithTheCode: a code never issued, one that
// only looks like a code, one revoked and one expired get the same status, the same
// content type, the same headers and the same body, byte for byte.
func TestVerifyAnswersAlikeWhateverIsWrongWithTheCode(t *testing.T) {
	in := mountIntake(t)
	in.seed(t, revoked, func(r *map[string]any) { (*r)["status"] = "revoked" })
	in.seed(t, lapsed, func(r *map[string]any) {
		(*r)["expires_at"] = time.Now().UTC().Add(-time.Minute).Format(baseDate)
	})
	in.seed(t, live, nil)

	wrong := []string{
		"HNZ-ZZZZZ-ZZZZZ",                    // well formed, never issued
		"HNZ-RVKD5-6PQRS", "hnz-rvkd5-6pqrs", // revoked, in two spellings
		"HNZ-EXPR7-8TVWX",                     // expired
		"HNZ-ABCD2-3EFG",                      // too short: nine characters
		"HNZ-ABCD2-3EFGHJ",                    // too long: eleven
		"HNZ-ABCD23",                          // the old six-character shape
		"ABC-ABCD2-3EFGH",                     // another prefix
		"HNZ-ABCD2-3EFGU",                     // U is not in the alphabet
		"HNZ-ABCD2-3EFG!",                     // punctuation
		"HNZ-ABCD2-3EFGH\u200b",               // a zero-width space
		"HNZ-ABCD2-3EFG\u00c8",                // a letter outside ASCII
		"ＨＮＺ-ＡＢＣＤ２-３ＥＦＧＨ",                     // full-width letters
		strings.Repeat("HNZ-ABCD2-3EFGH", 40), // a long body
		"0",
	}
	var first answer
	for i, code := range wrong {
		a := in.verify(t, code, from(100+i))
		if i == 0 {
			first = a
		}
		if a != first || a.code != http.StatusOK || a.body != no {
			t.Errorf("%q answered %d %q %s, the first answered %d %q %s", code, a.code, a.kind, a.body, first.code, first.kind, first.body)
		}
	}
	if a := in.verify(t, live, from(200)); a.body != yes {
		t.Fatalf("the live invite beside them answered %d %s", a.code, a.body)
	}
}

// TestVerifyAnswersOnlyValid: the answer holds one field and says nothing about who
// issued the code, when, or for what.
func TestVerifyAnswersOnlyValid(t *testing.T) {
	in := mountIntake(t)
	in.seed(t, live, func(r *map[string]any) { (*r)["note"] = "for ada"; (*r)["created_by"] = "admin/z" })
	a := in.verify(t, live, from(1))
	var got map[string]any
	if err := json.Unmarshal([]byte(a.body), &got); err != nil || len(got) != 1 || got["valid"] != true {
		t.Fatalf("answered %s, want exactly {\"valid\":true}", a.body)
	}
}

// TestVerifyDoesNotConsumeTheCode: a code verified any number of times stays good,
// and the record is exactly as it was.
func TestVerifyDoesNotConsumeTheCode(t *testing.T) {
	in := mountIntake(t)
	in.seed(t, live, nil)
	before := records(t, "hanzo", invitesName)
	for i := range 5 {
		if a := in.verify(t, live, from(7)); a.body != yes {
			t.Fatalf("verification %d: %d %s", i+1, a.code, a.body)
		}
	}
	after := records(t, "hanzo", invitesName)
	b, _ := json.Marshal(before)
	c, _ := json.Marshal(after)
	if string(b) != string(c) {
		t.Fatalf("verifying changed the store:\n%s\n%s", b, c)
	}
}

// TestFiveMissesLockOneCallerOut: a caller that has missed five times in an hour is
// refused 429 on its next verification, whatever it asks, so the refusal says
// nothing about whether a code would have been valid, and says when to come back.
// Another caller is untouched.
func TestFiveMissesLockOneCallerOut(t *testing.T) {
	in := mountIntake(t)
	in.seed(t, live, nil)
	for i := range 5 {
		if a := in.verify(t, miss(i), from(7)); a.body != no {
			t.Fatalf("miss %d: %d %s", i+1, a.code, a.body)
		}
	}
	for _, code := range []string{miss(9), live, "not a code"} {
		a := in.verify(t, code, from(7))
		if !problem(a, http.StatusTooManyRequests) {
			t.Fatalf("%q past five misses: want a 429 problem document, got %d %q %s", code, a.code, a.kind, a.body)
		}
		if secs, err := strconv.Atoi(retryAfter(a)); err != nil || secs < 1 {
			t.Fatalf("%q: Retry-After %q, want seconds", code, retryAfter(a))
		}
	}
	if a := in.verify(t, live, from(8)); a.body != yes {
		t.Fatalf("another caller: %d %s", a.code, a.body)
	}
}

// retryAfter is the Retry-After header of a response, from its dump of headers.
func retryAfter(a answer) string {
	for _, l := range strings.Split(a.head, "\n") {
		if v, ok := strings.CutPrefix(l, "Retry-After: "); ok {
			return v
		}
	}
	return ""
}

// TestAHitIsNotAMiss: only misses count toward the lockout. Four misses and as many
// hits do not lock a caller; the fifth miss does, and the next request, a valid code,
// is refused.
func TestAHitIsNotAMiss(t *testing.T) {
	in := mountIntake(t)
	in.seed(t, live, nil)
	ip := from(7)
	for i := range 4 {
		if a := in.verify(t, miss(i), ip); a.body != no {
			t.Fatalf("miss %d: %d %s", i+1, a.code, a.body)
		}
		if a := in.verify(t, live, ip); a.body != yes {
			t.Fatalf("hit %d: %d %s", i+1, a.code, a.body)
		}
	}
	if a := in.verify(t, miss(8), ip); a.body != no {
		t.Fatalf("the fifth miss: %d %s", a.code, a.body)
	}
	if a := in.verify(t, live, ip); !problem(a, http.StatusTooManyRequests) {
		t.Fatalf("after five misses even a hit is refused: got %d %s", a.code, a.body)
	}
}

// TestAnAddressInTheSameIPv6BlockSharesTheLockout: a caller cannot step round the
// lockout by rotating through the addresses of one /64.
func TestAnAddressInTheSameIPv6BlockSharesTheLockout(t *testing.T) {
	in := mountIntake(t)
	for i := range 5 {
		ip := fmt.Sprintf("2001:db8:1:2::%x", i+1)
		if a := in.verify(t, miss(i), ip); a.body != no {
			t.Fatalf("miss %d from %s: %d %s", i+1, ip, a.code, a.body)
		}
	}
	if a := in.verify(t, miss(9), "2001:db8:1:2::ffff"); !problem(a, http.StatusTooManyRequests) {
		t.Fatalf("a sixth address of the same /64: want 429, got %d %s", a.code, a.body)
	}
}

// TestGarbageFromManyCallersNeverClosesTheGate: callers that each guess wrongly, a
// great many of them, never make another caller's valid code answer 429. The
// deployment's count of misses past its budget says so once and refuses no one.
func TestGarbageFromManyCallersNeverClosesTheGate(t *testing.T) {
	in := mountIntake(t)
	in.seed(t, live, nil)
	for c := range 60 { // 60 callers, four misses each: nobody is locked out
		for i := range 4 {
			if a := in.verify(t, miss(c*4+i), from(1000+c)); a.body != no {
				t.Fatalf("caller %d miss %d: %d %s", c, i, a.code, a.body)
			}
		}
	}
	// The deployment's budget of misses for the window is spent.
	in.fill(t, budgetName, "all", missBudget, missBudget, missWindow)
	for i := range 5 {
		if a := in.verify(t, miss(5000+i), from(2000+i)); a.body != no {
			t.Fatalf("a miss past the budget: %d %s", a.code, a.body)
		}
	}
	for i := range 3 {
		if a := in.verify(t, live, from(3000+i)); a.body != yes {
			t.Fatalf("a valid code past the budget, from a caller that never missed: %d %s", a.code, a.body)
		}
	}
	if n := strings.Count(in.logs.String(), "invite miss budget"); n != 1 {
		t.Fatalf("the warning event was logged %d times, want once for the window:\n%s", n, in.logs.String())
	}
}

// TestUnderTheBudgetNothingIsSaid: misses under the deployment's budget raise no
// warning.
func TestUnderTheBudgetNothingIsSaid(t *testing.T) {
	in := mountIntake(t)
	in.fill(t, budgetName, "all", missBudget-3, missBudget, missWindow)
	for i := range 3 {
		in.verify(t, miss(i), from(i))
	}
	if strings.Contains(in.logs.String(), "invite miss budget") {
		t.Fatalf("a warning under the budget:\n%s", in.logs.String())
	}
}

// TestVerifyRefusalsAreProblemDocumentsInOrder: 429 for a caller past ten
// verifications an hour comes before the body is judged; then 413 past 4 KiB and
// 400 for a body with no code to read.
func TestVerifyRefusalsAreProblemDocumentsInOrder(t *testing.T) {
	in := mountIntake(t)
	in.seed(t, live, nil)
	for i, c := range []struct {
		name, body string
		code       int
	}{
		{"no body fields", `{}`, http.StatusBadRequest},
		{"an empty code", `{"code":""}`, http.StatusBadRequest},
		{"a blank code", `{"code":"   "}`, http.StatusBadRequest},
		{"a number for the code", `{"code":7}`, http.StatusBadRequest},
		{"not json", `not json`, http.StatusBadRequest},
		{"past 4 KiB", `{"code":"` + strings.Repeat("x", 4096) + `"}`, http.StatusRequestEntityTooLarge},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := in.send(t, http.MethodPost, "/v1/network/invite/verify", c.body, visitor, from(400+i))
			if !problem(a, c.code) {
				t.Fatalf("want a %d problem document, got %d %q %s", c.code, a.code, a.kind, a.body)
			}
		})
	}

	ip := from(50)
	for i := range 10 {
		if a := in.verify(t, live, ip); a.body != yes {
			t.Fatalf("verification %d: %d %s", i+1, a.code, a.body)
		}
	}
	a := in.verify(t, live, ip)
	if !problem(a, http.StatusTooManyRequests) {
		t.Fatalf("past ten an hour: want 429, got %d %s", a.code, a.body)
	}
	if secs, err := strconv.Atoi(retryAfter(a)); err != nil || secs < 1 || secs > 3600 {
		t.Fatalf("Retry-After %q, want 1 to 3600 seconds", retryAfter(a))
	}
	if a := in.send(t, http.MethodPost, "/v1/network/invite/verify", `{}`, visitor, ip); a.code != http.StatusTooManyRequests {
		t.Fatalf("past the rate a body is not judged: want 429, got %d %s", a.code, a.body)
	}
}

// TestVerifyNeedsTheDeployment: a deployment that cannot hold invites says so with
// 503 before it reads the body: a brand outside the registry, no KMS, and a KMS that
// cannot answer.
func TestVerifyNeedsTheDeployment(t *testing.T) {
	t.Run("an unregistered brand", func(t *testing.T) {
		in := mountIntake(t)
		t.Setenv("CLOUD_BRAND", "not-a-brand")
		if a := in.verify(t, miss(0), ""); !problem(a, http.StatusServiceUnavailable) {
			t.Fatalf("want 503, got %d %s", a.code, a.body)
		}
	})
	t.Run("no KMS", func(t *testing.T) {
		in := mountIntake(t)
		in.svc.State.seal = sealOf(in.svc.Coord) // a restart: the key is read again
		in.svc.KMS = nil
		if a := in.verify(t, miss(0), ""); !problem(a, http.StatusServiceUnavailable) {
			t.Fatalf("want 503, got %d %s", a.code, a.body)
		}
	})
	t.Run("a KMS that cannot answer", func(t *testing.T) {
		in := mountIntake(t)
		in.svc.State.seal = sealOf(in.svc.Coord)
		in.svc.KMS = brokenKMS{}
		if a := in.verify(t, miss(0), ""); !problem(a, http.StatusServiceUnavailable) {
			t.Fatalf("want 503, got %d %s", a.code, a.body)
		}
	})
}

// TestAFaultIsNotAMiss: when the deployment cannot answer, the caller is told so and
// has not spent one of its five misses.
func TestAFaultIsNotAMiss(t *testing.T) {
	in := mountIntake(t)
	in.seed(t, live, nil)
	in.svc.State.seal = sealOf(in.svc.Coord) // a restart: the key is read again
	in.svc.KMS = brokenKMS{}
	for i := range 6 {
		if a := in.verify(t, miss(0), from(60)); !problem(a, http.StatusServiceUnavailable) {
			t.Fatalf("request %d: want 503, got %d %s", i+1, a.code, a.body)
		}
	}
	in.svc.KMS = in.kms
	in.svc.State.seal = sealOf(in.svc.Coord)
	if a := in.verify(t, live, from(60)); a.body != yes {
		t.Fatalf("after the KMS came back: %d %s", a.code, a.body)
	}
}

// TestAShortKeyIsAFaultAndIsNeverReplaced: a key in KMS under the name that is too
// short to be one is read as a fault, by verify and by issue alike, and neither
// writes a new key over it, which would orphan every code already issued.
func TestAShortKeyIsAFaultAndIsNeverReplaced(t *testing.T) {
	in := mountUnsealed(t)
	if err := in.kms.PutSecret(context.Background(), inviteRef, []byte("too-short")); err != nil {
		t.Fatal(err)
	}
	if a := in.verify(t, miss(0), from(1)); !problem(a, http.StatusServiceUnavailable) {
		t.Fatalf("verify: want 503, got %d %s", a.code, a.body)
	}
	if a := in.send(t, http.MethodPost, invitesPath, `{"count":1}`, operator, ""); !problem(a, http.StatusServiceUnavailable) {
		t.Fatalf("issue: want 503, got %d %s", a.code, a.body)
	}
	got, err := in.kms.GetSecret(context.Background(), inviteRef)
	if err != nil || string(got) != "too-short" {
		t.Fatalf("the key is now %q, %v; it must be left as it was", got, err)
	}
	if n := len(records(t, "hanzo", invitesName)); n != 0 {
		t.Fatalf("a refused issue wrote %d invites", n)
	}
}

// TestAnAnonymousOpOnAnUnsealedDeploymentIs503AndMintsNothing: with no invite key in KMS
// a stranger's verification or waitlist ask is refused 503, and nothing the stranger did
// sealed one. Only an operator's issue mints the key.
func TestAnAnonymousOpOnAnUnsealedDeploymentIs503AndMintsNothing(t *testing.T) {
	in := mountUnsealed(t)
	for i := range 3 {
		if a := in.verify(t, miss(i), from(i)); !problem(a, http.StatusServiceUnavailable) {
			t.Fatalf("verify: want 503, got %d %s", a.code, a.body)
		}
		if a := in.join(t, `{"xHandle":"ada","role":"staker"}`, from(10+i)); !problem(a, http.StatusServiceUnavailable) {
			t.Fatalf("waitlist: want 503, got %d %s", a.code, a.body)
		}
	}
	if _, err := in.kms.GetSecret(context.Background(), inviteRef); !errors.Is(err, secret.ErrNone) {
		t.Fatalf("a stranger's request left %v in KMS, want no key", err)
	}
}

// TestTheFirstIssueMintsTheKeyAndThenTheIntakeAnswers: an operator's issue on an unsealed
// deployment seals the key, the code it made verifies, and the waitlist opens.
func TestTheFirstIssueMintsTheKeyAndThenTheIntakeAnswers(t *testing.T) {
	in := mountUnsealed(t)
	out := in.issue(t, `{"count":1}`)
	if k, err := in.kms.GetSecret(context.Background(), inviteRef); err != nil || len(k) < 32 {
		t.Fatalf("after the first issue KMS holds %d bytes, %v", len(k), err)
	}
	if a := in.verify(t, out.Invites[0].Code, from(1)); a.body != yes {
		t.Fatalf("the code just issued: %d %s", a.code, a.body)
	}
	if a := in.join(t, `{"xHandle":"ada","role":"staker"}`, from(2)); a.code != http.StatusAccepted {
		t.Fatalf("the waitlist after the first issue: %d %s", a.code, a.body)
	}
}

// TestSeveralFirstIssuesEndWithOneKey: operators' first issues that arrive together leave
// one key in KMS, and every code they made verifies under it.
func TestSeveralFirstIssuesEndWithOneKey(t *testing.T) {
	in := mountUnsealed(t)
	done := make(chan issued, 4)
	for range 4 {
		go func() { done <- in.issue(t, `{"count":1}`) }()
	}
	var codes []string
	for range 4 {
		codes = append(codes, (<-done).Invites[0].Code)
	}
	k, err := in.kms.GetSecret(context.Background(), inviteRef)
	if err != nil || len(k) < 32 {
		t.Fatalf("KMS holds %d bytes, %v", len(k), err)
	}
	for i, c := range codes {
		if a := in.verify(t, c, from(i)); a.body != yes {
			t.Fatalf("code %d does not verify under the one key: %d %s", i, a.code, a.body)
		}
	}
}

// TestVerifyIgnoresAForgedOrg: the brand org is the server's. A header naming
// another org neither finds that org's invite nor hides the brand's.
func TestVerifyIgnoresAForgedOrg(t *testing.T) {
	in := mountIntake(t)
	in.seed(t, live, nil)
	// The stranger's X-Org-Id: mallory is set by send. The invite is the brand's.
	if a := in.verify(t, live, from(70)); a.body != yes {
		t.Fatalf("with a forged org header: %d %s", a.code, a.body)
	}
	if n := len(records(t, "mallory", invitesName)); n != 0 {
		t.Fatalf("a forged org header routed %d records", n)
	}
	// The same, signed in as a member of another org: still the brand's invites.
	b, _ := json.Marshal(map[string]string{"code": live})
	other := caller{org: "acme", user: "acme/zed"}
	if a := in.send(t, http.MethodPost, "/v1/network/invite/verify", string(b), other, from(71)); a.body != yes {
		t.Fatalf("as a member of another org: %d %s", a.code, a.body)
	}
}

// TestNothingSensitiveIsLogged: not a code, an address or a handle, and not the
// address a request came from, appears in the server's log, on any path.
func TestNothingSensitiveIsLogged(t *testing.T) {
	in := mountIntake(t)
	in.seed(t, live, nil)
	ip := "203.0.113.99"
	in.verify(t, live, ip)
	in.verify(t, miss(1), ip)
	in.verify(t, "hnz-secretcode", ip)
	in.send(t, http.MethodPost, "/v1/network/waitlist", `{"email":"ada.private@example.com","role":"builder"}`, visitor, ip)
	in.send(t, http.MethodPost, "/v1/network/waitlist", `{"xHandle":"@privatehandle","role":"staker"}`, visitor, ip)
	in.svc.State.seal = sealOf(in.svc.Coord)
	in.svc.KMS = brokenKMS{}
	in.verify(t, miss(2), ip)
	out := strings.ToLower(in.logs.String())
	for _, secret := range []string{"abcd2", "3efgh", "00001", "secretcode", "ada.private", "example.com", "privatehandle", ip} {
		if strings.Contains(out, strings.ToLower(secret)) {
			t.Errorf("the log carries %q:\n%s", secret, in.logs.String())
		}
	}
}

// brokenKMS is a KMS that answers every read with a fault.
type brokenKMS struct{}

func (brokenKMS) GetSecret(context.Context, string) ([]byte, error) { return nil, errBroken }
func (brokenKMS) PutSecret(context.Context, string, []byte) error   { return errBroken }
func (brokenKMS) DeleteSecret(context.Context, string) error        { return errBroken }
func (brokenKMS) Sign(context.Context, string, []byte) ([]byte, error) {
	return nil, errBroken
}

var errBroken = errors.New("the KMS is down")

// TestNormalise states the one spelling a code has and what folds to it.
func TestNormalise(t *testing.T) {
	for _, c := range []struct {
		in   string
		want string
		ok   bool
	}{
		{"HNZ-7K9Q2-MXZ4P", "HNZ-7K9Q2-MXZ4P", true},
		{"hnz-7k9q2-mxz4p", "HNZ-7K9Q2-MXZ4P", true},
		{"HNZ7K9Q2MXZ4P", "HNZ-7K9Q2-MXZ4P", true},
		{" \tHNZ-7K9Q2-MXZ4P\r\n", "HNZ-7K9Q2-MXZ4P", true},
		{"HNZ 7K9Q2 MXZ4P", "HNZ-7K9Q2-MXZ4P", true},
		{"HNZ-7K9Q2MXZ4P", "HNZ-7K9Q2-MXZ4P", true},
		{"HNZ-7K-9Q2M-XZ4P", "HNZ-7K9Q2-MXZ4P", true},
		{"HNZ-IL0O1-2ABCD", "HNZ-11001-2ABCD", true}, // I and L are 1, O is 0
		{"hnz-il0o1-2abcd", "HNZ-11001-2ABCD", true},
		{"HNZ-UUUUU-UUUUU", "", false}, // U is not in the alphabet, and is not folded
		{"HNZ-7K9Q2-MXZ4", "", false},
		{"HNZ-7K9Q2-MXZ4PM", "", false},
		{"HNZ-7K9Q2M", "", false}, // the six-character shape no longer exists
		{"HN-7K9Q2-MXZ4P", "", false},
		{"XNZ-7K9Q2-MXZ4P", "", false},
		{"HNZ-7K9Q2-MXZ4\u00cd", "", false}, // a letter outside ASCII, which upper-casing must not fold in
		{"HNZ-7K9Q2-MXZ4\u0131", "", false}, // dotless i, which upper-cases to I
		{"HNZ-7K9Q2-MXZ4\u00a0P", "", false},
		{"", "", false},
		{"HNZ", "", false},
		{"HNZ-", "", false},
	} {
		got, ok := normalise(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("normalise(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

// FuzzNormalise: whatever it is given it never panics, an accepted spelling is the
// one canonical shape, and normalising that again changes nothing.
func FuzzNormalise(f *testing.F) {
	for _, s := range []string{"HNZ-7K9Q2-MXZ4P", "hnz7k9q2mxz4p", "", "HNZ-", "\x00", "HNZ-IL0O1-2ABCD", "ｈｎｚ-７Ｋ９Ｑ２-ＭＸＺ４Ｐ", "HNZ-7K9Q2-MXZ4P\n", "HNZ - - - 7K9Q2MXZ4P"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got, ok := normalise(s)
		if !ok {
			if got != "" {
				t.Fatalf("normalise(%q) refused but answered %q", s, got)
			}
			return
		}
		if !codeShape.MatchString(got) {
			t.Fatalf("normalise(%q) = %q, not the canonical shape", s, got)
		}
		again, ok := normalise(got)
		if !ok || again != got {
			t.Fatalf("normalise(%q) = %q, but normalising that gives %q, %v", s, got, again, ok)
		}
	})
}

// TestNewCodeIsRandomAndWellFormed: every code is the canonical shape, uses the
// whole alphabet evenly, and a thousand are all different (10 characters of 32 make
// 2^50, so a collision here is a defect and not luck).
func TestNewCodeIsRandomAndWellFormed(t *testing.T) {
	seen := map[string]bool{}
	counts := map[byte]int{}
	for range 1000 {
		c, err := newCode()
		if err != nil {
			t.Fatal(err)
		}
		if !codeShape.MatchString(c) {
			t.Fatalf("code %q is not the shape HNZ-XXXXX-XXXXX of Crockford characters", c)
		}
		if seen[c] {
			t.Fatalf("code %q was issued twice in 1000", c)
		}
		seen[c] = true
		for _, ch := range []byte(strings.ReplaceAll(c[4:], "-", "")) {
			counts[ch]++
		}
	}
	if len(counts) != 32 {
		t.Fatalf("10,000 characters used %d of the 32 in the alphabet", len(counts))
	}
	// Uniform: each of 32 characters expects 312.5 of 10,000; a modulo bias would
	// show as a character far off that. Five standard deviations is about 85.
	for ch, n := range counts {
		if n < 215 || n > 410 {
			t.Errorf("%q appeared %d times of 10,000, want about 312", ch, n)
		}
	}
}

// TestTalliesAreKeyedAndNeverTheValue: what the shared count is keyed under is a
// keyed hash of the address or the identifier, so it cannot be reversed by trying
// every address, and differs under another key.
func TestTalliesAreKeyedAndNeverTheValue(t *testing.T) {
	a, b := countKey([]byte("0123456789abcdef0123456789abcdef")), countKey([]byte("fedcba9876543210fedcba9876543210"))
	if tally(a, "ada@example.com") == tally(b, "ada@example.com") {
		t.Fatal("two keys count one address under one name")
	}
	if tally(a, "ada@example.com") != tally(a, "ada@example.com") || tally(a, "ada@example.com") == tally(a, "ada@example.org") {
		t.Fatal("a tally is not a function of its value")
	}
	if got := tally(a, "203.0.113.7"); len(got) != 64 || strings.Contains(got, "203") && strings.Contains(got, "113") {
		t.Fatalf("tally %q looks like the value", got)
	}
	if tally(a, "x") == tallyPlain("x") {
		t.Fatal("a tally is a plain SHA-256 of its value, which brute force reverses")
	}
}

// tallyPlain is what a tally must not be: the SHA-256 of the value alone.
func tallyPlain(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}
