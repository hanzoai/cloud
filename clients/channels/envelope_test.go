package channels

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/clients/integrations"
)

// envelope_test.go proves the portable envelope's closure: per-transport
// normalization into ONE shape, kind-tagged unions with no string sniffing,
// the narrow egress projection (C2-6), and the deterministic downgrade
// renderer. Pure — no store, no HTTP.

func TestNormalize(t *testing.T) {
	ev := func(provider, externalID, user, channel, thread, text, key string) integrations.IngressEvent {
		return integrations.IngressEvent{Org: "acme", In: integrations.Inbound{
			Provider: provider, ExternalID: externalID, User: user,
			Channel: channel, ThreadID: thread, Text: text, DedupeKey: key,
		}}
	}
	cases := []struct {
		name string
		norm func(integrations.IngressEvent) (Message, bool)
		ev   integrations.IngressEvent
		ok   bool
		want Message
	}{
		{
			name: "telegram positive chat id is a DM; ThreadID is the reply target",
			norm: telegramNormalize,
			ev:   ev("telegram", "HanzoBot", "42", "777", "55", "hi", "u-1"),
			ok:   true,
			want: Message{Channel: "telegram", Account: "hanzobot", Sender: Sender{ExternalID: "42", Org: "acme"},
				Room: Room{ID: "777", Kind: RoomDM}, Text: "hi", ReplyTo: "55", Idempotency: "u-1"},
		},
		{
			name: "telegram negative chat id is a group",
			norm: telegramNormalize,
			ev:   ev("telegram", "HanzoBot", "42", "-1001234", "", "hi", "u-2"),
			ok:   true,
			want: Message{Channel: "telegram", Account: "hanzobot", Sender: Sender{ExternalID: "42", Org: "acme"},
				Room: Room{ID: "-1001234", Kind: RoomGroup}, Text: "hi", Idempotency: "u-2"},
		},
		{
			name: "telegram unparseable chat id drops",
			norm: telegramNormalize,
			ev:   ev("telegram", "HanzoBot", "42", "abc", "", "hi", "u-3"),
			ok:   false,
		},
		{
			name: "telegram zero chat id drops",
			norm: telegramNormalize,
			ev:   ev("telegram", "HanzoBot", "42", "0", "", "hi", "u-4"),
			ok:   false,
		},
		{
			name: "slack D-conversation is a DM",
			norm: slackNormalize,
			ev:   ev("slack", "T024ABC", "u1", "D024BE91L", "", "hello", "e-1"),
			ok:   true,
			want: Message{Channel: "slack", Account: "t024abc", Sender: Sender{ExternalID: "u1", Org: "acme"},
				Room: Room{ID: "D024BE91L", Kind: RoomDM}, Text: "hello", Idempotency: "e-1"},
		},
		{
			name: "slack threaded channel event is a thread replying under thread_ts",
			norm: slackNormalize,
			ev:   ev("slack", "T024ABC", "u1", "C024BE91L", "1712.0001", "hello", "e-2"),
			ok:   true,
			want: Message{Channel: "slack", Account: "t024abc", Sender: Sender{ExternalID: "u1", Org: "acme"},
				Room: Room{ID: "C024BE91L", Kind: RoomThread}, Text: "hello", ReplyTo: "1712.0001", Idempotency: "e-2"},
		},
		{
			name: "slack bare channel event is a group",
			norm: slackNormalize,
			ev:   ev("slack", "T024ABC", "u1", "C024BE91L", "", "hello", "e-3"),
			ok:   true,
			want: Message{Channel: "slack", Account: "t024abc", Sender: Sender{ExternalID: "u1", Org: "acme"},
				Room: Room{ID: "C024BE91L", Kind: RoomGroup}, Text: "hello", Idempotency: "e-3"},
		},
		{
			name: "teams 19: conversation is a group (Bot Framework thread id contract)",
			norm: teamsNormalize,
			ev:   ev("teams", "Tenant-1", "u7", "19:abc@thread.tacv2", "", "hey", "a-1"),
			ok:   true,
			want: Message{Channel: "teams", Account: "tenant-1", Sender: Sender{ExternalID: "u7", Org: "acme"},
				Room: Room{ID: "19:abc@thread.tacv2", Kind: RoomGroup}, Text: "hey", Idempotency: "a-1"},
		},
		{
			name: "teams a: conversation is personal",
			norm: teamsNormalize,
			ev:   ev("teams", "Tenant-1", "u7", "a:1a2b3c", "", "hey", "a-2"),
			ok:   true,
			want: Message{Channel: "teams", Account: "tenant-1", Sender: Sender{ExternalID: "u7", Org: "acme"},
				Room: Room{ID: "a:1a2b3c", Kind: RoomDM}, Text: "hey", Idempotency: "a-2"},
		},
		{
			// C1-F6 fail-safe: an unknown conversation shape classifies DM — the
			// strictest direction, since dmPolicy defaults to pairing.
			name: "teams unknown conversation shape falls back to DM",
			norm: teamsNormalize,
			ev:   ev("teams", "Tenant-1", "u7", "48:whatever", "", "hey", "a-3"),
			ok:   true,
			want: Message{Channel: "teams", Account: "tenant-1", Sender: Sender{ExternalID: "u7", Org: "acme"},
				Room: Room{ID: "48:whatever", Kind: RoomDM}, Text: "hey", Idempotency: "a-3"},
		},
		{
			name: "discord guild interaction is always a group",
			norm: discordNormalize,
			ev:   ev("discord", "GUILD9", "u5", "555", "", "ping", "i-1"),
			ok:   true,
			want: Message{Channel: "discord", Account: "guild9", Sender: Sender{ExternalID: "u5", Org: "acme"},
				Room: Room{ID: "555", Kind: RoomGroup}, Text: "ping", Idempotency: "i-1"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := tc.norm(tc.ev)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if !ok {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("message = %+v,\nwant      %+v", got, tc.want)
			}
		})
	}
}

func TestActionValidateClosedSet(t *testing.T) {
	opts := []SelectOption{{Label: "One", Value: "a"}}
	cases := []struct {
		name string
		a    Action
		ok   bool
	}{
		{"command", Action{Kind: ActionCommand, Label: "Deploy", Command: "/deploy"}, true},
		{"url", Action{Kind: ActionURL, Label: "Docs", URL: "https://docs.example"}, true},
		{"select", Action{Kind: ActionSelect, Label: "Pick", Options: opts}, true},
		{"approval", Action{Kind: ActionApproval, Label: "Approve", Approval: &Approval{ID: "ap-1"}}, true},
		// No sniffing: a /command-looking value inside a url action stays a url
		// action — kinds are declared, never inferred from string shape.
		{"url that looks like a command", Action{Kind: ActionURL, URL: "/deploy"}, true},
		{"unknown kind", Action{Kind: "menu"}, false},
		{"command empty", Action{Kind: ActionCommand}, false},
		{"command with url set", Action{Kind: ActionCommand, Command: "/x", URL: "https://x"}, false},
		{"url empty", Action{Kind: ActionURL}, false},
		{"select empty options", Action{Kind: ActionSelect}, false},
		{"select blank option", Action{Kind: ActionSelect, Options: []SelectOption{{Label: "", Value: "a"}}}, false},
		{"select with command set", Action{Kind: ActionSelect, Options: opts, Command: "/x"}, false},
		{"approval nil", Action{Kind: ActionApproval}, false},
		{"approval empty id", Action{Kind: ActionApproval, Approval: &Approval{}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.a.validate()
			if (err == nil) != tc.ok {
				t.Fatalf("validate = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

func TestAttachmentValidate(t *testing.T) {
	cases := []struct {
		name string
		a    Attachment
		ok   bool
	}{
		{"image", Attachment{Kind: AttachmentImage, URL: "https://cdn.example/a.png", MIME: "image/png"}, true},
		{"file", Attachment{Kind: AttachmentFile, URL: "https://cdn.example/f.pdf"}, true},
		{"empty url", Attachment{Kind: AttachmentImage}, false},
		{"unknown kind", Attachment{Kind: "gif", URL: "https://x"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.a.validate()
			if (err == nil) != tc.ok {
				t.Fatalf("validate = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

func TestSendRequestValidate(t *testing.T) {
	att := []Attachment{{Kind: AttachmentFile, URL: "https://cdn.example/f.pdf"}}
	cases := []struct {
		name string
		r    SendRequest
		ok   bool
	}{
		// Room.Kind is optional on egress — doors address rooms by id alone.
		{"kind optional on egress", SendRequest{Room: Room{ID: "r"}, Text: "x"}, true},
		{"attachments alone suffice", SendRequest{Room: Room{ID: "r"}, Attachments: att}, true},
		{"room id required", SendRequest{Text: "x"}, false},
		{"bad kind rejected", SendRequest{Room: Room{ID: "r", Kind: "castle"}, Text: "x"}, false},
		{"content required", SendRequest{Room: Room{ID: "r"}}, false},
		{"invalid action rejected", SendRequest{Room: Room{ID: "r"}, Text: "x", Actions: []Action{{Kind: "menu"}}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.r.validate()
			if (err == nil) != tc.ok {
				t.Fatalf("validate = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

func TestMessageValidate(t *testing.T) {
	m := Message{Channel: "slack", Account: "MiXeD", Room: Room{ID: "r", Kind: RoomDM}, Text: "x"}
	if err := m.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if m.Account != "mixed" {
		t.Fatalf("account = %q, want lowercased", m.Account)
	}
	bad := []Message{
		{Room: Room{ID: "r", Kind: RoomDM}, Text: "x"},                    // channel required
		{Channel: "slack", Room: Room{Kind: RoomDM}, Text: "x"},           // room id required
		{Channel: "slack", Room: Room{ID: "r", Kind: "weird"}, Text: "x"}, // closed room kinds
		{Channel: "slack", Room: Room{ID: "r"}, Text: "x"},                // kind required on the full envelope
		{Channel: "slack", Room: Room{ID: "r", Kind: RoomDM}},             // content required
	}
	for i := range bad {
		if err := bad[i].validate(); err == nil {
			t.Fatalf("message %d must not validate: %+v", i, bad[i])
		}
	}
}

func TestRenderTextDeterministic(t *testing.T) {
	m := Message{
		Channel: "slack",
		Room:    Room{ID: "C1", Kind: RoomGroup},
		Text:    "body",
		Attachments: []Attachment{
			{Kind: AttachmentImage, URL: "https://cdn.example/a.png", MIME: "image/png"},
		},
		Actions: []Action{
			{Kind: ActionCommand, Label: "Deploy", Command: "/deploy"},
			{Kind: ActionURL, Label: "Docs", URL: "https://docs.example"},
			{Kind: ActionSelect, Label: "Pick", Options: []SelectOption{{Label: "One", Value: "a"}, {Label: "Two", Value: "b"}}},
			{Kind: ActionApproval, Label: "Approve", Approval: &Approval{ID: "ap-1"}},
		},
	}
	a, b := renderText(m), renderText(m)
	if a != b {
		t.Fatalf("renderText not deterministic:\n%q\n%q", a, b)
	}
	if !strings.HasPrefix(a, "body") {
		t.Fatalf("rendered = %q, want the text first", a)
	}
	for _, once := range []string{
		"https://cdn.example/a.png", "(image/png)",
		"[Deploy] /deploy", "[Docs] https://docs.example",
		"[Pick] One | Two", "[Approve] approval requested: ap-1",
	} {
		if n := strings.Count(a, once); n != 1 {
			t.Fatalf("%q rendered %d times, want exactly once in %q", once, n, a)
		}
	}
}

// TestSendRequestNarrowDecode proves C2-6: identity fields (sender, account,
// channel) are not decodable on the egress body — the struct simply has no
// such fields, so nothing a caller sends can alias them. The route layer
// additionally rejects unknown keys loudly (send_test.go).
func TestSendRequestNarrowDecode(t *testing.T) {
	raw := []byte(`{"room":{"id":"r1"},"text":"hi","sender":{"externalId":"evil"},"account":"spoof","channel":"slack"}`)
	var r SendRequest
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := SendRequest{Room: Room{ID: "r1"}, Text: "hi"}
	if !reflect.DeepEqual(r, want) {
		t.Fatalf("decoded = %+v, want only the outbound projection %+v", r, want)
	}
}
