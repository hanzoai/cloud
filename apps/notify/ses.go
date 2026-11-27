package notify

import (
	"github.com/hanzoai/cloud/apps/email"
	ntypes "github.com/hanzoai/notify/pkg/types"
)

// Amazon SES v2 carries a plain-text part beside the HTML body.
var _ alternative = (*email.SES)(nil)

// Amazon SES v2 — high-volume campaign and transactional email over HTTPS with AWS SigV4.
func init() {
	register(&provider{
		ID:       "ses",
		Channels: []ntypes.Channel{ntypes.ChannelEmail},
		Rank:     2,
		Keys:     []string{"access-key-id", "secret-access-key", "region", "from-email", "from-name", "configuration-set", "sns-topic-arn"},
		Needs:    []string{"access-key-id", "secret-access-key", "from-email"},
		Open: func(c map[string]string, to []string) (notifier, error) {
			return email.NewSES(c, to), nil
		},
	})
}
