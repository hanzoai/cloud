// Package crm is the sales and hiring model, kept in each org's own Base.
//
// A person, a company, an opportunity, a candidate, an application and the
// activity timeline are collections of the org's Base (hanzoai/base module crm,
// HIP-1120 as amended); CRUD, rules, history, aggregates and realtime are Base's
// own routes (/v1/base/collections/…). This package is the one writer a cloud app
// has into them (base.go, over the plane) and the vocabulary of the public forms:
// their sizes, programs and campaign tags, the references they hand back, and a
// sales inquiry's intro-call link.
//
// Every public form is an intake, filed by apps/marketing's POST
// /v1/marketing/leads. Its kind decides the records: a sales inquiry, an
// application to a program (beta, research, partner) and an investor's
// subscription (investor) are a person and the form on their timeline, and an
// application to a job opening is a candidate and its form, kept apart from the
// people sales works.
package crm
