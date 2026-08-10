// Package mq is queue and stream admin for your org: create them, watch them
// drain, ack what you pulled.
//
// It is Hanzo MQ, the managed message-queue product: org-scoped administration of
// durable JetStream queues on the platform message plane — streams, their
// messages, pull consumers and delivery — served at /v1/mq over the broker
// apps/pubsub embeds.
//
// # THE SPLIT — mq vs pubsub
//
// One broker, two ORTHOGONAL surfaces. pubsub is the messaging DATA plane
// (publish, subscribe, request/reply — the subject side). mq is the queue and
// stream ADMIN plane plus pull delivery (create/inspect/purge/delete streams,
// manage consumers, pull the next batch). No operation appears on both: the
// authored MQ spec's publish/subscribe/request/subjects operations are
// deliberately NOT served here — see typed_wire_test.go, where every refused
// operation of the authored spec (openapi d86248f^:mq/openapi.yaml) is pinned
// with its reason.
//
// # TENANCY
//
// The broker is the ONE in-cluster plane; it also carries platform-internal
// streams (the analytics event plane, the Kafka facade's topics). Isolation is
// therefore enforced HERE, from the validated principal and never from a
// request field:
//
//   - stream NAMES are namespaced per org on the wire ("MQ_<org>_<name>") and
//     presented bare; a caller can name only streams inside its own namespace.
//   - stream SUBJECTS are confined to the org's subject space "mq.<org>.>":
//     callers state subjects RELATIVE to it ("orders.*"), the prefix is added
//     on the way in and stripped on the way out. Two orgs can never bind
//     overlapping subjects, and no tenant stream can capture a platform
//     subject (event.>, commerce.>, …).
//
// # CONNECTION
//
// Mount dials pubsub.URL() — the ONE bus knob every app in this process reads —
// with unlimited reconnect, so this app mounts (and can describe itself) with
// no broker running; every op answers 503 until the plane is reachable and the
// health op reports degraded rather than lying. Shutdown drains the client.
package mq

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/pubsub"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/zap-proto/zip"
)

// zipdoc lifts the doc comment off each typed op — and off each field of its In
// and Out — into zipdoc_gen.go, which hands them to zip.Describe at init.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// apiTimeout bounds one JetStream management call so a wedged broker cannot
// hold a request open; pulls carry their own caller-chosen wait instead.
const apiTimeout = 10 * time.Second

// maxWait caps the wait a pull may ask for, so a caller cannot park requests
// on this surface indefinitely.
const maxWait = 60 * time.Second

// broker is the app's one connection to the plane, plus the mount instant the
// health op reports uptime from. Set once by Mount.
type broker struct {
	nc      *nats.Conn
	js      jetstream.JetStream
	mounted time.Time
}

// b is the running broker client. Package-level for Shutdown, like the sibling
// messaging apps (pubsub srv, kafka broker).
var b *broker

// Mount wires the MQ admin surface at /v1/mq and dials the platform broker.
// Registered in manifest/apps.go; the connection retries forever in the
// background, so mounting never depends on broker start order.
func Mount(app cloud.Router, deps cloud.Deps) error {
	log := luxlog.Default().New("subsystem", "mq")

	nc, err := nats.Connect(pubsub.URL(),
		nats.Name("cloud-mq"),
		nats.RetryOnFailedConnect(true),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
	)
	if err != nil {
		// Fail closed: only a malformed URL/options error lands here (a down
		// broker retries in the background); a config error must abort boot.
		return fmt.Errorf("mq.Mount: dial %s (fail-closed): %w", pubsub.URL(), err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return fmt.Errorf("mq.Mount: jetstream context: %w", err)
	}
	b = &broker{nc: nc, js: js, mounted: time.Now()}

	// cloud.Bridge — the ONE source of the org every handler scopes by (callerOf
	// reads what it parks) — is not installed here. Whoever composes the program
	// installs it once at the root — after the identity check that mints the
	// validated org and before any subsystem registers a route (serve.go) —
	// because that order is a property of the whole program and no subsystem can
	// assert it for itself.
	g := app.Group("/v1/mq")

	s := streams{b}
	zip.Get(g, "/streams", s.list)
	zip.Post(g, "/streams", s.create, zip.WithStatus(http.StatusCreated))
	zip.Get(g, "/streams/:name", s.get)
	zip.Put(g, "/streams/:name", s.update)
	zip.Delete(g, "/streams/:name", s.delete)
	zip.Post(g, "/streams/:name/purge", s.purge)

	m := messages{b}
	zip.Get(g, "/streams/:name/messages", m.list)
	zip.Delete(g, "/streams/:name/messages/:seq", m.delete)

	c := consumers{b}
	zip.Get(g, "/streams/:stream/consumers", c.list)
	zip.Post(g, "/streams/:stream/consumers", c.create, zip.WithStatus(http.StatusCreated))
	zip.Get(g, "/streams/:stream/consumers/:name", c.get)
	zip.Delete(g, "/streams/:stream/consumers/:name", c.delete)
	zip.Post(g, "/streams/:stream/consumers/:name/next", c.next)

	st := status{b}
	zip.Get(g, "/health", st.health)
	zip.Get(g, "/info", st.info)

	log.Info("mq admin surface mounted", "bus", pubsub.URL())
	return nil
}

// Shutdown drains and closes the broker client on graceful cloud shutdown.
// Idempotent.
func Shutdown(context.Context) error {
	if b != nil {
		b.nc.Close()
		b = nil
	}
	return nil
}

// live returns a bounded context for one management call, refusing early when
// the plane is unreachable so callers get an honest 503 instead of a timeout.
func (br *broker) live(ctx context.Context) (context.Context, context.CancelFunc, error) {
	if br == nil || br.nc == nil || br.nc.Status() != nats.CONNECTED {
		return nil, nil, zip.Errorf(http.StatusServiceUnavailable, "message plane unreachable")
	}
	ctx, cancel := context.WithTimeout(ctx, apiTimeout)
	return ctx, cancel, nil
}

// ── the org namespace ───────────────────────────────────────────────────────

// tok encodes an org id into the broker's name alphabet [A-Za-z0-9-_],
// injectively: '_' escapes the hex of any byte outside [A-Za-z0-9-], and '_'
// itself is escaped, so distinct orgs can never share a namespace.
func tok(org string) string {
	var sb strings.Builder
	for i := 0; i < len(org); i++ {
		ch := org[i]
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9', ch == '-':
			sb.WriteByte(ch)
		default:
			fmt.Fprintf(&sb, "_%02x", ch)
		}
	}
	return sb.String()
}

// name is the resource-name shape the authored spec admits for streams and
// consumers — also exactly the set that is safe inside a JetStream API subject.
var name = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,256}$`)

// streamID maps a caller's stream name into the org's namespace on the broker.
func streamID(org, n string) (string, error) {
	if !name.MatchString(n) {
		return "", zip.ErrBadRequest("stream name must match ^[a-zA-Z0-9_-]{1,256}$")
	}
	return "MQ_" + tok(org) + "_" + n, nil
}

// streamName is the inverse presentation: the bare name inside the org's
// namespace, and false for a stream that is not the org's (another tenant's,
// or a platform-internal stream).
func streamName(org, id string) (string, bool) {
	return strings.CutPrefix(id, "MQ_"+tok(org)+"_")
}

// subjectRoot is the org's subject space on the shared plane. Every subject a
// tenant stream binds, filters or reads lives under it.
func subjectRoot(org string) string { return "mq." + tok(org) + "." }

// wireSubject maps a caller's relative subject ("orders.*") onto the org's
// space. It refuses shapes NATS refuses (empty tokens, spaces) plus absolute
// escapes — a caller cannot name another org's space because the prefix is
// always added, never trusted.
func wireSubject(org, rel string) (string, error) {
	if rel == "" || strings.ContainsAny(rel, " \t\r\n") || strings.HasPrefix(rel, ".") ||
		strings.HasSuffix(rel, ".") || strings.Contains(rel, "..") {
		return "", zip.ErrBadRequest("bad subject " + strconv.Quote(rel))
	}
	return subjectRoot(org) + rel, nil
}

// relSubject presents a wire subject caller-relative again.
func relSubject(org, wire string) string {
	if rel, ok := strings.CutPrefix(wire, subjectRoot(org)); ok {
		return rel
	}
	return wire
}

// errHTTP maps a broker error onto the status the operation's contract names:
// the JetStream API's own code when it carries one (404 stream/consumer/message
// not found, 400 bad configuration), 409 for the two "already exists" answers
// the API reports as 400, and 503 when the plane did not answer.
func errHTTP(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := errors.AsType[*zip.HTTPError](err); ok {
		return err
	}
	if errors.Is(err, jetstream.ErrStreamNameAlreadyInUse) ||
		errors.Is(err, jetstream.ErrConsumerExists) ||
		errors.Is(err, jetstream.ErrConsumerNameAlreadyInUse) {
		return zip.ErrConflict(err.Error())
	}
	if api, ok := errors.AsType[*jetstream.APIError](err); ok {
		switch api.Code {
		case http.StatusNotFound:
			return zip.ErrNotFound(api.Description)
		case http.StatusBadRequest:
			return zip.ErrBadRequest(api.Description)
		case http.StatusServiceUnavailable:
			return zip.Errorf(http.StatusServiceUnavailable, "%s", api.Description)
		}
	}
	if errors.Is(err, nats.ErrTimeout) || errors.Is(err, nats.ErrNoResponders) ||
		errors.Is(err, context.DeadlineExceeded) || errors.Is(err, nats.ErrConnectionClosed) {
		return zip.Errorf(http.StatusServiceUnavailable, "message plane unreachable: %v", err)
	}
	return zip.ErrInternal(err.Error())
}

// page bounds a listing the way the authored spec does: limit 1–1000
// (default 100), offset ≥ 0.
func page(limit, offset int) (int, int) {
	if limit < 1 || limit > 1000 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

// dur parses the spec's duration strings — Go durations plus a whole-day
// suffix ("7d") — with "" and "0" meaning zero.
func dur(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" {
		return 0, nil
	}
	if days, ok := strings.CutSuffix(s, "d"); ok {
		if n, err := strconv.Atoi(days); err == nil {
			return time.Duration(n) * 24 * time.Hour, nil
		}
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, zip.ErrBadRequest("bad duration " + strconv.Quote(s))
	}
	return d, nil
}

// durOut presents a duration the way the spec's defaults read: "0" for zero.
func durOut(d time.Duration) string {
	if d == 0 {
		return "0"
	}
	return d.String()
}

// enum maps one spec vocabulary onto its broker constant, both ways; word
// answers the constant's spec word. All five vocabularies below are total over
// the authored spec's enums.
type enum[T comparable] map[string]T

func (e enum[T]) of(word, field string, def T) (T, error) {
	if word == "" {
		return def, nil
	}
	if v, ok := e[word]; ok {
		return v, nil
	}
	var zero T
	return zero, zip.ErrBadRequest("bad " + field + " " + strconv.Quote(word))
}

func (e enum[T]) word(v T) string {
	for w, x := range e {
		if x == v {
			return w
		}
	}
	return ""
}

var (
	retentions = enum[jetstream.RetentionPolicy]{
		"limits":    jetstream.LimitsPolicy,
		"interest":  jetstream.InterestPolicy,
		"workqueue": jetstream.WorkQueuePolicy,
	}
	storages = enum[jetstream.StorageType]{
		"file":   jetstream.FileStorage,
		"memory": jetstream.MemoryStorage,
	}
	acks = enum[jetstream.AckPolicy]{
		"none":     jetstream.AckNonePolicy,
		"all":      jetstream.AckAllPolicy,
		"explicit": jetstream.AckExplicitPolicy,
	}
	delivers = enum[jetstream.DeliverPolicy]{
		"all":               jetstream.DeliverAllPolicy,
		"last":              jetstream.DeliverLastPolicy,
		"new":               jetstream.DeliverNewPolicy,
		"by_start_sequence": jetstream.DeliverByStartSequencePolicy,
		"by_start_time":     jetstream.DeliverByStartTimePolicy,
		"last_per_subject":  jetstream.DeliverLastPerSubjectPolicy,
	}
	replays = enum[jetstream.ReplayPolicy]{
		"instant":  jetstream.ReplayInstantPolicy,
		"original": jetstream.ReplayOriginalPolicy,
	}
)
