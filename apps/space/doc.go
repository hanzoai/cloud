// Package space is where work lives: drives, folders and the files in them.
//
// A SPACE is a namespace owned by an org. It answers "where", while IAM answers
// "who" — org, team, user, agent. Those are separate axes, which is the whole
// reason a space earns its own name: a team works across spaces and a space
// hosts several teams. Billing hangs off the ORG, so a space never fragments an
// invoice; HR and Finance can each have one and still roll up to a single lens.
//
// A DRIVE is a named place inside a space. A FOLDER is emergent from "/" in a
// file's name — there is no folders table, because a second naming system beside
// the keys drifts from them the first time somebody renames one.
//
// # A drive is a prefix, not a bucket
//
// ListBuckets is an unpaginated GET over the WHOLE deployment; apps/s3 filters
// it client-side and says so: the listing is not paged "because an org's bucket
// count is small by construction". One bucket per drive makes that count
// orgs x spaces x drives, and then every tenant's drive list pays for every
// other tenant's drives. One bucket per (org, space) keeps the assumption true,
// and it is the shape every high-cardinality store here already uses —
// team-blobs, org-db, hanzo-sites, recordings.
//
// It also collapses two mechanisms into one: listing the drives IS listing the
// root folder, because a drive is just the first key segment.
//
// # Bytes never pass through this process
//
// Upload and download mint a presigned URL against the public endpoint and the
// caller talks to the store directly. That is not a preference, it is how
// apps/s3 already works, and it keeps file content and store credentials off the
// API entirely.
package space
