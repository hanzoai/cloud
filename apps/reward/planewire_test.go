// Copyright © 2026 Hanzo AI. MIT License.

package reward

import (
	"context"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk/kit"
	"github.com/hanzoai/cloud/sdk/plane"
)

// mustFields renders an event's fields for the plane.
func mustFields(t *testing.T, m map[string]any) []client.RewardField {
	t.Helper()
	f, err := client.RewardFields(m)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// TestRewardTypesCrossThePlaneCodec sends the three formerly map-carrying types
// through a real socket and the plane codec, which refuses a map at encode.
func TestRewardTypesCrossThePlaneCodec(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", kit.Sockets(t))
	const name = "rewardwire"
	app := zip.New(zip.Config{AppName: name})
	app.Post("/event", func(_ context.Context, in *client.RewardEventIn) (*client.RewardEventIn, error) { return in, nil },
		zip.WithOperationID("t_event"))
	app.Post("/bounty", func(_ context.Context, in *client.BountyTotals) (*client.BountyTotals, error) { return in, nil },
		zip.WithOperationID("t_bounty"))
	app.Post("/people", func(_ context.Context, in *client.RewardPeopleOut) (*client.RewardPeopleOut, error) { return in, nil },
		zip.WithOperationID("t_people"))
	go func() { _ = app.Listen(zip.SocketPath(name)) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	for i := 0; ; i++ {
		if c, err := net.Dial("unix", zip.SocketPath(name)); err == nil {
			_ = c.Close()
			break
		}
		if i > 200 {
			t.Fatal("never listening")
		}
		time.Sleep(10 * time.Millisecond)
	}
	ctx := context.Background()
	call := func(op string, in, out any) error {
		c, err := plane.Peer(name)
		if err != nil {
			return err
		}
		defer func() { _ = c.Close() }()
		switch v := in.(type) {
		case *client.RewardEventIn:
			r, err := zip.Call[client.RewardEventIn, client.RewardEventIn](ctx, c, op, v)
			if err == nil {
				*out.(*client.RewardEventIn) = *r
			}
			return err
		case *client.BountyTotals:
			r, err := zip.Call[client.BountyTotals, client.BountyTotals](ctx, c, op, v)
			if err == nil {
				*out.(*client.BountyTotals) = *r
			}
			return err
		default:
			r, err := zip.Call[client.RewardPeopleOut, client.RewardPeopleOut](ctx, c, op, v.(*client.RewardPeopleOut))
			if err == nil {
				*out.(*client.RewardPeopleOut) = *r
			}
			return err
		}
	}

	in := map[string]any{"repo": "hanzoai/x", "labels": []any{"a"}, "tokens": float64(5)}
	var ev client.RewardEventIn
	if err := call("t_event", &client.RewardEventIn{ID: "i", Fields: mustFields(t, in)}, &ev); err != nil {
		t.Fatalf("event: %v", err)
	}
	if got, err := ev.FieldMap(); err != nil || !reflect.DeepEqual(got, in) {
		t.Fatalf("fields = %v, %v", got, err)
	}

	var b client.BountyTotals
	if err := call("t_bounty", &client.BountyTotals{Orgs: client.OrgTotals(map[string]int{"acme": 30, "b": 1})}, &b); err != nil {
		t.Fatalf("bounty: %v", err)
	}
	if !reflect.DeepEqual(b.Map(), map[string]int{"acme": 30, "b": 1}) {
		t.Fatalf("totals = %v", b.Orgs)
	}

	var p client.RewardPeopleOut
	want := client.PillarCounts(map[string]int{"use": 3, "share": 1})
	if err := call("t_people", &client.RewardPeopleOut{People: []client.RewardPerson{{Subject: "s", Pillars: want}}}, &p); err != nil {
		t.Fatalf("people: %v", err)
	}
	if len(p.People) != 1 || !reflect.DeepEqual(p.People[0].Pillars, want) {
		t.Fatalf("pillars = %+v", p.People)
	}
}
