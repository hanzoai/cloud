// Package datastore mirrors the audit trail into a datastore MergeTree table
// for fleet-wide, long-retention query. It is a best-effort projection: the local
// hash chain stays the integrity authority, and with no datastore configured
// there is no mirror.
package datastore
