// Behavioral test for tag.js, run by TestTagBehavior (tag_test.go) under node.
// The tag is JavaScript, so its invariants are proven by RUNNING it — asserting
// on the source text would prove only that the source contains a string.
//
//   node tag_test.js <path-to-tag.js>

const fs = require('fs')
const vm = require('vm')
const assert = require('assert')

const source = fs.readFileSync(process.argv[2], 'utf8')

// run loads the tag into a fresh sandbox and returns what it sent.
// attrs are the data-* attributes on the <script> element.
// opts.storage seeds localStorage, opts.jar seeds the cookies — the two places a
// browser may already be carrying an anonymous id when this tag arrives.
function run(attrs, opts = {}) {
  const sent = { fetch: [], beacon: [] }
  const listeners = {}
  const storage = new Map(Object.entries(opts.storage || {}))
  const jar = new Map(Object.entries(opts.jar || {}))

  const script = {
    src: opts.src || 'https://api.hanzo.ai/v1/event.js',
    getAttribute: (k) => (k in attrs ? attrs[k] : null)
  }

  const sandbox = {
    console,
    URL,
    Blob: class Blob {
      constructor(parts) { this.text = parts.join('') }
    },
    crypto: { randomUUID: () => 'uuid-' + storage.size + '-' + Math.random().toString(36).slice(2, 8) },
    setTimeout: () => 1,
    clearTimeout: () => {},
    Date,
    JSON,
    Error,
    String,
    document: {
      currentScript: script,
      referrer: '',
      visibilityState: 'visible',
      // A real jar. The anonymous id lives in a cookie so it can outlive an
      // ORIGIN; a stub without one would leave the whole chain unexercised.
      get cookie() {
        return [...jar].map(([k, v]) => `${k}=${v}`).join('; ')
      },
      set cookie(raw) {
        const first = raw.split(';')[0]
        const eq = first.indexOf('=')
        if (eq > 0) jar.set(first.slice(0, eq).trim(), first.slice(eq + 1).trim())
      }
    },
    // hanzo.team is not hanzo.ai, so the chain writes a host-only cookie here —
    // the Domain= attribute would be rejected and the cookie dropped outright.
    location: { href: 'https://hanzo.team/', pathname: '/', hostname: 'hanzo.team', protocol: 'https:' },
    history: { pushState() {}, replaceState() {} },
    navigator: {
      sendBeacon: opts.noBeacon
        ? undefined
        : (url, blob) => { sent.beacon.push({ url, body: blob.text }); return true }
    },
    fetch: (url, init) => { sent.fetch.push({ url, init }); return { catch: () => {} } },
    addEventListener: (name, fn) => { (listeners[name] = listeners[name] || []).push(fn) },
    localStorage: {
      getItem: (k) => (storage.has(k) ? storage.get(k) : null),
      setItem: (k, v) => storage.set(k, String(v))
    }
  }
  sandbox.window = sandbox
  vm.runInNewContext(source, sandbox)
  return { sent, sandbox, storage, jar, fire: (n, e) => (listeners[n] || []).forEach((f) => f(e)) }
}

/** The anonymous id on the first event of the first transmission. */
function anonOf(r) {
  const body = r.sent.beacon.length ? r.sent.beacon[0].body : r.sent.fetch[0].init.body
  return JSON.parse(body).batch[0].anonymousId
}

// 1. NO KEY ⇒ INERT. The whole reason the tag exists: a keyless beacon is
//    accepted 200 into $public, a tenant the owning org cannot read.
{
  const r = run({})
  r.fire('pagehide')
  assert.deepStrictEqual(r.sent.beacon, [], 'keyless tag must not beacon')
  assert.deepStrictEqual(r.sent.fetch, [], 'keyless tag must not fetch')
  assert.strictEqual(r.sandbox.hanzo, undefined, 'keyless tag must not install a manual API')
}

// 2. A SECRET key is not a publishable key. Pasting sk- must be inert, not a
//    secret leaked into every page's HTML and sent to the ingest.
{
  const r = run({ 'data-key': 'sk-live-deadbeef' })
  r.fire('pagehide')
  assert.deepStrictEqual(r.sent.beacon, [], 'sk- must not send')
  assert.deepStrictEqual(r.sent.fetch, [], 'sk- must not send')
}

// 3. KEYED ⇒ a pageview on the canonical wire, with the key on the beacon query
//    (sendBeacon cannot set a header) and url present (host = domain(url)).
{
  const r = run({ 'data-key': 'pk-live-abc', 'data-product': 'team' })
  r.fire('pagehide')
  assert.strictEqual(r.sent.beacon.length, 1, 'one flush')
  const { url, body } = r.sent.beacon[0]
  assert.ok(url.includes('/v1/event?ingest_key=pk-live-abc'), 'key rides the query: ' + url)
  const batch = JSON.parse(body).batch
  assert.strictEqual(batch.length, 1)
  const ev = batch[0]
  assert.strictEqual(ev.type, 'pageview')
  assert.strictEqual(ev.url, 'https://hanzo.team/', 'url is what the warehouse derives host from')
  assert.strictEqual(ev.path, '/')
  assert.strictEqual(ev.product, 'team')
  assert.ok(ev.messageId && ev.timestamp && ev.distinctId, 'core fields stamped')
  assert.strictEqual(ev.event, undefined, 'naming is resolveEventName server-side')
}

// 4. Without sendBeacon the fetch path carries the key as a bearer. (A keyed tag also
//    fetches /v1/tags for its browser pixels; the /v1/event POST is the one asserted.)
{
  const r = run({ 'data-key': 'pk-live-abc' }, { noBeacon: true })
  r.fire('pagehide')
  const posts = r.sent.fetch.filter((f) => f.url.indexOf('/v1/event') !== -1)
  assert.strictEqual(posts.length, 1)
  const init = posts[0].init
  assert.strictEqual(init.headers.authorization, 'Bearer pk-live-abc')
  assert.strictEqual(init.keepalive, true)
  assert.ok(JSON.parse(init.body).batch.length === 1)
}

// 5. The key may ride the src query, for a host that strips data-* attributes.
{
  const r = run({}, { src: 'https://api.hanzo.ai/v1/event.js?key=pk-live-xyz' })
  r.fire('pagehide')
  assert.strictEqual(r.sent.beacon.length, 1, 'src ?key= is honored')
}

// 6. THE COOKIE WINS. Identity is hzAnonId — the one chain, vendored from
//    @hanzo/event and served ahead of this file — so a browser that already
//    carries an id arrives as the person it already is. This tag used to read
//    localStorage alone, which is ORIGIN-scoped: it could not see the shared
//    cookie, so an origin carrying only this tag was its own population.
{
  const id = '01920000-0000-7000-8000-0000000000ee'
  const r = run({ 'data-key': 'pk-live-abc' }, { jar: { hz_anon_id: id } })
  r.fire('pagehide')
  assert.strictEqual(anonOf(r), id, 'the cookie is the identity')
}

// 7. AN EXISTING hz_id IS ADOPTED, NOT ORPHANED. hz.js minted into a key of its
//    own, so browsers in the wild carry one. Minting over it would detach a
//    returning visitor from their own history.
{
  const legacy = '01920000-0000-7000-8000-0000000000ff'
  const r = run({ 'data-key': 'pk-live-abc' }, { storage: { hz_id: legacy } })
  r.fire('pagehide')
  assert.strictEqual(anonOf(r), legacy, 'hz.js legacy id adopted')
  assert.strictEqual(r.jar.get('hz_anon_id'), legacy, 'carried onto the shared key')
  assert.strictEqual(r.storage.get('hz_anon_id'), legacy)
}

// 8. A browser carrying nothing is given ONE id, in the durable place. The
//    session stays origin-local and in localStorage — deliberately unchanged.
{
  const r = run({ 'data-key': 'pk-live-abc' })
  r.fire('pagehide')
  const id = anonOf(r)
  assert.ok(/^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab]/.test(id), 'v7 anon id: ' + id)
  assert.strictEqual(r.jar.get('hz_anon_id'), id, 'the cookie outlives the origin')
  assert.strictEqual(r.storage.get('hz_anon_id'), id)
  assert.ok(r.storage.has('hz_session'), 'hz_session')
  assert.ok(!r.jar.has('hz_session'), 'a session is not shared across surfaces')
}

// 9. SPA navigation is a pageview: pushState fires no event, so history is
//    patched. A repeat of the same href is not a second pageview.
{
  const r = run({ 'data-key': 'pk-live-abc' })
  r.sandbox.location.href = 'https://hanzo.team/inbox'
  r.sandbox.location.pathname = '/inbox'
  r.sandbox.history.pushState({}, '', '/inbox')
  r.sandbox.history.pushState({}, '', '/inbox') // same href ⇒ no duplicate
  r.fire('pagehide')
  const batch = JSON.parse(r.sent.beacon[0].body).batch
  assert.strictEqual(batch.length, 2, 'initial + one SPA pageview')
  assert.strictEqual(batch[1].path, '/inbox')
}

// 10. An uncaught error is captured as a typed error event.
{
  const r = run({ 'data-key': 'pk-live-abc' })
  r.fire('error', { error: new Error('boom') })
  r.fire('pagehide')
  const batch = JSON.parse(r.sent.beacon[0].body).batch
  const err = batch.find((e) => e.type === 'error')
  assert.ok(err, 'error captured')
  assert.strictEqual(err.error.message, 'boom')
  assert.strictEqual(err.error.handled, false)
}

// 11. Config-driven injection: a keyed tag fetches its site's browser pixels from
//     /v1/tags (dual-resolved per site by this key or the host), so the hosted
//     one-liner injects GA/Meta/TikTok/X first-party. The pixels + the deduping
//     event_id are unit-tested in track.js; here we prove the hosted tag makes the
//     request — and that a keyless tag never does.
{
  const r = run({ 'data-key': 'pk-live-abc' })
  assert.ok(
    r.sent.fetch.some((f) => f.url.indexOf('/v1/tags?key=pk-live-abc') !== -1),
    'keyed tag fetches /v1/tags for its browser pixels'
  )
  const keyless = run({})
  assert.ok(
    !keyless.sent.fetch.some((f) => f.url.indexOf('/v1/tags') !== -1),
    'keyless tag never fetches /v1/tags'
  )
}

console.log('tag.js: 11/11 behavioral checks passed')
