// Package pubsub is the NATS driver for sdk/coord's bus: it carries what pods say to
// each other on the deployment's shared hanzoai/pubsub cluster. Linking it registers
// it; CLOUD_PUBSUB_URL then names the cluster.
package pubsub
