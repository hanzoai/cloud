# self

What it costs to run this yourself.

Every other lane measures what the software does. This one measures whether you
can have it at all. `run.sh` builds the binary from this repository, starts it,
asks it what it serves, and reaches one operation through each door it opens.

```
bench/self/run.sh
```

Taken on an M-series laptop, 2026-09-10, median of four runs.

| | measured |
|---|---|
| private modules required | **0** |
| build from source | 13 s (9–18) |
| binary | 80 MB |
| boot to a healthy answer | **1.2 s** |
| operations served by default | 13 over 8 paths |

And the doors, each asked separately, because a door that does not answer is the
row that matters:

| door | answered |
|---|---|
| REST | 200 |
| MCP `tools/list` | 13 tools |
| op-call plane | 200 |

## What the numbers do and do not say

**Zero private modules is the load-bearing one.** It is what separates running
the software from renting it, and it is a property of `go.mod` you can check
without trusting this table.

**Thirteen operations is the default, not the ceiling.** Subsystems here are
disabled by default and fail closed: one without its configuration refuses to
mount rather than serving something degraded. What a filled-in deployment serves
is a different number and this lane does not claim it.

**The boot number is measured against the health port, and that matters.** The
API port answers the console shell for a path it does not route, so a 200 there
says a listener is up and nothing about readiness. Timing against it produced a
faster-looking number from a measurement that was not of readiness at all. Worth
knowing before you time anyone's boot, including your own.

## Where running it yourself costs something

**A plain `go build` cannot encrypt the data plane, and the binary says so.**
The encrypted store is SQLCipher, and cgo links ordinary SQLite unless the build
says `-tags libsqlite3` against libsqlcipher. So a from-source binary given a
master key now refuses to start, naming that recipe, and this lane runs the
explicit development path instead — an unencrypted store, warned about on every
boot.

That refusal is new. Before it, such a build announced `data-plane encryption
ACTIVE`, wrote every store with the plaintext SQLite header, and died on the
SECOND boot in a migration that needed a SQLCipher function it did not have.
Between those two moments it had written data to disk in the clear while saying
it had not. The check now runs at the one seam every store open funnels through,
so no ordering elsewhere can produce a plaintext file: measured, zero stores
reach the disk before it refuses.

Take the row as it reads. Running this yourself is free and takes a second; an
encrypted data plane costs you a build with the library linked.

**Build time is a warm cache.** A cold build downloads 152 modules first, which
is a measurement of a network rather than of this code.

**A second boot is faster than the first** — around 0.3 s once the binary is in
the page cache. The table gives the first one, because that is the one a person
trying this actually waits for.
