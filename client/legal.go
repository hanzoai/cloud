package client

// legal.go is the legal capability's half of the contract: a peer that needs a
// document SIGNED asks the process that owns documents and signatures, and never
// renders or records one of its own.
//
// Two ops, a write and the read that confirms it — one conversion, so a caller
// that opens a signature can always learn how it ended. The tenant rides the
// caller on both: a document is generated in, and read from, the caller's own
// org, so there is no field here that could name another.

const (
	// LegalSign renders a built-in template for the caller's org and opens a
	// signature request over it.
	LegalSign = "legal_sign"
	// LegalDocument reads where one of the caller's documents stands.
	LegalDocument = "legal_document"
)

// LegalField is one merge field: a template key and the value rendered into it.
// A list and not a map, because a map does not cross the plane.
type LegalField struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// LegalSigner is one person who must sign.
type LegalSigner struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// LegalSignIn asks for one document to be generated and sent for signature.
//
// Template names a BUILT-IN template only. An org may override a template for
// its own documents, and that is right for an NDA; it is wrong for a document
// another capability relies on, whose text is fixed by someone other than the
// org — a W-9 certification is the IRS's words, not the payee's.
type LegalSignIn struct {
	Template string        `json:"template" validate:"required"`
	Fields   []LegalField  `json:"fields"`
	Signers  []LegalSigner `json:"signers"`
}

// LegalDocumentIn names one of the caller's documents.
type LegalDocumentIn struct {
	ID string `json:"id" validate:"required"`
}

// LegalSigning is where a document stands.
type LegalSigning struct {
	// ID is the document's id — the one /v1/legal/documents/{id} answers.
	ID string `json:"id"`
	// Template is the built-in template it was rendered from.
	Template string `json:"template"`
	// Status is draft, out_for_signature, signed or voided.
	Status string `json:"status"`
	// Provider names the e-signature provider holding the request.
	Provider string `json:"provider"`
	// Signed is when the provider reported completion, unix seconds; zero until then.
	Signed int64 `json:"signed"`
	// SignedBy is the validated IAM user who recorded the completion; empty until
	// then. A caller that named its signer compares the two.
	SignedBy string `json:"signedBy"`
	// Digest is the SHA-256 of the rendered body, hex — what was signed.
	Digest string `json:"digest"`
}
