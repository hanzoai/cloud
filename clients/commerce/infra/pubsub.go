// Package infra provides infrastructure clients.
//
// This file implements the NATS client for pub/sub messaging and order events.
package infra

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hanzoai/pubsub-go"
	"github.com/hanzoai/pubsub-go/jetstream"
)

// PubSubConfig holds NATS configuration
type PubSubConfig struct {
	// Enabled enables the pubsub service
	Enabled bool

	// URL is the NATS server URL
	URL string

	// Name is the client name
	Name string

	// Token for authentication (optional)
	Token string

	// User for authentication (optional)
	User string

	// Password for authentication (optional)
	Password string

	// TLS enables TLS connection
	TLS bool

	// ReconnectWait is the wait time between reconnects
	ReconnectWait time.Duration

	// MaxReconnects is the maximum reconnection attempts
	MaxReconnects int

	// EnableJetStream enables JetStream for persistence
	EnableJetStream bool
}

// PubSubClient wraps the NATS client
type PubSubClient struct {
	config *PubSubConfig
	conn   *nats.Conn
	js     jetstream.JetStream
}

// NewPubSubClient creates a new NATS pubsub client
func NewPubSubClient(ctx context.Context, cfg *PubSubConfig) (*PubSubClient, error) {
	if cfg.ReconnectWait == 0 {
		cfg.ReconnectWait = 2 * time.Second
	}
	if cfg.MaxReconnects == 0 {
		cfg.MaxReconnects = 60
	}
	if cfg.Name == "" {
		cfg.Name = "commerce"
	}

	opts := []nats.Option{
		nats.Name(cfg.Name),
		nats.ReconnectWait(cfg.ReconnectWait),
		nats.MaxReconnects(cfg.MaxReconnects),
		nats.DisconnectErrHandler(func(nc *nats.Conn, err error) {
			if err != nil {
				fmt.Printf("NATS disconnected: %v\n", err)
			}
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			fmt.Printf("NATS reconnected to %s\n", nc.ConnectedUrl())
		}),
	}

	if cfg.Token != "" {
		opts = append(opts, nats.Token(cfg.Token))
	}
	if cfg.User != "" && cfg.Password != "" {
		opts = append(opts, nats.UserInfo(cfg.User, cfg.Password))
	}

	conn, err := nats.Connect(cfg.URL, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to nats: %w", err)
	}

	client := &PubSubClient{
		config: cfg,
		conn:   conn,
	}

	// Initialize JetStream if enabled
	if cfg.EnableJetStream {
		js, err := jetstream.New(conn)
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("failed to create jetstream context: %w", err)
		}
		client.js = js
	}

	return client, nil
}

// Publish publishes a message to a subject
func (c *PubSubClient) Publish(ctx context.Context, subject string, data []byte) error {
	err := c.conn.Publish(subject, data)
	if err != nil {
		return fmt.Errorf("failed to publish: %w", err)
	}
	return nil
}

// PublishJSON publishes a JSON message
func (c *PubSubClient) PublishJSON(ctx context.Context, subject string, v interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("failed to marshal json: %w", err)
	}
	return c.Publish(ctx, subject, data)
}

// Request publishes a request and waits for a response
func (c *PubSubClient) Request(ctx context.Context, subject string, data []byte, timeout time.Duration) ([]byte, error) {
	msg, err := c.conn.RequestWithContext(ctx, subject, data)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	return msg.Data, nil
}

// RequestJSON publishes a JSON request and unmarshals the response
func (c *PubSubClient) RequestJSON(ctx context.Context, subject string, req interface{}, resp interface{}, timeout time.Duration) error {
	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	respData, err := c.Request(ctx, subject, data, timeout)
	if err != nil {
		return err
	}

	if err := json.Unmarshal(respData, resp); err != nil {
		return fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return nil
}

// Subscribe subscribes to a subject
func (c *PubSubClient) Subscribe(subject string, handler func(*Message)) (*Subscription, error) {
	sub, err := c.conn.Subscribe(subject, func(msg *nats.Msg) {
		handler(&Message{
			Subject: msg.Subject,
			Reply:   msg.Reply,
			Data:    msg.Data,
			msg:     msg,
		})
	})
	if err != nil {
		return nil, fmt.Errorf("failed to subscribe: %w", err)
	}

	return &Subscription{sub: sub}, nil
}

// QueueSubscribe subscribes to a subject with a queue group
func (c *PubSubClient) QueueSubscribe(subject, queue string, handler func(*Message)) (*Subscription, error) {
	sub, err := c.conn.QueueSubscribe(subject, queue, func(msg *nats.Msg) {
		handler(&Message{
			Subject: msg.Subject,
			Reply:   msg.Reply,
			Data:    msg.Data,
			msg:     msg,
		})
	})
	if err != nil {
		return nil, fmt.Errorf("failed to queue subscribe: %w", err)
	}

	return &Subscription{sub: sub}, nil
}

// JetStream operations

// EnsureStream creates a stream if it doesn't exist
func (c *PubSubClient) EnsureStream(ctx context.Context, cfg *StreamConfig) error {
	if c.js == nil {
		return fmt.Errorf("jetstream not enabled")
	}

	_, err := c.js.Stream(ctx, cfg.Name)
	if err == nil {
		return nil // Stream exists
	}

	_, err = c.js.CreateStream(ctx, jetstream.StreamConfig{
		Name:        cfg.Name,
		Description: cfg.Description,
		Subjects:    cfg.Subjects,
		Retention:   jetstream.RetentionPolicy(cfg.Retention),
		MaxAge:      cfg.MaxAge,
		MaxBytes:    cfg.MaxBytes,
		MaxMsgs:     cfg.MaxMsgs,
		Storage:     jetstream.StorageType(cfg.Storage),
		Replicas:    cfg.Replicas,
	})
	if err != nil {
		return fmt.Errorf("failed to create stream: %w", err)
	}

	return nil
}

// PublishToStream publishes a message to a JetStream stream
func (c *PubSubClient) PublishToStream(ctx context.Context, subject string, data []byte) (*PubAck, error) {
	if c.js == nil {
		return nil, fmt.Errorf("jetstream not enabled")
	}

	ack, err := c.js.Publish(ctx, subject, data)
	if err != nil {
		return nil, fmt.Errorf("failed to publish to stream: %w", err)
	}

	return &PubAck{
		Stream:   ack.Stream,
		Sequence: ack.Sequence,
		Domain:   ack.Domain,
	}, nil
}

// PublishJSONToStream publishes a JSON message to a stream
func (c *PubSubClient) PublishJSONToStream(ctx context.Context, subject string, v interface{}) (*PubAck, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal json: %w", err)
	}
	return c.PublishToStream(ctx, subject, data)
}

// CreateConsumer creates a durable consumer
func (c *PubSubClient) CreateConsumer(ctx context.Context, stream string, cfg *ConsumerConfig) (jetstream.Consumer, error) {
	if c.js == nil {
		return nil, fmt.Errorf("jetstream not enabled")
	}

	jsCfg := jetstream.ConsumerConfig{
		Name:          cfg.Name,
		Durable:       cfg.Durable,
		Description:   cfg.Description,
		FilterSubject: cfg.FilterSubject,
		AckPolicy:     jetstream.AckPolicy(cfg.AckPolicy),
		AckWait:       cfg.AckWait,
		MaxDeliver:    cfg.MaxDeliver,
		MaxAckPending: cfg.MaxAckPending,
	}

	if cfg.DeliverPolicy == DeliverAll {
		jsCfg.DeliverPolicy = jetstream.DeliverAllPolicy
	} else if cfg.DeliverPolicy == DeliverNew {
		jsCfg.DeliverPolicy = jetstream.DeliverNewPolicy
	}

	consumer, err := c.js.CreateOrUpdateConsumer(ctx, stream, jsCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create consumer: %w", err)
	}

	return consumer, nil
}

// ConsumeMessages starts consuming messages from a consumer
func (c *PubSubClient) ConsumeMessages(ctx context.Context, stream, consumer string, handler func(*StreamMessage) error) error {
	if c.js == nil {
		return fmt.Errorf("jetstream not enabled")
	}

	cons, err := c.js.Consumer(ctx, stream, consumer)
	if err != nil {
		return fmt.Errorf("failed to get consumer: %w", err)
	}

	cc, err := cons.Consume(func(msg jetstream.Msg) {
		sm := &StreamMessage{
			Subject:  msg.Subject(),
			Data:     msg.Data(),
			Headers:  msg.Headers(),
			Metadata: nil,
			msg:      msg,
		}

		if meta, _ := msg.Metadata(); meta != nil {
			sm.Metadata = &MessageMetadata{
				Sequence:   meta.Sequence.Stream,
				Timestamp:  meta.Timestamp,
				NumPending: meta.NumPending,
			}
		}

		if err := handler(sm); err != nil {
			msg.Nak()
		} else {
			msg.Ack()
		}
	})
	if err != nil {
		return fmt.Errorf("failed to consume: %w", err)
	}

	<-ctx.Done()
	cc.Stop()

	return nil
}

// Health checks the NATS connection
func (c *PubSubClient) Health(ctx context.Context) HealthStatus {
	start := time.Now()

	if !c.conn.IsConnected() {
		return HealthStatus{
			Healthy: false,
			Latency: time.Since(start),
			Error:   "not connected",
		}
	}

	return HealthStatus{
		Healthy: true,
		Latency: time.Since(start),
	}
}

// Close closes the NATS connection
func (c *PubSubClient) Close() error {
	if c.conn != nil {
		c.conn.Close()
	}
	return nil
}

// Conn returns the underlying NATS connection for advanced operations
func (c *PubSubClient) Conn() *nats.Conn {
	return c.conn
}

// JetStream returns the JetStream context for advanced operations
func (c *PubSubClient) JetStream() jetstream.JetStream {
	return c.js
}

// Message represents a received message
type Message struct {
	Subject string
	Reply   string
	Data    []byte
	msg     *nats.Msg
}

// Respond sends a response to a request
func (m *Message) Respond(data []byte) error {
	return m.msg.Respond(data)
}

// RespondJSON sends a JSON response
func (m *Message) RespondJSON(v interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return m.Respond(data)
}

// Subscription wraps a NATS subscription
type Subscription struct {
	sub *nats.Subscription
}

// Unsubscribe removes the subscription
func (s *Subscription) Unsubscribe() error {
	return s.sub.Unsubscribe()
}

// Drain unsubscribes and drains pending messages
func (s *Subscription) Drain() error {
	return s.sub.Drain()
}

// StreamConfig configures a JetStream stream
type StreamConfig struct {
	Name        string
	Description string
	Subjects    []string
	Retention   RetentionPolicy
	MaxAge      time.Duration
	MaxBytes    int64
	MaxMsgs     int64
	Storage     StorageType
	Replicas    int
}

// RetentionPolicy defines message retention
type RetentionPolicy int

const (
	RetentionLimits    RetentionPolicy = 0
	RetentionInterest  RetentionPolicy = 1
	RetentionWorkQueue RetentionPolicy = 2
)

// StorageType defines storage backend
type StorageType int

const (
	StorageFile   StorageType = 0
	StorageMemory StorageType = 1
)

// ConsumerConfig configures a JetStream consumer
type ConsumerConfig struct {
	Name          string
	Durable       string
	Description   string
	FilterSubject string
	DeliverPolicy DeliverPolicy
	AckPolicy     AckPolicy
	AckWait       time.Duration
	MaxDeliver    int
	MaxAckPending int
}

// DeliverPolicy defines message delivery policy
type DeliverPolicy int

const (
	DeliverAll DeliverPolicy = iota
	DeliverNew
)

// AckPolicy defines acknowledgment policy
type AckPolicy int

const (
	AckExplicit AckPolicy = 0
	AckNone     AckPolicy = 1
	AckAll      AckPolicy = 2
)

// PubAck represents a publish acknowledgment
type PubAck struct {
	Stream   string
	Sequence uint64
	Domain   string
}

// StreamMessage represents a message from a stream
type StreamMessage struct {
	Subject  string
	Data     []byte
	Headers  map[string][]string
	Metadata *MessageMetadata
	msg      jetstream.Msg
}

// MessageMetadata contains message metadata
type MessageMetadata struct {
	Sequence   uint64
	Timestamp  time.Time
	NumPending uint64
}
