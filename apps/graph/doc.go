// Package graph is one organization's entities and the relations between them,
// held as assertions: somebody, at some moment, from some evidence, asserted
// that this thing stands in that relation to that other thing.
//
// Nodes and edges are how it reads on the wire; an assertion is what is stored.
// A node is NOT a row — an entity exists because something was asserted about it
// — so there is no node table, no create and no cascade delete. An edge is an
// assertion whose value names another entity; a property is an assertion whose
// value is a scalar. One table, one bit (`names`) telling them apart.
//
// WHY THIS AND NOT A MUTABLE NODE/EDGE STORE. An upsert-by-id graph is easier to
// write and silently loses the four things this plane exists to provide: who
// said so, when it became true, what disagreed, and what it looked like last
// Tuesday. Provenance is columns, point-in-time is the derived knowable instant,
// conflict is the shared total order, and entity resolution is the `same`
// relation resolved by that same order. None of them is a subsystem.
//
// THE SCHEMA IS ASSERTIONS TOO. An entity's type is (entity, `type`, t); what a
// relation may join is (`relation:<name>`, `domain` | `range`, t). A write whose
// types miss a declaration in force at its instant is refused; an undeclared
// relation is open. So the ontology has provenance and history for free
// (schema.go). It binds what every member may assert, so only an admin of the
// organization may write it, and each write is on the audit trail.
//
// RULES ARE ASSERTIONS TOO. A rule is (`rule:<name>`, `rule`, its Datalog text),
// admitted only while the rules in force stratify, and derive concludes what
// they entail as of an instant, with a proof of each conclusion (derive.go,
// internal/datalog). A rule is a declaration: an admin's to write, on the trail.
//
// A DECISION IS AN ENTITY. By convention it is typed `decision`, carries
// `scenario`, `reasoning`, `outcome` and `confidence` as properties, and
// `decided_by`, `caused_by`, `influenced` and `precedent` as edges. Nothing
// enforces the convention unless the org declares it; nothing stores a decision
// anywhere else. Its validity is the assertions' own time, a reversal is a later
// `outcome`, and a precedent is found by search and followed by neighbors.
//
// THE ALGEBRA IS NOT HERE. The derived instant, the content address, the
// observation window and the order live in `claim`, shared with the ground-truth
// plane (apps/label) so that this binary holds ONE answer to "somebody asserted
// something about a thing" rather than two that drift. This package contributes
// the vocabulary, the bounds and the store — which is all a caller of that
// algebra is meant to contribute.
//
// TENANCY is physical: one SQLite file per (organization, project) through
// cloud.OrgDB, so another organization's assertions are not in the database being
// read and no predicate can be forgotten. See HIP-1198.
//
// That pair IS the graph database, which is why this plane holds no registry of
// graphs. An org names one by carrying a project, its default project is the
// whole-org view, and the files are made on first assertion and reclaimed when
// idle. Nothing here would be gained by a second primitive: a graphs table would
// be a name for a project, kept somewhere else, able to disagree with it.
package graph
