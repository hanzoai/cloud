# LLM.md — mk

Guidance for mk. Moved from the root LLM.md; cross-cutting rules stay in the root.

### The fleet is a set of TARGETS, and the numbers that made it one

| what | how | cold |
|---|---|---|
| one plugin, alone | `make -C apps/auto build` | 57.6s at `-p=2`, 41.4s at `-p=$(nproc)` |
| all 121, the old shell loop | `for d in apps/*/Makefile; …` | **586s** |
| all 121, scheduled | `make -f mk/fleet.mk binaries` | **300s** (25s warm) |

The FLOOR is why the fleet is so much cheaper than 121 × one: the root package
`github.com/hanzoai/cloud` is 587 packages (214 stdlib, 373 external) and EVERY
app inherits it — `apps/auto` is 588 packages, `o11y` the largest at 2054. So the
57.6s cold floor is paid ONCE into a shared cache and the marginal app costs
~1.4s. **That makes cache SHARING, not floor size, the thing that matters**: 121
apps built in 121 isolated caches would pay that floor 121 times, ~118 minutes of
identical work. No single import dominates the floor either — the largest
exclusive contributors are `hanzo-ds/go` (30 packages, for `datastore.Open` in
audit_mirror.go) and `iam/pkg/model` (23, for the two-string `model.OrgRef` in
token_validator.go); everything else shares a deep common core (`circl`'s 23 PQ
packages arrive via `luxfi/zap`'s handshake, protobuf via prometheus).

- **J apps at once, P compilers each, and `J*P ≈ NPROC`.** The link is what costs
  memory: measured peak RSS is 1.67 GB for the heaviest plugin (o11y), 1.4 GB
  median, 273 MB for `cmd/cloud`. J is therefore bounded by MEMORY (3 GiB per
  concurrent app) read from the **cgroup** before `/proc/meminfo`, because the
  git-runner pod is 26Gi on 6 CPU while `nproc` inside it reports the node's
  cores. Oversubscribing CPU makes a build slower; oversubscribing memory makes it
  killed. `J*P` is capped at NPROC because it measurably matters —
  `J=20,P=2` (40 actions on 20 cores) took **431s**, the same work at `J=10,P=2`
  took **300s**.
- **A solo build is J=1, so it gets the whole box**: `mk/go.mk` sets
  `-p=$(NPROC)`. The runner injects `GOFLAGS=-p=2` into every job and `?=`
  deliberately does not override it — the operator sizing the pod knows what it
  holds.
- **`make -k`, not `set -e`.** The old loop stopped at the first failure and the
  apps behind it never ran, which reports nothing, and nothing is
  indistinguishable from passing. make continues and names each failed target.
- **The exemptions are exemptions from DESCRIBING, never from BUILDING.** kafka
  (needs a live broker) and zen (coresident, no standalone mount) were skipped by
  the only sweep that touched an app, so nothing ever compiled them — they could
  stop linking on main with every gate green. `binaries` carries no exemptions;
  `describe` builds those two and skips only the projection.
- `binaries` → `bin/<app>` (121). `dist` → `dist/<app>-<os>-<arch>` for every
  platform, the publishable layout (4.1 GiB per platform).
