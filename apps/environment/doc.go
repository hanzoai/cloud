// Package environment is what a codebase is before its agent starts: the
// commands that install it, the command that starts it, and the secrets it reads.
//
//	GET    /v1/environment                      every codebase's environment in the org
//	GET    /v1/environment/:repo                one; a codebase with none answers `none`
//	PUT    /v1/environment/:repo                save the install and start scripts
//	DELETE /v1/environment/:repo                forget it, and its secrets
//	PUT    /v1/environment/:repo/secrets/:name  set one secret's value
//	DELETE /v1/environment/:repo/secrets/:name  remove one
//
// A member reads; an org admin writes. That is the estate's one rule
// (cloud.Admin), and it is the right one here twice over: a script saved on a
// codebase runs at the start of every later run on it, and a secret set on it
// reaches every one of those runs' agents.
//
// A CODING RUN is the reader that matters, and it asks over the plane
// (plane.go): a sandbox run on a codebase is told its scripts and where each
// secret lives, and applies them after the clone and before its harness
// (apps/coding sandboxrunner.go). A run on a claimed machine applies none of it —
// that machine is its own environment, and the org's secrets do not travel to it.
//
// A SETUP run writes the proposal (apps/coding mode.go). Its agent explores the
// checkout, installs and checks what it finds, and ends its answer with the
// environment it would save. Nothing proposed runs until someone saves it: a
// proposal is what the agent found, and the saved scripts are what every later
// run executes.
//
// SECRETS ARE NAMES HERE AND VALUES IN KMS, at
// orgs/<org>/environment/<repo>/<NAME>. This store lists which names are set; the
// value is sealed by KMS, read back only by a call acting for this org, and never
// answered by any route below.
package environment
