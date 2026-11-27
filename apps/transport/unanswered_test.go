package transport

// unanswered_test.go — a message that will not get a turn is still answered.
//
// Both of these used to end in a log line and nothing in the channel: the person
// asked, and the bot was silent, which reads exactly like a bot that is not
// there. The sentence goes where the turn would have answered.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
)

// A full pool sheds the turn and says so.
func TestAShedTurnSaysSo(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		newApp(t)
		sl := spySlack(t)
		poolOnce.Do(func() {})
		prev := pool
		pool = newLimiter(0, 0)
		t.Cleanup(func() { pool = prev })

		ingest(context.Background(), ingressEv("acme-shed", "slack", "T024ABC", "u9", "C024BE91L", "", "fix the hero", "s1", ""))

		if got := sl.find(t, busyText); got.channel != "C024BE91L" || got.org != "acme-shed" {
			t.Fatalf("the shed turn was answered at %+v, want the channel it was asked in", got)
		}

	})
}

// An unreadable access policy runs nothing and says so.
func TestAnUnreadablePolicySaysSo(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newApp(t)
		sl := spySlack(t)
		if err := e.store(t).db.Close(); err != nil {
			t.Fatal(err)
		}

		ingest(context.Background(), ingressEv("acme-gate", "slack", "T024ABC", "u9", "C024BE91L", "", "fix the hero", "p1", ""))

		if got := sl.find(t, policyUnreadable); got.channel != "C024BE91L" {
			t.Fatalf("the refusal was said at %+v, want the channel it was asked in", got)
		}

	})
}

// A sign-in link that cannot be sent privately is never posted to the channel, and
// the channel is told why nothing ran — in a sentence that carries no link.
func TestAnUndeliverableSignInLinkIsSaid(t *testing.T) {
	recordTurns(t)
	prevIdentify, prevAsk := identify, ask
	identify = func(context.Context, string, string, *client.ChatIdentityIn) (*client.ChatIdentityOut, error) {
		return &client.ChatIdentityOut{Say: "connect: https://hanzo.id/link?nonce=only-for-you", Ephemeral: true}, nil
	}
	ask = func(context.Context, string, string, *client.ChatSendIn) (*client.ChatSendOut, error) {
		return nil, errors.New("no private reply on this transport")
	}
	t.Cleanup(func() { identify, ask = prevIdentify, prevAsk })

	h := &heard{}
	turn(service(), listening(h, true), "acme", asked())
	all := h.all()
	if len(all) != 1 || all[0].text != linkUndeliverable {
		t.Fatalf("want the channel told the link could not be sent, got %+v", all)
	}
	if strings.Contains(all[0].text, "nonce") {
		t.Fatalf("the private link reached the channel: %q", all[0].text)
	}
}
