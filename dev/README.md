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
| serving the project's FILES | **needs object storage** |
| TLS | **needs an ingress in front** |

The last two are not switches, so they are worth stating plainly.

**Files.** `apps/projects` keeps a site's bytes in object storage and reads them
back through the same client in both directions; there is no from-disk mode to
fall back to. Without a store, the deploy answers 503 and the address answers
"site not found". `apps/s3` is the DATA plane over a SeaweedFS gateway, not the
gateway — cloud does not embed one. Point `S3_ACCESS_KEY` / `S3_SECRET_KEY` /
`S3_ENDPOINT` at a store and the address serves.

**TLS.** zip terminates no TLS — there is no `tls.Config` in it — so this is
plaintext on a loopback port. TLS belongs to `hanzoai/ingress` in front, which is
also the estate's rule for who may terminate it.

## The boot is warm, and that is a choice

`make up` reuses `e2e/run.sh`, which wakes every app after boot so its own
assertions read a stack that exists. The instance is therefore ready rather than
lazy — ~2 GB resident. Laziness is what the RUNTIME does, and a plain `./bin/cloud`
shows it: the host plus the four apps that own listeners, and nothing else until
asked.
