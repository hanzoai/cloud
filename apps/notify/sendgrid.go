package notify

import (
	"github.com/hanzoai/cloud/apps/email"
	ntypes "github.com/hanzoai/notify/pkg/types"
)

// SendGrid v3 carries a plain-text part beside the HTML body.
var _ alternative = (*email.SendGrid)(nil)

// SendGrid — dedicated high-volume outreach and bulk marketing email provider.
//
// Ranked behind twilio_email (Rank 0) and mail (Rank 1) on the default notify channel
// so standard transactional notifications continue to leave through Twilio Email/SMS,
// while CRM outreach and bulk campaigns pin `"provider": "sendgrid"` with separate
// SendGrid API key, sender identity, and dedicated IP pool credentials.
func init() {
	register(&provider{
		ID:       "sendgrid",
		Channels: []ntypes.Channel{ntypes.ChannelEmail},
		Rank:     2,
		Keys:     []string{"api-key", "from-email", "from-name", "reply-to", "ip-pool", "asm-group-id", "webhook-public-key"},
		Needs:    []string{"api-key", "from-email"},
		Open: func(c map[string]string, to []string) (notifier, error) {
			return email.NewSendGrid(c, to), nil
		},
	})
}
