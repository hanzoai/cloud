// Package contract reads the one document a repository uses to declare what it
// builds, deploys and publishes.
//
// A repository writes that document in one of several spellings. hanzo.yml,
// hanzo.yaml and hanzo.json are three ways to write the SAME document — YAML 1.2
// is a superset of JSON, so one parser reads all three, against one schema.
// hanzo.config.js, hanzo.config.ts and .hanzo/contract.go are generators: they are
// run once, at the edge, and what they print is the document (eval.go says why).
//
// So reading a contract is always: resolve → evaluate if the spelling is code →
// canonical document → digest. Load is the reading half and is what every
// downstream reader gets; Eval is the only thing in the estate that runs a
// contract.
package contract
