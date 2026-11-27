package provider

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
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

	// A media type with no id has nothing to fetch. Reading it as empty text
	// would put an agent in a conversation it cannot see.
	const image = `{"entry":[{"changes":[{"value":{
		"metadata":{"phone_number_id":"PN1"},
		"messages":[{"from":"1555","id":"wamid.B","type":"image"}]}}]}]}`
	if _, ok := parseWhatsAppEvent([]byte(image)); ok {
		t.Fatal("an image with no media id was read as a turn")
	}

	if _, ok := parseWhatsAppEvent([]byte("not json")); ok {
		t.Fatal("garbage parsed as a message")
	}
}

func TestWhatsAppParseReadsEachMediaTypeAndAGroup(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		kind     string
		id       string
		mime     string
		caption  string
		filename string
		group    string
	}{
		{
			name: "image",
			body: `{"entry":[{"changes":[{"value":{"metadata":{"phone_number_id":"PN1"},
				"messages":[{"from":"1555","id":"wamid.I","type":"image",
				"image":{"id":"IMG","mime_type":"image/jpeg","caption":"the mock"}}]}}]}]}`,
			kind: "image", id: "IMG", mime: "image/jpeg", caption: "the mock",
		},
		{
			name: "document",
			body: `{"entry":[{"changes":[{"value":{"metadata":{"phone_number_id":"PN1"},
				"messages":[{"from":"1555","id":"wamid.D","type":"document",
				"document":{"id":"DOC","mime_type":"application/pdf","filename":"spec.pdf","caption":"read this"}}]}}]}]}`,
			kind: "document", id: "DOC", mime: "application/pdf", caption: "read this", filename: "spec.pdf",
		},
		{
			name: "audio",
			body: `{"entry":[{"changes":[{"value":{"metadata":{"phone_number_id":"PN1"},
				"messages":[{"from":"1555","id":"wamid.A","type":"audio",
				"audio":{"id":"AUD","mime_type":"audio/ogg"}}]}}]}]}`,
			kind: "audio", id: "AUD", mime: "audio/ogg",
		},
		{
			name: "video",
			body: `{"entry":[{"changes":[{"value":{"metadata":{"phone_number_id":"PN1"},
				"messages":[{"from":"1555","id":"wamid.V","type":"video",
				"video":{"id":"VID","mime_type":"video/mp4","caption":"clip"}}]}}]}]}`,
			kind: "video", id: "VID", mime: "video/mp4", caption: "clip",
		},
		{
			name: "sticker",
			body: `{"entry":[{"changes":[{"value":{"metadata":{"phone_number_id":"PN1"},
				"messages":[{"from":"1555","id":"wamid.S","type":"sticker",
				"sticker":{"id":"STK","mime_type":"image/webp"}}]}}]}]}`,
			kind: "sticker", id: "STK", mime: "image/webp",
		},
		{
			name: "group on the message",
			body: `{"entry":[{"changes":[{"value":{"metadata":{"phone_number_id":"PN1"},
				"messages":[{"from":"1555","id":"wamid.G","type":"text","group_id":"GRP",
				"text":{"body":"in the group"}}]}}]}]}`,
			caption: "in the group", group: "GRP",
		},
		{
			name: "group on the context",
			body: `{"entry":[{"changes":[{"value":{"metadata":{"phone_number_id":"PN1"},
				"messages":[{"from":"1555","id":"wamid.C","type":"text",
				"context":{"group_id":"CTX"},"text":{"body":"context group"}}]}}]}]}`,
			caption: "context group", group: "CTX",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, ok := parseWhatsAppEvent([]byte(tc.body))
			if !ok {
				t.Fatal("not parsed")
			}
			if m.Account != "PN1" || m.From != "1555" {
				t.Fatalf("who = %s/%s", m.Account, m.From)
			}
			if m.GroupID != tc.group {
				t.Fatalf("group = %q, want %q", m.GroupID, tc.group)
			}
			if tc.kind == "" {
				if m.Text != tc.caption || m.Media != nil {
					t.Fatalf("text message = %+v", m)
				}
				return
			}
			if m.Media == nil {
				t.Fatal("media was dropped")
			}
			if m.Media.Kind != tc.kind || m.Media.ID != tc.id || m.Media.MIME != tc.mime ||
				m.Media.Caption != tc.caption || m.Media.Filename != tc.filename {
				t.Fatalf("media = %+v", m.Media)
			}
			if m.Text != tc.caption {
				t.Fatalf("caption = %q", m.Text)
			}
		})
	}
}

func TestWhatsAppGroupFieldsAreAcknowledgedNotParsedAsMessages(t *testing.T) {
	const body = `{"entry":[{"changes":[
		{"field":"group_lifecycle_update","value":{"group_id":"G1"}},
		{"field":"group_participants_update","value":{"group_id":"G1"}},
		{"field":"group_settings_update","value":{"groups":[{"id":"G2"}]}},
		{"field":"group_status_update","value":{"group_id":"G3"}},
		{"field":"messages","value":{"metadata":{"phone_number_id":"PN1"},
			"messages":[{"from":"1555","id":"wamid.T","type":"text","text":{"body":"hi"}}]}}
	]}]}`
	got := whatsappGroupUpdates([]byte(body))
	want := []whatsappGroupUpdate{
		{"group_lifecycle_update", "G1"},
		{"group_participants_update", "G1"},
		{"group_settings_update", "G2"},
		{"group_status_update", "G3"},
	}
	if len(got) != len(want) {
		t.Fatalf("updates = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("update %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	m, ok := parseWhatsAppEvent([]byte(body))
	if !ok || m.Text != "hi" || m.GroupID != "" {
		t.Fatalf("message beside the group fields = %+v ok=%v", m, ok)
	}
	if len(whatsappGroupUpdates([]byte("not json"))) != 0 {
		t.Fatal("garbage parsed as a group update")
	}
}

func TestWhatsAppLinkURLIsThePersonsNumber(t *testing.T) {
	u, err := linkURL(nil, "whatsapp", "PN1", "+15551234567")
	if err != nil {
		t.Fatal(err)
	}
	if u != "https://wa.me/15551234567" {
		t.Fatalf("link = %q", u)
	}
	if _, err := linkURL(nil, "whatsapp", "PN1", "  "); err == nil {
		t.Fatal("an empty number produced a link")
	}
}

func TestWhatsAppTurnTextUsesTheSlackFile(t *testing.T) {
	got := whatsappTurnText("the mock", &whatsappDownloaded{
		ID: "IMG", Name: "shot.jpg", MIME: "image/jpeg", Size: 4,
		Image: "data:image/jpeg;base64,AAAA",
	})
	for _, want := range []string{
		"the mock\n",
		`"id":"IMG"`,
		`"name":"shot.jpg"`,
		`"mimetype":"image/jpeg"`,
		`"size":4`,
		"data:image/jpeg;base64,AAAA",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("turn text %q is missing %q", got, want)
		}
	}
	if strings.Contains(got, "permalink") {
		t.Fatalf("turn text invented a permalink: %s", got)
	}
}
