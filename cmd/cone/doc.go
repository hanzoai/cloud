// Command cone names what a change reaches: the packages a diff touches, and the
// binaries whose import closure holds one of them.
//
// The dev lane (.hanzo/workflows/dev.yml) builds the dev branch's image ON a
// published release instead of from nothing. Every binary in that release was
// built -trimpath from the same inputs, so a binary whose closure holds no
// changed package is byte for byte the one the release already ships, and only
// the binaries in the change's cone are compiled. That is what takes a dev image
// from the release train's half hour to minutes. The cone has to be complete
// rather than tight: a binary it misses runs the release's code under the dev
// build's name, so every rule below errs toward rebuilding.
//
// Two subcommands, because the two halves run in two places:
//
//	cone dirs <base>       on the runner, git only, no module graph
//	cone bins <dir>…       in the build, after `go mod download`, roots on stdin
//
// `dirs` prints the package directories the diff from <base> to HEAD touches,
// one per line, and `mod:<path>` for each module go.mod moved (a required
// version or a replacement), and `app:<name>` for each app manifest/apps.go now
// spawns that the base did not (it has no binary there to keep); or the single
// word `all` when a toolchain input moved (the go directive or toolchain line,
// the Dockerfile, .dockerignore): then nothing built on the base can be trusted
// and the lane builds the whole image. go.sum moves only with go.mod, and alone
// it selects no different source. A .go file,
// or a source file the go command compiles beside one (cgo's .c, .h, .s and the
// rest), belongs to its own directory's package. Any file also belongs to every
// package at or above it that carries a //go:embed directive, since an embed
// pattern may reach into subdirectories; that is the only way a data file is in
// a binary. A directory Go never makes a package of
// (testdata, dot and underscore names) holds no package's files, only data an
// enclosing package can embed, and testdata is also read by the tests of the
// package that holds it. A file under no package touches nothing.
//
// `bins` reads build roots (`./plugin/commerce`) from stdin and prints those
// whose import closure, as `go list` resolves it under the caller's environment
// (CGO_ENABLED, GOFLAGS, -tags given as CONE_TAGS), holds a package in one of
// the given directories or of one of the given `mod:` modules; `app:` entries
// name binaries outright and are the caller's to build. Run it once per build configuration, so a constraint
// that holds only with or without cgo is read the way that binary is built.
//
// Both print nothing for nothing, so an empty change rebuilds only the host,
// which carries the version and is always rebuilt.
package main
