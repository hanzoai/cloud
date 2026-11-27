package driver

import (
	"errors"
	"strings"
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/hanzoai/cloud/sdk/bus"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/cloud/sdk/object"
)

// Every plugin binary serves through cloud.Listen, so linking the ledger here links it
// in each. Without it a deployment that was told to bill meters nothing and looks
// healthy; this fails the build of any binary that drops the import.
func TestTheLedgerIsLinkedIntoEveryBinaryThatServes(t *testing.T) {
	t.Setenv("CLOUD_ENV", "mainnet")
	if _, err := meter.New(meter.Config{}); errors.Is(err, meter.ErrNoDriver) {
		t.Fatal("a named deployment found no ledger linked: its debits would vanish")
	}
}

// A plugin keeps its objects where the deployment says. With the driver unlinked,
// S3_ADMIN_* would refuse (object.ErrNoDriver) and the plugin's files would be
// unreachable, or worse land on its own disk.
func TestAPluginLinksTheObjectStoreDriver(t *testing.T) {
	t.Setenv("S3_ADMIN_ACCESS_KEY", "AKIATEST")
	t.Setenv("S3_ADMIN_SECRET_KEY", "secrettest")
	s, err := object.Open(&object.Local{Dir: t.TempDir()})
	if errors.Is(err, object.ErrNoDriver) {
		t.Fatal("cloud.Listen links no object store driver for S3_ADMIN_*")
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, local := s.(*object.Local); local {
		t.Fatal("a plugin configured with an object store opened local disk")
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

// The apps whose product is the bus read JetStream's refusals as statuses through
// bus.Err, which only the stream half of the driver teaches it.
func TestTheStreamDriverMapsJetStreamRefusals(t *testing.T) {
	err := bus.Err(jetstream.ErrKeyNotFound)
	if err == nil || !strings.Contains(err.Error(), "key not found") {
		t.Fatalf("bus.Err(ErrKeyNotFound) = %v, want a 404 naming the key", err)
	}
}
