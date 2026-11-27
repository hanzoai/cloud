package email

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// ---- 1. Verifier Independence Tests ----

func TestVerifier_SyntaxCheck(t *testing.T) {
	v := NewVerifier(nil, 3*time.Second)

	// Valid syntax
	res, err := v.Verify(context.Background(), &VerificationInput{
		Email:  "alice@example.com",
		Checks: []string{"syntax"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Checks.Syntax == nil || res.Checks.Syntax.Status != StatusPass {
		t.Fatalf("expected syntax pass, got %+v", res.Checks.Syntax)
	}
	if res.Checks.Syntax.Username != "alice" || res.Checks.Syntax.Domain != "example.com" {
		t.Errorf("unexpected syntax parts: %+v", res.Checks.Syntax)
	}
	if res.Checks.Domain != nil || res.Checks.MX != nil || res.Checks.Classification != nil {
		t.Errorf("expected only syntax check to run, got %+v", res.Checks)
	}

	// Invalid syntax
	resBad, err := v.Verify(context.Background(), &VerificationInput{
		Email:  "invalid-email-address",
		Checks: []string{"syntax"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resBad.Checks.Syntax == nil || resBad.Checks.Syntax.Status != StatusFail {
		t.Fatalf("expected syntax fail, got %+v", resBad.Checks.Syntax)
	}
}

func TestVerifier_ClassificationAttributes(t *testing.T) {
	v := NewVerifier(nil, 3*time.Second)

	// Disposable address
	resDisp, err := v.Verify(context.Background(), &VerificationInput{
		Email:  "tempuser@mailinator.com",
		Checks: []string{"classification"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resDisp.Checks.Classification == nil {
		t.Fatal("expected classification check result")
	}
	// Crucial rule: Free/role/disposable addresses are attributes; classification status is pass
	if resDisp.Checks.Classification.Status != StatusPass {
		t.Errorf("expected classification status pass, got %s", resDisp.Checks.Classification.Status)
	}
	if !resDisp.Checks.Classification.Disposable {
		t.Errorf("expected disposable true for mailinator.com")
	}

	// Free webmail address
	resFree, err := v.Verify(context.Background(), &VerificationInput{
		Email:  "user@gmail.com",
		Checks: []string{"classification"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resFree.Checks.Classification.Free {
		t.Errorf("expected free true for gmail.com")
	}

	// Role account
	resRole, err := v.Verify(context.Background(), &VerificationInput{
		Email:  "support@example.com",
		Checks: []string{"classification"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resRole.Checks.Classification.Role {
		t.Errorf("expected role true for support@example.com")
	}
}

func TestVerifier_SuggestionsWithoutRewriting(t *testing.T) {
	v := NewVerifier(nil, 3*time.Second)
	suggest := true

	res, err := v.Verify(context.Background(), &VerificationInput{
		Email:       "user@gmai.com",
		Checks:      []string{"syntax", "domain", "mx", "classification"},
		Suggestions: &suggest,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Crucial rule: Suggestions never silently rewrite the submitted address
	if res.Email != "user@gmai.com" {
		t.Errorf("expected submitted email user@gmai.com unchanged, got %q", res.Email)
	}
	if len(res.Suggestions) == 0 {
		t.Fatal("expected suggestion for user@gmai.com")
	}
	if res.Suggestions[0].Email != "user@gmail.com" {
		t.Errorf("expected suggested email user@gmail.com, got %q", res.Suggestions[0].Email)
	}
	if res.Suggestions[0].Reason != "domain_typo" {
		t.Errorf("expected reason domain_typo, got %q", res.Suggestions[0].Reason)
	}
}

func TestVerifier_SMTPExplicitOptInOnly(t *testing.T) {
	v := NewVerifier(nil, 3*time.Second)

	// Default checks (no SMTP)
	resDefault, err := v.Verify(context.Background(), &VerificationInput{
		Email: "alice@example.com",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resDefault.Checks.SMTP != nil {
		t.Errorf("SMTP probe should NOT run by default: %+v", resDefault.Checks.SMTP)
	}

	// Explicit SMTP check requested
	resExplicit, err := v.Verify(context.Background(), &VerificationInput{
		Email:  "alice@example.com",
		Checks: []string{"smtp"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resExplicit.Checks.SMTP == nil {
		t.Fatal("expected SMTP check when explicitly requested")
	}
}

// ---- 2. Credentials: two rails, the org's default first ----

func TestCredentialResolver_Static(t *testing.T) {
	res := NewStaticCredentialResolver()
	ctx := context.Background()

	if _, _, err := res.ResolveEmailCredentials(ctx, "acme", ""); err != ErrNoCredentials {
		t.Fatalf("an org with nothing: %v, want ErrNoCredentials", err)
	}
	// The transactional rails are notify's: holding them is holding nothing here.
	for k, v := range map[string]string{"account-sid": "AC1", "auth-token": "t", "from-email": "tw@acme.io"} {
		res.Set("acme", "twilio_email", k, v)
	}
	res.Set("acme", "mail", "smtp-host", "smtp.acme.io")
	res.Set("acme", "mail", "sender-email", "noreply@acme.io")
	if _, _, err := res.ResolveEmailCredentials(ctx, "acme", ""); err != ErrNoCredentials {
		t.Errorf("an org with Twilio and SMTP only: %v, want ErrNoCredentials", err)
	}
	for _, rail := range []string{"twilio_email", "mail", "twilio"} {
		if _, _, err := res.ResolveEmailCredentials(ctx, "acme", rail); err == nil {
			t.Errorf("pinned %s resolved", rail)
		}
	}

	res.Set("acme", "sendgrid", "api-key", "SG.k")
	res.Set("acme", "sendgrid", "from-email", "out@acme.io")
	if p, _, err := res.ResolveEmailCredentials(ctx, "acme", ""); err != nil || p != "sendgrid" {
		t.Errorf("SendGrid only: %s %v", p, err)
	}
	for k, v := range map[string]string{"access-key-id": "AKIA", "secret-access-key": "s", "from-email": "ses@acme.io"} {
		res.Set("acme", "ses", k, v)
	}
	if p, _, _ := res.ResolveEmailCredentials(ctx, "acme", ""); p != "ses" {
		t.Errorf("both held, no default: %s, want ses", p)
	}
	_ = res.SetDefault(ctx, "acme", "sendgrid")
	if p, _, _ := res.ResolveEmailCredentials(ctx, "acme", ""); p != "sendgrid" {
		t.Errorf("default sendgrid: %s", p)
	}
	if p, _, _ := res.ResolveEmailCredentials(ctx, "acme", "ses"); p != "ses" {
		t.Errorf("a pin beats the default: %s", p)
	}
}

// ---- 3. The ledger: durable, idempotent ----

// ledger is a delivery service over a real Base, sending through a stand-in rail
// that counts its sends and answers "<provider>-<n>" as the provider's id.
type ledger struct {
	svc   *Service
	plane *fakePlane
	creds *StaticCredentialResolver
	sends atomic.Int32
	tags  []map[string]string
}

func newLedger(t *testing.T) *ledger {
	t.Helper()
	l := &ledger{plane: stubPlane(t), creds: NewStaticCredentialResolver()}
	for k, v := range map[string]string{"access-key-id": "AKIA", "secret-access-key": "s", "from-email": "team@acme.io"} {
		l.creds.Set("acme", "ses", k, v)
	}
	l.creds.Set("acme", "sendgrid", "api-key", "SG.k")
	l.creds.Set("acme", "sendgrid", "from-email", "team@acme.io")
	nowhere(l.creds, "acme")
	l.svc = l.service()
	return l
}

// service is another delivery service over the same Base and rail: what a restart,
// or another replica, reads the ledger with.
func (l *ledger) service() *Service {
	return NewDeliveryService(l.creds, func(_ context.Context, provider string, _ map[string]string, _ string, _ []string, _, _, _ string, _, tags map[string]string) (string, error) {
		n := l.sends.Add(1)
		l.tags = append(l.tags, tags)
		return provider + "-" + strconv.Itoa(int(n)), nil
	}, luxlog.New("test"))
}

func (l *ledger) send(t *testing.T, id, to, provider, campaign, idem string) *MessageRecord {
	t.Helper()
	r, err := l.svc.Send(context.Background(), "acme", &Outgoing{ID: id, Provider: provider, To: to, From: "team@acme.io", Subject: "News", Text: "Hello", Campaign: campaign, Idempotency: idem})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestLedger_SurvivesARestartAndIsOneAcrossReplicas(t *testing.T) {
	l := newLedger(t)
	sent := l.send(t, MessageID("acme", ""), "ada@acme.io", "ses", "fall", "")
	if sent.Status != StatusSent || sent.ProviderID != "ses-1" || sent.Origin != originAPI {
		t.Fatalf("sent %+v", sent)
	}
	// A fresh service is a process that never saw the send.
	again, err := l.service().GetMessage(context.Background(), "acme", sent.ID)
	if err != nil {
		t.Fatalf("another replica reads it: %v", err)
	}
	if again.Status != StatusSent || again.Campaign != "fall" || again.To[0] != "ada@acme.io" || again.ProviderID != "ses-1" {
		t.Errorf("another replica read %+v", again)
	}
	if _, err := l.service().GetMessage(context.Background(), "other", sent.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("another org read it: %v", err)
	}
	if _, err := l.svc.GetMessage(context.Background(), "acme", "nonexistent"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a malformed id: %v", err)
	}
	// The provider carries our id and the campaign back on every event.
	if l.tags[0]["message"] != sent.ID || l.tags[0]["campaign"] != "fall" {
		t.Errorf("tags %v", l.tags[0])
	}
}

func TestLedger_AnIdempotentRetrySendsOnceFromAnyReplica(t *testing.T) {
	l := newLedger(t)
	id := MessageID("acme", "key-1:ada@acme.io")
	if id != MessageID("acme", "key-1:ada@acme.io") || id == MessageID("other", "key-1:ada@acme.io") {
		t.Fatal("an idempotency key names one message per org")
	}
	first := l.send(t, id, "ada@acme.io", "ses", "", "key-1:ada@acme.io")
	l.svc = l.service()
	second := l.send(t, id, "ada@acme.io", "ses", "", "key-1:ada@acme.io")
	if l.sends.Load() != 1 {
		t.Errorf("%d sends, want 1", l.sends.Load())
	}
	if second.ID != first.ID || second.ProviderID != first.ProviderID {
		t.Errorf("the retry answered %+v, want %+v", second, first)
	}
}

// ---- 4. HTTP Routes ----

func setupTestApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Use(cloud.Bridge())

	creds := NewStaticCredentialResolver()
	creds.Set("acme", "ses", "access-key-id", "AKIA")
	creds.Set("acme", "ses", "secret-access-key", "s")
	creds.Set("acme", "ses", "from-email", "team@acme.com")
	nowhere(creds, "acme")

	verifier := NewVerifier(nil, 3*time.Second)
	delivery := NewDeliveryService(creds, func(context.Context, string, map[string]string, string, []string, string, string, string, map[string]string, map[string]string) (string, error) {
		return "ses-1", nil
	}, luxlog.New("test"))
	stubPlane(t)

	h := NewHandlers(verifier, delivery, luxlog.New("test"))
	RegisterRoutes(app, h)
	return app
}

func TestHTTP_Verify(t *testing.T) {
	app := setupTestApp(t)

	body := `{"email":"user@gmai.com","checks":["syntax","domain","mx","classification"],"suggestions":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/email/verify", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Org-Id", "acme")
	req.Header.Set("X-User-Id", "usr_verify")

	resp, err := app.Test(req, deadline)
	if err != nil {
		t.Fatalf("test error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(respBody))
	}

	var res VerificationResult
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if res.Email != "user@gmai.com" {
		t.Errorf("expected email user@gmai.com, got %q", res.Email)
	}
	if res.Checks.Syntax == nil || res.Checks.Syntax.Status != StatusPass {
		t.Errorf("expected syntax pass, got %+v", res.Checks.Syntax)
	}
	if res.Checks.Classification == nil || res.Checks.Classification.Status != StatusPass {
		t.Errorf("expected classification pass, got %+v", res.Checks.Classification)
	}
	if len(res.Suggestions) == 0 || res.Suggestions[0].Email != "user@gmail.com" {
		t.Errorf("expected suggestion user@gmail.com, got %+v", res.Suggestions)
	}
}

// call is one request to app as a member of acme (user "") or anonymously.
func call(t *testing.T, app *zip.App, method, path, body, user string, out any) int {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	req.Header.Set("Content-Type", "application/json")
	if user != "" {
		req.Header.Set("X-Org-Id", "acme")
		req.Header.Set("X-User-Id", user)
	}
	resp, err := app.Test(req, deadline)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if out != nil && resp.StatusCode < 300 {
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("%s %s: %v: %s", method, path, err, b)
		}
	}
	return resp.StatusCode
}

func TestHTTP_MessagesSendAndStatus(t *testing.T) {
	app := setupTestApp(t)
	body := `{"from":"team@example.com","to":["user@example.net"],"subject":"Welcome","text":"Hello."}`
	if got := call(t, app, http.MethodPost, "/v1/email", body, "", nil); got != http.StatusUnauthorized {
		t.Errorf("an anonymous send answered %d, want 401", got)
	}
	var sent Dispatch
	if got := call(t, app, http.MethodPost, "/v1/email", body, "usr_123", &sent); got != http.StatusOK || len(sent.Messages) != 1 {
		t.Fatalf("send answered %d: %+v", got, sent)
	}
	rec := *sent.Messages[0]
	if !ourID.MatchString(rec.ID) || rec.Status != StatusSent || rec.Provider != "ses" {
		t.Errorf("unexpected record from send: %+v", rec)
	}

	var got MessageRecord
	if code := call(t, app, http.MethodGet, "/v1/email/"+rec.ID, "", "usr_123", &got); code != http.StatusOK {
		t.Fatalf("GET /v1/email/:id answered %d", code)
	}
	if got.ID != rec.ID || got.Status != StatusSent || got.Subject != "Welcome" || got.To[0] != "user@example.net" {
		t.Errorf("unexpected retrieved record: %+v", got)
	}

	var list ListMessagesResult
	if code := call(t, app, http.MethodGet, "/v1/email", "", "usr_123", &list); code != http.StatusOK || len(list.Messages) != 1 || list.Messages[0].ID != rec.ID {
		t.Fatalf("list answered %d: %+v", code, list)
	}
	if code := call(t, app, http.MethodGet, "/v1/email", "", "", nil); code != http.StatusUnauthorized {
		t.Errorf("an anonymous list answered %d, want 401", code)
	}
	if code := call(t, app, http.MethodGet, "/v1/email/stats", "", "", nil); code != http.StatusUnauthorized {
		t.Errorf("anonymous stats answered %d, want 401", code)
	}
}

// An address is not an id: the static segments answer as themselves, whatever
// order they were declared in, and a verification is a POST, never a lookup.
func TestHTTP_StaticSegmentsAreNotMessageIDs(t *testing.T) {
	app := setupTestApp(t)
	for _, path := range []string{"/v1/email/ses/status", "/v1/email/sendgrid/status", "/v1/email/stats"} {
		if got := call(t, app, http.MethodGet, path, "", "usr_123", nil); got != http.StatusOK {
			t.Errorf("GET %s answered %d, want 200", path, got)
		}
	}
	// Verifying is POST only: a GET on the same segment is a message lookup that finds nothing.
	if got := call(t, app, http.MethodGet, "/v1/email/verify?email=hello@example.com", "", "usr_123", nil); got != http.StatusNotFound {
		t.Errorf("GET /v1/email/verify answered %d, want 404", got)
	}
	// The old addresses are gone.
	for _, path := range []string{"/v1/email-verify", "/v1/notify/email/verify", "/v1/notify/email/messages", "/v1/notify/email/ses/status"} {
		if got := call(t, app, http.MethodGet, path, "", "usr_123", nil); got != http.StatusNotFound {
			t.Errorf("%s answered %d, want 404", path, got)
		}
	}
}

func TestSES_SendSigV4TagsAndConfigurationSet(t *testing.T) {
	var gotAuth, gotPath string
	var gotBody []byte
	fakeSES := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"MessageId":"ses-msg-998877"}`))
	}))
	defer fakeSES.Close()

	creds := map[string]string{
		"access-key-id": "AKIAIOSFODNN7EXAMPLE", "secret-access-key": "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		"region": "us-east-1", "from-email": "growth@hanzo.ai", "from-name": "Hanzo Growth", "endpoint": fakeSES.URL,
	}
	sesSender := NewSES(creds, []string{"founder@acme.io"})
	sesSender.Alternative("Plain text fallback")
	sesSender.SetTags(map[string]string{"message": "msg_00000000000000000000000b", "campaign": "fall"})
	msgID, err := sesSender.SendWithID(context.Background(), "Launch Sequence #1", "<h1>Hello Founder</h1>")
	if err != nil || msgID != "ses-msg-998877" {
		t.Fatalf("SES SendWithID: %q %v", msgID, err)
	}
	if gotPath != "/v2/email/outbound-emails" || !strings.Contains(gotAuth, "AWS4-HMAC-SHA256") {
		t.Errorf("path %q auth %q", gotPath, gotAuth)
	}
	var req sesSendEmailRequest
	if err := json.Unmarshal(gotBody, &req); err != nil {
		t.Fatal(err)
	}
	// A credential that names no configuration set names none on the message: a
	// set the account does not hold would fail every send. The message carries our
	// id and the campaign as tags either way.
	if req.ConfigurationSetName != "" || strings.Contains(string(gotBody), "ConfigurationSetName") {
		t.Errorf("a configuration set %q nobody configured", req.ConfigurationSetName)
	}
	if len(req.EmailTags) != 2 || req.EmailTags[0] != (sesMessageTag{"campaign", "fall"}) || req.EmailTags[1] != (sesMessageTag{"message", "msg_00000000000000000000000b"}) {
		t.Errorf("tags %+v", req.EmailTags)
	}
	creds["configuration-set"] = "hanzo-crm"
	sesSender = NewSES(creds, []string{"founder@acme.io"})
	if _, err := sesSender.SendWithID(context.Background(), "Launch", "<p>Hi</p>"); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(gotBody, &req); err != nil || req.ConfigurationSetName != "hanzo-crm" {
		t.Errorf("a credential's configuration set was not used: %q %v", req.ConfigurationSetName, err)
	}
}

func TestSendGrid_SeparateOutreachAccountCustomArgsAndCategories(t *testing.T) {
	var capturedAuth, capturedPool, capturedFrom, capturedUnsub string
	var capturedArgs map[string]any
	var capturedCats []any

	fakeSG := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		capturedPool, _ = body["ip_pool_name"].(string)
		if fromObj, ok := body["from"].(map[string]any); ok {
			capturedFrom, _ = fromObj["email"].(string)
		}
		if hdrs, ok := body["headers"].(map[string]any); ok {
			capturedUnsub, _ = hdrs["List-Unsubscribe-Post"].(string)
		}
		capturedArgs, _ = body["custom_args"].(map[string]any)
		capturedCats, _ = body["categories"].([]any)
		w.Header().Set("X-Message-Id", "sg-msg-445566")
		w.WriteHeader(http.StatusAccepted)
	}))
	defer fakeSG.Close()

	sg := NewSendGrid(map[string]string{
		"api-key":    "SG.test_outreach_key_separate_from_twilio",
		"from-email": "outreach@mail.hanzo.ai",
		"from-name":  "Hanzo Outreach",
		"reply-to":   "replies@mail.hanzo.ai",
		"ip-pool":    "cold-outreach-pool",
		"endpoint":   fakeSG.URL,
	}, []string{"lead@target.io"})
	sg.Alternative("Plain text fallback")
	sg.SetHeaders(Unsubscribe("https://api.hanzo.ai/v1/marketing/unsubscribe?token=t"))
	sg.SetTags(map[string]string{"message": "msg_00000000000000000000000c", "campaign": "fall"})

	msgID, err := sg.SendWithID(context.Background(), "Outreach Subject", "<h1>Hello Lead</h1>")
	if err != nil || msgID != "sg-msg-445566" {
		t.Fatalf("SendGrid SendWithID: %q %v", msgID, err)
	}
	if capturedAuth != "Bearer SG.test_outreach_key_separate_from_twilio" || capturedPool != "cold-outreach-pool" || capturedFrom != "outreach@mail.hanzo.ai" {
		t.Errorf("auth %q pool %q from %q", capturedAuth, capturedPool, capturedFrom)
	}
	if capturedUnsub != "List-Unsubscribe=One-Click" {
		t.Errorf("expected the sender's RFC 8058 List-Unsubscribe-Post header, got %q", capturedUnsub)
	}
	if capturedArgs["message"] != "msg_00000000000000000000000c" || capturedArgs["campaign"] != "fall" {
		t.Errorf("custom args %v", capturedArgs)
	}
	if len(capturedCats) != 2 || capturedCats[0] != defaultCategory || capturedCats[1] != "fall" {
		t.Errorf("categories %v, want the default and the campaign", capturedCats)
	}

	// A message given no List-Unsubscribe carries none: the header is the
	// commercial sender's to give, never invented here.
	capturedUnsub = ""
	bare := NewSendGrid(map[string]string{"api-key": "SG.k", "from-email": "outreach@mail.hanzo.ai", "endpoint": fakeSG.URL}, []string{"lead@target.io"})
	if _, err := bare.SendWithID(context.Background(), "Receipt", "<p>Paid</p>"); err != nil {
		t.Fatal(err)
	}
	if capturedUnsub != "" {
		t.Errorf("a message given no unsubscribe link carried List-Unsubscribe-Post %q", capturedUnsub)
	}
}

// /v1/email/health is the host's liveness route, registered before the app's
// routes; the app's /:id must not take it, and must not depend on the order.
func TestRouter_StaticBeatsParamInAnyOrder(t *testing.T) {
	for _, first := range []string{"static", "param"} {
		app := zip.New(zip.Config{Logger: luxlog.New("test")})
		static := func(c *zip.Ctx) error { return c.JSON(http.StatusOK, map[string]string{"by": "static"}) }
		param := func(c *zip.Ctx) error { return c.JSON(http.StatusOK, map[string]string{"by": "param"}) }
		if first == "static" {
			app.Raw(http.MethodGet, "/v1/email/health", static)
			app.Raw(http.MethodGet, "/v1/email/:id", param)
		} else {
			app.Raw(http.MethodGet, "/v1/email/:id", param)
			app.Raw(http.MethodGet, "/v1/email/health", static)
		}
		resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/v1/email/health", nil), deadline)
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]string
		_ = json.NewDecoder(resp.Body).Decode(&got)
		_ = resp.Body.Close()
		if got["by"] != "static" {
			t.Errorf("%s declared first: /health answered by %q", first, got["by"])
		}
	}
}
