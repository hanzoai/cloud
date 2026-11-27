// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"context"
	"errors"
	"fmt"

	"github.com/hanzoai/cloud/client"
	notifypeer "github.com/hanzoai/cloud/client/notify"
)

// mailOrg is the org commerce's mail is sent AS. notify resolves the provider
// credential by org (KMS orgs/<org>/notify/<provider>/*), and the platform's own
// mail — IAM's codes and invitations — goes out under the platform app's org, so
// commerce's does too.
const mailOrg = "hanzo"

// notifyMail is commerce's mail.Sender: one notify send per recipient, over the
// internal plane, so commerce holds no mail credential of its own and there is one
// sender in the fleet. commerce calls it after a sale is sealed, on a goroutine of
// its own with a bound, and logs a failure rather than failing the sale.
type notifyMail struct{}

// Send delivers one plain-text message to every address in to, and reports every
// recipient that was not reached by its position, never by its address. A panic on
// the way is this send's error, never the process's: it runs inside the binary that
// serves every money path.
func (notifyMail) Send(ctx context.Context, to []string, subject, body string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("mail: the notify send panicked: %v", r)
		}
	}()
	var errs []error
	for i, addr := range to {
		if _, err := notifypeer.NotifySend(ctx, &client.Send{
			Org: mailOrg, Channel: "email", To: addr, Subject: subject, Body: body,
		}); err != nil {
			errs = append(errs, fmt.Errorf("recipient %d of %d: %w", i+1, len(to), err))
		}
	}
	return errors.Join(errs...)
}
