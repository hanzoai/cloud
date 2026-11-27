# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package principal

struct Attestation {
    Agent     bytes       @0
    Org       text        @8
    Parents   list<bytes> @16
    Statement text        @24
    Kid       text        @32
    Expires   i64         @40
}

struct Clearance {
    ID        text       @0
    Payer     text       @8
    Payee     text       @16
    Amount    text       @24
    Category  text       @32
    Rail      text       @40
    Performed text       @48
    Platform  list<bool> @56
    Decision  bytes      @64
    DecidedAt i64        @72
    Notice    text       @80
}

struct JWKS {
    Keys list<bytes> @0
}

struct Match {
    ID        text        @0
    Org       text        @8
    List      text        @16
    Ref       text        @24
    Entry     text        @32
    Hits      list<bytes> @40
    Score     f64         @48
    Reason    text        @56
    Status    text        @64
    DecidedBy text        @72
    DecidedAt i64         @80
    Note      text        @88
    FoundAt   i64         @96
}

struct Statement {
    ID      text @0
    Agent   text @8
    Org     text @16
    Kind    text @24
    Form    text @32
    Issued  i64  @40
    Expires i64  @48
    Revoked i64  @56
    Status  text @64
    JWS     text @72
}

struct View {
    Org        text        @0
    Identity   bytes       @8
    Entity     bytes       @16
    Tax        bytes       @24
    Sanctions  bytes       @32
    Wallets    list<bytes> @40
    Agents     list<bytes> @48
    Compliance bytes       @56
    Sources    list<bytes> @64
}

struct agentRef {
    Ref text @0
}

struct clearIn {
    Payee     text       @0
    Amount    text       @8
    Category  text       @16
    Rail      text       @24
    Performed text       @32
    Platform  list<bool> @40
}

struct clearanceList {
    Clearances list<bytes> @0
}

struct clearanceRef {
    ID text @0
}

struct clearancesIn {
    Payee text @0
}

struct decideIn {
    ID       text @0
    Decision text @8
    Note     text @16
}

struct matchList {
    Matches list<bytes> @0
}

struct matchesIn {
    Status text @0
}

struct statementRef {
    ID text @0
}

interface principal {
    # Returns the caller's org as an economic principal, composed from the apps that
    # own each fact: its legal entity and founders' identity verification (company),
    # the W-9 or W-8 it certified (tax), its wallets (wallet), the agents that answer
    # to it — spawned ones with the agent that spawned them — (agents), its screening
    # against the OFAC, UN, EU and UK sanctions lists and the embargoed jurisdictions,
    # run now, and what it still lacks to pay and be paid. The read changes nothing:
    # a possible match it finds is recorded for a platform reviewer when a payment to
    # or from the org is cleared.
    # Any member reads it. The detail behind a verdict — founders' names and
    # statuses, masked tax numbers, treaty claims, sanctions matches — is answered to
    # the org's admins only. An owning app this deployment does not run is named in
    # sources as absent rather than read as empty.
    get_principal() returns (rep: View)
    # Attests which principal one of the caller's agents answers to: the org, the
    # chain of agents that spawned it, and the same signed with the platform's key as
    # a JWS (EdDSA) a verifier checks offline against GET /v1/principal/keys. Any
    # member of the agent's org.
    # An agent of another org is not found. A spawned agent answers to its parent's
    # org, which is its own: a parent is resolved only in the org the agent is created
    # in. The statement is valid for 24 hours.
    get_principal_agents_by_ref(req: agentRef) returns (rep: Attestation)
    # Lists the caller org's clearances, newest first, 200 at most; a payee narrows
    # them. Any member of the paying org.
    get_principal_clearance(req: clearancesIn) returns (rep: clearanceList)
    # Reads one of the caller org's clearances, as it was decided. Any member of the
    # paying org; another org's clearance is not found.
    get_principal_clearance_by_id(req: clearanceRef) returns (rep: Clearance)
    # Publishes the keys attestations are signed with, as a JSON Web Key Set (RFC
    # 7517; Ed25519 keys as RFC 8037 writes them). Open to anyone: a verifier
    # verifying an agent's statement fetches this once and verifies offline.
    # A key a rotation retired stays here and signs nothing, so a statement signed
    # before the rotation verifies until it expires.
    get_principal_keys() returns (rep: JWKS)
    # Lists sanctions matches across every org, oldest first — the platform's review
    # queue. SuperAdmin only: a match names an org the platform screened, and the
    # org it names is not the one to decide it.
    # A match is one org against one designation or embargoed jurisdiction, with
    # every name, address or country of the org that brought them close. Open
    # matches are the ones no payment to or from their org clears past until a
    # reviewer decides them; a match is recorded when a payment to or from its org is
    # cleared.
    get_principal_matches(req: matchesIn) returns (rep: matchList)
    # Reads whether one statement the platform signed about an agent still stands:
    # live, expired, or revoked and when. Open to anyone — a verifier holding a
    # TaxPrincipalCredential checks it here after checking its signature offline
    # against GET /v1/principal/keys. The answer names nothing the statement itself
    # does not.
    get_principal_statements_by_id(req: statementRef) returns (rep: Statement)
    # Decides whether the caller's org may pay another org, BEFORE the payment: what
    # must happen first, what must be withheld and why, what must be reported, by
    # whom and when, and how the payment may settle net of withholding. Any member of
    # the paying org; the decision is recorded and on the audit trail in one
    # transaction.
    # The rules, each cited on the answer: payer's and payee's founders identity-verified
    # (Hanzo policy); payer and payee screened clear of the OFAC, UN, EU and UK lists
    # and the embargoed jurisdictions — a confirmed match blocks, a possible one waits
    # on a Hanzo platform reviewer, and lists that are not loaded and fresh clear
    # nobody; the payee's status is what the form the payee granted the payer says,
    # and until it grants one the payee furnishes it; a U.S. payee with no TIN on
    # file owes its W-9, and paid without one 24% backup withholding applies (IRC
    # §3406); a foreign payee on a valid W-8BEN or W-8BEN-E is withheld on at 30% on
    # U.S.-source income (IRC §1441, §1442), reduced by a treaty rate the form
    # claims; a service performed outside the United States is foreign-source and
    # not withheld on; Form 1099 by the payer for a reportable U.S. payee at the
    # year's threshold, Forms 1042-S and 1042 for U.S.-source income to a foreign
    # person, and platform-operator reports (EU DAC7, UK, Canada, Australia) where
    # the payer is the payee's platform. Every filer named is an org, never Hanzo.
    # Nothing of the payee is answered but what the payer must know to act: that the
    # payment clears, what the payee must furnish, or that it cannot clear and the
    # payer should contact support — never why, and never the payee's status before
    # the payee grants its form. A payer asks about at most 50 new payees a day and
    # 120 payments an hour.
    post_principal_clearance(req: clearIn) returns (rep: Clearance)
    # Rotates the attestation signing key: a new Ed25519 key is generated, sealed in
    # KMS beside the others, and signs every statement from now on; every earlier key
    # is retired — still published, its private half destroyed. SuperAdmin only, on
    # the audit trail before the new set is written, and followed on the trail by a
    # correction if the write does not take. One rotation runs at a time, and one
    # that finds the set rotated under it since it read it is refused (409). The
    # first call provisions the deployment's first key.
    post_principal_keys() returns (rep: JWKS)
    # Decides one sanctions match, as a Hanzo platform reviewer. SuperAdmin only —
    # an admin of the org the match names is refused, because that would be the org
    # deciding its own case — and the decision is written and put on the audit trail
    # in one transaction: a decision the trail does not take is not made.
    # cleared records a namesake: the org's names resemble a designated person's and
    # the other identifiers show a different one. It covers the hits the reviewer
    # read, and a later name close to the same person reopens it. confirmed records
    # the designated person itself, and blocks every payment to or from the org
    # whatever it is later called. A decision may be changed only by another
    # decision, with its own note.
    post_principal_matches_by_id(req: decideIn) returns (rep: Match)
}

# ---------------------------------------------------------------------
# 10 op(s) here. What follows is what this schema does not carry.
#
# opaque (15) — crosses, arrives without its name:
#   Attestation.Agent  iam.AgentBrief
#   Attestation.Parents  iam.AgentBrief (list element)
#   Clearance.Decision  principal.Decision
#   JWKS.Keys  iam.JWK (list element)
#   Match.Hits  iam.Hit (list element)
#   View.Agents  iam.AgentBrief (list element)
#   View.Compliance  iam.Summary
#   View.Entity  iam.Entity
#   View.Identity  iam.Identity
#   View.Sanctions  iam.Screening
#   View.Sources  iam.Source (list element)
#   View.Tax  iam.Form
#   View.Wallets  iam.WalletBrief (list element)
#   clearanceList.Clearances  principal.Clearance (list element)
#   matchList.Matches  iam.Match (list element)
