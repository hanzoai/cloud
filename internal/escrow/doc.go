// Package escrow keeps a record's secrets in Hanzo KMS, and the record keeps only where.
//
// A store holds a credential nowhere: not in the clear, not concealed, not sealed. The
// value is written to KMS under a path made from the org, the app, the record and the
// field, and the column holds that path. A read asks KMS for it; a delete asks KMS to
// forget it. Sealed columns (sdk/tenure) are for personal data that is not a secret,
// and for the blind index that finds a row by a value it was presented.
//
// The path is
//
//	orgs/<org>/<app>/<record>/<field>@default
//
// in the record's own org: the org is folded by kms.OrgPath, as for every other app's
// secrets, and the store serves a tenant's path only to a call acting for that tenant, so
// every call here states the record's org ([plane.For]). The environment slug is the
// one apps/provider and apps/destination already seal under.
//
// What a column holds is "kms:" and the path. A value that does not begin so is not a
// reference: it is what a file held before the value moved to KMS, and the store's open
// moves it ([Held] tells the two apart).
package escrow
