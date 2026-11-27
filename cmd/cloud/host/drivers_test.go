package host

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/sdk/bus"
	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/hanzoai/cloud/sdk/object"
)

// The host takes its place in the deployment over the KV: it must link the drivers,
// or joinDeployment would fall back to answering alone.
func TestTheHostLinksTheDriversForItsSharedServices(t *testing.T) {
	c, err := coord.New(coord.Config{URL: "kv://127.0.0.1:1", BusURL: "nats://127.0.0.1:1", Self: coord.Member{ID: "cloud-0"}, Secret: bytes.Repeat([]byte{1}, 32)})
	if errors.Is(err, coord.ErrNoDriver) {
		t.Fatal("the host links no driver for KV_URL or CLOUD_PUBSUB_URL")
	}
	if err != nil {
		t.Fatal(err)
	}
	if !c.Shared() {
		t.Fatal("a host given KV_URL answers alone")
	}
}

// The host answers the browser at the public port, so it must be able to open the
// object store a deployment configures; with the driver unlinked, S3_ADMIN_* would
// refuse (object.ErrNoDriver) and every upload link would fail.
func TestTheHostLinksTheObjectStoreDriver(t *testing.T) {
	t.Setenv("S3_ADMIN_ACCESS_KEY", "AKIATEST")
	t.Setenv("S3_ADMIN_SECRET_KEY", "secrettest")
	s, err := object.Open(&object.Local{Dir: t.TempDir()})
	if errors.Is(err, object.ErrNoDriver) {
		t.Fatal("the host links no object store driver for S3_ADMIN_*")
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, local := s.(*object.Local); local {
		t.Fatal("a host configured with an object store opened local disk")
	}
}

// A process pointed at the cluster reaches it through the pubsub driver. Without the
// driver it would refuse every publish, and the fleet's feeds would go quiet.
func TestTheBusDriverIsLinked(t *testing.T) {
	t.Setenv("CLOUD_PUBSUB_URL", "nats://127.0.0.1:1")
	err := bus.Publish("x", nil)
	if err != nil && strings.Contains(err.Error(), bus.ErrNoDriver.Error()) {
		t.Fatal("no bus driver is linked: " + err.Error())
	}
}
