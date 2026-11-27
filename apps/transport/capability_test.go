package transport

import (
	"context"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
)

// capability_test.go holds the published capabilities against what the transports
// actually do.
//
// caps is a promise: routes.go projects it onto GET /v1/transport and the send
// path never consults it again, so a wrong flag is not a refusal — it is a
// promise a caller acts on and a failure at the platform. Nothing pinned a single
// one of them. Every flag on every transport could be set true and the suite
// stayed green, which is how whatsapp came to advertise native media and then
// drop every attachment, and how an attachment-only send to it reached the endpoint
// with no text at all and answered 502.
//
// So each flag is checked against a FACT about this transport, and a flag can
// only be set once the behaviour behind it exists.

// probe is one inbound event a transport must classify, and the channel kind it has
// to produce. The kinds a transport can produce ARE its dm/group/thread
// capabilities, so the probes carry the negative cases too — discord's
// guild-less interaction is here to show that a DM is unreachable rather than
// merely unlisted.
type probe struct {
	what string
	ev   client.TransportIngestIn
	kind ChannelKind
}

var probes = map[string][]probe{
	"discord": {
		{"a guild channel", ingressEv("acme", "discord", "G1", "u1", "c-1", "", "hi", "d1", ""), ChannelGroup},
		// The interactions ingress refuses an interaction with no guild before it
		// ever reaches normalize, so nothing arrives classified as a DM.
		{"no guild", ingressEv("acme", "discord", "", "u1", "c-1", "", "hi", "d2", ""), ChannelGroup},
	},
	"github": {
		// An issue or pull request is one conversation; there is no direct message.
		{"an issue thread", ingressEv("acme", "github", "111", "u1", "acme/widgets#7", "", "hi", "gh1", ""), ChannelThread},
	},
	"linear": {
		{"an issue", ingressEv("acme", "linear", "org-1", "u1", "6f0b-issue", "", "hi", "ln1", ""), ChannelThread},
	},
	"slack": {
		{"an IM conversation", ingressEv("acme", "slack", "T1", "u1", "D024BE91L", "", "hi", "s1", ""), ChannelDM},
		{"a public channel", ingressEv("acme", "slack", "T1", "u1", "C024BE91L", "", "hi", "s2", ""), ChannelGroup},
		{"a threaded message", ingressEv("acme", "slack", "T1", "u1", "C024BE91L", "171.2", "hi", "s3", ""), ChannelThread},
	},
	"teams": {
		{"a channel conversation", ingressEv("acme", "teams", "t1", "u1", "19:x@thread.tacv2", "", "hi", "t1", "https://smba.example/amer/"), ChannelGroup},
		{"a personal chat", ingressEv("acme", "teams", "t1", "u1", "a:1a2b", "", "hi", "t2", "https://smba.example/amer/"), ChannelDM},
	},
	"telegram": {
		{"a supergroup", ingressEv("acme", "telegram", "-1001", "u1", "-1001", "", "hi", "g1", ""), ChannelGroup},
		{"a private chat", ingressEv("acme", "telegram", "777", "u1", "777", "", "hi", "g2", ""), ChannelDM},
	},
	"whatsapp": {
		{"a person's number", ingressEv("acme", "whatsapp", "PN1", "15551230000", "15551230000", "", "hi", "w1", "wamid.1"), ChannelDM},
		{"a group", whatsappGroupEv(), ChannelGroup},
	},
}

// whatsappGroupEv is one group message with the image the adapter already fetched.
func whatsappGroupEv() client.TransportIngestIn {
	ev := ingressEv("acme", "whatsapp", "PN1", "15551230000", "Y2FwaV9ncm91cDox", "", "the mock", "w2", "wamid.2")
	ev.Group = true
	ev.Attachments = []client.IngestAttachment{{
		Kind: "image", URL: "whatsapp-media:MEDIA", MIME: "image/jpeg", Name: "shot.jpg",
	}}
	return ev
}

// TestCapabilitiesAreFactsAboutTheTransport reads dm/group/thread off what
// normalize produces, so a flag cannot be set without the classification behind
// it. Mutation-checked in both directions: DM:true on discord (which cannot
// classify one) and Group:false on slack (which can) each fail here.
func TestCapabilitiesAreFactsAboutTheTransport(t *testing.T) {
	for _, tr := range transports {
		cases, ok := probes[tr.id]
		if !ok {
			t.Fatalf("%s: the registry gained a transport with no probes; add the shapes it classifies", tr.id)
		}
		produced := map[ChannelKind]bool{}
		for _, p := range cases {
			m, ok := tr.normalize(p.ev)
			if !ok {
				t.Fatalf("%s: normalize refused %s", tr.id, p.what)
			}
			if m.Channel.Kind != p.kind {
				t.Fatalf("%s: %s classified %q, want %q", tr.id, p.what, m.Channel.Kind, p.kind)
			}
			produced[m.Channel.Kind] = true
		}
		for _, want := range []struct {
			kind ChannelKind
			flag bool
			name string
		}{
			{ChannelDM, tr.caps.DM, "dm"},
			{ChannelGroup, tr.caps.Group, "group"},
			{ChannelThread, tr.caps.Thread, "thread"},
		} {
			if want.flag != produced[want.kind] {
				t.Errorf("%s advertises %s=%v and classifies %s=%v — a capability is what the transport does",
					tr.id, want.name, want.flag, want.kind, produced[want.kind])
			}
		}
	}
}

// sendProbe is where one transport can be sent, and the reply root its egress
// needs to find.
var sendProbes = map[string]struct{ channel, root string }{
	"discord":  {"c-1", ""},
	"github":   {"acme/widgets#7", ""},
	"linear":   {"6f0b-issue", ""},
	"slack":    {"C1", ""},
	"teams":    {"19:x@thread.tacv2", "https://smba.example/amer/"},
	"telegram": {"777", ""},
	"whatsapp": {"15551230000", ""},
}

// richMessage carries one of everything the envelope can hold, so a transport
// that drops attachments or actions is visible in what its endpoint received.
func richMessage(transport, channel string) Message {
	return Message{
		Transport: transport,
		Channel:   Channel{ID: channel, Kind: ChannelDM},
		Text:      "the invoice",
		Attachments: []Attachment{{
			Kind: AttachmentFile, URL: "https://example.invalid/invoice.pdf", MIME: "application/pdf",
		}},
		Actions: []Action{{Kind: ActionURL, Label: "open", URL: "https://example.invalid/i"}},
	}
}

// TestEveryTransportFlattensWhatItCannotRender pins media/actions to renderText,
// the ONE downgrade path. No transport renders an attachment or a control
// natively, so both flags are false everywhere and every egress hands the endpoint
// renderText's flattening — a transport that passed m.Text raw would drop the
// attachment silently, and an attachment-only send would reach the endpoint with
// nothing to say and fail at the platform.
func TestEveryTransportFlattensWhatItCannotRender(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newApp(t)
		st := e.store(t)
		s := mounted.Load()
		if s == nil {
			t.Fatal("transports not mounted")
		}
		spies := map[string]*endpointRec{
			"discord": spyDiscord(t), "github": spyGitHub(t), "linear": spyLinear(t),
			"slack": spySlack(t), "teams": spyTeams(t), "telegram": spyTelegram(t), "whatsapp": spyWhatsApp(t),
		}
		ctx := context.Background()
		now := time.Now().Unix()

		for _, tr := range transports {
			if tr.caps.Media || tr.caps.Actions {
				t.Errorf("%s advertises media=%v actions=%v, and renderText is still the only renderer — "+
					"a transport claims native rendering once it has it", tr.id, tr.caps.Media, tr.caps.Actions)
				continue
			}
			p, ok := sendProbes[tr.id]
			if !ok {
				t.Fatalf("%s: the registry gained a transport with no send probe", tr.id)
			}
			if err := st.upsertRoute(ctx, "acme", tr.id, p.channel, p.root, now, 0); err != nil {
				t.Fatalf("%s: seed route: %v", tr.id, err)
			}
			m := richMessage(tr.id, p.channel)
			if _, err := tr.send(ctx, s, "acme", m); err != nil {
				t.Fatalf("%s: send: %v", tr.id, err)
			}
			call := spies[tr.id].call(t, 0)
			if call.text != renderText(m) {
				t.Errorf("%s sent %q, want the flattened %q", tr.id, call.text, renderText(m))
			}
		}

	})
}

// TestEveryTransportSendsAsTheOrg pins the tenant onto the wire. The org is what
// buys the credential — integrations resolves the per-org token from it and
// refuses an empty one — so an endpoint that drops it leaves the custody check
// nothing to check. Four of the five dropped it, which no test could see because
// only slack's spy recorded the field.
func TestEveryTransportSendsAsTheOrg(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newApp(t)
		st := e.store(t)
		s := mounted.Load()
		if s == nil {
			t.Fatal("transports not mounted")
		}
		spies := map[string]*endpointRec{
			"discord": spyDiscord(t), "github": spyGitHub(t), "linear": spyLinear(t),
			"slack": spySlack(t), "teams": spyTeams(t), "telegram": spyTelegram(t), "whatsapp": spyWhatsApp(t),
		}
		ctx := context.Background()
		const org = "acme"

		for _, tr := range transports {
			p := sendProbes[tr.id]
			if err := st.upsertRoute(ctx, org, tr.id, p.channel, p.root, time.Now().Unix(), 0); err != nil {
				t.Fatalf("%s: seed route: %v", tr.id, err)
			}
			if _, err := tr.send(ctx, s, org, Message{
				Transport: tr.id, Channel: Channel{ID: p.channel, Kind: ChannelDM}, Text: "hi",
			}); err != nil {
				t.Fatalf("%s: send: %v", tr.id, err)
			}
			if call := spies[tr.id].call(t, 0); call.org != org {
				t.Errorf("%s sent as %q, want %q — the org is what resolves the credential", tr.id, call.org, org)
			}
		}

	})
}

// TestEveryEndpointNamesTheOrgOnTheWire reads what actually reached the plane. The
// endpoints are spied everywhere else in this package, so their own bodies were the
// one thing no test could see — and four of the five built a send that named no
// org at all, which left telegram and whatsapp undeliverable and let discord and
// teams spend a shared app credential with nothing to check it against.
func TestEveryEndpointNamesTheOrgOnTheWire(t *testing.T) {
	var sent []client.ChatSendIn
	saved := ask
	ask = func(ctx context.Context, app, op string, in *client.ChatSendIn) (*client.ChatSendOut, error) {
		sent = append(sent, *in)
		return &client.ChatSendOut{MessageID: "m"}, nil
	}
	t.Cleanup(func() { ask = saved })

	ctx := context.Background()
	const org = "acme"
	if _, err := discordEndpoint(ctx, org, "c-1", "", "hi"); err != nil {
		t.Fatalf("discord: %v", err)
	}
	if _, err := slackEndpoint(ctx, org, "C1", "", "", "hi", client.Controls{}); err != nil {
		t.Fatalf("slack: %v", err)
	}
	if err := teamsEndpoint(ctx, org, "https://smba.example/amer/", "19:x@thread.tacv2", "hi"); err != nil {
		t.Fatalf("teams: %v", err)
	}
	if err := telegramEndpoint(ctx, org, 777, 0, "hi"); err != nil {
		t.Fatalf("telegram: %v", err)
	}
	if _, err := whatsappEndpoint(ctx, org, "15551230000", "", "hi", false); err != nil {
		t.Fatalf("whatsapp: %v", err)
	}
	if _, err := githubEndpoint(ctx, org, "acme/widgets#7", "hi"); err != nil {
		t.Fatalf("github: %v", err)
	}
	if _, err := linearEndpoint(ctx, org, "6f0b-issue", "hi"); err != nil {
		t.Fatalf("linear: %v", err)
	}
	if len(sent) != len(transports) {
		t.Fatalf("%d sends reached the wire, want one per transport", len(sent))
	}
	for _, in := range sent {
		if in.Org != org {
			t.Errorf("%s sent as %q, want %q", in.Provider, in.Org, org)
		}
	}
}
