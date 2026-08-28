package integrations

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func metaSignature(secret, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return whatsappSignaturePrefix + hex.EncodeToString(mac.Sum(nil))
}

// The signature is the whole authenticity of an inbound: a message accepted here
// creates the reply route that authorises this org to answer, so anything that
// gets past this check gets to hand an org a conversation to answer under its own
// number.
func TestWhatsAppSignatureGuardsTheRoute(t *testing.T) {
	const secret, body = "s3cret", `{"entry":[]}`
	t.Setenv("WHATSAPP_APP_SECRET", secret)

	if !whatsappSigned(metaSignature(secret, body), []byte(body)) {
		t.Fatal("a correctly signed body was refused")
	}
	if whatsappSigned(metaSignature("wrong-secret", body), []byte(body)) {
		t.Fatal("a body signed with another secret was accepted")
	}
	if whatsappSigned(metaSignature(secret, body), []byte(body+" ")) {
		t.Fatal("a signature was accepted for a body it does not cover")
	}
	if whatsappSigned("", []byte(body)) || whatsappSigned("sha256=zz", []byte(body)) {
		t.Fatal("a missing or unparseable signature was accepted")
	}

	// A TRUNCATED signature must not pass. hmac.Equal compares length first, so a
	// prefix of the right digest is rejected — a comparison that checked only as
	// many bytes as it was given would accept `sha256=` plus one correct byte, and
	// an attacker can find one byte at a time.
	full := metaSignature(secret, body)
	for _, n := range []int{len(whatsappSignaturePrefix) + 2, len(whatsappSignaturePrefix) + 8, len(full) - 2} {
		if whatsappSigned(full[:n], []byte(body)) {
			t.Fatalf("a %d-character prefix of a valid signature was accepted", n)
		}
	}

	// No secret configured must fail CLOSED. An endpoint that accepts everything
	// when it is misconfigured is worse than one that accepts nothing, because
	// nothing about it looks wrong.
	t.Setenv("WHATSAPP_APP_SECRET", "")
	if whatsappSigned(metaSignature(secret, body), []byte(body)) {
		t.Fatal("an unconfigured endpoint accepted a signed body")
	}
}

// Meta batches entry × changes × messages, and sends status callbacks through the
// same address with no message at all. Those are not turns.
func TestWhatsAppParseTakesTextAndDropsTheRest(t *testing.T) {
	const msg = `{"entry":[{"changes":[{"value":{
		"metadata":{"phone_number_id":"PN1"},
		"messages":[{"from":"15551234567","id":"wamid.A","type":"text","text":{"body":"hello"}}]}}]}]}`
	m, ok := parseWhatsAppEvent([]byte(msg))
	if !ok {
		t.Fatal("a text message was not parsed")
	}
	if m.Account != "PN1" || m.From != "15551234567" || m.Text != "hello" || m.MessageID != "wamid.A" {
		t.Fatalf("parsed = %+v", m)
	}

	// A status callback carries no messages.
	const status = `{"entry":[{"changes":[{"value":{
		"metadata":{"phone_number_id":"PN1"},
		"statuses":[{"id":"wamid.A","status":"delivered"}]}}]}]}`
	if _, ok := parseWhatsAppEvent([]byte(status)); ok {
		t.Fatal("a delivery receipt was read as a message")
	}

	// A non-text message is real but unanswerable here; reading it as empty text
	// would put an agent in a conversation it cannot see.
	const image = `{"entry":[{"changes":[{"value":{
		"metadata":{"phone_number_id":"PN1"},
		"messages":[{"from":"1555","id":"wamid.B","type":"image"}]}}]}]}`
	if _, ok := parseWhatsAppEvent([]byte(image)); ok {
		t.Fatal("an image was read as a text turn")
	}

	if _, ok := parseWhatsAppEvent([]byte("not json")); ok {
		t.Fatal("garbage parsed as a message")
	}
}
