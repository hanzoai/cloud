package notify

import (
	ntypes "github.com/hanzoai/notify/pkg/types"
	"github.com/hanzoai/notify/service/mail"
)

// SMTP — the email path an org already runs. Port defaults to 587 (submission with
// STARTTLS); user and password are optional, because a relay that authorizes by
// source address takes neither.
func init() {
	register(&provider{
		ID:       "mail",
		Channels: []ntypes.Channel{ntypes.ChannelEmail},
		Rank:     1,
		Keys:     []string{"smtp-host", "smtp-port", "smtp-user", "smtp-password", "sender-email", "sender-name"},
		Needs:    []string{"smtp-host", "sender-email"},
		Open: func(c map[string]string, to []string) (notifier, error) {
			port := c["smtp-port"]
			if port == "" {
				port = "587"
			}
			m := mail.New(c["sender-email"], c["smtp-host"]+":"+port)
			m.AuthenticateSMTP("", c["smtp-user"], c["smtp-password"], c["smtp-host"])
			m.AddReceivers(to...)
			return m, nil
		},
	})
}
