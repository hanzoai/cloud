// Package forgeimport copies the repositories of the forge (git.hanzo.ai) into the
// objects store apps/git reads (apps/git/objects): each repository is fetched over the
// git protocol, as one pack, and every ref is copied. It reads the forge and writes the
// store; it never touches the forge's own copy and switches no traffic.
package forgeimport
