# A local project

```sh
make up            # or: ./dev/up.sh path/to/hanzo.config.js
```

Brings the whole cloud up for the project declared in `hanzo.config.js` — the
same binary that serves api.hanzo.ai, all 117 capabilities, on one port.

## The config is the whole surface

```js
export default {
  name: 'demo',        // the address: demo.localhost
  org: 'hanzo',
  serve: './public',
  port: 28080,
};
```

Nothing here selects capabilities. Selecting them would be a second way to say
what a request already says: the host routes a prefix to a child and starts that
child on the first request that reaches it. `run.sh` reports the split at boot —
"lazy (no socket until first use): 59 apps".

`port` is one number and the rest of the block follows it (`+1` health, `+2` zap,
`+4..6` the three brokers), so two projects, or a project and a test run, can be
up at once. Every listener here is one somebody else's instance may already hold.

## What runs, and what does not

| | |
|---|---|
| the `/v1` API, all 117 capabilities | **yes** — `curl $BASE/v1` |
| lazy start per capability | **yes** — a cold capability costs ~80ms on first use |
| `<name>.localhost` reaching the site plane | **yes** — `CLOUD_SITES_APEX=localhost` |
| serving the project's FILES | **yes**, with a store — see below |
| TLS | **needs an ingress in front** |
| egress | **runs elsewhere, on purpose** — see below |

The last two are not switches, so they are worth stating plainly.

**Files.** `apps/projects` keeps a site's bytes in object storage and reads them
back through the same client in both directions; there is no from-disk mode to
fall back to. `apps/s3` is the DATA plane over an S3 gateway, not the
gateway — cloud embeds no store. `hanzoai/s3` IS that gateway, so the loop closes
without anything outside the estate:

```sh
go build -o s3 ./s3                 # in ~/work/hanzo/s3
cat > s3conf.json <<'JSON'
{ "identities": [ { "name": "local",
    "credentials": [ { "accessKey": "local", "secretKey": "local" } ],
    "actions": ["Admin","Read","Write","List","Tagging"] } ] }
JSON
./s3 server -s3 -s3.port=29000 -s3.config=s3conf.json -dir=<data> -ip=127.0.0.1 \
  -master.port=29333 -volume.port=29080 -filer.port=29888
```

Then run `make up` with the store named:

```sh
S3_ADMIN_ENDPOINT=127.0.0.1:29000 S3_ADMIN_ACCESS_KEY=local \
S3_ADMIN_SECRET_KEY=local S3_SECURE=false S3_PUBLIC_SECURE=false make up
```

The identity file is not optional. Without it `s3` serves anonymous reads
and refuses every SIGNED request, which is what cloud sends — the deploy fails
with "Signed request requires setting up Hanzo S3 authentication" while a plain
`curl` of the endpoint looks perfectly healthy. Its own ports also collide by
default: the volume server derives its gRPC port as +10000, so a default `-volume.port=8080`
takes 18080, which is where a cloud already is.

**Deploying is metered.** $1.00 a deploy by default, and an unfunded org is
refused 402 — the money plane working. Funding one locally is deliberately hard:
a SuperAdmin grant needs a client in the reserved `admin` org, and IAM forbids
exactly that on the public token endpoint (`policy.IsReservedOrg`). So `up.sh`
takes the free tier the code already names — `CLOUD_HOSTING_FEE_CENTS=0`, which
`gateHosting` documents as "un-gated" — because that is operator configuration
rather than a way around a gate.

**TLS.** zip terminates no TLS — there is no `tls.Config` in it — so this is
plaintext on a loopback port. TLS belongs to `hanzoai/ingress` in front, which is
also the estate's rule for who may terminate it.

**Egress is not missing from this list — it is deliberately not here.** It holds
upstream credentials so that a caller asks it for a CALL and never for a key, and
that only means anything while the key is somewhere the caller is not. Its own
architecture doc puts the limit exactly: "relocating the reader while the store
stays put buys nothing … if a credential is read by the very process tree it is
leaving, moving the reader moves nothing. The boundary has to move where the
STORE is." Mounting egress as an app in this host would put the credential back
inside the blast radius it exists to escape — and a host outside the cluster but
inside the same cloud account is not outside a compromise of that account either,
so where it runs is a deliberate choice rather than an assumed property.

It is the same shape as `credz` one layer out: cloud already refuses to let the
KMS root key reach any child but the broker. Egress is that rule applied to money
instead of to a master key.

## The boot is warm, and that is a choice

`make up` reuses `e2e/run.sh`, which wakes every app after boot so its own
assertions read a stack that exists. The instance is therefore ready rather than
lazy — ~2 GB resident. Laziness is what the RUNTIME does, and a plain `./bin/cloud`
shows it: the host plus the four apps that own listeners, and nothing else until
asked.
