# Fleet-wide targets: the same per-app contract, applied to the whole set.
#
# Included from the repo root (`include mk/fleet.mk`), or run directly as
# `make -f mk/fleet.mk <target>`. It defines no build logic of its own — every
# recipe here delegates to mk/plugin.mk, so "how an app is built" stays in
# exactly one file no matter how many apps a command touches.
#
# THE FLEET IS A SET OF TARGETS, NOT A LOOP. Every sweep here used to be a
# `for` in a shell recipe, and a shell loop can only do one thing at a time: 121
# apps, one after another, on a 20-core box with the Go toolchain itself bounded
# to two compilers. Measured on this repo from an empty build cache, that shape
# links every plugin in 586s; the same 121 builds scheduled across the box take
# 300s. The apps are independent — nothing in one app's graph waits on another's
# — so the serialisation bought nothing and cost about half the wall clock.
# (Warm, the whole fleet relinks in 25s.)
#
# Naming each app as its own target is also what fixed the reporting. A `set -e`
# loop stops at the first failure and the apps behind it never run, which reports
# nothing, and nothing is indistinguishable from passing (see `describe` below
# for the day that cost). `make -k` continues past every failure AND names each
# one in its own error line, so the property the loop was hand-rolling is now
# make's.

# lastword, not firstword: firstword is whichever makefile make STARTED with, so
# running `make -f mk/fleet.mk` resolved the root correctly while `include
# mk/fleet.mk` from the root Makefile resolved to the repo's PARENT and broke the
# go.mk include below. lastword is this file in both cases, which is what makes the
# header's "included at the root it changes nothing" actually true.
ROOT := $(abspath $(dir $(lastword $(MAKEFILE_LIST)))..)
include $(ROOT)/mk/go.mk

# The apps, read from the Makefiles themselves. Those sit alongside each app's
# plugin/<app>/main.go, so globbing them reads the same single source of truth
# the mains do — no second list to fall out of step.
APPDIRS := $(patsubst %/Makefile,%,$(wildcard $(ROOT)/apps/*/Makefile))

# Where plugin.mk puts each app binary, spelled the same way here because the
# fleet sweep is the one caller that builds ALL of them and therefore the one
# that has to clean up after itself. Kept identical to mk/plugin.mk's BIN; if
# that moves, this follows.
BIN := $(ROOT)/bin

# The PUBLISHABLE layout, and the only reason it is not $(BIN): a release carries
# every app for every platform, and a flat directory cannot hold two `billing`s.
# So the platform is spelled into the name — <app>-<os>-<arch>, which is both
# what hanzoai/ci's Go lane writes and the triple manifest/release.go keys the
# release index on. Same recipe, same flags, one extra suffix (mk/plugin.mk).
DIST := $(ROOT)/dist

# The platforms a release carries. linux/amd64 is what the clusters run;
# linux/arm64 is what the dev boxes and the GB10 builders run, and a plugin the
# people writing it cannot execute is a plugin nobody tests. Overridable so a
# one-platform check is `PLATFORMS=linux/amd64 make dist`.
PLATFORMS ?= linux/amd64 linux/arm64

# Three apps in the manifest have a plugin/<app> here and no source directory: they
# are external modules (hanzoai/authz, hanzoai/licensing, hanzoai/metrics) wired
# into apps.Wire() by import. There is nothing for a per-app Makefile to sit
# beside, so they are named here and run through the SAME mk/plugin.mk recipe by
# name instead of by location. When those repos publish their own documents this
# list goes away and the `app` function below stops needing its second branch.
EXTERNAL := authz licensing metrics

# THE FLEET: every app that ships as a binary, by name. The directory name is the
# app name for all but the four packages that back a differently-named mount
# (auditlog→audit, eval→evals, plugin→plugins, sandbox→sandboxes) — and that
# mapping stays where it already is, in the app's own Makefile's APPS list, which
# is why the targets below address an app through its DIRECTORY and let the
# Makefile there say what it builds.
FLEET := $(notdir $(APPDIRS)) $(EXTERNAL)

# HOW YOU REACH ONE APP — stated once, because every target below needs it and a
# second spelling is a second thing to keep in step. A local app is reached
# through its own Makefile; an external one has no directory to sit beside, so it
# is reached by name through the same mk/plugin.mk. That difference is data (does
# the Makefile exist), not a separate code path, so `authz` is an ordinary member
# of $(FLEET) rather than a second loop appended to every sweep.
app = $(if $(wildcard $(ROOT)/apps/$1/Makefile), \
        $(MAKE) --no-print-directory -C $(ROOT)/apps/$1, \
        $(MAKE) --no-print-directory -f $(ROOT)/mk/plugin.mk ROOT=$(ROOT) APPS=$1)

# Apps that cannot project their own document without a live dependency. kafka's
# Mount is fail-closed on a pubsub broker (apps/kafka/kafka.go), and a document is
# a projection of ROUTES — it should not need a running message bus — so this is a
# defect in the app, not a property of the gate. It is exempt BY NAME and prints
# itself when skipped, rather than being silently passed over, and the exemption
# is cheap to hold honest: kafka's subset declares zero paths, so it contributes
# nothing to the fleet spec and has no drift it could hide. It goes away when the
# adaptor moves out to hanzoai/stream.
OPENAPI_NEEDS_BROKER := kafka

# Apps with NO STANDALONE MOUNT, DERIVED from the manifest rather than named.
#
# A coresident app is middleware on a SIBLING's router (manifest.App.Coresident):
# it claims no prefix, and mounting it alone is refused by construction — "zen
# installed middleware at /v1, outside the prefixes it owns". describe mounts one
# app alone, so there is nothing here for it to project. That is a property of
# coresidency, not a defect like kafka's broker, and like kafka the subset is
# empty and therefore has no drift it could hide.
#
# Read from apps.go the same way the root Makefile reads APPS, because the ONE
# place an app declares it is coresident must also be the place this loop learns
# it. Named here instead, the next coresident app would go missing exactly the way
# zen did — and zen went missing INVISIBLY, by having no Makefile at all, so the
# glob below simply never saw it and nothing said so.
CORESIDENT := $(shell sed -n 's/.*{Name: "\([^"]*\)".*Coresident: true.*/\1/p' $(ROOT)/manifest/apps.go)

# THE TWO EXEMPTIONS ABOVE ARE EXEMPTIONS FROM DESCRIBING, NEVER FROM BUILDING.
# They were both, because the only sweep that touched an app was `describe`, so
# an app skipped there was an app nothing ever compiled: kafka and zen could stop
# linking on main and every gate stayed green. A document needs a live broker or
# a sibling's router; a LINK needs neither. Splitting the set is what lets
# `binaries` below carry no exemptions at all, which is the whole property the
# host+plugin model rests on.
DESCRIBABLE := $(filter-out $(OPENAPI_NEEDS_BROKER) $(CORESIDENT),$(FLEET))
UNMOUNTABLE := $(filter $(OPENAPI_NEEDS_BROKER) $(CORESIDENT),$(FLEET))

# HOW MUCH OF THE BOX A FLEET SWEEP TAKES: J apps at once, each with $(FLEET_P)
# compilers. They MULTIPLY, and that is the whole of the sizing rule — the box
# sees J*P concurrent build actions, so J*P is what has to fit, never J alone.
#
# P first, because J is derived from it. TWO, and the reason is not that 2 is
# fastest — it is that 2 is what the git-runner pod already asks for. That pod
# injects GOFLAGS=-p=2 into every job, and `fan` below passes GOFLAGS on the make
# COMMAND LINE, which beats the environment. Any other P here silently overrides
# the operator who sized the cgroup, which is the one decision this file has no
# standing to make. Agreeing with it costs nothing and keeps one answer.
#
# Fewer-and-fatter did measure faster: J=5 P=4 came in at 268s against 300s for
# J=10 P=2 — the same 20 actions, split differently — which is the shape you would
# expect when ten cold builds sharing 587 packages each compile them. It is not
# adopted, and NOT because it is unsafe: an earlier P=4 run lost `commerce` to a
# SIGTERM at 120 of 121, but a clean rerun finished all 121, so that was other
# work on the box and not this setting. It is not adopted because 268 against 300
# is inside the run-to-run spread the same J=10 P=2 config showed on this machine
# (300s and 354s), so there is no measurement here that outweighs agreeing with
# the pod. Revisit on a quiet box, and move P and the pod's setting together.
FLEET_P ?= 2

# J is bounded by BOTH, and by the smaller: cores because J*P must not exceed
# them, memory because that is what kills.
#
# Measured, and both bounds bite. The heaviest per-app link (o11y, 2054 packages,
# 93 MiB) peaks at 1.67 GB resident and the median is ~1.4 GB, so 3 GiB per
# concurrent app covers a link plus its compilers. And oversubscribing CPU is not
# free the way it is usually assumed to be: this fleet from an empty cache took
# 431s at J=20,P=2 — 40 actions on 20 cores — against 300s at J=10,P=2. Half
# again as slow for twice the parallelism.
#
# The MEMORY bound reads the CGROUP limit before /proc/meminfo, and that is the
# whole point: the git-runner pod is capped at 26Gi on 6 CPU while `nproc` inside
# it reports the NODE's cores. Sizing off nproc there asks for a dozen concurrent
# links in a cgroup that holds eight — an OOMKill, not a slow build. (That pod
# also injects GOFLAGS=-p=2 into every job, which mk/go.mk's `?=` deliberately
# does not override: the operator sizing the pod is the one who knows what it
# holds.)
#
# $(or …) and := , not ?= : this has to be a NUMBER by the time it reaches -j, and
# a `?=` variable stays recursive — make would hand `-j` the unexpanded shell
# script. $(or) short-circuits, so an explicit JOBS= from the environment or the
# command line skips the probe entirely.
#
# No `case` in here, and that is not style: make balances parentheses inside
# $(shell …), so a case pattern's own `)` closes the function early and the rest
# of the script leaks out as make syntax. Parameter expansion says the same thing
# with no parentheses to miscount — ${m%%[!0-9]*} is empty exactly when m is not
# a plain number, which is what "max" and an absent cgroup both look like.
JOBS := $(or $(JOBS),$(shell \
	  m=$$(cat /sys/fs/cgroup/memory.max 2>/dev/null || cat /sys/fs/cgroup/memory/memory.limit_in_bytes 2>/dev/null); \
	  [ -n "$${m%%[!0-9]*}" ] || m=$$(awk '/MemAvailable/{print $$2*1024}' /proc/meminfo 2>/dev/null); \
	  [ -n "$$m" ] || m=8589934592; \
	  c=$$(( $(NPROC) / $(FLEET_P) )); j=$$((m / 3221225472)); \
	  [ "$$j" -gt "$$c" ] && j=$$c; [ "$$j" -lt 1 ] && j=1; echo $$j))

# Every sweep is this: schedule the per-app targets across $(JOBS), keep going
# past a failure, and let make name each one. Passing GOFLAGS on the command line
# is what reaches the app's own sub-make and the `go` inside it in one step.
#
# THERE IS NO SHARED-FLOOR PREBUILD HERE, AND IT IS NOT AN OVERSIGHT. Every app
# links the same root package (587 of apps/auto's 588), and Go's build cache
# dedupes RESULTS rather than work in flight, so J cold builds do compile that
# floor J times. Warming it first is the obvious fix and it was measured twice,
# back to back, from empty caches at J=10 P=2:
#
#   no prebuild                          300s
#   go build <root>            (587 pkg) 339s
#   go build <root>/apps/...  (4149 pkg) 327s
#
# Both prebuilds are SLOWER. The warm-up is one process on a dependency-shaped
# graph, so it leaves most of the box idle for its whole duration, and that costs
# more than the duplication it removes — the fan-out was already overlapping each
# app's own work with its neighbours' floor. Recording it because it is the first
# thing the next person will reach for.
fan = $(MAKE) -f $(ROOT)/mk/fleet.mk -k -j$(JOBS) GOFLAGS=-p=$(FLEET_P) $1

.PHONY: binaries describe dist openapi check

# FIRST, so a bare `make -f mk/fleet.mk` runs the two-second check and not the
# twelve-minute rebuild. (Included at the root it changes nothing: the default
# goal is still the root Makefile's own first target.)
#
# The composition proof: compose the apps' documents and check the result against
# the fully-mounted golden. It links no subsystem and mounts nothing — see
# openapi/weave_test.go for what it refuses. OUT=<path> writes it elsewhere.
#
# It writes BOTH projections, from one weave: openapi.yaml is everything the
# fleet serves, and public.yaml beside it is the part that declared itself part
# of the published contract (openapi/public.go). Two files, one command, one
# composition — a second command for the second file is how two documents come to
# describe two different commits.
openapi: ## Compose every app's document into openapi.yaml + public.yaml. OUT=<path> to write them elsewhere.
	@$(GO) test -count=1 $(ROOT)/openapi -weave="$(abspath $(if $(OUT),$(OUT),$(ROOT)/openapi.yaml))"
	@echo ">> openapi.yaml — $$(grep -c '^  /' $(ROOT)/openapi.yaml) paths"
	@echo ">> public.yaml  — $$(grep -c '^  /' $(ROOT)/public.yaml) paths"

# ONE APP, EITHER VERB. Both delegate to mk/plugin.mk through `app` — nothing
# about how a binary is produced is restated here — and being TARGETS rather than
# loop bodies is what lets make schedule them and name the ones that fail.
#
# STATIC pattern rules, so the target set is CLOSED: `build/nosuchapp` is an
# error naming the app, not a rule that quietly matches anything. They are also
# explicit rules, which is what makes them work at all — an implicit `build/%:`
# is never searched for a target listed in .PHONY, so the phony declaration these
# obviously wanted silently turned every one of them into "nothing to be done".
$(addprefix build/,$(FLEET)): build/%:
	+@$(call app,$*) build

$(addprefix describe/,$(DESCRIBABLE)): describe/%:
	+@$(call app,$*) describe

# EVERY PLUGIN LINKS, ALONE. No exemptions, because there is no such thing as an
# app that cannot be compiled by itself — an app that only builds as part of the
# whole is the defect this whole host+plugin model exists to prevent, and it is
# the one property nothing else in the tree checked. `describe` implies this for
# most apps, but it skips the two that cannot MOUNT alone, and those were exactly
# the two nothing ever compiled.
#
# It is also the target the release lane calls, one platform at a time (`dist`).
binaries: ## Build every app's binary into <root>/bin. The link proof: no exemptions.
	+@$(call fan,$(addprefix build/,$(FLEET)))
	@echo ">> binaries: $(words $(FLEET)) plugins in $(BIN) ($$(du -sh $(BIN) | cut -f1))"

# THE RELEASE LAYOUT: every app, every platform, named <app>-<os>-<arch> — the
# triple manifest/release.go looks an app up by, so the index hanzoai/ci writes
# beside these files is readable by the host that downloads them.
#
# CGO_ENABLED=0, inherited from mk/go.mk and deliberate here rather than merely
# default: a plugin fetched over the network runs on a box we did not build, and
# a cgo binary would demand that box carry a matching libsqlite3. The image's
# plugins are built the other way (cgo + libsqlite3) because there the host
# controls the filesystem they land on. Two consumers, two builds, one recipe.
# EXPORT, not a `VAR=x cmd` prefix: `fan` is two commands joined by &&, and a
# prefix assignment reaches only the first of them — the floor would cross-compile
# and every app behind it would build for the host. Exported, both halves and
# every sub-make below them see one target platform.
dist: ## Build every app for every platform into <root>/dist as <app>-<os>-<arch>.
	@rm -rf $(DIST) && mkdir -p $(DIST)
	+@for p in $(PLATFORMS); do \
	  echo ">> $$p"; \
	  export GOOS=$${p%%/*} GOARCH=$${p##*/}; \
	  $(call fan,BIN=$(DIST) SUFFIX=-$$GOOS-$$GOARCH $(addprefix build/,$(FLEET))) || exit 1; \
	done
	@echo ">> dist: $$(ls $(DIST) | wc -l) binaries for $(words $(PLATFORMS)) platforms ($$(du -sh $(DIST) | cut -f1))"

# The exemptions are honoured HERE as well as in the gate, because the gate's
# own failure message says "fix: make describe" — and that fix routed through this
# sweep, which mounted kafka, which fails closed without a live broker. So the one
# command told to repair a red gate could not run at all. Each exemption is
# defined once and read everywhere it applies; a repair path that skipped fewer
# apps than the gate would be the same bug again.
#
# The unmountable two are BUILT here rather than passed over, so this sweep still
# touches every app in the fleet and `describe` remains a superset of `binaries`.
#
# EVERY app is attempted, and the failures are NAMED. Under `set -e` the loop
# this replaced stopped at the first app that failed, and the apps after it never
# ran — which reports nothing at all, and nothing is indistinguishable from
# passing. On 2026-08-05 an unbalanced brace in apps/commerce/describe.go stopped
# the loop there; ~45 apps behind it silently never regenerated, and because
# `meet` was where the run appeared to end, three separate people diagnosed a
# defect in `meet`. Two real defects were braided by one truncation. A gate that
# hides what it did not check is worse than one that checks nothing, because it
# is believed. `make -k` is that property, kept by make instead of by hand.
# THE SCRATCH THIS NEEDS, AND HOW TO GET IT ON A MAC.
#
# Every app opens its cek-encrypted store to mount, and the pure-Go SQLCipher
# codec refuses to decrypt onto persistent storage: it wants RAM-backed scratch,
# and it verifies that rather than believing a flag. On Linux /dev/shm is already
# tmpfs, so this is invisible there — which is exactly how it stayed unfixed on
# darwin while the release train ran on Linux.
#
# macOS ships tmpfs and mounts none by default. `make ramfs` mounts one; it is
# the only step that needs root, it is needed once per boot, and after it
# `make describe` behaves identically on both platforms. An hdiutil RAM disk is
# NOT a substitute and is correctly rejected — it is HFS+/APFS on a device statfs
# cannot tell from a real disk, so accepting it would mean trusting a claim the
# codec cannot verify.
RAMFS ?= /tmp/hanzo-ramfs

# Say it ONCE, up front, instead of 121 identical codec errors after a build.
# The failure this replaces named the codec and the env var but not the command,
# so it read as "this platform cannot" — and that is the conclusion somebody
# reached, twice.
ramfs-check:
ifeq ($(shell uname),Darwin)
	@if [ -z "$$HANZO_SQLITE_RAMFS_DIR" ] && [ "$$(/usr/bin/stat -f %T $(RAMFS) 2>/dev/null)" != "tmpfs" ]; then \
	  echo "describe needs RAM-backed scratch and macOS mounts no tmpfs by default."; \
	  echo "    make ramfs      # once per boot, needs sudo"; \
	  exit 1; \
	fi
endif


ramfs: ## Mount the RAM-backed scratch the codec requires (macOS: needs sudo, once per boot).
ifeq ($(shell uname),Darwin)
	@if [ "$$(/usr/bin/stat -f %T $(RAMFS) 2>/dev/null)" = "tmpfs" ] || mount | grep -q " $(RAMFS) "; then \
	  echo ">> ramfs: $(RAMFS) already mounted"; \
	else \
	  mkdir -p $(RAMFS); \
	  echo ">> ramfs: mounting tmpfs at $(RAMFS) (sudo)"; \
	  sudo /sbin/mount_tmpfs $(RAMFS); \
	fi
	@echo ">> ramfs: export HANZO_SQLITE_RAMFS_DIR=$(RAMFS)"
else
	@echo ">> ramfs: /dev/shm is already tmpfs on this platform — nothing to do"
endif

describe: ## Every app describes itself (one binary per app, all at once).
	@$(MAKE) --no-print-directory ramfs-check
	@GOOS= GOARCH= $(GO) generate -run zipdoc $(ROOT)/...
	@echo ">> build-only: $(UNMOUNTABLE) — no standalone mount to project (broker / coresident)"
	+@$(call fan,$(addprefix describe/,$(DESCRIBABLE)) $(addprefix build/,$(UNMOUNTABLE)))
	@echo ">> $$(ls $(ROOT)/plugin/*/openapi.json | wc -l) app documents"
	# The witness, written by the sweep that generates the documents rather than
	# beside it: openapi/closure.json says what these documents were generated FROM,
	# and it is only true if the same run produced both. `make closure-check` reads
	# it back in two seconds, which is how a go.mod bump stops being invisible until
	# the fifty-minute gate. Here rather than in the per-app describe because 121
	# concurrent writers to one file is a race, and one `go list` covers the fleet.
	@cd $(ROOT) && $(GO) run ./cmd/closure -write -describable="$(DESCRIBABLE)"

# The drift gate. It REGENERATES FROM SOURCE and fails on any diff, which is the
# whole difference between it and openapi.
#
# openapi compares two COMMITTED artifacts — the apps' documents and the golden
# they compose into. Both are derived, and nothing forces either back to the routes, so
# they agree with each other while both are wrong. That is not hypothetical: it is
# how plugin/ingress lost eight paths (/v1/ingress/routes, /services, /middlewares,
# /tls, /status and their :id forms). Routes were added, the subset was never
# regenerated, the golden was composed from that same stale document, openapi passed,
# and the entire ingress API was absent from openapi.yaml — and therefore from every
# SDK generated off it, so no Python, Go or TS caller could reach it at all.
#
# The same blindness hid a second thing: main once carried documents generated by
# one zip version against a go.mod pinning an older one that could not produce
# them. Everything agreed with everything, and the next regeneration would have
# silently dropped the parameter examples, the derived required-ness and the $ref
# sharing. Two independent failures, one cause — so the fix is not a better
# comparison between derived things, it is regenerating the derived thing.
#
# It checks with `git status --porcelain`, not `git diff`: a NEW app produces a
# NEW subset, which is untracked and therefore invisible to a diff — the failure
# that matters most is exactly the one a diff would miss.
#
# It CALLS describe rather than restating it. The two used to be the same sweep
# written twice, with different skip messages and different error text, and the
# copy is what let them disagree: a repair path that skipped fewer apps than the
# gate is the bug the exemption comment above already had to be written once.
# One sweep, one set of exemptions, one place to change either.
#
# One behaviour went with the copy: this used to delete each binary as it went,
# and `describe` never did, so the same work left the tree in two different states
# depending on which name you called it by. It leaves them now — 4.1 GiB in an
# ignored ./bin, on a runner that requests 38Gi of ephemeral storage — and you
# have the binaries you just built.
check: describe ## Regenerate every document + openapi.yaml FROM SOURCE and fail on any diff. The drift gate.
	@out=$$($(MAKE) --no-print-directory -f $(ROOT)/mk/fleet.mk openapi OUT=$(ROOT)/openapi.yaml 2>&1) \
	  || { echo "$$out"; echo "!! the compose refused; nothing was written"; exit 1; }
	@stale=$$(git -C $(ROOT) status --porcelain -- openapi.yaml public.yaml openapi/floor.json openapi/closure.json plugin/); \
	if [ -n "$$stale" ]; then \
	  echo "$$stale"; \
	  git -C $(ROOT) diff --stat -- openapi.yaml public.yaml plugin/; \
	  echo ""; \
	  echo "STALE: regenerating the document from source produced something other than what is"; \
	  echo "committed. The list above is published surface — routes that exist and are"; \
	  echo "undocumented, or documented and gone. The SDK repos pull this file, so a route"; \
	  echo "missing here is a route no generated client can reach."; \
	  echo ""; \
	  echo "  fix:  make describe  # then commit openapi.yaml, plugin/*/openapi.json and openapi/closure.json"; \
	  echo ""; \
	  echo "  openapi/closure.json alone means only the DEPENDENCIES moved — the documents"; \
	  echo "  are current and the witness is what was not committed. 'make closure-check'"; \
	  echo "  would have said so in two seconds."; \
	  echo ""; \
	  exit 1; \
	fi
	@echo ">> openapi.yaml regenerated from source and unchanged — $$(grep -c '^  /' $(ROOT)/openapi.yaml) paths"
	@echo ">> public.yaml  regenerated from source and unchanged — $$(grep -c '^  /' $(ROOT)/public.yaml) paths"
