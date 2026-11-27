package cloud

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/infer"
	"github.com/hanzoai/account"
)

// meteredOver is a meter wired to a fake ledger that answers every balance with
// enough to pay, so a test reads only who was charged and how much.
func meteredOver(t *testing.T, inner infer.Client) (*meteredAI, *fakeCommerce) {
	t.Helper()
	fc := &fakeCommerce{balanceBody: `{"available":100000}`}
	srv := fc.server(t)
	return &meteredAI{
		inner: inner,
		meter: NewMeter(Deps{Metering: mustClient(t, srv.URL)}, AIMeterProvider),
		log:   nil,
	}, fc
}

// An inference for an org that names no account is refused before any model is
// asked. The org's own account is the platform's pool in the signup org, so an
// org standing in for the payer is how a stranger's call landed on Hanzo's books.
func TestAnInferenceForAnOrgThatNamesNoPayerIsRefused(t *testing.T) {
	inner := &recordingAI{resp: &infer.ChatResponse{Content: "hi", TotalTokens: 100}}
	m, fc := meteredOver(t, inner)

	_, err := m.ChatCompletion(context.Background(), &infer.ChatRequest{
		Model: "enso-flash", Prompt: "hello", Org: account.SignupOrg,
	})
	if !errors.Is(err, ErrNoLedger) {
		t.Fatalf("an org with no payer answered %v, want ErrNoLedger", err)
	}
	if inner.chatReq != nil {
		t.Fatal("the model was asked for a call nobody pays for")
	}
	if waitFor(func() bool { return fc.usages() > 0 }, 200*time.Millisecond) {
		d, _ := fc.debits.last()
		t.Fatalf("a call nobody pays for was debited to %q", d.In.Subject)
	}
}

// The account a request names is the account charged: in the signup org that is
// the person, never the pool beside them.
func TestASignupOrgPersonsInferenceDebitsTheirWallet(t *testing.T) {
	inner := &recordingAI{resp: &infer.ChatResponse{Content: "hi", PromptTokens: 60, CompletionTokens: 40, TotalTokens: 100}}
	m, fc := meteredOver(t, inner)
	alice := account.Person(account.SignupOrg, "alice")

	if _, err := m.ChatCompletion(context.Background(), &infer.ChatRequest{
		Model: "enso-flash", Prompt: "hello", Org: account.SignupOrg, Payer: alice,
	}); err != nil {
		t.Fatalf("chat: %v", err)
	}
	if !waitFor(func() bool { return fc.usages() == 1 }, time.Second) {
		t.Fatalf("want one debit, got %d", fc.usages())
	}
	d, _ := fc.debits.last()
	if d.In.Subject != alice.Subject() || d.Org != account.SignupOrg {
		t.Fatalf("debited %q in %q's books, want %q in %q's", d.In.Subject, d.Org, alice.Subject(), account.SignupOrg)
	}
}

// A call no org stands behind is the platform's own work: it runs and is billed to
// nobody, as it always has been.
func TestASystemCallIsServedAndBilledToNobody(t *testing.T) {
	inner := &recordingAI{resp: &infer.ChatResponse{Content: "ok", TotalTokens: 10}}
	m, fc := meteredOver(t, inner)
	if _, err := m.ChatCompletion(context.Background(), &infer.ChatRequest{Model: "enso-flash", Prompt: "sys"}); err != nil {
		t.Fatalf("system call: %v", err)
	}
	if inner.chatReq == nil {
		t.Fatal("a system call never reached the model")
	}
	if waitFor(func() bool { return fc.usages() > 0 }, 200*time.Millisecond) {
		t.Fatal("a system call was debited")
	}
}
