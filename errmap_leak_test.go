package cloud

// A 500 is the ABSENCE of a decision, and the body it carried was whatever text
// the fault happened to hold. That text is ours, not the caller's: infrastructure
// hostnames, the names of secret references, migration internals, half-built
// features. The first 500 a new customer sees should name what THEY can act on.
//
// The strings below are real: each is the text a live error in this repo carries
// (apps/kms/kms.go's ZapDB migration wrap, config.go's master-key reference,
// apps/account/iam.go's unreachable-IAM wrap, apps/knowledge/sync.go's unbuilt
// connectors). They are the exact shapes that reached a browser.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// internalFaults are undecided errors — a handler returned a plain Go error, so
// nobody chose a status OR a sentence. The client must not invent one from the text.
var internalFaults = []struct {
	name    string
	err     error
	secrets []string // substrings that must never reach the wire
}{
	{
		"kms migration internals",
		errors.New("kms.New: legacy ZapDB migration: open /var/lib/zapdb/store: permission denied"),
		[]string{"ZapDB", "/var/lib/zapdb", "permission denied"},
	},
	{
		"the name of a secret reference",
		errors.New("master key: CLOUD_KMS_MASTER_KEY_REF is not set"),
		[]string{"CLOUD_KMS_MASTER_KEY_REF"},
	},
	{
		"in-cluster topology",
		fmt.Errorf("iam unreachable: %w", errors.New(
			`Post "http://iam.hanzo.svc/v1/iam/users/get": dial tcp 10.245.0.7:8000: connect: connection refused`)),
		[]string{"iam.hanzo.svc", "10.245.0.7", "connection refused"},
	},
	{
		"an unbuilt feature",
		errors.New("slack sync: listing not yet implemented (connection stored; wire channels.history to ingestDoc)"),
		[]string{"not yet implemented", "channels.history", "ingestDoc"},
	},
}

// TestAnUndecidedFaultLeaksNothing: an error that decided nothing says nothing.
// It still renders 500 — the status is the honest part — but the body is a stable
// sentence rather than our internals.
func TestAnUndecidedFaultLeaksNothing(t *testing.T) {
	for _, tc := range internalFaults {
		t.Run(tc.name, func(t *testing.T) {
			status, body := getThing(t, tc.err)
			if status != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500 (body %s)", status, body)
			}
			for _, secret := range tc.secrets {
				if strings.Contains(body, secret) {
					t.Fatalf("the 500 body leaked %q:\n  %s", secret, body)
				}
			}
			// The whole error text, verbatim, is the specific shape that leaked.
			if strings.Contains(body, tc.err.Error()) {
				t.Fatalf("the 500 body is the raw fault text:\n  %s", body)
			}
		})
	}
}

// TestAnUndecidedFaultStillSaysSomethingUseful — redaction must not produce an
// empty card. The client gets a stable, actionable sentence.
func TestAnUndecidedFaultStillSaysSomethingUseful(t *testing.T) {
	_, body := getThing(t, errors.New("kms.New: legacy ZapDB migration: boom"))
	// A refusal is an RFC 9457 problem document: the sentence is `detail`, and
	// `status` repeats the code so a reader holding only the body still has it.
	var seen struct {
		Status int    `json:"status"`
		Msg    string `json:"detail"`
	}
	if err := json.Unmarshal([]byte(body), &seen); err != nil {
		t.Fatalf("body is not a problem document: %s", body)
	}
	if strings.TrimSpace(seen.Msg) == "" {
		t.Fatalf("a redacted 500 must still say something: %s", body)
	}
	if seen.Status != http.StatusInternalServerError {
		t.Fatalf("status field = %d, want 500", seen.Status)
	}
}

// TestAuthoredMessagesSurvive holds the other half of the rule. A handler that
// CHOSE a status and a sentence is making a decision, and the client does not
// second-guess it — otherwise "Billing temporarily unavailable" would render as
// a generic fault and the console would lose the one 5xx a user can read.
func TestAuthoredMessagesSurvive(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{zip.ErrInternal("store unavailable"), "store unavailable"},
		{zip.Errorf(http.StatusServiceUnavailable, "Billing temporarily unavailable"), "Billing temporarily unavailable"},
		{zip.Errorf(http.StatusBadGateway, "could not reach the provider"), "could not reach the provider"},
	} {
		_, body := getThing(t, tc.err)
		if !strings.Contains(body, tc.want) {
			t.Fatalf("an authored 5xx sentence was swallowed: want %q in %s", tc.want, body)
		}
	}
}
