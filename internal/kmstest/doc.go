// Package kmstest is a KMS in memory, for the tests of a store that keeps its secrets
// there. It answers what the real store answers for a tenant's path: a call acting for
// the org the path names, and no other.
package kmstest
