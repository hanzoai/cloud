// Command gitimport copies the repositories of the forge (git.hanzo.ai) into the
// objects store apps/git reads: S3 for pack bytes, hanzo-sql (CLOUD_SQL_DSN) for the
// index, the refs and the importer's own registry. It reads the forge over the git
// protocol and writes nothing to it; no traffic is switched.
//
//	FORGE_TOKEN=... gitimport -forge https://git.hanzo.ai -list -workers 4
//	gitimport -forge https://git.hanzo.ai -repos hanzoai/docs,hanzoai/mcp
//	gitimport -status
//
// The pack store is the S3_ADMIN_* object store, bucket -bucket (default hanzo-git).
// Run it with CLOUD_SQL_DSN and CLOUD_ENV set to import into hanzo-sql; without a DSN
// the registry and the stores are files under -dir. It can be run again at any time: a
// repository that is done is compared with the forge's refs and fetched again only if
// they moved, and a run that died is picked up where the registry says it stopped. Only
// one runner works at a time (a lease in the registry).
package main
