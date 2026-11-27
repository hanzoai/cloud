// Package tenant mints the key every per-org read and write is bound by, and
// is the only way to obtain one.
//
// THE KEY IS `<brand>/<org>`, NOT `<org>`. An org name is unique within an
// issuer and not across issuers: `acme` on hanzo.id and `acme` on zoo.ngo are
// two unrelated businesses. Keyed on the bare org they become one — one set of
// rows, one dataset, one model, and one derived-key salt — so one brand's
// subjects would silently collide with the other's. The engine states the same
// shape at luxfi/aml pkg/api/tenant.go and every OrgID it carries is qualified;
// a plane that minted a bare org would be handing the engine a key it documents
// as invalid.
//
// The brand half NEVER comes from a header or a body. It is the deployment's
// own brand, for the same reason the org half is taken from the validated
// principal and not from a field: a caller that can choose its brand has chosen
// which tenant space its org lands in, which is the collision qualification
// exists to prevent, arrived at from the other side.
//
// THE BRAND HALF IS CANONICAL AND REGISTERED, and both halves of that are
// load-bearing. `hanzo` and `Hanzo` are one brand and must be one key, or a
// deployment started with CLOUD_BRAND=Hanzo reads a tenant nobody ever wrote.
// And a brand no registry vouches for mints nothing at all: the planes that
// WRITE the surfaces read here (apps/risk's rollup) already refuse an
// unregistered brand, so a mint that accepted one would key rows under a tenant
// the writer can never produce — a reader and a writer disagreeing about whose a
// row is, which is the same defect as a cross-tenant read with the sign flipped.
// [brand.Registered] is the one registry both sides ask.
//
// WHY [Key] IS A STRUCT AND NOT A STRING. Its single field is unexported, so:
// no other package can write a Key literal, encoding/json cannot decode one (it
// has no exported field to decode into), and therefore no request body, query
// parameter or path segment can ever carry one. The only non-zero Key in the
// process came out of [Mint], and [Of] is the only caller of Mint that a request
// path can reach. "This read is tenant-scoped" is a fact the compiler checks
// rather than a convention a reviewer checks.
//
// It is its own package rather than a file inside one subsystem because the key
// is not any one subsystem's: the dataset plane, the scoring plane and the
// compliance plane must agree on it BYTE FOR BYTE or a dataset is keyed
// differently from the model fitted on it. One definition, imported.
package tenant
