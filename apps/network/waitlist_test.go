package network

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

const pending = `{"status":"pending"}`

// join asks to join the waitlist, from ip.
func (in *intake) join(t *testing.T, body, ip string) answer {
	t.Helper()
	return in.send(t, http.MethodPost, "/v1/network/waitlist", body, visitor, ip)
}

// waiting is the waitlist records the brand's Base holds.
func waiting(t *testing.T) []map[string]any { return records(t, "hanzo", waitlistName) }

// noIdentifierIn checks that none of the identifiers appears in a stored row, in
// any answer, or in the server log: the only trace of one is its HMAC digest.
func (in *intake) noIdentifierIn(t *testing.T, answers []answer, identifiers ...string) {
	t.Helper()
	rows, _ := json.Marshal(waiting(t))
	dump := map[string]string{"stored rows": string(rows), "log": in.logs.String()}
	for i, a := range answers {
		dump[fmt.Sprintf("answer %d", i+1)] = a.body + "\n" + a.head
	}
	for _, id := range identifiers {
		for where, s := range dump {
			if strings.Contains(strings.ToLower(s), strings.ToLower(id)) {
				t.Errorf("%s carry the plaintext identifier %q:\n%s", where, id, s)
			}
		}
	}
}

// TestJoiningAnswersAlikeForANewAndARepeatEntry: the first ask for an address, the
// same address again in other letters, and the same one from another caller get the
// same 202, byte for byte, and are one entry.
func TestJoiningAnswersAlikeForANewAndARepeatEntry(t *testing.T) {
	in := mountIntake(t)
	first := in.join(t, `{"email":"ada@example.com","role":"builder"}`, from(1))
	answers := []answer{first}
	if first.code != http.StatusAccepted || first.body != pending {
		t.Fatalf("want 202 %s, got %d %s", pending, first.code, first.body)
	}
	for _, c := range []struct{ body, ip string }{
		{`{"email":"ada@example.com","role":"builder"}`, from(1)},
		{`{"email":" Ada@Example.COM ","role":"staker","source":"elsewhere"}`, from(2)},
	} {
		a := in.join(t, c.body, c.ip)
		answers = append(answers, a)
		if a != first {
			t.Errorf("%s answered %d %q %s, the first %d %q %s", c.body, a.code, a.kind, a.body, first.code, first.kind, first.body)
		}
	}
	if n := len(waiting(t)); n != 1 {
		t.Fatalf("one address asked three times is %d entries, want 1", n)
	}

	h := in.join(t, `{"xHandle":"AdaLovelace","role":"miner"}`, from(3))
	answers = append(answers, h)
	if h != first {
		t.Fatalf("a handle answered %d %q %s, an address %d %q %s", h.code, h.kind, h.body, first.code, first.kind, first.body)
	}
	again := in.join(t, `{"xHandle":"@adalovelace","role":"miner"}`, from(4))
	answers = append(answers, again)
	if again != h {
		t.Fatalf("a repeat handle answered %d %s", again.code, again.body)
	}
	if n := len(waiting(t)); n != 2 {
		t.Fatalf("an address and a handle are %d entries, want 2", n)
	}
	in.noIdentifierIn(t, answers, "ada@example.com", "AdaLovelace", "adalovelace")
}

// TestAnEntryHoldsExactlyItsFieldsAndNoIdentifier: an address entry is its kind,
// its HMAC digest, the role, where it came from, the language and when; an X handle
// entry is the same. Neither holds the identifier, anything about the caller (no
// address, agent or header), nor the digest of the other kind.
func TestAnEntryHoldsExactlyItsFieldsAndNoIdentifier(t *testing.T) {
	in := mountIntake(t)
	before := time.Now().UTC().Add(-2 * time.Second)
	a := in.join(t, `{"email":" Grace@Example.com ","role":"miner","source":"hanzo.network/join","locale":"pt-BR"}`, "203.0.113.77")
	b := in.join(t, `{"xHandle":"@Grace_Hopper","role":"researcher"}`, "203.0.113.78")

	allowed := map[string]bool{"id": true, "kind": true, "role": true,
		"source": true, "locale": true, "created_at": true,
		"collectionId": true, "collectionName": true} // Base's own, on every record
	var email, handle map[string]any
	for _, r := range waiting(t) {
		for k := range r {
			if !allowed[k] {
				t.Errorf("an entry carries a field %q the design does not list: %v", k, r)
			}
		}
		switch r["kind"] {
		case "email":
			email = r
		case "x":
			handle = r
		}
		raw, _ := json.Marshal(r)
		for _, never := range []string{"203.0.113", "X-Forwarded", "mallory", "User-Agent"} {
			if strings.Contains(string(raw), never) {
				t.Errorf("an entry carries %q: %s", never, raw)
			}
		}
	}
	if email == nil || handle == nil {
		t.Fatalf("want one address entry and one X entry, got %v", waiting(t))
	}
	for field, want := range map[string]any{
		"role": "miner", "source": "hanzo.network/join", "locale": "pt-BR",
	} {
		if email[field] != want {
			t.Errorf("address entry %s = %v, want %v", field, email[field], want)
		}
	}
	for field, want := range map[string]any{"role": "researcher", "source": "", "locale": ""} {
		if handle[field] != want {
			t.Errorf("X entry %s = %v, want %v", field, handle[field], want)
		}
	}
	key := in.key(t)
	for _, c := range []struct {
		r        map[string]any
		kind, id string
	}{
		{email, kindEmail, "grace@example.com"},
		{handle, kindX, "Grace_Hopper"},
	} {
		at, err := time.Parse(baseDate, fmt.Sprint(c.r["created_at"]))
		if err != nil || at.Before(before) || at.After(time.Now().Add(time.Second)) {
			t.Errorf("created_at = %v (%v), want now", c.r["created_at"], err)
		}
		id, _ := c.r["id"].(string)
		if !regexp.MustCompile(`^[0-9a-f]{15}$`).MatchString(id) {
			t.Errorf("id %q is not a digest", id)
		}
		if want := waitID(key, c.kind, c.id); id != want {
			t.Errorf("%s id %q, want the HMAC digest %q", c.kind, id, want)
		}
	}
	if email["id"] == handle["id"] {
		t.Error("an address and a handle share an id")
	}
	in.noIdentifierIn(t, []answer{a, b}, "grace@example.com", "Grace_Hopper", "grace_hopper")
}

// TestWaitlistDigestsAreKeyedSeparateAndCostNoSecondSecret: the filed id is the
// HMAC of a domain, a kind and the normalized identifier under the one invite
// key. Neither an unkeyed hash, an invite code's MAC, nor a second KMS secret
// can stand in for it.
func TestWaitlistDigestsAreKeyedSeparateAndCostNoSecondSecret(t *testing.T) {
	in := mountIntake(t)
	key := in.key(t)
	a := in.join(t, `{"email":"domain@example.com","role":"builder"}`, from(1))
	rows := waiting(t)
	if len(rows) != 1 {
		t.Fatalf("one ask filed %d entries, want 1", len(rows))
	}
	id, _ := rows[0]["id"].(string)
	if want := waitID(key, kindEmail, "domain@example.com"); id != want {
		t.Fatalf("id %q, want the keyed digest %q", id, want)
	}
	if id == waitID([]byte(strings.Repeat("k", 32)), kindEmail, "domain@example.com") {
		t.Fatal("a different key files the same id")
	}
	plain := sha256.Sum256([]byte(kindEmail + "\x00" + "domain@example.com"))
	if id == hex.EncodeToString(plain[:])[:15] {
		t.Fatal("the id is an unkeyed SHA-256, which anyone can reverse by trying addresses")
	}
	for _, other := range []string{
		inviteID(mac(key, "domain@example.com")),
		inviteID(mac(key, "HNZ-00000-00000")),
		waitID(key, kindX, "domain@example.com"),
	} {
		if id == other {
			t.Fatalf("the waitlist digest %q is also %q: no domain separation", id, other)
		}
	}
	if waitID(key, kindEmail, "DOMAIN@EXAMPLE.COM") != id {
		t.Fatal("the same address in other letters filed under another id")
	}
	if refs := in.kms.Refs(); len(refs) != 1 || refs[0] != inviteRef {
		t.Fatalf("KMS holds %v, want only %s", refs, inviteRef)
	}
	in.noIdentifierIn(t, []answer{a}, "domain@example.com")
}

// TestNoIdentifierInAnyRefusal: a 400 and a 429 say nothing about the identifier
// they were asked for, and leave none in a row or in the log.
func TestNoIdentifierInAnyRefusal(t *testing.T) {
	in := mountIntake(t)
	answers := []answer{
		in.join(t, `{"email":"private@example.com","role":"investor"}`, from(1)),
		in.join(t, `{"email":"private@example.com","role":"builder"}`, from(2)),
		in.join(t, `{"xHandle":"@Private_Handle","role":"staker"}`, from(3)),
	}
	if !problem(answers[0], http.StatusBadRequest) || answers[1].code != http.StatusAccepted || answers[2].code != http.StatusAccepted {
		t.Fatalf("want 400, 202, 202; got %d, %d, %d", answers[0].code, answers[1].code, answers[2].code)
	}
	for i := range 10 {
		a := in.join(t, `{"email":"spam@example.com","role":"builder"}`, "203.0.113.9")
		answers = append(answers, a)
		if a.code != http.StatusAccepted {
			t.Fatalf("request %d: want 202, got %d %s", i+1, a.code, a.body)
		}
	}
	limited := in.join(t, `{"email":"limited@example.com","role":"builder"}`, "203.0.113.9")
	answers = append(answers, limited)
	if !problem(limited, http.StatusTooManyRequests) {
		t.Fatalf("past the rate want 429, got %d %s", limited.code, limited.body)
	}
	in.noIdentifierIn(t, answers,
		"private@example.com", "Private_Handle", "spam@example.com", "limited@example.com")
}

// TestARepeatChangesNothing: a second ask for the same identifier, with another role,
// source and language, leaves the first entry as it was, and when it was made.
func TestARepeatChangesNothing(t *testing.T) {
	in := mountIntake(t)
	first := in.join(t, `{"email":"ada@example.com","role":"builder","source":"first","locale":"en"}`, from(1))
	was := waiting(t)
	if len(was) != 1 {
		t.Fatalf("the first ask filed %d entries, want 1", len(was))
	}
	// Make the entry old, so a repeat that stamped now would show.
	in.put(t, waitlistName, map[string]any{"id": was[0]["id"], "created_at": "2020-01-01 00:00:00.000Z"})
	was = waiting(t)
	again := in.join(t, `{"email":"ada@example.com","role":"staker","source":"second","locale":"fr"}`, from(2))
	now := waiting(t)
	a, _ := json.Marshal(was)
	b, _ := json.Marshal(now)
	if string(a) != string(b) {
		t.Fatalf("a repeat changed the entry:\n%s\n%s", a, b)
	}
	in.noIdentifierIn(t, []answer{first, again}, "ada@example.com")
}

// TestTheIdentifierIsEitherAnAddressOrAnXHandle: exactly one of them, a role of the
// four, and each in its shape; anything else is a 400 problem document and files
// nothing.
func TestTheIdentifierIsEitherAnAddressOrAnXHandle(t *testing.T) {
	in := mountIntake(t)
	ips := 0 // each case from a caller of its own, so none meets the rate limit
	for _, c := range []struct{ name, body string }{
		{"both", `{"email":"ada@example.com","xHandle":"ada","role":"builder"}`},
		{"neither", `{"role":"builder"}`},
		{"an empty address", `{"email":"","role":"builder"}`},
		{"a blank handle", `{"xHandle":"  ","role":"builder"}`},
		{"a bare at sign", `{"xHandle":"@","role":"builder"}`},
		{"not an address", `{"email":"not-an-email","role":"builder"}`},
		{"two addresses", `{"email":"a@example.com, b@example.com","role":"builder"}`},
		{"an address in brackets", `{"email":"Ada <ada@example.com>","role":"builder"}`},
		{"no dotted domain", `{"email":"ada@localhost","role":"builder"}`},
		{"an address with a control character", `{"email":"ada\u0000@example.com","role":"builder"}`},
		{"an address with a zero-width space", `{"email":"ada​@example.com","role":"builder"}`},
		{"past 320 characters", `{"email":"` + strings.Repeat("a", 320) + `@example.com","role":"builder"}`},
		{"a handle of 16", `{"xHandle":"` + strings.Repeat("a", 16) + `","role":"builder"}`},
		{"two at signs", `{"xHandle":"@@ada","role":"builder"}`},
		{"a handle with a dot", `{"xHandle":"ada.lovelace","role":"builder"}`},
		{"a handle with a space", `{"xHandle":"ada lovelace","role":"builder"}`},
		{"a handle with a newline inside", `{"xHandle":"ad\na","role":"builder"}`},
		{"a handle in another script", `{"xHandle":"ада","role":"builder"}`},
		{"a handle with a Kelvin sign", `{"xHandle":"Kada","role":"builder"}`},
		{"a wallet address", `{"wallet":"0x0000000000000000000000000000000000000001","role":"builder"}`},
		{"the generic handle field", `{"handle":"ada","role":"builder"}`},
		{"no role", `{"email":"ada@example.com"}`},
		{"an empty role", `{"email":"ada@example.com","role":""}`},
		{"the old role operator", `{"email":"ada@example.com","role":"operator"}`},
		{"the old role holder", `{"email":"ada@example.com","role":"holder"}`},
		{"another role", `{"email":"ada@example.com","role":"investor"}`},
		{"a number for the email", `{"email":7,"role":"builder"}`},
		{"not json", `nope`},
	} {
		ips++
		t.Run(c.name, func(t *testing.T) {
			if a := in.join(t, c.body, from(ips)); !problem(a, http.StatusBadRequest) {
				t.Fatalf("want a 400 problem document, got %d %q %s", a.code, a.kind, a.body)
			}
		})
	}
	if a := in.join(t, `{"email":"ada@example.com","role":"builder","source":"`+strings.Repeat("x", 4096)+`"}`, from(999)); !problem(a, http.StatusRequestEntityTooLarge) {
		t.Fatalf("past 4 KiB want 413, got %d %s", a.code, a.body)
	}
	if n := len(waiting(t)); n != 0 {
		t.Fatalf("refused requests filed %d entries", n)
	}
}

// TestTheFourRolesAreAcceptedInAnyCase, and a handle keeps its case: a source is cut
// at 200 characters on one line and a language that is no language tag is kept as
// none.
func TestTheFourRolesAreAcceptedInAnyCase(t *testing.T) {
	in := mountIntake(t)
	var answers []answer
	for i, role := range []string{"miner", "builder", "researcher", "staker", " Builder ", "STAKER"} {
		body := fmt.Sprintf(`{"xHandle":"person_%d","role":%q}`, i, role)
		a := in.join(t, body, from(i))
		answers = append(answers, a)
		if a.code != http.StatusAccepted {
			t.Fatalf("role %q: %d %s", role, a.code, a.body)
		}
	}
	long := strings.Repeat("s", 500) + "\nline two"
	answers = append(answers, in.join(t, `{"xHandle":"Bounded","role":"staker","source":`+mustJSON(long)+`,"locale":"not a locale!"}`, from(50)))
	key := in.key(t)
	got := map[string]map[string]any{}
	for _, r := range waiting(t) {
		got[fmt.Sprint(r["id"])] = r
	}
	b := got[waitID(key, kindX, "Bounded")]
	if b == nil {
		t.Fatalf("no entry for Bounded in %v", got)
	}
	if src, _ := b["source"].(string); len(src) != 200 || strings.ContainsAny(src, "\n\r") {
		t.Errorf("source is %d characters %q, want 200 on one line", len(src), src)
	}
	if b["locale"] != "" {
		t.Errorf("locale = %q, want none", b["locale"])
	}
	for handle, want := range map[string]string{"person_4": "builder", "person_5": "staker"} {
		r := got[waitID(key, kindX, handle)]
		if r == nil || r["role"] != want {
			t.Errorf("%s filed as %v, want %s", handle, r, want)
		}
	}
	in.noIdentifierIn(t, answers, "person_4", "person_5", "Bounded")
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// TestOneCallerJoinsAFew: one address joins ten times in an hour and is then refused
// 429, with Retry-After, before its body is judged; another address is not.
func TestOneCallerJoinsAFew(t *testing.T) {
	in := mountIntake(t)
	for i := range 10 {
		if a := in.join(t, fmt.Sprintf(`{"xHandle":"person_%d","role":"staker"}`, i), "203.0.113.7"); a.code != http.StatusAccepted {
			t.Fatalf("request %d: want 202, got %d %s", i+1, a.code, a.body)
		}
	}
	a := in.join(t, `{"xHandle":"one_more","role":"staker"}`, "203.0.113.7")
	if !problem(a, http.StatusTooManyRequests) {
		t.Fatalf("past the rate want 429, got %d %s", a.code, a.body)
	}
	if secs, err := strconv.Atoi(retryAfter(a)); err != nil || secs < 1 || secs > 3600 {
		t.Fatalf("Retry-After %q, want 1 to 3600 seconds", retryAfter(a))
	}
	if strings.Contains(strings.ToLower(a.body), "identifier") {
		t.Fatalf("the refusal speaks of an identifier: %s", a.body)
	}
	if a := in.join(t, `{}`, "203.0.113.7"); a.code != http.StatusTooManyRequests {
		t.Fatalf("past the rate a body is not judged: want 429, got %d %s", a.code, a.body)
	}
	if a := in.join(t, `{"xHandle":"one_more","role":"staker"}`, "198.51.100.9"); a.code != http.StatusAccepted {
		t.Fatalf("another caller want 202, got %d %s", a.code, a.body)
	}
}

// TestAnIdentifiersTenthAskIsIndistinguishableFromItsFirst: however many callers ask for
// one address, the first, the third, the tenth and every one after answer the same status,
// the same body and the same headers, and each does the same work: reads the entry and
// writes it. There is no count of an identifier's asks, so nothing about an answer, or
// how long it takes, says how often an address has been asked for. Per-caller limits
// bound the writes.
func TestAnIdentifiersTenthAskIsIndistinguishableFromItsFirst(t *testing.T) {
	in := mountIntake(t)
	var first answer
	var answers []answer
	for i := range 12 {
		a := in.join(t, `{"email":"Victim@Example.com","role":"staker"}`, from(i))
		answers = append(answers, a)
		if i == 0 {
			first = a
		}
		if a != first || a.code != http.StatusAccepted || a.body != pending {
			t.Fatalf("ask %d answered %d %q %s %q, the first %d %q %s %q", i+1, a.code, a.kind, a.body, a.head, first.code, first.kind, first.body, first.head)
		}
		if strings.Contains(strings.ToLower(a.body+a.head), "identifier") || retryAfter(a) != "" {
			t.Fatalf("ask %d speaks of the identifier or of waiting: %s %s", i+1, a.body, a.head)
		}
		// Take the entry out each time and see the next ask put it back: every ask
		// writes, the tenth as the first.
		was := waiting(t)
		if len(was) != 1 {
			t.Fatalf("ask %d left %d entries, want 1", i+1, len(was))
		}
		in.remove(t, waitlistName, fmt.Sprint(was[0]["id"]))
	}
	a := in.join(t, `{"email":"bystander@example.com","role":"staker"}`, from(30))
	answers = append(answers, a)
	if a.code != http.StatusAccepted {
		t.Fatalf("another address want 202, got %d %s", a.code, a.body)
	}
	if n := len(waiting(t)); n != 1 {
		t.Fatalf("another address was not written: %d entries", n)
	}
	in.noIdentifierIn(t, answers, "victim@example.com", "bystander@example.com")
}

// TestTheDeploymentsJoinCountNeverClosesTheList: past the hourly count the deployment is
// said to be busy, once, in the log, and a caller who has never asked is still taken: a
// count that refused at a threshold would let any hundred addresses close the waitlist
// for everyone. The caller's own rate is what bounds the writes.
func TestTheDeploymentsJoinCountNeverClosesTheList(t *testing.T) {
	in := mountIntake(t)
	in.fill(t, burstName, "all", waitBurst, waitBurst, burstWindow)
	for i := range 4 {
		a := in.join(t, fmt.Sprintf(`{"xHandle":"person_%d","role":"staker"}`, i), from(50+i))
		if a.code != http.StatusAccepted || a.body != pending {
			t.Fatalf("a fresh caller past the count: %d %s", a.code, a.body)
		}
	}
	if n := len(waiting(t)); n != 4 {
		t.Fatalf("the list holds %d entries, want all 4", n)
	}
	if n := strings.Count(in.logs.String(), "waitlist count"); n != 1 {
		t.Fatalf("the alarm was logged %d times, want once for the window:\n%s", n, in.logs.String())
	}
}

// TestJoiningNeedsTheDeployment: a brand outside the registry is a 503.
func TestJoiningNeedsTheDeployment(t *testing.T) {
	in := mountIntake(t)
	t.Setenv("CLOUD_BRAND", "not-a-brand")
	if a := in.join(t, `{"xHandle":"ada","role":"staker"}`, ""); !problem(a, http.StatusServiceUnavailable) {
		t.Fatalf("want 503, got %d %s", a.code, a.body)
	}
}

// TestJoiningIgnoresAForgedOrg: the entry lands in the brand's Base, never in the org
// a header names, whether the caller is a stranger or a member elsewhere.
func TestJoiningIgnoresAForgedOrg(t *testing.T) {
	in := mountIntake(t)
	in.join(t, `{"xHandle":"ada","role":"staker"}`, from(1)) // send forges X-Org-Id: mallory
	other := caller{org: "acme", user: "acme/zed"}
	in.send(t, http.MethodPost, "/v1/network/waitlist", `{"xHandle":"zed","role":"staker"}`, other, from(2))
	if n := len(waiting(t)); n != 2 {
		t.Fatalf("the brand's Base holds %d entries, want 2", n)
	}
	for _, org := range []string{"mallory", "acme"} {
		if n := len(records(t, org, waitlistName)); n != 0 {
			t.Fatalf("a forged org header routed %d entries to %s", n, org)
		}
	}
}
