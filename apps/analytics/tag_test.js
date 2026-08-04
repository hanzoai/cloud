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
function run(attrs, opts = {}) {
  const sent = { fetch: [], beacon: [] }
  const listeners = {}
  const storage = new Map()

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
      visibilityState: 'visible'
    },
    location: { href: 'https://hanzo.team/', pathname: '/' },
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
  return { sent, sandbox, storage, fire: (n, e) => (listeners[n] || []).forEach((f) => f(e)) }
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

// 4. Without sendBeacon the fetch path carries the key as a bearer.
{
  const r = run({ 'data-key': 'pk-live-abc' }, { noBeacon: true })
  r.fire('pagehide')
  assert.strictEqual(r.sent.fetch.length, 1)
  const init = r.sent.fetch[0].init
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

// 6. Identity uses @hanzo/event's storage keys, so a page carrying both clients
//    is one person and not two.
{
  const r = run({ 'data-key': 'pk-live-abc' })
  assert.ok(r.storage.has('hz_anon_id'), 'hz_anon_id')
  assert.ok(r.storage.has('hz_session'), 'hz_session')
}

// 7. SPA navigation is a pageview: pushState fires no event, so history is
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

// 8. An uncaught error is captured as a typed error event.
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

console.log('tag.js: 8/8 behavioral checks passed')
