// Package bus is how a process TALKS to the message bus. It does not run one.
//
// This surface lived in apps/pubsub beside the embedded NATS server, so the six
// apps that wanted only the bus ADDRESS had to import the app that embeds a
// broker in order to get it. Measured, that single edge put
// github.com/hanzoai/pubsub/server — a whole NATS server and everything it links
// — into 27 plugin binaries, and boot time across this fleet tracks binary size
// almost linearly. apps/event imported it for exactly two values: URL() and
// TenantPrefix.
//
// So the bus is two things and they are separated. apps/pubsub RUNS one and is
// linked by plugin/pubsub alone; this package ADDRESSES one and is linked by
// everybody. Nothing here can start a server, which is the property that keeps
// the separation true as callers are added.
package bus
