// Package principals serves an org as an economic principal: who it is, what it
// may pay,
// and which agents answer to it.
//
// The IAM org IS the principal — there is no second registry. What the org is as
// a payer or payee is held by the apps that own each fact, and this package
// composes them and keeps nothing of theirs: company holds the legal entity and
// its founders' identity checks, tax holds the W-9 or W-8 the org certified,
// wallet holds its addresses, agents holds the agents that act for it. Each is
// asked over the internal plane, for the caller's own org.
//
// It serves /v1/principal, and owns three things of its own:
//
//   - SCREENING. Every name an org is known by — the entity, its founders, the
//     name on its tax form — and every address it holds is screened against the
//     published OFAC, UN, EU and UK lists, in this process, with
//     github.com/luxfi/aml, and every country its form ties it to against the
//     comprehensively embargoed jurisdictions. No principal data leaves the
//     platform to be screened. A match is the org against one designation, so a
//     decision on it is about the org whatever the org is later called. A
//     possible match is decided by a Hanzo platform reviewer (SuperAdmin), never
//     by the org it names; a confirmed one blocks every payment to or from it.
//   - CLEARANCE. Before a payment, whether org A may pay org B an amount for a
//     category of service over a rail: what must happen first, what must be
//     withheld, what must be reported and by whom, and how it may settle. Every
//     field names the rule that decided it, and every clearance is a record on
//     the audit trail.
//   - ATTESTATION. Which principal an agent answers to, signed with the
//     platform's key so anyone can verify it offline — and, while the org's tax
//     form is certified and valid, a TaxPrincipalCredential for each of its
//     agents that says so (statements.go).
//
// WHAT IT DOES NOT DO is part of the product. Hanzo files nothing with any tax
// authority: every obligation a clearance names is filed by an org — the payer,
// almost always — never by Hanzo, and the forms it names are the ones tax
// prepares for that org to file. A clearance is a statement of the rules as this
// platform applies them to the facts it holds, with the facts it does not hold
// returned as questions; it is not legal or tax advice, and the determination
// belongs to each org and its counsel.
//
// TENANCY. Every op acts for the caller's own org, from the validated IAM claim.
// Another org is read only as the platform's own act — to screen it and to
// learn whether it is verified — and nothing of it is answered to the caller but
// what the caller must act on: the payment clears, the payee must furnish
// something, or it cannot clear and the caller should contact support. Its
// status as a U.S. or foreign person is the form it grants, and nothing before.
// Members read; org admins see the detail behind a verdict; SuperAdmin alone
// decides a sanctions match and rotates the signing key. Every decision is on the
// audit trail in the transaction that writes it, and a read writes nothing.
package principals
