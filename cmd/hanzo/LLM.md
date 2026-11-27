# LLM.md — cmd/hanzo

Guidance for cmd/hanzo. Moved from the root LLM.md; cross-cutting rules stay in the root.

## The `hanzo` name is TWO binaries — the Rust CLI, and cmd/hanzo's control half

The `hanzo` fabric CLI is the RUST binary at `~/work/hanzo/cli`. Its control-plane
verbs speak the routes THIS process serves, authorized off a plain `hanzo auth login`
(the IAM access token is the final bearer fallback — no `--platform-token`).

This module also builds `cmd/hanzo` (restored in 59c6dbd5 after the 22f4fc64
deletion): a CLIENT-ONLY control binary that links `cli` and nothing else. It runs
the verbs `cli.newRootCmd` registers and hands EVERY other verb to the Rust CLI,
resolved as `hanzo-node` (or `HANZO_FABRIC_CLI`), so the one `hanzo` name is a
superset of both. It cannot mount a subsystem — serving is `cmd/cloud`'s job.

### `hanzo --version` — a binary that describes ITSELF, and a delegate that warns

Both halves are wired now, and the runtime half does not depend on the build half:

- `resolveVersion()` (cmd/hanzo/main.go) is the ladder, most authoritative first —
  the stamped tag; `info.Main.Version` from `debug.ReadBuildInfo` (Go ≥1.24 derives
  a pseudo-version from the checkout by itself, so a bare `go build ./cmd/hanzo`
  already knows its commit); a `0.0.0-dev+<12-char-sha>[-dirty]` synthesised from
  the `vcs.*` settings (the rung that carries Go ≤1.23, where `Main.Version` is
  `(devel)`); then, and only then, `dev`. It resolves ONCE and assigns
  `cli.Version` — cli never re-derives it. `readBuildInfo` is a var so every rung
  is reachable from a test, because which rung fires depends on the toolchain.
- `make hanzo` builds it and appends `-X main.version=$(VERSION)` to LDFLAGS
  (`VERSION ?= git describe --tags --always --dirty`). Appended, not folded into
  the `LDFLAGS ?= -s -w` default, so `make LDFLAGS=...` overrides what it always
  did and still cannot produce an unstamped binary. `make cloud` stamps
  `github.com/hanzoai/cloud.Version` the same way, matching what the Dockerfile
  already passed — plugin builds have no version symbol and are untouched.

The version command's OUTPUT CONTRACT is a stream split, and both sides matter:

- **the delegate is a stderr WARNING, raised only when actionable**: version
  differs (naming both paths and both versions, and that verbs handed to
  `hanzo-node` run THAT build), or present-but-unreadable. Absent or in agreement
  prints NOTHING. Ours is compared with its leading `v` trimmed, the same
  normalisation `delegateVersion` applies to the delegate's, so `v1.2.3` against
  `1.2.3` is agreement and not a false alarm.

Do not point `HANZO_FABRIC_CLI` at this binary itself. `fabricCLI` self-guards the
PATH rung (`os.Executable`), but the explicit override is taken as given, and a
binary delegating to itself recurses until `delegateVersion`'s 3s timeout ends it —
which then reports the delegate as unreadable.

Four names come from cobra rather than from our `AddCommand`. `help` and `completion`
are added in `newRootCmd` (`InitDefaultHelpCmd` / `InitDefaultCompletionCmd`, both
idempotent) because otherwise Execute adds them too late for the router to see them.
`__complete` / `__completeNoDesc` are registered inside ExecuteC with no public hook,
so IsControlVerb claims them by cobra's own exported constants — they are what the
emitted completion scripts invoke, so delegating them kills TAB completion even when
`hanzo completion bash` prints a perfect script.

The CLI does not import this module and never will: its cloud surface is GENERATED
from a spec. `genspec` joins the authored master (`hanzoai/openapi` `hanzo.yaml`)
with a live route table into `spec/cloud.json`, and `genproduct` emits
`src/commands/product/generated.rs` from that. The registry can only REFUTE an
authored operation, never add one — so a route this module serves reaches no command
until `hanzoai/openapi` authors it. When a verb is missing from the CLI, author the
route there; do not hand-write the command.

- `hanzo platform fleet list|get` → `GET /v1/platform/fleet[/{app}]` (`apps/platform`
  fleet.go drift board). `--env`/`--health`/`--drift` filter it.
- `hanzo platform fleet deploy <app>` → `POST /v1/platform/fleet/{app}/deploy` — a
  zero-downtime ROLLING RESTART (stamps the Deployment pod-template
  `hanzo.ai/restartedAt` annotation; never changes the declared TAG — that stays a
  git commit CD reconciles). `--env` picks the ns.
- `hanzo cluster list|show` → `apps/visor`, tenant-scoped.

`cli/` (Go, package `cli`, ~10.4k lines) is NOT built and NOT importable by anything
here — zero importers, no `main`, no Makefile target. It is retained ONLY as the
reference for the client-side tools not yet ported to Rust: the GPU worker daemon
(`gpu.go`/`studio.go` — hardware enumeration, the claim/heartbeat loop, ComfyUI
supervision, systemd install), `agent publish`'s local half, and `engine install`.
Rust's `node join` is a one-shot registration, not that daemon. Do not add to `cli/`,
do not wire it into a build, and do not delete it until those are ported — deleting
it destroys the only spec for work that is owed.

`POST /v1/platform/runner` (native buildkit fabric) has no CLI verb today: it is served here
but unauthored in `hanzoai/openapi`, and the bare name `runner` is already taken by
the Rust CLI's CI-runner daemon. Authoring it needs a name decision first. Called
directly, with `image:` it builds a container image; with NO `image:` it reads the
repo's own contract (`binaries:` + `bucket:`, in whichever spelling — see
`contract/`) and builds the ARTIFACT lane instead — see below.
