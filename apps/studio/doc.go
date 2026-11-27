// Package studio is Hanzo Studio's workflow backend: every creation — an image, a
// poster, a document, a deck, a type system, a 3D model, an ad, a film, a voice, a
// song — is a run of a graph of steps that the person who started it owns.
//
// A RUN IS A DURABLE WORKFLOW. Its graph is scheduled by flow/core in Durable mode
// on this process's tasks engine (durable.go): each step is activities, recorded
// before the next begins, so a process or a worker that dies mid-run resumes at the
// step it was on and never repeats one that finished. A step runs on one of four
// executors (exec.go): the model gateway, the org's own GPUs (studio.render), the
// platform's CPU render workers (render), or — the seam for templates moving to it —
// native flow-core on hanzo-engine (flow.run).
//
// A RUN IS PAID FOR UP FRONT (money.go): its estimate is reserved against the org's
// wallet when it is created (402 when it cannot be), each finished step is debited
// once, and the run settles to what its steps cost.
//
// A RUN IS ITS PERSON'S. The org is the validated principal's and the creator the
// person the identity boundary vouched for (principal.SelfFrom); both are stamped
// by the server and nothing in a request names either. Within the org a run is its
// creator's until they share it; outside the org it does not exist (404, the same
// as absent). The store enforces the org twice — the queries bind it and
// PostgreSQL's row-level security refuses any row of another (store.go).
//
// ITS FILES ARE LIBRARY ITEMS (library.go): written and read through the one
// Library as the run's person, never in a store of Studio's own. Its state is on
// the jobs board as it changes (track), so a render shows in Scheduled beside every
// other product's background work.
//
// Surface — /v1/studio: templates, flows, documents, brands, runs (with events,
// cancel, retry and versions), estimate, gpus, chat and threads; and the render
// lane's door, /v1/studio/render, for the render workers alone.
package studio
