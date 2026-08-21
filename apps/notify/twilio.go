package notify

import (
	ntypes "github.com/hanzoai/notify/pkg/types"
	"github.com/hanzoai/notify/service/twilio"
	"github.com/hanzoai/notify/service/twilioemail"
)

// Twilio — one vendor, two providers: the programmable-messaging API for sms, voice
// and WhatsApp, and the email API. They are separate declarations because they take
// separate credentials and deliver on separate channels; an org may hold either.
//
// The delivery clients are notifyd's OWN packages, imported directly. Only the
// credential-to-client mapping lives here.
func init() {
	register(&provider{
		ID:       "twilio",
		Channels: []ntypes.Channel{ntypes.ChannelSMS, ntypes.ChannelVoice, ntypes.ChannelWhatsApp},
		Rank:     0, // preferred for sms: the notify-twilio credential is the live path
		Keys:     []string{"account-sid", "auth-token", "from-number"},
		Needs:    []string{"account-sid", "auth-token", "from-number"},
		Open: func(c map[string]string, to []string) (notifier, error) {
			t, err := twilio.New(c["account-sid"], c["auth-token"], c["from-number"])
			if err != nil {
				return nil, err
			}
			t.AddReceivers(to...)
			return t, nil
		},
	})

	register(&provider{
		ID:       "twilio_email",
		Channels: []ntypes.Channel{ntypes.ChannelEmail},
		Rank:     0, // preferred for email
		Keys:     []string{"account-sid", "auth-token", "from-email", "from-name"},
		Needs:    []string{"account-sid", "auth-token", "from-email"},
		Open: func(c map[string]string, to []string) (notifier, error) {
			t := twilioemail.New(c["account-sid"], c["auth-token"], c["from-email"], c["from-name"])
			t.AddReceivers(to...)
			return t, nil
		},
	})
}
