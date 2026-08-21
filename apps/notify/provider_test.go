package notify

import (
	"context"
	"slices"
	"testing"

	ntypes "github.com/hanzoai/notify/pkg/types"
)

// ── a fifth provider, added the way a real one would be ──────────────────────────
//
// Everything above the divider in a real provider file is exactly this: one
// declaration and a register() in an init(). It touches provider.go not at all,
// notify.go not at all, and the routes not at all — which is what the tests below
// assert. Rank 9 keeps it behind every shipped provider, so it changes no existing
// deployment's choice.

type courier struct{ to []string }

func (c *courier) Send(context.Context, string, string) error { return nil }

func init() {
	register(&provider{
		ID:       "courier",
		Channels: []ntypes.Channel{ntypes.ChannelSMS, ntypes.ChannelPush},
		Rank:     9,
		Keys:     []string{"token", "sender"},
		Needs:    []string{"token"},
		Open: func(_ map[string]string, to []string) (notifier, error) {
			return &courier{to: to}, nil
		},
	})
}

// ── the seam ─────────────────────────────────────────────────────────────────────

// The four shipped providers each declare their channels, their keys and the subset
// they cannot deliver without — the three facts that used to be three switches.
func TestEveryProviderDeclaresItself(t *testing.T) {
	for _, want := range []struct {
		id       string
		channels []ntypes.Channel
		keys     int
		needs    []string
	}{
		{"twilio", []ntypes.Channel{ntypes.ChannelSMS, ntypes.ChannelVoice, ntypes.ChannelWhatsApp}, 3,
			[]string{"account-sid", "auth-token", "from-number"}},
		{"plivo", []ntypes.Channel{ntypes.ChannelSMS, ntypes.ChannelVoice, ntypes.ChannelWhatsApp}, 3,
			[]string{"auth-id", "auth-token"}},
		{"twilio_email", []ntypes.Channel{ntypes.ChannelEmail}, 4,
			[]string{"account-sid", "auth-token", "from-email"}},
		{"mail", []ntypes.Channel{ntypes.ChannelEmail}, 6, []string{"smtp-host", "sender-email"}},
	} {
		t.Run(want.id, func(t *testing.T) {
			p, ok := providers[want.id]
			if !ok {
				t.Fatalf("%s is not registered", want.id)
			}
			if !slices.Equal(p.Channels, want.channels) {
				t.Fatalf("channels = %v, want %v", p.Channels, want.channels)
			}
			if len(p.Keys) != want.keys {
				t.Fatalf("keys = %v, want %d of them", p.Keys, want.keys)
			}
			if !slices.Equal(p.Needs, want.needs) {
				t.Fatalf("needs = %v, want %v", p.Needs, want.needs)
			}
			// Needs must be a SUBSET of Keys, or the pick asks for a credential the
			// resolver never reads and the provider is unreachable forever.
			for _, n := range p.Needs {
				if !slices.Contains(p.Keys, n) {
					t.Fatalf("needs %q, which Keys never reads", n)
				}
			}
		})
	}
}

// The preference order the switch used to hard-code is now Rank, and it still says
// the same thing: Twilio before Plivo on sms, Twilio before SMTP on email.
func TestPreferenceOrderIsDeclared(t *testing.T) {
	ids := func(ps []*provider) []string {
		out := make([]string, 0, len(ps))
		for _, p := range ps {
			out = append(out, p.ID)
		}
		return out
	}
	if got := ids(forChannel(ntypes.ChannelSMS)); !slices.Equal(got, []string{"twilio", "plivo", "courier"}) {
		t.Fatalf("sms order = %v, want twilio, plivo, then the test provider", got)
	}
	if got := ids(forChannel(ntypes.ChannelEmail)); !slices.Equal(got, []string{"twilio_email", "mail"}) {
		t.Fatalf("email order = %v, want twilio_email then mail", got)
	}
}

// A fifth provider is picked, has its declared keys read from KMS, and delivers —
// with no edit to the send path, the registry or the routes.
func TestAFifthProviderIsOneFile(t *testing.T) {
	s := &service{kms: fakeKMS{m: map[string]string{
		"orgs/hanzo/notify/courier/token":  "tok",
		"orgs/hanzo/notify/courier/sender": "hanzo",
	}}}

	// It is picked only because no shipped sms provider is configured for this org:
	// Rank keeps it last, so adding it cannot move an existing deployment's choice.
	got, err := s.pick(t.Context(), "hanzo", "sms")
	if err != nil || got != "courier" {
		t.Fatalf("pick = %q err=%v, want courier", got, err)
	}
	c := s.creds(t.Context(), "hanzo", "courier")
	if c["token"] != "tok" || c["sender"] != "hanzo" {
		t.Fatalf("creds = %v, want the keys the provider declared", c)
	}
	n, err := open("courier", c, []string{"+15550001111"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(n.(*courier).to, []string{"+15550001111"}) {
		t.Fatalf("recipients = %v, want the ones handed to open", n.(*courier).to)
	}
}

// It also reaches a channel no shipped provider serves: `push` was refused outright
// by the old switch's default arm, so a push provider could not have been added
// without editing it.
func TestANewChannelArrivesWithItsProvider(t *testing.T) {
	s := &service{kms: fakeKMS{m: map[string]string{"orgs/hanzo/notify/courier/token": "tok"}}}
	if got, err := s.pick(t.Context(), "hanzo", "push"); err != nil || got != "courier" {
		t.Fatalf("pick(push) = %q err=%v, want courier", got, err)
	}
}

// The required set is stated ONCE: the pick refuses a provider whose credentials are
// incomplete, and so does the send, from the same declaration.
func TestNeedsIsAskedOnceAndAnsweredTwice(t *testing.T) {
	s := &service{kms: fakeKMS{m: map[string]string{"orgs/hanzo/notify/courier/sender": "hanzo"}}}
	if _, err := s.pick(t.Context(), "hanzo", "push"); err == nil {
		t.Fatal("pick accepted a provider missing its required credential")
	}
	if _, err := open("courier", map[string]string{"sender": "hanzo"}, nil); err == nil {
		t.Fatal("open accepted a send missing the same credential")
	}
}

// Every send fails CLOSED on a name nobody registered, and reads no credential for
// one either — an unknown provider cannot be used to probe the KMS key space.
func TestAnUnknownProviderIsRefusedAndReadsNothing(t *testing.T) {
	if _, err := open("carrier-pigeon", map[string]string{"token": "t"}, nil); err == nil {
		t.Fatal("open accepted an unregistered provider")
	}
	if keysFor("carrier-pigeon") != nil {
		t.Fatal("an unregistered provider must read no credentials")
	}
	s := &service{kms: fakeKMS{m: map[string]string{"orgs/hanzo/notify/carrier-pigeon/token": "tok"}}}
	if c := s.creds(t.Context(), "hanzo", "carrier-pigeon"); len(c) != 0 {
		t.Fatalf("creds = %v, want none for an unregistered provider", c)
	}
}

// A provider that omits any of the three facts is a programming error, refused at
// init rather than at the first send that needs the missing one.
func TestAnIncompleteProviderIsRefusedAtInit(t *testing.T) {
	for name, p := range map[string]*provider{
		"nil":        nil,
		"no id":      {Channels: []ntypes.Channel{ntypes.ChannelSMS}, Open: func(map[string]string, []string) (notifier, error) { return nil, nil }},
		"no channel": {ID: "x", Open: func(map[string]string, []string) (notifier, error) { return nil, nil }},
		"no open":    {ID: "x", Channels: []ntypes.Channel{ntypes.ChannelSMS}},
		"duplicate":  {ID: "twilio", Channels: []ntypes.Channel{ntypes.ChannelSMS}, Open: func(map[string]string, []string) (notifier, error) { return nil, nil }},
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("register accepted an incomplete provider")
				}
			}()
			register(p)
		})
	}
}

// The send path names no provider: nothing outside a provider's own file decides
// which one delivers. Asserted by driving a send end to end through the fifth
// provider — a string switch would have refused a name it had never heard of.
func TestTheSendPathNamesNoProvider(t *testing.T) {
	s := &service{kms: fakeKMS{m: map[string]string{"orgs/hanzo/notify/courier/token": "tok"}}}
	used, err := s.sendReal(t.Context(), "hanzo", "sms", "", []string{"+15550001111"}, "", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if used != "courier" {
		t.Fatalf("delivered through %q, want courier", used)
	}
	if _, err := s.sendReal(t.Context(), "hanzo", "sms", "carrier-pigeon", nil, "", "x"); err == nil {
		t.Fatal("a pinned unknown provider must be refused")
	}
}
