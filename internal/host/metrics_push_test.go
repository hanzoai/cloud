// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package host

import (
	"context"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/hanzoai/cloud/sdk/coord/coordtest"
	"github.com/hanzoai/o11y/pkg/zapmetricreceiver"
	luxlog "github.com/luxfi/log"
	luxmetric "github.com/luxfi/metric"
	"github.com/prometheus/client_golang/prometheus"
)

// A gauge recorded in this process reaches the o11y metrics receiver whole:
// name, labels and value, in one batch over ZAP.
func TestPushMetricsReachesTheReceiver(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	var mu sync.Mutex
	got := map[string]float64{}
	rcv, err := zapmetricreceiver.New(zapmetricreceiver.Config{
		Listen: "127.0.0.1:" + strconv.Itoa(port),
		NodeID: "test-metrics",
		OnBatch: func(_ context.Context, b *zapmetricreceiver.MetricBatch) error {
			mu.Lock()
			defer mu.Unlock()
			for _, f := range b.Families {
				if f.Name != "test_push_last_action_timestamp_seconds" {
					continue
				}
				for _, m := range f.Metrics {
					if m.Value != nil {
						got[m.Labels["entry"]] = *m.Value
					}
				}
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rcv.Stop()

	g := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "test_push_last_action_timestamp_seconds", Help: "test"}, []string{"entry"})
	metricRegistry.MustRegister(g)
	defer metricRegistry.Unregister(g)
	g.WithLabelValues("cron-sql-backup").Set(1790776417)

	exp, err := luxmetric.NewZAPExporter(luxmetric.ZAPExporterConfig{Endpoint: "127.0.0.1:" + strconv.Itoa(port), AppName: "cloud"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = exp.Shutdown(context.Background()) }()

	deadline := time.Now().Add(10 * time.Second)
	for {
		if err := pushMetricsOnce(context.Background(), exp); err != nil {
			t.Logf("push: %v", err)
		}
		mu.Lock()
		v, ok := got["cron-sql-backup"]
		mu.Unlock()
		if ok {
			if v != 1790776417 {
				t.Fatalf("value = %v, want 1790776417", v)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the receiver never saw the gauge")
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// Three pods pushing to one receiver are three identities, each heard. The receiver
// admits one connection per ZAP identity, and an identity that named the service
// alone was the same for every pod, so the pods knocked each other off and the store
// kept whichever batch landed last. Each pod now pushes as itself and says so in
// service.instance.id.
//
// MUTATION: name the exporter "cloud-"+service again and the receiver hears one pod
// at a time, never all three.
func TestEveryPodPushesUnderItsOwnName(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	addr := "127.0.0.1:" + strconv.Itoa(port)
	t.Setenv("O11Y_METRICS_ADDR", addr)

	var mu sync.Mutex
	heard := map[string]bool{}
	rcv, err := zapmetricreceiver.New(zapmetricreceiver.Config{
		Listen: addr,
		NodeID: "test-metrics",
		OnBatch: func(_ context.Context, b *zapmetricreceiver.MetricBatch) error {
			for _, f := range b.Families {
				if f.Name != "test_pods_gauge" {
					continue
				}
				mu.Lock()
				heard[b.Resource["service.instance.id"]] = true
				mu.Unlock()
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rcv.Stop()

	g := prometheus.NewGauge(prometheus.GaugeOpts{Name: "test_pods_gauge", Help: "test"})
	metricRegistry.MustRegister(g)
	defer metricRegistry.Unregister(g)
	g.Set(1)

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	was := coord.Default()
	defer coord.SetDefault(was)
	pods := []string{"cloud-0", "cloud-1", "cloud-2"}
	pod := coordtest.Memory(t)
	for _, id := range pods {
		coord.SetDefault(pod(id))
		PushMetrics(ctx, luxlog.New("test"), "workflow")
	}

	deadline := time.Now().Add(15 * time.Second)
	for {
		mu.Lock()
		n := len(heard)
		all := heard["cloud-0"] && heard["cloud-1"] && heard["cloud-2"]
		mu.Unlock()
		if all {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the receiver heard %d of %d pods: %v", n, len(pods), heard)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
