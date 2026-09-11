# cipher

What is on the disk.

`self/` measures that a plain build cannot encrypt and refuses rather than
pretending. This is the other half: build one that can, write a value through
the API, and then look for that value in the files.

```
bench/cipher/run.sh
```

Taken on an M-series laptop, 2026-09-11. Three builds, given a master key.

| build | with a master key |
|---|---|
| `CGO_ENABLED=0` | 7 stores · **7 distinct headers** · canary in **0** files |
| `CGO_ENABLED=1`, as `make build` does it | **refuses** — "this build cannot encrypt a store" |
| `CGO_ENABLED=1 -tags libsqlite3` | 7 stores · **7 distinct headers** · canary in **0** files |
| the control: `CLOUD_DEV_UNENCRYPTED=1` | 7 stores · 1 header, `SQLite format 3` · canary in **1** file |

**The refusal is a result, not an error.** cgo is on by default on macOS and that
build links ordinary SQLite, so it cannot encrypt. Given a key it stops and names
the recipe rather than writing a plaintext store and reporting success — which
is what it did before the check existed, and the second boot then died on a
SQLCipher function it did not have.

**Two builds do encrypt**, and they agree: seven stores, seven different first
bytes, and the value nowhere on the disk.

A value nothing else could have written goes in through
`POST /v1/base/collections/notes`. Then the server is stopped and every file
under the data directory is searched for it.

**Seven headers against one is the second signal.** Each store gets its own data
encryption key, so each file begins with different ciphertext. A plaintext run
writes the same magic seven times.

## The control is the point

The second column is not a comparison with a competitor. It is the proof that
the first column measures anything at all: the same binary, the same two writes,
the same search, on a store that is deliberately not encrypted. It finds the
canary in two files. Without that, a zero on the left could mean the write never
reached the disk, or that the search does not work on the file — and both of
those happened while this lane was being written.

The second one is worth keeping. A store is a binary file, and `grep` declines
to read one unless told: the first control run reported a clean zero for a file
that plainly contained the string. `LC_ALL=C grep -a` is in the script for that
reason, and the lane **fails** if the control ever reports zero again.

## What this does and does not say

**It says the value is not in the file.** Two writes, seven stores, one search,
with a control that proves the search works.

**It is not a cryptanalysis.** SQLCipher's construction, the KDF and the page
MAC are its own; this measures that the data plane uses it and that the
plaintext is absent, not that the cipher is sound.

**The key is not in the picture.** `CLOUD_KMS_MASTER_KEY_REF` is the one the
operator injects, and where it comes from is the KMS's question, not this one.
A per-store DEK sits beside each database as `<store>.db.dek`, wrapped by that
master key — so this lane says the store is unreadable without the master key,
and says nothing about how well the master key is kept.
