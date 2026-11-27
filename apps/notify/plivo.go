package notify

import (
	ntypes "github.com/hanzoai/notify/pkg/types"
	"github.com/hanzoai/notify/service/plivo"
)

// Plivo — the second sms path. from-number is read but not required: Plivo accepts a
// send without a source on accounts that carry a default one, so demanding it here
// would refuse a delivery the provider would have made.
func init() {
	register(&provider{
		ID:       "plivo",
		Channels: []ntypes.Channel{ntypes.ChannelSMS, ntypes.ChannelVoice, ntypes.ChannelWhatsApp},
		Rank:     1,
		Keys:     []string{"auth-id", "auth-token", "from-number"},
		Needs:    []string{"auth-id", "auth-token"},
		Open: func(c map[string]string, to []string) (notifier, error) {
			p, err := plivo.New(
				&plivo.ClientOptions{AuthID: c["auth-id"], AuthToken: c["auth-token"]},
				&plivo.MessageOptions{Source: c["from-number"]},
			)
			if err != nil {
				return nil, err
			}
			p.AddReceivers(to...)
			return p, nil
		},
	})
}
