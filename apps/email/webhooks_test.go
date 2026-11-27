package email

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // SNS SignatureVersion 1.
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/crm"
	"github.com/hanzoai/cloud/sdk"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// The clock every signed delivery in these tests is read against.
var signedAt = time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

const (
	topic      = "arn:aws:sns:us-east-1:123456789012:ses-events"
	otherTopic = "arn:aws:sns:us-east-1:999999999999:someone-elses"
	certURL    = "https://sns.us-east-1.amazonaws.com/SimpleNotificationService-test.pem"
	// worldMsg is our id for the one message the world sent, which every event about it
	// carries back as its message tag.
	worldMsg = "msg_00000000000000000000000a"
	bounce   = `{"eventType":"Bounce","mail":{"messageId":"ses-1","source":"growth@hanzo.ai","destination":["bounced@acme.io"],"tags":{"message":["` + worldMsg + `"]}},"bounce":{"bounceType":"Permanent","bounceSubType":"General","bouncedRecipients":[{"emailAddress":"bounced@acme.io"}]}}`
)

// world is the receivers over a delivery service holding one sent message, an
// organization with its webhook credentials, and an SNS that answers from a
// local server whatever host it is asked for.
type world struct {
	app     *zip.App
	svc     *Service
	msg     *MessageRecord
	rsa     *rsa.PrivateKey
	ec      *ecdsa.PrivateKey
	fetches atomic.Int32 // certificates and confirmations asked of "SNS"
	confirm atomic.Int32
	plane   *fakePlane // what reached the CRM and the suppression list
}

func newWorld(t *testing.T) *world {
	t.Helper()
	w := &world{}
	var err error
	if w.rsa, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
		t.Fatal(err)
	}
	if w.ec, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader); err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "sns.amazonaws.com"},
		NotBefore:    signedAt.Add(-time.Hour),
		NotAfter:     signedAt.Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &w.rsa.PublicKey, w.rsa)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})

	sns := httptest.NewTLSServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		w.fetches.Add(1)
		if r.URL.Path == "/confirm" {
			w.confirm.Add(1)
			return
		}
		_, _ = rw.Write(certPEM)
	}))
	t.Cleanup(sns.Close)
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // the stand-in for SNS has no public name.
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, sns.Listener.Addr().String())
		},
	}}

	pub, err := x509.MarshalPKIXPublicKey(&w.ec.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	creds := NewStaticCredentialResolver()
	for k, v := range map[string]string{"access-key-id": "AKIA", "secret-access-key": "s", "from-email": "a@acme.io", "sns-topic-arn": topic} {
		creds.Set("acme", "ses", k, v)
	}
	for k, v := range map[string]string{"api-key": "SG.k", "from-email": "a@acme.io", "webhook-public-key": base64.StdEncoding.EncodeToString(pub)} {
		creds.Set("acme", "sendgrid", k, v)
	}
	nowhere(creds, "acme")
	// An organization with SES and SendGrid sending, and no webhook credential at all.
	for _, p := range []struct{ provider, key string }{{"ses", "access-key-id"}, {"ses", "secret-access-key"}, {"ses", "from-email"}, {"sendgrid", "api-key"}, {"sendgrid", "from-email"}} {
		creds.Set("bare", p.provider, p.key, "x")
	}

	w.svc = NewDeliveryService(creds, func(context.Context, string, map[string]string, string, []string, string, string, string, map[string]string, map[string]string) (string, error) {
		return "ses-1", nil
	}, luxlog.New("test"))
	w.svc.now = func() time.Time { return signedAt }
	w.plane = stubPlane(t)
	w.msg, err = w.svc.Send(context.Background(), "acme", &Outgoing{ID: worldMsg, Provider: "ses", From: "a@acme.io", To: "bounced@acme.io", Subject: "hi", Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}

	h := NewHandlers(NewVerifier(nil, time.Second), w.svc, luxlog.New("test"))
	h.sns = NewSNS(client)
	h.sns.now = func() time.Time { return signedAt }
	h.now = func() time.Time { return signedAt }
	w.app = zip.New(zip.Config{Logger: luxlog.New("test")})
	w.app.Use(cloud.Bridge())
	RegisterRoutes(w.app, h)
	return w
}

// status is what the delivery service now says of the sent message.
func (w *world) status(t *testing.T) DeliveryStatus {
	t.Helper()
	got, err := w.svc.GetMessage(context.Background(), "acme", w.msg.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got.Status
}

func (w *world) post(t *testing.T, path string, body []byte, headers map[string]string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "text/plain; charset=UTF-8")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := w.app.Test(req, deadline)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// notification is a signed SNS Notification carrying the bounce, as SNS sends it.
func (w *world) notification(version string, mutate func(*snsEnvelope)) *snsEnvelope {
	e := &snsEnvelope{
		Type: "Notification", MessageID: "m-1", TopicARN: topic, Message: bounce,
		Timestamp: signedAt.Format(time.RFC3339), SignatureVersion: version, SigningCertURL: certURL,
	}
	if mutate != nil {
		mutate(e)
	}
	w.sign(e)
	return e
}

func (w *world) sign(e *snsEnvelope) {
	text := []byte(canonical(e))
	var sum []byte
	var hash crypto.Hash
	if e.SignatureVersion == "2" {
		h := sha256.Sum256(text)
		sum, hash = h[:], crypto.SHA256
	} else {
		h := sha1.Sum(text) //nolint:gosec // SNS SignatureVersion 1.
		sum, hash = h[:], crypto.SHA1
	}
	sig, err := rsa.SignPKCS1v15(rand.Reader, w.rsa, hash, sum)
	if err != nil {
		panic(err)
	}
	e.Signature = base64.StdEncoding.EncodeToString(sig)
}

func js(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func TestSESWebhook_SignedByTheTopicIsRecorded(t *testing.T) {
	for _, version := range []string{"1", "2"} {
		t.Run("SignatureVersion"+version, func(t *testing.T) {
			w := newWorld(t)
			if got := w.post(t, "/v1/email/webhooks/ses?org=acme", js(w.notification(version, nil)), nil); got != http.StatusOK {
				t.Fatalf("a delivery SNS signed answered %d, want 200", got)
			}
			if got := w.status(t); got != StatusBounced {
				t.Errorf("the bounce was not recorded, status %q", got)
			}
			w.reached(t, "ses:ses-1 bounced", "bounced@acme.io", "bounce: Permanent General")
		})
	}
}

// reached holds what a verified event carried on: the mail's status to the CRM,
// and a hard bounce or a complaint to the suppression list.
func (w *world) reached(t *testing.T, event, address, reason string) {
	t.Helper()
	w.plane.mu.Lock()
	defer w.plane.mu.Unlock()
	if len(w.plane.events) != 1 || w.plane.events[0] != event {
		t.Errorf("the CRM was told %v, want %q", w.plane.events, event)
	}
	if len(w.plane.suppressed) != 1 || w.plane.suppressed[0].Address != address || w.plane.suppressed[0].Reason != reason || w.plane.suppressed[0].Org != "acme" {
		t.Errorf("the suppression list was given %+v, want %s for %q", w.plane.suppressed, address, reason)
	}
}

// quiet holds that a refused delivery reached neither.
func (w *world) quiet(t *testing.T) {
	t.Helper()
	w.plane.mu.Lock()
	defer w.plane.mu.Unlock()
	if len(w.plane.events) != 0 || len(w.plane.suppressed) != 0 {
		t.Errorf("a refused delivery told the CRM %v and the list %v", w.plane.events, w.plane.suppressed)
	}
}

func TestSESWebhook_RefusalsAnswer401AndRecordNothing(t *testing.T) {
	cases := []struct {
		name    string
		path    string
		body    func(w *world) []byte
		fetched int32 // what the refusal may have asked of SNS
	}{
		{"unsigned: the SES event itself", "/v1/email/webhooks/ses?org=acme", func(*world) []byte { return []byte(bounce) }, 0},
		{"unsigned: an envelope with no signature", "/v1/email/webhooks/ses?org=acme", func(w *world) []byte {
			e := w.notification("1", nil)
			e.Signature = ""
			return js(e)
		}, 0},
		{"signed by some other key", "/v1/email/webhooks/ses?org=acme", func(w *world) []byte {
			e := w.notification("1", nil)
			other, _ := rsa.GenerateKey(rand.Reader, 2048)
			sum := sha1.Sum([]byte(canonical(e))) //nolint:gosec // SNS SignatureVersion 1.
			sig, _ := rsa.SignPKCS1v15(rand.Reader, other, crypto.SHA1, sum[:])
			e.Signature = base64.StdEncoding.EncodeToString(sig)
			return js(e)
		}, 1},
		{"altered after signing", "/v1/email/webhooks/ses?org=acme", func(w *world) []byte {
			e := w.notification("1", nil)
			e.Message = `{"eventType":"Delivery"}`
			return js(e)
		}, 1},
		{"a valid signature from someone else's topic", "/v1/email/webhooks/ses?org=acme", func(w *world) []byte {
			return js(w.notification("1", func(e *snsEnvelope) { e.TopicARN = otherTopic }))
		}, 0},
		{"certificate on a host that is not SNS", "/v1/email/webhooks/ses?org=acme", func(w *world) []byte {
			return js(w.notification("1", func(e *snsEnvelope) { e.SigningCertURL = "https://evil.example.com/cert.pem" }))
		}, 0},
		{"certificate on SNS of another region", "/v1/email/webhooks/ses?org=acme", func(w *world) []byte {
			return js(w.notification("1", func(e *snsEnvelope) { e.SigningCertURL = "https://sns.eu-west-1.amazonaws.com/cert.pem" }))
		}, 0},
		{"certificate over http", "/v1/email/webhooks/ses?org=acme", func(w *world) []byte {
			return js(w.notification("1", func(e *snsEnvelope) { e.SigningCertURL = "http://sns.us-east-1.amazonaws.com/cert.pem" }))
		}, 0},
		{"certificate host with a lookalike suffix", "/v1/email/webhooks/ses?org=acme", func(w *world) []byte {
			return js(w.notification("1", func(e *snsEnvelope) {
				e.SigningCertURL = "https://sns.us-east-1.amazonaws.com.evil.example.com/cert.pem"
			}))
		}, 0},
		{"certificate url with credentials", "/v1/email/webhooks/ses?org=acme", func(w *world) []byte {
			return js(w.notification("1", func(e *snsEnvelope) {
				e.SigningCertURL = "https://sns.us-east-1.amazonaws.com@evil.example.com/cert.pem"
			}))
		}, 0},
		{"certificate url that is not a certificate", "/v1/email/webhooks/ses?org=acme", func(w *world) []byte {
			return js(w.notification("1", func(e *snsEnvelope) { e.SigningCertURL = "https://sns.us-east-1.amazonaws.com/admin?x=.pem" }))
		}, 0},
		{"a signature version SNS does not have", "/v1/email/webhooks/ses?org=acme", func(w *world) []byte {
			return js(w.notification("3", nil))
		}, 0},
		{"an organization that holds no topic", "/v1/email/webhooks/ses?org=bare", func(w *world) []byte { return js(w.notification("1", nil)) }, 0},
		{"an organization that does not exist", "/v1/email/webhooks/ses?org=nobody", func(w *world) []byte { return js(w.notification("1", nil)) }, 0},
		{"no organization named", "/v1/email/webhooks/ses", func(w *world) []byte { return js(w.notification("1", nil)) }, 0},
		{"not json", "/v1/email/webhooks/ses?org=acme", func(*world) []byte { return []byte("<xml/>") }, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			if got := w.post(t, c.path, c.body(w), nil); got != http.StatusUnauthorized {
				t.Errorf("answered %d, want 401", got)
			}
			if got := w.status(t); got != StatusSent {
				t.Errorf("a refused delivery changed the message to %q", got)
			}
			if got := w.fetches.Load(); got > c.fetched {
				t.Errorf("a refused delivery made %d fetches of SNS, at most %d are reasoned for", got, c.fetched)
			}
			w.quiet(t)
		})
	}
}

func TestSESWebhook_SubscriptionConfirmation(t *testing.T) {
	const confirmURL = "https://sns.us-east-1.amazonaws.com/confirm"
	confirmation := func(w *world, mutate func(*snsEnvelope)) []byte {
		e := &snsEnvelope{
			Type: "SubscriptionConfirmation", MessageID: "m-2", TopicARN: topic, Token: "tok", Message: "confirm",
			Timestamp: signedAt.Format(time.RFC3339), SignatureVersion: "1", SigningCertURL: certURL, SubscribeURL: confirmURL,
		}
		if mutate != nil {
			mutate(e)
		}
		w.sign(e)
		return js(e)
	}

	w := newWorld(t)
	if got := w.post(t, "/v1/email/webhooks/ses?org=acme", confirmation(w, nil), nil); got != http.StatusOK {
		t.Fatalf("a signed confirmation answered %d, want 200", got)
	}
	if got := w.confirm.Load(); got != 1 {
		t.Errorf("the subscription was confirmed %d times, want once", got)
	}
	if got := w.status(t); got != StatusSent {
		t.Errorf("a confirmation changed the message to %q", got)
	}

	for name, body := range map[string]func(*world) []byte{
		"unsigned": func(w *world) []byte {
			e := confirmation(w, nil)
			var m snsEnvelope
			_ = json.Unmarshal(e, &m)
			m.Signature = ""
			return js(m)
		},
		"a subscribe url off SNS": func(w *world) []byte {
			return confirmation(w, func(e *snsEnvelope) { e.SubscribeURL = "https://evil.example.com/confirm" })
		},
		"a subscribe url on another region": func(w *world) []byte {
			return confirmation(w, func(e *snsEnvelope) { e.SubscribeURL = "https://sns.eu-west-1.amazonaws.com/confirm" })
		},
		"another topic": func(w *world) []byte { return confirmation(w, func(e *snsEnvelope) { e.TopicARN = otherTopic }) },
	} {
		w := newWorld(t)
		if got := w.post(t, "/v1/email/webhooks/ses?org=acme", body(w), nil); got != http.StatusUnauthorized {
			t.Errorf("%s: answered %d, want 401", name, got)
		}
		if got := w.confirm.Load(); got != 0 {
			t.Errorf("%s: the subscribe url was fetched", name)
		}
	}
}

// ---- SendGrid ----

func (w *world) delivery(t *testing.T, body string, key *ecdsa.PrivateKey, at time.Time) map[string]string {
	t.Helper()
	ts := strconv.FormatInt(at.Unix(), 10)
	sum := sha256.Sum256([]byte(ts + body))
	sig, err := ecdsa.SignASN1(rand.Reader, key, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{sendGridTimestamp: ts, sendGridSignature: base64.StdEncoding.EncodeToString(sig)}
}

const spam = `[{"event":"spamreport","email":"bounced@acme.io","sg_message_id":"sg-1.filter","sg_event_id":"ev-spam","timestamp":1761393600,"message":"` + worldMsg + `"}]`

func TestSendGridWebhook_SignedByTheKeyIsRecorded(t *testing.T) {
	w := newWorld(t)
	headers := w.delivery(t, spam, w.ec, signedAt)
	if got := w.post(t, "/v1/email/webhooks/sendgrid?org=acme", []byte(spam), headers); got != http.StatusOK {
		t.Fatalf("a delivery SendGrid signed answered %d, want 200", got)
	}
	if got := w.status(t); got != StatusComplained {
		t.Errorf("the complaint was not recorded, status %q", got)
	}
	w.reached(t, "sendgrid:sg-1 complained", "bounced@acme.io", "complaint: abuse")
}

func TestSendGridWebhook_RefusalsAnswer401AndRecordNothing(t *testing.T) {
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	cases := []struct {
		name    string
		path    string
		body    string
		headers func(w *world) map[string]string
	}{
		{"no signature headers", "/v1/email/webhooks/sendgrid?org=acme", spam, func(*world) map[string]string { return nil }},
		{"signed by another key", "/v1/email/webhooks/sendgrid?org=acme", spam, func(w *world) map[string]string { return w.delivery(t, spam, other, signedAt) }},
		{"altered after signing", "/v1/email/webhooks/sendgrid?org=acme", spam, func(w *world) map[string]string {
			return w.delivery(t, `[{"event":"delivered","email":"bounced@acme.io"}]`, w.ec, signedAt)
		}},
		{"a timestamp that was not signed", "/v1/email/webhooks/sendgrid?org=acme", spam, func(w *world) map[string]string {
			h := w.delivery(t, spam, w.ec, signedAt)
			h[sendGridTimestamp] = strconv.FormatInt(signedAt.Unix()+1, 10)
			return h
		}},
		{"signed an hour ago", "/v1/email/webhooks/sendgrid?org=acme", spam, func(w *world) map[string]string {
			return w.delivery(t, spam, w.ec, signedAt.Add(-time.Hour))
		}},
		{"signed an hour from now", "/v1/email/webhooks/sendgrid?org=acme", spam, func(w *world) map[string]string {
			return w.delivery(t, spam, w.ec, signedAt.Add(time.Hour))
		}},
		{"a timestamp that is not a time", "/v1/email/webhooks/sendgrid?org=acme", spam, func(w *world) map[string]string {
			h := w.delivery(t, spam, w.ec, signedAt)
			h[sendGridTimestamp] = "yesterday"
			return h
		}},
		{"an organization that holds no key", "/v1/email/webhooks/sendgrid?org=bare", spam, func(w *world) map[string]string { return w.delivery(t, spam, w.ec, signedAt) }},
		{"an organization that does not exist", "/v1/email/webhooks/sendgrid?org=nobody", spam, func(w *world) map[string]string { return w.delivery(t, spam, w.ec, signedAt) }},
		{"no organization named", "/v1/email/webhooks/sendgrid", spam, func(w *world) map[string]string { return w.delivery(t, spam, w.ec, signedAt) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			if got := w.post(t, c.path, []byte(c.body), c.headers(w)); got != http.StatusUnauthorized {
				t.Errorf("answered %d, want 401", got)
			}
			if got := w.status(t); got != StatusSent {
				t.Errorf("a refused delivery changed the message to %q", got)
			}
			w.quiet(t)
		})
	}
}

// A blocked bounce is a server refusing for now; it is recorded and suppresses
// nothing.
func TestSendGridWebhook_ABlockedBounceSuppressesNothing(t *testing.T) {
	w := newWorld(t)
	body := `[{"event":"bounce","type":"blocked","email":"bounced@acme.io","sg_message_id":"sg-2.filter","timestamp":1761393600}]`
	if got := w.post(t, "/v1/email/webhooks/sendgrid?org=acme", []byte(body), w.delivery(t, body, w.ec, signedAt)); got != http.StatusOK {
		t.Fatalf("answered %d", got)
	}
	w.plane.mu.Lock()
	defer w.plane.mu.Unlock()
	if len(w.plane.suppressed) != 0 {
		t.Errorf("a blocked bounce put %v on the list", w.plane.suppressed)
	}
	if len(w.plane.events) != 1 || w.plane.events[0] != "sendgrid:sg-2 bounced" {
		t.Errorf("the CRM was told %v", w.plane.events)
	}
}

// A key on another curve is not the key SendGrid signs with.
func TestSendGridWebhook_OnlyP256(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err := verifySendGrid(base64.StdEncoding.EncodeToString(der), "1761393600", "AAAA", []byte(spam), signedAt); err == nil {
		t.Error("a P-384 key verified a delivery")
	}
}

// ---- verify ----

type countVerifier struct{ calls, probes atomic.Int32 }

func (v *countVerifier) Verify(_ context.Context, in *VerificationInput) (*VerificationResult, error) {
	v.calls.Add(1)
	if probes(in) {
		v.probes.Add(1)
	}
	return &VerificationResult{Email: in.Email}, nil
}

func verifyApp(t *testing.T, v Verifier, lim limits) *zip.App {
	t.Helper()
	h := NewHandlers(v, NewDeliveryService(NewStaticCredentialResolver(), nil, nil), luxlog.New("test"))
	h.limit = lim
	// Counters of their own, so no other test's checks are in them.
	h.verifying = cloud.NewWindow("email:verify:"+t.Name(), lim.verify, verifyWindow)
	h.probing = cloud.NewWindow("email:probe:"+t.Name(), lim.probe, verifyWindow)
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Use(cloud.Bridge())
	RegisterRoutes(app, h)
	return app
}

func verifyAs(t *testing.T, app *zip.App, user, body string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/email/verify", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	if user != "" {
		req.Header.Set("X-Org-Id", "acme")
		req.Header.Set("X-User-Id", user)
	}
	resp, err := app.Test(req, deadline)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func TestVerify_NeedsASignedInCaller(t *testing.T) {
	v := &countVerifier{}
	app := verifyApp(t, v, limits{100, 1000, 10, 100})
	for _, body := range []string{`{"email":"a@example.com"}`, `{"email":"a@example.com","checks":["smtp"]}`} {
		if got := verifyAs(t, app, "", body); got != http.StatusUnauthorized {
			t.Errorf("anonymous %s answered %d, want 401", body, got)
		}
	}
	if v.calls.Load() != 0 {
		t.Errorf("an anonymous caller reached the verifier %d times", v.calls.Load())
	}
	if got := verifyAs(t, app, "u_1", `{"email":"a@example.com"}`); got != http.StatusOK {
		t.Errorf("a signed-in caller answered %d, want 200", got)
	}
}

func TestVerify_ProbesAreHeldTighter(t *testing.T) {
	v := &countVerifier{}
	app := verifyApp(t, v, limits{verify: 100, verifyAll: 1000, probe: 2, probeAll: 3})
	probe := `{"email":"a@example.com","checks":["syntax","SMTP"]}`
	plain := `{"email":"a@example.com"}`

	for i := 0; i < 2; i++ {
		if got := verifyAs(t, app, "u_1", probe); got != http.StatusOK {
			t.Fatalf("probe %d answered %d, want 200", i+1, got)
		}
	}
	if got := verifyAs(t, app, "u_1", probe); got != http.StatusTooManyRequests {
		t.Errorf("a third probe from one caller answered %d, want 429", got)
	}
	if got := verifyAs(t, app, "u_1", plain); got != http.StatusOK {
		t.Errorf("a plain check after the probes answered %d, want 200: the probe allowance is its own", got)
	}
	// Another caller has an allowance of their own, until the deployment's is spent.
	if got := verifyAs(t, app, "u_2", probe); got != http.StatusOK {
		t.Errorf("another caller's first probe answered %d, want 200", got)
	}
	if got := verifyAs(t, app, "u_2", probe); got != http.StatusTooManyRequests {
		t.Errorf("the deployment's allowance of 3 was passed and a probe answered %d, want 429", got)
	}
	if got := v.probes.Load(); got != 3 {
		t.Errorf("%d probes reached the verifier, want 3", got)
	}
}

func TestVerify_ChecksAreHeldPerCaller(t *testing.T) {
	v := &countVerifier{}
	app := verifyApp(t, v, limits{verify: 2, verifyAll: 3, probe: 1, probeAll: 1})
	body := `{"email":"a@example.com"}`
	for i := 0; i < 2; i++ {
		if got := verifyAs(t, app, "u_1", body); got != http.StatusOK {
			t.Fatalf("check %d answered %d, want 200", i+1, got)
		}
	}
	if got := verifyAs(t, app, "u_1", body); got != http.StatusTooManyRequests {
		t.Errorf("a third check from one caller answered %d, want 429", got)
	}
	if got := verifyAs(t, app, "u_2", body); got != http.StatusOK {
		t.Errorf("another caller answered %d, want 200", got)
	}
	if got := verifyAs(t, app, "u_3", body); got != http.StatusTooManyRequests {
		t.Errorf("past the deployment's allowance a check answered %d, want 429", got)
	}
}

// receipt is the notification SES publishes for the raw message its receipt rule took
// in, with the verdicts it reached.
func receipt(raw, encoding string, verdicts ...string) string {
	content := raw
	if encoding == "BASE64" {
		content = base64.StdEncoding.EncodeToString([]byte(raw))
	}
	status := map[string]string{"spamVerdict": "PASS", "virusVerdict": "PASS", "dmarcVerdict": "PASS"}
	for _, v := range verdicts {
		status[v] = "FAIL"
	}
	return string(js(map[string]any{
		"notificationType": "Received",
		"mail":             map[string]any{"messageId": "ses-in-1", "destination": []string{"growth@acme.io"}},
		"receipt": map[string]any{
			"timestamp":   signedAt.Format(time.RFC3339),
			"spamVerdict": map[string]string{"status": status["spamVerdict"]}, "virusVerdict": map[string]string{"status": status["virusVerdict"]},
			"dmarcVerdict": map[string]string{"status": status["dmarcVerdict"]}, "action": map[string]string{"type": "SNS", "encoding": encoding},
		},
		"content": content,
	}))
}

const reply = "From: =?UTF-8?Q?Ada_Lovelace?= <Ada@Acme.io>\r\nTo: growth@acme.io\r\nSubject: =?UTF-8?Q?Re=3A_Your_code_=E2=9C=93?=\r\n" +
	"Message-ID: <r1@mail.acme.io>\r\nIn-Reply-To: <ses-1@email.amazonses.com>\r\nReferences: <ses-1@email.amazonses.com>\r\n" +
	"MIME-Version: 1.0\r\nContent-Type: multipart/alternative; boundary=b1\r\n\r\n" +
	"--b1\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\nThanks, got it =E2=80=94 Ada\r\n" +
	"--b1\r\nContent-Type: text/html; charset=UTF-8\r\n\r\n<p>Thanks, got it</p>\r\n--b1--\r\n"

func TestSESWebhook_AReplyIsFiledOnItsPerson(t *testing.T) {
	for _, encoding := range []string{"UTF8", "BASE64"} {
		t.Run(encoding, func(t *testing.T) {
			w := newWorld(t)
			got := w.post(t, "/v1/email/webhooks/ses?org=acme", js(w.notification("2", func(e *snsEnvelope) { e.Message = receipt(reply, encoding) })), nil)
			if got != http.StatusOK {
				t.Fatalf("a receipt SNS signed answered %d, want 200", got)
			}
			w.plane.mu.Lock()
			defer w.plane.mu.Unlock()
			if len(w.plane.filed) != 1 {
				t.Fatalf("the CRM was given %d mails, want the reply", len(w.plane.filed))
			}
			want := crm.Mail{
				ID: "in:ses-in-1", From: "Ada@Acme.io", To: "growth@acme.io", Subject: "Re: Your code ✓", Body: "Thanks, got it — Ada",
				MessageID: "<r1@mail.acme.io>", References: "<ses-1@email.amazonses.com>", At: signedAt, Rail: "ses",
			}
			if m := w.plane.filed[0]; m != want {
				t.Errorf("filed %+v, want %+v", m, want)
			}
		})
	}
}

func TestSESWebhook_WhatIsNotAReplyIsFiledNowhere(t *testing.T) {
	auto := strings.Replace(reply, "Message-ID:", "Auto-Submitted: auto-replied\r\nMessage-ID:", 1)
	bounce := strings.Replace(reply, "From: =?UTF-8?Q?Ada_Lovelace?= <Ada@Acme.io>", "From: MAILER-DAEMON@acme.io", 1)
	for name, message := range map[string]string{
		"an automatic answer":                   receipt(auto, "UTF8"),
		"a bounce":                              receipt(bounce, "UTF8"),
		"a message that failed DMARC":           receipt(reply, "UTF8", "dmarcVerdict"),
		"a message that failed the virus check": receipt(reply, "UTF8", "virusVerdict"),
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			if got := w.post(t, "/v1/email/webhooks/ses?org=acme", js(w.notification("2", func(e *snsEnvelope) { e.Message = message })), nil); got != http.StatusOK {
				t.Fatalf("answered %d, want 200: SNS would send it again", got)
			}
			w.plane.mu.Lock()
			defer w.plane.mu.Unlock()
			if len(w.plane.filed) != 0 {
				t.Errorf("filed %+v", w.plane.filed)
			}
		})
	}
}

func TestSESWebhook_AReceiptFromAnotherTopicIsRefused(t *testing.T) {
	w := newWorld(t)
	got := w.post(t, "/v1/email/webhooks/ses?org=acme", js(w.notification("2", func(e *snsEnvelope) { e.Message, e.TopicARN = receipt(reply, "UTF8"), otherTopic })), nil)
	if got != http.StatusUnauthorized {
		t.Fatalf("a receipt from another topic answered %d, want 401", got)
	}
	w.plane.mu.Lock()
	defer w.plane.mu.Unlock()
	if len(w.plane.filed) != 0 {
		t.Errorf("filed %+v", w.plane.filed)
	}
}
