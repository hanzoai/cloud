package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/sdk/secret"
)

// failingKMS answers every read with one error, the way a store that holds
// nothing — or a store that cannot answer — does.
type failingKMS struct{ err error }

func (k failingKMS) GetSecret(context.Context, string) ([]byte, error)    { return nil, k.err }
func (k failingKMS) PutSecret(context.Context, string, []byte) error      { return nil }
func (k failingKMS) DeleteSecret(context.Context, string) error           { return nil }
func (k failingKMS) Sign(context.Context, string, []byte) ([]byte, error) { return nil, k.err }

// A person with no link is told so, with their own connect URL, whichever way
// the store says "not held": the plane peer answers the typed sentinel wrapped
// in its op name (cloud.KMSPeer), the embedded store answers it bare.
func TestUnlinkedPersonGetsTheirConnectLink(t *testing.T) {
	for name, absent := range map[string]error{
		"plane peer": fmt.Errorf("kms kms_get: %w", secret.ErrNone),
		"embedded":   secret.ErrNone,
	} {
		t.Run(name, func(t *testing.T) {
			s := linkService(failingKMS{err: absent})
			link, say, private := channelIdentity(s, "hanzo", "slack", "T03258TMTMK", "U031THSR8J3")
			if link.Subject != "" {
				t.Fatalf("an unlinked person resolved to %q", link.Subject)
			}
			if !strings.Contains(say, "not linked to a Hanzo account") {
				t.Fatalf("the answer must say the account is not linked: %q", say)
			}
			if !strings.Contains(say, "https://api.hanzo.ai/v1/provider/slack/link?state=") {
				t.Fatalf("the answer must carry the connect URL: %q", say)
			}
			if !private {
				t.Fatal("a connect URL must only ever reach the person it was minted for")
			}
		})
	}
}

// A store that cannot answer is not an unlinked person: no URL, no claim that
// they are unlinked, and a sentence that says a retry is what fixes it.
func TestUnreadableLinkIsNotReadAsUnlinked(t *testing.T) {
	s := linkService(failingKMS{err: errors.New("kms kms_get: dial unix: connection refused")})
	_, say, _ := channelIdentity(s, "hanzo", "slack", "T03258TMTMK", "U031THSR8J3")
	if say != linkUnreadable {
		t.Fatalf("a store outage must answer linkUnreadable, got %q", say)
	}
	if strings.Contains(say, "/link?state=") || strings.Contains(say, "not linked") {
		t.Fatalf("a store outage must not send the person to re-link: %q", say)
	}
}
