// Package cluster answers what a Kubernetes namespace MEANS: which tenant owns
// it, and which environment it runs.
//
// It is ONE function, and it lives apart from the observer that scans the
// cluster so the boards that only RENDER the answer can ask it without linking a
// Kubernetes client. Every reader shares this decision: platform confines a
// non-super OrgAdmin to the namespaces whose tenant equals their validated org,
// and admin groups a workload as first-party infra or a customer deployment on
// the same fact. A board that re-derived it once classified every hanzo-mainnet
// workload as a customer's, which looks exactly like a correct answer.
//
// Class is TOTAL: every input yields a decision, and an unrecognised namespace is
// classified OUT (ok=false) rather than guessed at, so a reader can never reach
// beyond the tenants named here.
package cluster
