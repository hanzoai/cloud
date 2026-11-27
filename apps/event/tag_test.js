// Behavioral test for tag.js, run by TestTagBehavior (tag_test.go) under node.
const fs = require('fs')
const vm = require('vm')
const assert = require('assert')

const source = fs.readFileSync(process.argv[2], 'utf8')

// run loads the tag into a fresh sandbox and returns what it sent.
function run(attrs, opts = {}) {
  const sent = { fetch: [], beacon: [] }
  const loaded = []
  const listeners = {}
  const fire = (n, e) => (listeners[n] || []).forEach((f) => f(e))
  const storage = new Map(Object.entries(opts.storage || {}))
  const choice = opts.choice === undefined ? 'analytics,marketing,ads' : opts.choice
  const jar = new Map(Object.entries({ ...(choice === null ? {} : { '__Host-hz_consent': choice, '__Host-hz_consent_v': '2' }), ...(opts.jar || {}) }))
  // opts.session seeds the tab's sessionStorage; by default the tab has seen an eu rule.
  const session = new Map(Object.entries(opts.session || { hz_consent_policy: JSON.stringify({ region: 'DE', mode: 'eu', version: 3, floor: 2, refused: false }) }))
  const writes = []
  const timers = []

  const script = {
    src: opts.src || 'https://api.hanzo.ai/v1/event/tag.js',
    nonce: opts.nonce,
    getAttribute: (k) => (k in attrs ? attrs[k] : null)
  }

  // As much of the DOM as the banner touches: elements, attributes, children, a
  // shadow root, click and focus.
  class Node {
    constructor(tag) { this.tag = tag; this.attrs = {}; this.children = []; this.parent = null; this.text = ''; this.on = {} }
    get textContent() { return this.text + this.children.map((c) => c.textContent).join('') }
    set textContent(v) { this.text = String(v) }
    setAttribute(k, v) { this.attrs[k] = String(v) }
    get isConnected() { return attached(this) }
    getAttribute(k) { return k in this.attrs ? this.attrs[k] : null }
    appendChild(c) { c.parent = this; this.children.push(c); return c }
    remove() { if (this.parent) this.parent.children = this.parent.children.filter((c) => c !== this); this.parent = null }
    addEventListener(n, fn) { (this.on[n] = this.on[n] || []).push(fn) }
    click() { (this.on.click || []).forEach((f) => f({})) }
    key(k, shiftKey = false) {
      const e = { key: k, shiftKey, prevented: false, preventDefault() { this.prevented = true } }
      ;(this.on.keydown || []).forEach((f) => f(e))
      return e
    }
    attachShadow() {
      const root = (this.shadow = new Node('#shadow'))
      root.host = this
      if (opts.constructable) root.adoptedStyleSheets = []
      // The root's own activeElement: the focused node when it lives in this root.
      Object.defineProperty(root, 'activeElement', {
        get() {
          let n = document.focused
          while (n && n.parent) n = n.parent
          return n === root ? document.focused : null
        }
      })
      return root
    }
    // As a browser does: the page sees the shadow host focused, the root sees the
    // node, and the host hears focusin naming where focus came from, if that is still
    // in the page.
    focus() {
      let n = this
      while (n.parent) n = n.parent
      const prev = document.activeElement
      const host = n.host || this
      document.activeElement = host
      document.focused = this
      const from = prev && prev !== host && attached(prev) ? prev : null
      if (host !== prev) (host.on.focusin || []).forEach((f) => f({ relatedTarget: from }))
    }
  }
  const attached = (n) => {
    for (; n; n = n.parent || n.host) if (n === document.body) return true
    return false
  }
  const document = {
    currentScript: script,
    // opts.noShadow is a browser with no shadow DOM.
    createElement: (tag) => Object.assign(new Node(tag), opts.noShadow ? { attachShadow: undefined } : {}),
    createTextNode: (text) => Object.assign(new Node('#text'), { text }),
    createElementNS: (ns, tag) => Object.assign(new Node(tag), { ns }),
    // Only attribute selectors are asked: [name].
    querySelector: (sel) => {
      const name = sel.slice(1, -1)
      const find = (n) => (n.attrs && name in n.attrs ? n : n.children.map(find).find(Boolean) || null)
      return find(document.body)
    },
    head: { appendChild: (el) => { loaded.push(el.src) } },
    body: new Node('body'),
    activeElement: null,
    referrer: opts.referrer || '',
    visibilityState: 'visible',
    // A real jar. The anonymous id lives in a cookie so it can outlive an
    // ORIGIN; a stub without one would leave the whole chain unexercised.
    // blocked is a jar the page may no longer read, as a sandboxed frame's throws.
    get cookie() {
      if (this.blocked) throw Object.assign(new Error('The operation is insecure.'), { name: 'SecurityError' })
      return [...jar].map(([k, v]) => `${k}=${v}`).join('; ')
    },
    set cookie(raw) {
      writes.push(raw)
      const first = raw.split(';')[0]
      const eq = first.indexOf('=')
      if (eq <= 0) return
      if (/;\s*Max-Age=0(;|$)/.test(raw)) jar.delete(first.slice(0, eq).trim())
      else jar.set(first.slice(0, eq).trim(), first.slice(eq + 1).trim())
    }
  }

  const sandbox = {
    console,
    URL,
    Blob: class Blob {
      constructor(parts, opts) { this.text = parts.join(''); this.type = (opts && opts.type) || '' }
    },
    Event: class Event {
      constructor(type) { this.type = type }
    },
    // opts.constructable is a browser with constructed stylesheets.
    CSSStyleSheet: opts.constructable ? class CSSStyleSheet { replaceSync(t) { this.text = t } } : undefined,
    dispatchEvent: (e) => fire(e.type, e),
    crypto: { randomUUID: () => 'uuid-' + storage.size + '-' + Math.random().toString(36).slice(2, 8) },
    // Timers never run by themselves; r.timers holds them for a test to run.
    setTimeout: (fn, ms) => { timers.push([fn, ms]); return 1 },
    clearTimeout: () => {},
    Date,
    JSON,
    Error,
    String,
    document,
    // hanzo.team is not hanzo.ai, so the chain writes a host-only cookie here —
    // the Domain= attribute would be rejected and the cookie dropped outright.
    location: opts.location || { href: 'https://hanzo.team/', origin: 'https://hanzo.team', pathname: '/', hostname: 'hanzo.team', protocol: 'https:' },
    history: { pushState() {}, replaceState() {} },
    navigator: {
      globalPrivacyControl: opts.gpc === true,
      doNotTrack: opts.dnt,
      sendBeacon: opts.noBeacon
        ? undefined
        : (url, blob) => { sent.beacon.push({ url, body: blob.text, type: blob.type }); return true }
    },
    fetch: (url, init) => {
      sent.fetch.push({ url, init })
      if (opts.tags && url.indexOf('/v1/project/tags') !== -1) return Promise.resolve({ ok: true, json: () => Promise.resolve(opts.tags) })
      return { catch: () => {} }
    },
    addEventListener: (name, fn) => { (listeners[name] = listeners[name] || []).push(fn) },
    sessionStorage: {
      getItem: (k) => (session.has(k) ? session.get(k) : null),
      setItem: (k, v) => { if (opts.full) throw Object.assign(new Error('The quota has been exceeded.'), { name: 'QuotaExceededError' }); session.set(k, String(v)) }
    },
    localStorage: {
      getItem: (k) => (storage.has(k) ? storage.get(k) : null),
      // opts.full is a store that refuses every write, as a full or blocked one does.
      setItem: (k, v) => {
        if (opts.full) throw Object.assign(new Error('The quota has been exceeded.'), { name: 'QuotaExceededError' })
        storage.set(k, String(v))
      },
      removeItem: (k) => storage.delete(k)
    }
  }
  sandbox.window = sandbox
  vm.runInNewContext(source, sandbox)
  return { sent, sandbox, storage, session, jar, writes, loaded, document, fire, timers }
}

/** The anonymous id on the first event of the first transmission. */
function anonOf(r) {
  const body = r.sent.beacon.length ? r.sent.beacon[0].body : r.sent.fetch[0].init.body
  return JSON.parse(body).batch[0].anonymousId
}

// 1..12: core wire + identity checks.
{
  const r = run({})
  r.fire('pagehide')
  assert.deepStrictEqual(r.sent.beacon, [], 'keyless tag must not beacon')
  assert.deepStrictEqual(r.sent.fetch, [], 'keyless tag must not fetch')
  assert.strictEqual(r.sandbox.hanzo, undefined, 'keyless tag must not install a manual API')
}
{
  const r = run({ 'data-key': 'sk-live-deadbeef' })
  r.fire('pagehide')
  assert.deepStrictEqual(r.sent.beacon, [], 'sk- must not send')
  assert.deepStrictEqual(r.sent.fetch, [], 'sk- must not send')
}
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
{
  const r = run({ 'data-key': 'pk-live-abc' }, { noBeacon: true })
  r.fire('pagehide')
  const posts = r.sent.fetch.filter((f) => f.url.indexOf('/v1/event') !== -1)
  assert.strictEqual(posts.length, 1)
  const { url, init } = posts[0]
  assert.ok(url.includes('/v1/event?ingest_key=pk-live-abc'), 'key rides the query: ' + url)
  assert.strictEqual(init.keepalive, true)
  assert.ok(JSON.parse(init.body).batch.length === 1)
  assert.strictEqual(init.headers.authorization, undefined, 'a bearer would preflight the POST')
  assert.strictEqual(init.credentials, 'omit', 'the key is the credential; cookies must not ride')
}
{
  const r = run({}, { src: 'https://api.hanzo.ai/v1/event/tag.js?key=pk-live-xyz' })
  r.fire('pagehide')
  assert.strictEqual(r.sent.beacon.length, 1, 'src ?key= is honored')
}
{
  const id = '01920000-0000-7000-8000-0000000000ee'
  const r = run({ 'data-key': 'pk-live-abc' }, { jar: { 'iam-anon-id': id } })
  r.fire('pagehide')
  assert.strictEqual(anonOf(r), id, 'the cookie is the identity')
}
{
  const legacy = '01920000-0000-7000-8000-0000000000ff'
  const r = run({ 'data-key': 'pk-live-abc' }, { storage: { hz_id: legacy } })
  r.fire('pagehide')
  assert.strictEqual(anonOf(r), legacy, 'hz.js legacy id adopted')
  assert.strictEqual(r.jar.get('iam-anon-id'), legacy, 'carried onto the shared key')
  assert.strictEqual(r.storage.get('iam-anon-id'), legacy)
}
{
  const r = run({ 'data-key': 'pk-live-abc' })
  r.fire('pagehide')
  const id = anonOf(r)
  assert.ok(/^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab]/.test(id), 'v7 anon id: ' + id)
  assert.strictEqual(r.jar.get('iam-anon-id'), id, 'the cookie outlives the origin')
  assert.strictEqual(r.storage.get('iam-anon-id'), id)
  assert.ok(r.storage.has('hz_session'), 'hz_session')
  assert.ok(!r.jar.has('hz_session'), 'a session is not shared across surfaces')
}
{
  const r = run({ 'data-key': 'pk-live-abc' })
  r.sandbox.location.href = 'https://hanzo.team/inbox'
  r.sandbox.location.pathname = '/inbox'
  r.sandbox.history.pushState({}, '', '/inbox')
  r.sandbox.history.pushState({}, '', '/inbox')
  r.fire('pagehide')
  const batch = JSON.parse(r.sent.beacon[0].body).batch
  assert.strictEqual(batch.length, 2, 'initial + one SPA pageview')
  assert.strictEqual(batch[1].path, '/inbox')
}
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
{
  const r = run({ 'data-key': 'pk-live-abc' })
  assert.ok(
    r.sent.fetch.some((f) => f.url.indexOf('/v1/project/tags?key=pk-live-abc') !== -1),
    'keyed tag fetches /v1/project/tags for its browser pixels'
  )
  const keyless = run({})
  assert.ok(
    !keyless.sent.fetch.some((f) => f.url.indexOf('/v1/project/tags') !== -1),
    'keyless tag never fetches /v1/project/tags'
  )
}

// 12. EVERY SEND IS A CORS-SIMPLE REQUEST — beacon and fetch alike.
{
  const SAFELISTED = ['text/plain', 'application/x-www-form-urlencoded', 'multipart/form-data']
  const r = run({ 'data-key': 'pk-live-abc' })
  r.fire('pagehide')
  assert.strictEqual(r.sent.beacon.length, 1, 'one flush')
  assert.ok(
    SAFELISTED.indexOf(r.sent.beacon[0].type) !== -1,
    'beacon body must carry a CORS-safelisted type, got: ' + JSON.stringify(r.sent.beacon[0].type)
  )

  const f = run({ 'data-key': 'pk-live-abc' }, { noBeacon: true })
  f.fire('pagehide')
  const post = f.sent.fetch.filter((x) => x.url.indexOf('/v1/event') !== -1)[0]
  assert.ok(
    SAFELISTED.indexOf(post.init.headers['content-type']) !== -1,
    'fetch body must carry a CORS-safelisted type, got: ' + JSON.stringify(post.init.headers['content-type'])
  )
  assert.deepStrictEqual(
    Object.keys(post.init.headers),
    ['content-type'],
    'a second header would preflight the send: ' + JSON.stringify(post.init.headers)
  )
}

console.log('tag.js: 12/12 behavioral checks passed')

// 99. Visitor region governs third parties; first-party stream always sends.
{
  const settle = () => new Promise((r) => setImmediate(r))
  const rule = (mode, o = {}) => ({ region: '', mode, defaults: {}, refused: false, version: 3, floor: 2, ...o })
  const tags = (mode, o, more = []) => ({ tags: [{ platform: 'ga4', type: 'ga', id: 'G-TEST' }, { platform: 'meta', type: 'meta', id: '123' }, ...more], consent: rule(mode, o) })
  const events = (r) => r.sent.beacon.flatMap((b) => JSON.parse(b.body).batch)
  // What each tag was told, as values of this realm.
  const plain = (v) => JSON.parse(JSON.stringify(v))
  const layer = (r) => Array.from(r.sandbox.dataLayer || [], (a) => plain(Array.from(a)))
  const fbq = (r) => Array.from((r.sandbox.fbq && r.sandbox.fbq.queue) || [], (a) => plain(Array.from(a)))
  const consents = (r) => layer(r).filter((a) => a[0] === 'consent')
  const gaEvents = (r) => layer(r).filter((a) => a[0] === 'event').map((a) => a[1])
  const metaTracks = (r) => fbq(r).filter((a) => a[0] === 'track' || a[0] === 'trackCustom').map((a) => a[1])
  const fetched = (r, host) => r.loaded.filter((u) => u.includes(host)).length
  const grant = (r, c, rule = 'eu') => { r.jar.set('__Host-hz_consent', c); r.jar.set('__Host-hz_consent_v', '3'); r.jar.set('__Host-hz_consent_rule', rule); r.fire('hzconsent') }
  const OPT_OUT = ['consent', 'update', { ad_storage: 'denied', ad_user_data: 'denied', ad_personalization: 'denied' }]
  const LDU = ['dataProcessingOptions', ['LDU'], 0, 0]
  const check = async () => {
    for (const [what, opts] of [
      ['no choice', { choice: null }],
      ['a refusal', { choice: '' }],
      ['GPC', { gpc: true }],
      ['DNT', { dnt: '1' }],
      ["cloud's refusal", { tags: tags('eu', { refused: true }) }],
      ['a rule this tag does not know', { choice: null, tags: tags('opt-out') }]
    ]) {
      const r = run({ 'data-key': 'pk-live-abc' }, { tags: tags('eu'), ...opts })
      await settle(); await settle()
      r.sandbox.hanzo.identify('sub-1')
      r.sandbox.hanzo.track('signup_completed')
      r.fire('error', { error: new Error('boom') })
      r.fire('pagehide')
      const sent = events(r)
      assert.deepStrictEqual(sent.map((e) => e.type), ['pageview', 'identify', 'event', 'error'], 'eu, ' + what + ': the stream sends everything')
      for (const e of sent) {
        assert.ok(e.anonymousId && e.sessionId && e.distinctId, what + ': as the visitor, in their session')
        assert.strictEqual(e.properties.consent, '', what + ': and forwardable under nothing')
      }
      assert.strictEqual(sent[2].distinctId, 'sub-1', what + ': identify names the person')
      assert.ok(r.jar.has('iam-anon-id'), what + ': the anonymous id is kept')
      if (opts.gpc || opts.dnt || what === "cloud's refusal") {
        // A refusing browser loads no third party at all, Google Analytics included.
        assert.deepStrictEqual([r.loaded, layer(r), fbq(r)], [[], [], []], what + ': nothing third-party')
        continue
      }
      // Google Analytics cookieless, before anything else is said to it.
      assert.strictEqual(fetched(r, 'googletagmanager'), 1, what + ': Google Analytics loads')
      assert.deepStrictEqual(layer(r)[0], ['consent', 'default', { analytics_storage: 'denied', ad_storage: 'denied', ad_user_data: 'denied', ad_personalization: 'denied', wait_for_update: 500 }], what + ': all four denied first')
      assert.ok(consents(r).every((a) => Object.values(a[2]).every((v) => v !== 'granted')), what + ': and never granted')
      assert.deepStrictEqual(gaEvents(r), ['sign_up'], what + ': it hears events as cookieless pings')
      // Meta held: revoked before its init, no library, no PageView, no event.
      assert.deepStrictEqual(fbq(r)[0], ['consent', 'revoke'], what + ': Meta is revoked before anything else')
      assert.ok(fbq(r).findIndex((a) => a[0] === 'init' && a[1] === '123') > 0, what + ': then init')
      assert.strictEqual(fetched(r, 'fbevents'), 0, what + ': fbevents is not fetched')
      assert.deepStrictEqual(metaTracks(r), [], what + ': Meta hears nothing, not even the PageView')
    }
    {
      // eu: Performance upgrades Google Analytics to cookies; Marketing fetches fbevents,
      // grants Meta and sends one PageView, with no reload; Ads lifts Limited Data Use.
      const r = run({ 'data-key': 'pk-live-abc' }, { choice: null, tags: tags('eu') })
      let reloads = 0
      r.sandbox.location.reload = () => { reloads++ }
      await settle(); await settle()
      grant(r, 'analytics')
      assert.deepStrictEqual(consents(r).at(-1), ['consent', 'update', { analytics_storage: 'granted', ad_storage: 'denied', ad_user_data: 'denied', ad_personalization: 'denied' }])
      assert.strictEqual(fetched(r, 'fbevents'), 0, 'Performance alone leaves Meta held')
      grant(r, 'analytics,marketing,ads')
      assert.deepStrictEqual(consents(r).at(-1)[2], { analytics_storage: 'granted', ad_storage: 'granted', ad_user_data: 'granted', ad_personalization: 'granted' }, 'accept: all four granted')
      const q = fbq(r)
      assert.ok(q.findIndex((a) => a[0] === 'consent' && a[1] === 'grant') > q.findIndex((a) => a[0] === 'init'), 'Meta granted after its init')
      assert.deepStrictEqual(q.at(-1), ['track', 'PageView'], 'then its PageView')
      assert.deepStrictEqual(q.filter((a) => a[0] === 'dataProcessingOptions').at(-1), ['dataProcessingOptions', []], 'Limited Data Use lifted')
      assert.strictEqual(fetched(r, 'fbevents'), 1, 'fbevents once')
      r.sandbox.hanzo.track('purchase', { value: 1 })
      assert.deepStrictEqual(metaTracks(r), ['PageView', 'Purchase'], 'Meta hears events now')
      assert.strictEqual(reloads, 0, 'with no reload')
      r.fire('pagehide')
      assert.ok(events(r).every((e) => e.properties.consent === 'analytics,marketing,ads'), 'each event states the grants in force when it is sent')
    }
    {
      // eu: a withdrawal reaches Google and Meta at once, with no reload while only they
      // run; the third parties' cookies are forgotten, the stream keeps the visitor.
      const r = run({ 'data-key': 'pk-live-abc' }, { tags: tags('eu'), jar: { _ga: 'x', _ga_TEST: 'x', _fbp: 'x', _gcl_au: 'x' } })
      let reloads = 0
      r.sandbox.location.reload = () => { reloads++ }
      await settle(); await settle()
      assert.strictEqual(metaTracks(r).length, 1, 'a stored yes sends the PageView')
      const id = r.jar.get('iam-anon-id')
      grant(r, '')
      assert.deepStrictEqual(consents(r).at(-1)[2], { analytics_storage: 'denied', ad_storage: 'denied', ad_user_data: 'denied', ad_personalization: 'denied' }, 'Google hears all four denied')
      assert.deepStrictEqual(fbq(r).at(-1), ['consent', 'revoke'], 'Meta is revoked')
      assert.strictEqual(reloads, 0, 'no reload: both were told')
      for (const n of ['_ga', '_ga_TEST', '_fbp', '_gcl_au']) assert.ok(!r.jar.has(n), n + ' is forgotten')
      assert.strictEqual(r.jar.get('iam-anon-id'), id, 'the first-party visitor stays')
      r.sandbox.hanzo.track('purchase', { value: 1 })
      assert.strictEqual(metaTracks(r).length, 1, 'nothing reaches Meta after it')
      r.fire('pagehide')
      const buy = events(r).find((e) => e.event === 'purchase')
      assert.ok(buy && buy.properties.consent === '', 'the stream still has it, forwardable under nothing')
    }
    {
      // A platform that cannot be told is reloaded away.
      const r = run({ 'data-key': 'pk-live-abc' }, { tags: tags('eu', {}, [{ platform: 'tiktok', type: 'tiktok', id: 'TT1' }]) })
      let reloads = 0
      r.sandbox.location.reload = () => { reloads++ }
      await settle(); await settle()
      grant(r, 'analytics')
      assert.strictEqual(reloads, 1, 'TikTok withdrawn reloads the page')
    }
    for (const mode of ['us_privacy', 'global']) {
      // Everything runs at once, Consent Mode granted, Meta live; events reach both.
      const r = run({ 'data-key': 'pk-live-abc' }, { choice: null, session: {}, tags: tags(mode) })
      await settle(); await settle()
      assert.deepStrictEqual(layer(r)[0], ['consent', 'default', { analytics_storage: 'granted', ad_storage: 'granted', ad_user_data: 'granted', ad_personalization: 'granted' }], mode + ': granted by default')
      assert.ok(!fbq(r).some((a) => a[0] === 'consent' || a[0] === 'dataProcessingOptions'), mode + ': Meta is neither held nor limited')
      assert.strictEqual(fetched(r, 'fbevents'), 1, mode + ': fbevents is fetched')
      r.sandbox.hanzo.track('signup_completed')
      assert.deepStrictEqual([gaEvents(r), metaTracks(r)], [['sign_up'], ['PageView', 'CompleteRegistration']], mode + ': both hear it')
      r.fire('pagehide')
      // The load's view was queued before the rule arrived, and goes under it.
      assert.ok(events(r).every((e) => e.properties.consent === 'analytics,marketing,ads'), mode + ': forwardable under the defaults')
    }
    {
      // us_privacy: the opt-out is exactly LDU and the three ad flags denied, with no reload;
      // Google's ad cookies go, Google Analytics' and Meta's stay; opting back in lifts both.
      const r = run({ 'data-key': 'pk-live-abc' }, { choice: null, tags: tags('us_privacy', { region: 'US-CA' }), jar: { _ga: 'x', _fbp: 'x', _gcl_au: 'x' } })
      let reloads = 0
      r.sandbox.location.reload = () => { reloads++ }
      await settle(); await settle()
      grant(r, 'analytics,marketing')
      assert.deepStrictEqual(fbq(r).at(-1), LDU, 'exactly LDU (0, 0)')
      assert.deepStrictEqual(consents(r).at(-1), OPT_OUT, 'exactly the three ad flags denied')
      assert.strictEqual(reloads, 0, 'no reload')
      assert.ok(!fbq(r).some((a) => a[0] === 'consent'), 'Meta runs on')
      grant(r, 'analytics,marketing,ads')
      assert.deepStrictEqual(fbq(r).at(-1), ['dataProcessingOptions', []])
      assert.deepStrictEqual(consents(r).at(-1)[2], { ad_storage: 'granted', ad_user_data: 'granted', ad_personalization: 'granted' })
    }
    for (const [mode, opts] of [['us_privacy', { gpc: true }], ['us_privacy', { dnt: '1' }], ['global', { gpc: true }], ['us_privacy', { tags: tags('us_privacy', { refused: true }) }]]) {
      // A refusal outside eu loads no third party either, and forwards nothing.
      const r = run({ 'data-key': 'pk-live-abc' }, { choice: null, tags: tags(mode), ...opts })
      await settle(); await settle()
      r.sandbox.hanzo.track('purchase', { value: 1 })
      assert.deepStrictEqual([r.loaded, layer(r), fbq(r)], [[], [], []], mode + ': nothing third-party')
      r.fire('pagehide')
      assert.ok(events(r).every((e) => e.properties.consent === ''), mode + ': forwardable under nothing')
    }
    {
      // A stored us_privacy opt-out is exactly LDU and the three ad flags denied from the first call.
      const r = run({ 'data-key': 'pk-live-abc' }, { choice: null, jar: { '__Host-hz_consent': 'analytics,marketing', '__Host-hz_consent_v': '3', '__Host-hz_consent_rule': 'us_privacy' }, tags: tags('us_privacy') })
      await settle(); await settle()
      assert.deepStrictEqual(fbq(r).find((a) => a[0] === 'dataProcessingOptions'), LDU)
      assert.ok(fbq(r).findIndex((a) => a[0] === 'dataProcessingOptions') < fbq(r).findIndex((a) => a[0] === 'init'), 'before init')
      assert.deepStrictEqual(consents(r)[1], OPT_OUT)
    }
    {
      // M3: an opt-out made under us_privacy is no eu yes; M4: below a raised floor it keeps its refusal.
      const jar = { '__Host-hz_consent': 'analytics,marketing', '__Host-hz_consent_v': '3', '__Host-hz_consent_rule': 'us_privacy' }
      const eu = run({ 'data-key': 'pk-live-abc' }, { choice: null, jar, tags: tags('eu') })
      await settle(); await settle()
      assert.strictEqual(fetched(eu, 'fbevents'), 0, 'under eu it grants nothing')
      const raised = run({ 'data-key': 'pk-live-abc' }, { choice: null, jar, tags: tags('us_privacy', { version: 5, floor: 5 }) })
      await settle(); await settle()
      assert.deepStrictEqual(fbq(raised).find((a) => a[0] === 'dataProcessingOptions'), LDU, 'a raised floor keeps the opt-out')
    }
    {
      // A withdrawal that needs a reload waits while the sheet is open, and reloads once when it closes.
      const r = run({ 'data-key': 'pk-live-abc', 'data-banner': '' }, { choice: null, tags: tags('global', {}, [{ platform: 'tiktok', type: 'tiktok', id: 'TT1' }]) })
      let reloads = 0
      r.sandbox.location.reload = () => { reloads++ }
      await settle(); await settle()
      r.sandbox.hzConsent.open()
      r.jar.set('__Host-hz_consent', 'analytics'); r.jar.set('__Host-hz_consent_v', '3'); r.jar.set('__Host-hz_consent_rule', 'global')
      r.fire('pageshow')
      assert.strictEqual(reloads, 0, 'not while the sheet is open')
      r.fire('pageshow')
      const box = (n) => [n, ...(n.shadow ? box(n.shadow) : []), ...n.children.flatMap(box)]
      const x = r.document.body.children.flatMap(box).find((n) => n.tag === 'button' && n.attrs['aria-label'] === 'Close')
      x.click()
      assert.strictEqual(reloads, 1, 'once, when it closes')
    }
    {
      // Each gtag event is addressed to its own platform: Google Ads' never reaches Google Analytics.
      const r = run({ 'data-key': 'pk-live-abc' }, { choice: null, tags: tags('global', {}, [{ platform: 'google-ads', type: 'gads', id: 'AW-1' }]) })
      await settle(); await settle()
      r.sandbox.hanzo.track('purchase', { value: 1 })
      assert.deepStrictEqual(layer(r).filter((a) => a[0] === 'event').map((a) => a[2].send_to), [['G-TEST'], ['AW-1']])
    }
    {
      // global: Analytics saved off stops Google Analytics and reloads; Marketing off revokes Meta.
      const r = run({ 'data-key': 'pk-live-abc' }, { choice: null, tags: tags('global') })
      let reloads = 0
      r.sandbox.location.reload = () => { reloads++ }
      await settle(); await settle()
      grant(r, 'ads')
      assert.strictEqual(r.sandbox['ga-disable-G-TEST'], true, 'Google Analytics stops at once')
      assert.deepStrictEqual(fbq(r).at(-1), ['consent', 'revoke'], 'Meta is revoked')
      assert.strictEqual(reloads, 1, 'and the page reloads')
    }
    {
      // An event queued under a grant another tab withdrew goes under what is allowed now.
      const r = run({ 'data-key': 'pk-live-abc' }, { tags: tags('eu') })
      r.sandbox.location.reload = () => {}
      await settle(); await settle()
      r.sandbox.hanzo.track('checkout_started')
      r.jar.set('__Host-hz_consent', 'analytics')
      r.fire('pagehide')
      const e = events(r).find((x) => x.event === 'checkout_started')
      assert.ok(e && e.properties.consent === 'analytics', 'narrowed to what is still allowed: ' + (e && e.properties.consent))
    }
    {
      // eu: a browser that refuses now over a stored yes forgets what the third parties
      // kept, not the visitor.
      const r = run({ 'data-key': 'pk-live-abc' }, { gpc: true, choice: 'analytics,marketing', jar: { _ga: 'x', 'iam-anon-id': '01920000-0000-7000-8000-0000000000bb' }, tags: tags('eu') })
      await settle(); await settle()
      assert.ok(!r.jar.has('_ga'), "the third party's cookie is forgotten")
      assert.strictEqual(r.jar.get('iam-anon-id'), '01920000-0000-7000-8000-0000000000bb', 'the first-party visitor stays')
      assert.strictEqual(fetched(r, 'fbevents'), 0, 'and Meta stays held')
    }
    for (const [what, jar, held] of [
      ['a tossed bare cookie from a sibling subdomain', { hz_consent: 'analytics,marketing,ads', hz_consent_v: '3' }, true],
      ['a choice made under a version below the floor', { '__Host-hz_consent': 'analytics,marketing,ads', '__Host-hz_consent_v': '1' }, true],
      ['a choice made under version 2', { '__Host-hz_consent': 'analytics,marketing,ads', '__Host-hz_consent_v': '2' }, false]
    ]) {
      const r = run({ 'data-key': 'pk-live-abc' }, { choice: null, jar, tags: tags('eu') })
      await settle(); await settle()
      assert.strictEqual(fetched(r, 'fbevents') === 0, held, what + (held ? ' grants nothing' : ' still counts'))
    }
    {
      const jar = (c) => ({ '__Host-hz_consent': c, '__Host-hz_consent_v': '3', '__Host-hz_consent_rule': 'eu' })
      const r = run({ 'data-key': 'pk-live-abc', 'data-banner': '' }, { choice: null, jar: jar('analytics,marketing,ads'), tags: tags('eu', { version: 4, floor: 4 }) })
      await settle(); await settle()
      assert.strictEqual(fetched(r, 'fbevents'), 0, 'below a raised floor the grants are void')
      assert.ok(r.document.body.children.some((n) => 'data-hz-consent' in n.attrs), 'and the visitor is asked again')
      const none = run({ 'data-key': 'pk-live-abc', 'data-banner': '' }, { choice: null, jar: jar(''), tags: tags('eu', { version: 4, floor: 4 }) })
      await settle(); await settle()
      assert.ok(!none.document.body.children.some((n) => 'data-hz-consent' in n.attrs), 'a choice that allowed nothing is never asked again')
    }
    {
      // The rule is kept for the tab, and a tab with none waits for cloud's.
      const r = run({ 'data-key': 'pk-live-abc' }, { choice: null, session: {}, tags: tags('global', { region: 'JP' }) })
      await settle(); await settle()
      assert.strictEqual(JSON.parse(r.session.get('hz_consent_policy')).mode, 'global', 'the tab keeps the rule')
    }
  }
  check().then(() => console.log('consent ok'), (e) => { console.error(e); process.exit(1) })
}

// 101. data-banner draws the shadow-DOM consent banner and writes choices identically to @hanzo/event.
{
  const settle = () => new Promise((r) => setImmediate(r))
  const walk = (n) => [n, ...(n.shadow ? walk(n.shadow) : []), ...n.children.flatMap(walk)]
  const drawn = (r) => r.document.body.children.filter((n) => 'data-hz-consent' in n.attrs)
  const all = (r) => drawn(r).flatMap(walk)
  const buttons = (r) => all(r).filter((n) => n.tag === 'button' && n.attrs.class !== 'fold')
  const labels = (r) => buttons(r).map((b) => b.textContent)
  const press = (r, label) => buttons(r).find((n) => n.textContent === label).click()
  const inputs = (r) => all(r).filter((n) => n.tag === 'input')
  const folds = (r) => all(r).filter((n) => n.attrs.class === 'fold')
  const box = (r) => all(r).find((n) => n.attrs.role === 'region' || n.attrs.role === 'dialog')
  const links = (r) => all(r).filter((n) => n.tag === 'a').map((a) => [a.textContent, a.attrs.href])
  const intro = (r) => all(r).find((n) => n.tag === 'p' && n.textContent.startsWith('We use')).textContent
  const chose = (r) => decodeURIComponent(r.jar.get('__Host-hz_consent'))
  const written = (r) => r.writes.filter((w) => w.indexOf('hz_consent') !== -1)
  const linked = (r) => r.document.body.appendChild(r.document.createElement('a'))
  const policy = (consent) => ({ tags: [], consent: { region: '', mode: 'eu', defaults: {}, refused: false, version: 3, floor: 2, ...consent } })
  const BAR = ['×', 'Manage preferences', 'Accept all']
  const SHEET = ['×', 'Accept all', 'Save preferences']
  const INTRO = 'We use cookies and similar technologies to provide our services, measure performance, and support advertising. ' +
    'Essential cookies are required for the site to work and cannot be turned off. You can allow or reject optional cookies below.'
  const KEPT = 395 * 86400
  const at = (href) => {
    const u = new URL(href)
    return { href, origin: u.origin, pathname: u.pathname, hostname: u.hostname, protocol: u.protocol }
  }
  const check = async () => {
    {
      const r = run({ 'data-key': 'pk-live-abc' }, { choice: null })
      assert.deepStrictEqual(r.document.body.children, [], 'no data-banner draws nothing')
      assert.strictEqual(r.sandbox.hzConsent, undefined, 'and exposes nothing')
    }
    {
      const r = run({ 'data-key': 'pk-live-abc', 'data-banner': '' }, { choice: null })
      assert.strictEqual(drawn(r).length, 1, 'an unanswered visitor is asked')
      const host = drawn(r)[0]
      assert.ok(host.shadow, 'in a shadow root of its own')
      const style = host.shadow.children[0]
      assert.strictEqual(style.tag, 'style', 'styled from inside it')
      assert.ok(style.textContent.includes('prefers-color-scheme:dark'), 'dark and light')
      assert.ok(!/url\(|@import|@font-face/.test(style.textContent), 'nothing fetched')
      assert.ok(/border-radius:16px/.test(style.textContent) && /env\(safe-area-inset-bottom/.test(style.textContent), 'a rounded card inside the safe area')
      assert.ok(/button\{[^}]*font-size:13px[^}]*min-height:36px[^}]*border-radius:12px/.test(style.textContent), 'buttons: 13px text, 36px tall, a 12px radius inside the 16px card')
      assert.ok(/button:hover\{background:var\(--hover\)\}/.test(style.textContent) && /--hover:rgba\(255,255,255,\.08\)/.test(style.textContent), "hover is the theme's own surface, light and dark")
      assert.ok(/button\.go:hover\{background:color-mix\(in srgb,var\(--ink\)/.test(style.textContent), 'the primary shifts toward the card, never to an unrelated white')
      assert.ok(!/opacity/.test(style.textContent), 'nothing is drawn faded')
      assert.strictEqual(box(r).attrs.role, 'region', 'the bar is a region, not a dialog')
      assert.strictEqual(all(r).find((n) => n.tag === 'p').textContent,
        'Essential cookies keep this site running. With your OK, optional cookies help us measure performance and ads.')
      assert.deepStrictEqual(links(r), [], 'the bar links no policy')
      assert.deepStrictEqual(labels(r), BAR, 'no Reject button: × is the refusal')
      assert.deepStrictEqual(buttons(r).map((b) => b.attrs.class), ['x', '', 'go'], 'Manage preferences secondary, Accept all primary')
      assert.strictEqual(buttons(r)[0].attrs['aria-label'], 'Close and use essential cookies only', '× says what it chooses')
      assert.strictEqual(buttons(r)[0].attrs.title, 'Close and use essential cookies only', 'to the pointer too')
      assert.strictEqual(r.document.activeElement, null, 'it takes no focus')
      press(r, 'Accept all')
      assert.strictEqual(chose(r), 'analytics,marketing,ads')
      assert.deepStrictEqual(written(r), [
        '__Host-hz_consent=analytics%2Cmarketing%2Cads; Max-Age=' + KEPT + '; Path=/; SameSite=Lax; Secure',
        '__Host-hz_consent_v=3; Max-Age=' + KEPT + '; Path=/; SameSite=Lax; Secure',
        '__Host-hz_consent_rule=eu; Max-Age=' + KEPT + '; Path=/; SameSite=Lax; Secure'
      ], 'written as @hanzo/event setOwn writes it, beside the rule it was made under')
      assert.deepStrictEqual(drawn(r), [], 'and gone')
      r.fire('pagehide')
      assert.ok(JSON.parse(r.sent.beacon[0].body).batch.some((e) => e.type === 'pageview'), 'the view went, choice or none')
    }
    {
      // × keeps essential cookies only: it stores a refusal and forgets the third
      // parties' cookies, by exact name and numbered form, on this host and every domain
      // above it; nothing essential — the first-party analytics included — and nothing
      // merely spelled alike.
      const measured = {
        _ga: 'x', _ga_ABC123: 'x', _gid: 'x', _gat: 'x', _gat_UA_1: 'x', _fbp: 'x', _fbc: 'x', _gcl_au: 'x', _gac_UA_1: 'x', _uetsid: 'x', _uetvid: 'x',
        _ttp: 'x', ttcsid: 'x', ttcsid_C4ABC: 'x', li_fat_id: 'x', _pin_unauth: 'x', _epik: 'x', _derived_epik: 'x', _rdt_uuid: 'x', _rdt_cid: 'x', hz_touch: 'x',
        hz_cid: 'x'
      }
      const essential = {
        'iam-session': 's', hanzo_session: 's', casdoor_session_id: 's', hz_known: '1', csrf_token: 't', '__Host-csrf': 't', mine: 'keep',
        'iam-anon-id': '01920000-0000-7000-8000-0000000000cc', hz_internal: '1'
      }
      const alike = { _gateway_session: 's', _gatekeeper: 's', _uetoken: 's', ttcsid_app: 's', _gaming: 's' }
      const r = run({ 'data-key': 'pk-live-abc', 'data-banner': '' }, {
        choice: null, location: at('https://www.example.co.uk/'),
        jar: { ...measured, ...essential, ...alike }, storage: { hz_first_touch: '{}', hz_cohort: '1', hz_session: '{}', theirs: 'keep' }
      })
      press(r, '×')
      assert.strictEqual(chose(r), '', 'essential only is stored')
      assert.strictEqual(r.jar.get('__Host-hz_consent_v'), '3', 'beside its version')
      for (const n of Object.keys(measured)) assert.ok(!r.jar.has(n), n + ' is forgotten')
      for (const [n, v] of Object.entries({ ...essential, ...alike })) assert.strictEqual(r.jar.get(n), v, n + ' stays')
      for (const d of ['.www.example.co.uk', '.example.co.uk', '.co.uk']) assert.ok(r.writes.includes('_ga=; Max-Age=0; Path=/; Domain=' + d), '_ga forgotten on ' + d)
      assert.ok(!r.writes.some((w) => /^(iam-session|hanzo_session|casdoor_session_id|hz_known|csrf_token|__Host-csrf|_gateway_session|ttcsid_app)=; Max-Age=0/.test(w)), 'nothing essential or alike is ever expired')
      assert.ok(r.storage.has('hz_first_touch') && r.storage.has('hz_cohort') && r.storage.get('theirs') === 'keep', 'first-party storage stays')
      assert.deepStrictEqual(drawn(r), [])
    }
    {
      // Manage preferences opens Cookie Preferences, a modal dialog: the intro, the
      // site's own policies, a section per purpose, the strictly necessary always on.
      const r = run({ 'data-key': 'pk-live-abc', 'data-banner': '/privacy', 'data-cookies': '/cookies', 'data-terms': 'https://hanzo.team/legal/terms' }, { choice: null })
      const link = linked(r)
      link.focus()
      buttons(r).find((n) => n.textContent === 'Manage preferences').focus() // tabbed into the bar
      press(r, 'Manage preferences')
      const sheet = box(r)
      assert.strictEqual(sheet.attrs.role, 'dialog', 'the sheet is a dialog')
      assert.strictEqual(sheet.attrs['aria-modal'], 'true', 'a modal one')
      const title = all(r).find((n) => n.tag === 'h2')
      assert.strictEqual(title.textContent, 'Cookie Preferences')
      assert.strictEqual(sheet.attrs['aria-labelledby'], title.attrs.id, 'named by its title')
      assert.strictEqual(intro(r), INTRO + ' Learn more in our Cookie Policy, Privacy Policy, and Terms of Service.')
      assert.deepStrictEqual(links(r), [
        ['Cookie Policy', 'https://hanzo.team/cookies'], ['Privacy Policy', 'https://hanzo.team/privacy'], ['Terms of Service', 'https://hanzo.team/legal/terms']
      ], "the site's own policies, resolved on the page")
      assert.deepStrictEqual(folds(r).map((f) => f.textContent), [
        'Strictly Necessary Cookies', 'Performance Cookies', 'Marketing Cookies', 'Sell, Share, or Use Your Data for Targeted Advertising'
      ])
      const parts = all(r).filter((n) => n.attrs.class === 'section')
      assert.ok(parts[0].textContent.includes('Always Active') && !walk(parts[0]).some((n) => n.tag === 'input'), 'the strictly necessary have no switch')
      for (const w of ['sign-in', 'CSRF', 'load balancing', 'this choice', 'form', "this site's own first-party analytics", 'never sold or shared']) {
        assert.ok(parts[0].textContent.includes(w), 'strictly necessary covers ' + w)
      }
      assert.ok(parts[1].textContent.includes('Google Analytics'), 'Performance is Google Analytics')
      assert.ok(parts[2].textContent.includes('The Meta pixel'), 'Marketing is the Meta pixel')
      assert.ok(parts[3].textContent.includes('Google and Meta use your visit to choose the ads'), 'targeted ads is Google and Meta')
      const copy = parts.map((p) => p.textContent).join(' ')
      for (const w of ['LinkedIn', 'TikTok', 'Pinterest', 'Reddit', ' X ', 'aggregated', 'other ad platforms']) assert.ok(!copy.includes(w), 'no ' + w)
      const boxes = inputs(r)
      assert.deepStrictEqual(boxes.map((b) => [b.attrs.role, b.attrs['aria-label'], b.checked, b.disabled]), [
        ['switch', 'Performance Cookies', false, false], ['switch', 'Marketing Cookies', false, false],
        ['switch', 'Sell, Share, or Use Your Data for Targeted Advertising', false, false]
      ], 'one switch per optional purpose, nothing on')
      const bodies = all(r).filter((n) => n.attrs.class === 'body')
      assert.ok(bodies.every((b) => b.hidden) && folds(r).every((f) => f.attrs['aria-expanded'] === 'false'), 'sections start folded')
      folds(r)[1].click()
      assert.strictEqual(folds(r)[1].attrs['aria-expanded'], 'true')
      assert.strictEqual(bodies[1].hidden, false, 'a chevron unfolds its section')
      assert.strictEqual(folds(r)[1].attrs['aria-controls'], bodies[1].attrs.id)
      assert.ok(walk(folds(r)[1]).some((n) => n.attrs.class === 'chev' && n.attrs['aria-hidden'] === 'true'), 'with a chevron')
      folds(r)[1].click()
      assert.strictEqual(bodies[1].hidden, true, 'and folds it again')
      assert.deepStrictEqual(labels(r), SHEET, 'no Reject button in the sheet either')
      assert.strictEqual(buttons(r)[2].attrs.class, 'go', 'Save preferences is the primary')
      assert.strictEqual(buttons(r)[0].attrs['aria-label'], 'Close', 'the × is named')
      assert.strictEqual(buttons(r)[0].attrs.title, 'Close')
      assert.strictEqual(r.document.focused, buttons(r)[0], 'the sheet takes focus when asked for')
      // Tab stays inside: past the last control it wraps to the first, and back.
      const save = buttons(r)[2]
      save.focus()
      let e = sheet.key('Tab')
      assert.ok(e.prevented && r.document.focused === buttons(r)[0], 'Tab from the last control wraps to ×')
      e = sheet.key('Tab', true)
      assert.ok(e.prevented && r.document.focused === save, 'Shift+Tab from × wraps to the last')
      boxes[0].focus()
      assert.strictEqual(sheet.key('Tab').prevented, false, 'Tab between controls is the browser\'s own')
      boxes[0].checked = true
      press(r, 'Save preferences')
      assert.strictEqual(chose(r), 'analytics', 'Performance Cookies alone')
      assert.deepStrictEqual(drawn(r), [])
      assert.strictEqual(r.document.activeElement, link, 'focus goes back to what held it before the banner')
    }
    for (const [attrs, tail, hrefs] of [
      [{}, '', []],
      [{ 'data-banner': 'javascript:alert(1)' }, '', []],
      [{ 'data-banner': '/privacy' }, ' Learn more in our Privacy Policy.', ['https://hanzo.team/privacy']],
      [{ 'data-banner': '/privacy', 'data-terms': '/terms' }, ' Learn more in our Privacy Policy and Terms of Service.', ['https://hanzo.team/privacy', 'https://hanzo.team/terms']]
    ]) {
      // A customer's sheet links the policies it names and no one else's.
      const r = run({ 'data-key': 'pk-live-abc', 'data-banner': '', ...attrs }, { choice: null })
      press(r, 'Manage preferences')
      assert.strictEqual(intro(r), INTRO + tail, JSON.stringify(attrs))
      assert.deepStrictEqual(links(r).map((l) => l[1]), hrefs, JSON.stringify(attrs) + ' links nothing it did not name')
    }
    {
      // A stored choice is not asked again; a page's own link reopens it, Escape or ×
      // lets it go, and focus goes back where it was.
      const r = run({ 'data-key': 'pk-live-abc', 'data-banner': '' }, { choice: 'analytics' })
      assert.deepStrictEqual(drawn(r), [], 'a visitor who chose is not asked')
      const link = linked(r)
      link.focus()
      r.sandbox.hzConsent.open()
      assert.deepStrictEqual(inputs(r).map((b) => b.checked), [true, false, false], 'the stored choice')
      box(r).key('Escape')
      assert.deepStrictEqual(drawn(r), [], 'Escape closes the sheet')
      assert.strictEqual(r.document.activeElement, link, 'and hands focus back')
      r.sandbox.hzConsent.open()
      press(r, '×')
      assert.deepStrictEqual(drawn(r), [], '× closes it')
      assert.strictEqual(chose(r), 'analytics', 'and changes nothing')
      assert.strictEqual(r.document.activeElement, link)
      r.sandbox.hzConsent.open()
      inputs(r)[1].checked = true
      press(r, 'Save preferences')
      assert.strictEqual(chose(r), 'analytics,marketing')
      assert.strictEqual(r.document.activeElement, link, 'focus goes back')
      r.fire('hzconsent-open')
      assert.strictEqual(inputs(r).length, 3, "@hanzo/ui's ConsentLink opens it too")
      press(r, 'Accept all')
      assert.strictEqual(chose(r), 'analytics,marketing,ads', 'Accept all from the sheet')
    }
    for (const leave of [(r) => box(r).key('Escape'), (r) => press(r, '×')]) {
      // Closing the sheet an unanswered visitor opened goes back to the bar.
      const r = run({ 'data-key': 'pk-live-abc', 'data-banner': '' }, { choice: null })
      press(r, 'Manage preferences')
      leave(r)
      assert.deepStrictEqual(labels(r), BAR, 'still asked')
      assert.strictEqual(r.jar.has('__Host-hz_consent'), false, 'and nothing stored')
    }
    {
      // A stored yes withdrawn by GPC forgets what it kept, and nothing essential.
      const r = run({ 'data-key': 'pk-live-abc', 'data-banner': '' }, { choice: 'analytics,marketing', gpc: true, jar: { _ga: 'x', _fbp: 'x', hz_known: '1', 'iam-session': 's' }, tags: policy({}) })
      await settle(); await settle()
      assert.ok(!r.jar.has('_ga') && !r.jar.has('_fbp'), 'measurement forgotten')
      assert.ok(r.jar.get('hz_known') === '1' && r.jar.get('iam-session') === 's' && r.jar.get('__Host-hz_consent') === 'analytics,marketing', 'essentials stay')
    }
    {
      // GPC: not asked; the sheet opens locked off, and saving stores a refusal.
      const r = run({ 'data-key': 'pk-live-abc', 'data-banner': '' }, { choice: null, gpc: true })
      assert.deepStrictEqual(drawn(r), [], 'a refusing browser is not asked')
      r.sandbox.hzConsent.open()
      assert.ok(inputs(r).every((b) => b.disabled && !b.checked), 'locked off')
      assert.deepStrictEqual(labels(r), ['×', 'Save preferences'], 'with nothing to accept')
      assert.ok(all(r).some((n) => n.tag === 'p' && n.textContent.includes('asks sites not to track you')), 'and says why')
      inputs(r)[0].checked = true // a script ticks a locked switch
      press(r, 'Save preferences')
      assert.strictEqual(chose(r), '', 'a refusal is stored, whatever was ticked')
    }
    {
      // A policy from cloud whose floor is past the choice's version asks again.
      const r = run({ 'data-key': 'pk-live-abc', 'data-banner': '' }, { choice: 'analytics', tags: policy({ version: 4, floor: 3 }) })
      assert.deepStrictEqual(drawn(r), [], 'before cloud answers the choice stands')
      await settle(); await settle()
      assert.deepStrictEqual(labels(r), BAR, 'a new version is asked again')
      press(r, 'Accept all')
      assert.strictEqual(r.jar.get('__Host-hz_consent_v'), '4', 'under the version in force')
    }
    {
      // A strict style-src: the <style> carries the script's nonce, and a browser with
      // constructed stylesheets is given one instead of a <style>.
      const r = run({ 'data-key': 'pk-live-abc', 'data-banner': '' }, { choice: null, nonce: 'n0nce' })
      assert.strictEqual(drawn(r)[0].shadow.children[0].attrs.nonce, 'n0nce', "the <style> carries the script's nonce")
      const c = run({ 'data-key': 'pk-live-abc', 'data-banner': '' }, { choice: null, constructable: true })
      const root = drawn(c)[0].shadow
      assert.ok(!root.children.some((n) => n.tag === 'style'), 'no <style> where a sheet can be constructed')
      assert.ok(root.adoptedStyleSheets.length === 1 && /border-radius:16px/.test(root.adoptedStyleSheets[0].text), 'the constructed sheet holds the style')
    }
    {
      // A browser with no shadow DOM gets no banner, and the rest of the tag.
      const r = run({ 'data-key': 'pk-live-abc', 'data-banner': '' }, { choice: null, noShadow: true })
      assert.deepStrictEqual(r.document.body.children, [], 'no banner')
      assert.strictEqual(r.sandbox.hzConsent, undefined)
      r.fire('pagehide')
      assert.ok(r.sent.beacon.length && JSON.parse(r.sent.beacon[0].body).batch.some((e) => e.type === 'pageview'), 'the stream still goes')
    }
    {
      // A page that takes the bar out (a framework re-rendering body) has it back.
      const r = run({ 'data-key': 'pk-live-abc', 'data-banner': '' }, { choice: null })
      drawn(r)[0].remove()
      r.fire('visibilitychange')
      assert.deepStrictEqual(labels(r), BAR, 'drawn again')
    }
    {
      // A plain-http page keeps the bare names, without Secure.
      const r = run({ 'data-key': 'pk-live-abc', 'data-banner': '' }, { choice: null, location: at('http://localhost.test/') })
      press(r, 'Accept all')
      assert.deepStrictEqual(written(r), [
        'hz_consent=analytics%2Cmarketing%2Cads; Max-Age=' + KEPT + '; Path=/; SameSite=Lax',
        'hz_consent_v=3; Max-Age=' + KEPT + '; Path=/; SameSite=Lax',
        'hz_consent_rule=eu; Max-Age=' + KEPT + '; Path=/; SameSite=Lax'
      ])
    }
    {
      // us_privacy: no bar, and "Your Privacy Choices" with the opt-out icon at the foot of
      // the page, opening the sheet: what runs by default, a switch each, Ads the opt-out.
      const r = run({ 'data-key': 'pk-live-abc', 'data-banner': '' }, { choice: null, session: {}, tags: policy({ mode: 'us_privacy', region: 'US-CA' }) })
      assert.deepStrictEqual(r.document.body.children, [], 'nothing drawn before the rule is known')
      await settle(); await settle()
      assert.deepStrictEqual(drawn(r), [], 'no bar')
      const foot = r.document.body.children.filter((n) => 'data-hz-consent-foot' in n.attrs)
      assert.strictEqual(foot.length, 1, 'the link, once')
      const nodes = walk(foot[0])
      const b = nodes.find((n) => n.tag === 'button')
      assert.strictEqual(b.textContent, 'Your Privacy Choices')
      const svg = nodes.find((n) => n.tag === 'svg')
      assert.ok(svg && svg.ns === 'http://www.w3.org/2000/svg' && svg.children.filter((n) => n.tag === 'path').length === 4 && svg.attrs['aria-hidden'] === 'true', 'with the opt-out icon')
      assert.ok(!nodes.some((n) => /do not sell/i.test(n.text)), 'never the Do Not Sell sentence')
      b.click()
      assert.strictEqual(all(r).find((n) => n.tag === 'h2').textContent, 'Your Privacy Choices')
      assert.deepStrictEqual(labels(r), ['×', 'Save preferences'], 'all is on already: nothing to accept')
      assert.deepStrictEqual(inputs(r).map((i) => [i.checked, i.disabled]), [[true, false], [true, false], [true, false]], 'every switch on, each free')
      const parts = all(r).filter((n) => n.attrs.class === 'section')
      assert.ok(parts[1].textContent.includes('On by default') && parts[1].textContent.includes('Google Analytics'))
      assert.ok(parts[2].textContent.includes('On by default') && parts[2].textContent.includes('Meta pixel'))
      assert.ok(parts[3].textContent.includes('opt out of the sale or sharing'), 'Ads off is the opt-out')
      inputs(r)[2].checked = false
      press(r, 'Save preferences')
      assert.strictEqual(chose(r), 'analytics,marketing', 'the opt-out is stored')
      assert.strictEqual(r.document.body.children.filter((n) => 'data-hz-consent-foot' in n.attrs).length, 1, 'and the link stays')
    }
    {
      // A page that carries its own link keeps it, and the tag adds none.
      const r = run({ 'data-key': 'pk-live-abc', 'data-banner': '' }, { choice: null, tags: policy({ mode: 'us_privacy' }) })
      const own = r.document.body.appendChild(r.document.createElement('a'))
      own.setAttribute('data-hz-consent-link', '')
      await settle(); await settle()
      assert.deepStrictEqual(r.document.body.children.filter((n) => 'data-hz-consent-foot' in n.attrs), [], 'no second link')
    }
    {
      // us_privacy under GPC: nothing runs, and every switch is locked off.
      const r = run({ 'data-key': 'pk-live-abc', 'data-banner': '' }, { choice: null, gpc: true, tags: policy({ mode: 'us_privacy' }) })
      await settle(); await settle()
      r.sandbox.hzConsent.open()
      assert.deepStrictEqual(inputs(r).map((i) => [i.checked, i.disabled]), [[false, true], [false, true], [false, true]])
      assert.ok(all(r).some((n) => n.tag === 'p' && n.textContent.includes('asks sites not to track you')), 'and says why')
    }
    {
      // global: the notice, closed for good by ×, which grants nothing and stores no choice.
      const r = run({ 'data-key': 'pk-live-abc', 'data-banner': '' }, { choice: null, tags: policy({ mode: 'global', region: 'JP' }) })
      await settle(); await settle()
      assert.strictEqual(all(r).find((n) => n.tag === 'p').textContent, 'We use cookies to improve your experience. By continuing to browse, you agree.')
      assert.deepStrictEqual(labels(r), ['×', 'Manage preferences'])
      assert.deepStrictEqual(r.document.body.children.filter((n) => 'data-hz-consent-foot' in n.attrs), [], 'the link is us_privacy\'s alone')
      press(r, '×')
      assert.deepStrictEqual(drawn(r), [], 'closed')
      assert.strictEqual(r.jar.get('__Host-hz_consent_notice'), '1', 'and remembered')
      assert.ok(!r.jar.has('__Host-hz_consent'), 'no choice is stored')
      const again = run({ 'data-key': 'pk-live-abc', 'data-banner': '' }, { choice: null, jar: { '__Host-hz_consent_notice': '1' }, tags: policy({ mode: 'global' }) })
      await settle(); await settle()
      assert.deepStrictEqual(drawn(again), [], 'a closed notice stays closed')
      again.sandbox.hzConsent.open()
      assert.strictEqual(all(again).find((n) => n.tag === 'h2').textContent, 'Your Privacy Choices', 'the sheet is still there')
      const gpc = run({ 'data-key': 'pk-live-abc', 'data-banner': '' }, { choice: null, gpc: true, tags: policy({ mode: 'global' }) })
      await settle(); await settle()
      assert.deepStrictEqual(drawn(gpc), [], 'a refusing browser is not told')
    }
    {
      // A tab with no rule and no answer draws nothing until 1.5 s, then eu's bar.
      const r = run({ 'data-key': 'pk-live-abc', 'data-banner': '' }, { choice: null, session: {} })
      assert.deepStrictEqual(r.document.body.children, [], 'nothing yet')
      r.timers.find((t) => t[1] === 1500)[0]()
      assert.deepStrictEqual(labels(r), BAR, 'eu, the strict answer')
      assert.deepStrictEqual(r.document.body.children.filter((n) => 'data-hz-consent-foot' in n.attrs), [], 'with no link')
      const us = run({ 'data-key': 'pk-live-abc', 'data-banner': '' }, { choice: null, session: {}, tags: policy({ mode: 'us_privacy' }) })
      await settle(); await settle()
      assert.deepStrictEqual(drawn(us), [], 'us_privacy asks nothing')
    }
  }
  check().then(() => console.log('banner ok'), (e) => { console.error(e); process.exit(1) })
}

// 102. Storage/cookie errors never escape into the host's load or history router.
{
  for (const choice of [undefined, null]) {
    let r
    assert.doesNotThrow(() => { r = run({ 'data-key': 'pk-live-abc' }, { choice, full: true }) }, 'the load survives a full store')
    r.sandbox.location.href = 'https://hanzo.team/a'
    r.sandbox.location.pathname = '/a'
    assert.doesNotThrow(() => r.sandbox.history.pushState({}, '', '/a'), 'pushState survives it')
    r.sandbox.location.href = 'https://hanzo.team/b'
    r.sandbox.location.pathname = '/b'
    assert.doesNotThrow(() => r.sandbox.history.replaceState({}, '', '/b'), 'replaceState survives it')
  }
  {
    // Whatever throws inside the tag while the host navigates stays inside the tag.
    const r = run({ 'data-key': 'pk-live-abc' })
    r.document.blocked = true
    r.sandbox.location.href = 'https://hanzo.team/c'
    r.sandbox.location.pathname = '/c'
    assert.doesNotThrow(() => r.sandbox.history.pushState({}, '', '/c'), "a throw in the tag never reaches the host's pushState")
  }
  {
    // The views still go, with the session kept on the page.
    const r = run({ 'data-key': 'pk-live-abc' }, { full: true })
    r.fire('pagehide')
    assert.ok(r.sent.beacon.length && JSON.parse(r.sent.beacon[0].body).batch.some((e) => e.type === 'pageview' && e.sessionId), 'the view goes, with a session')
  }
  console.log('router ok')
}

// 103. Multiple Meta pixels fetch fbevents once, init each in order, and send one PageView/event per moment.
{
  const settle = () => new Promise((r) => setImmediate(r))
  const config = (ids) => ({ tags: ids.map((id) => ({ platform: 'meta', type: 'meta', id })), consent: { region: '', mode: 'eu', defaults: {}, refused: false, version: 3, floor: 2 } })
  const calls = (r) => Array.from(r.sandbox.fbq.queue, (a) => Array.from(a)) // arrays of this realm
  const check = async () => {
    for (const ids of [['1790308611893001', '758250658033751', '366194547064903'], ['123']]) {
      const r = run({ 'data-key': 'pk-live-abc' }, { choice: 'analytics,marketing', tags: config(ids) })
      await settle(); await settle()
      assert.strictEqual(r.loaded.filter((u) => u.includes('fbevents')).length, 1, ids.length + ': fbevents loads once')
      const q = calls(r)
      assert.deepStrictEqual(q.filter((a) => a[0] === 'init').map((a) => a[1]), ids, ids.length + ': each pixel is init\'ed, in order')
      assert.strictEqual(q.filter((a) => a[0] === 'track' && a[1] === 'PageView').length, 1, ids.length + ': one PageView reaches them all')
      assert.ok(q.findIndex((a) => a[0] === 'track') > q.map((a) => a[0]).lastIndexOf('init'), 'after the last init')
      assert.strictEqual(q.filter((a) => a[0] === 'dataProcessingOptions').length, 1, 'Limited Data Use, once, before the first init')
      assert.ok(q.findIndex((a) => a[0] === 'dataProcessingOptions') < q.findIndex((a) => a[0] === 'init'))
      r.sandbox.hanzo.track('purchase', { value: 1 })
      const buys = calls(r).filter((a) => a[0] === 'track' && a[1] === 'Purchase')
      assert.strictEqual(buys.length, 1, ids.length + ': one track per event')
      assert.ok(buys[0][3] && buys[0][3].eventID, 'with the eventID the server dedupes on')
      r.fire('hzconsent')
      assert.strictEqual(calls(r).filter((a) => a[0] === 'init').length, ids.length, 'a second look inits nobody again')
    }
  }
  check().then(() => console.log('meta ok'), (e) => { console.error(e); process.exit(1) })
}

// 104. OPENAI PIXEL (oaiq) + REDDIT PIXEL (rdt): loads oaiq.min.js under Marketing,
//      queues arguments in oaiq.q, inits { pixelId }, and measures checkout_started
//      with { type: 'contents' } and shared event_id.
{
  const settle = () => new Promise((r) => setImmediate(r))
  const plain = (v) => JSON.parse(JSON.stringify(v))
  const cfg = {
    tags: [
      { platform: 'openai', type: 'openai', id: 'TXKHZduRxZZGp4B1ysfiwm' },
      { platform: 'reddit', type: 'reddit', id: 'a2_k2sajv4zerym' }
    ],
    consent: { region: '', mode: 'eu', defaults: {}, refused: false, version: 3, floor: 2 }
  }
  const check = async () => {
    const r = run({ 'data-key': 'pk-live-abc' }, { choice: 'analytics,marketing', tags: cfg })
    await settle(); await settle()
    assert.ok(r.loaded.includes('https://bzrcdn.openai.com/sdk/oaiq.min.js'), 'oaiq sdk loaded')
    assert.ok(r.loaded.includes('https://www.redditstatic.com/ads/pixel.js'), 'reddit sdk loaded')
    const oaiq = () => Array.from(r.sandbox.oaiq.q, (a) => plain(Array.from(a)))
    assert.deepStrictEqual(oaiq(), [['init', { pixelId: 'TXKHZduRxZZGp4B1ysfiwm' }]])
    r.sandbox.hanzo.track('checkout_started', { value: 20, currency: 'USD' })
    r.sandbox.hanzo.track('order_completed', { value: 49, currency: 'USD' })
    r.fire('pagehide')
    const sent = r.sent.beacon.flatMap((b) => JSON.parse(b.body).batch).filter((e) => e.type === 'event')
    assert.deepStrictEqual(oaiq().slice(1), [
      ['measure', 'checkout_started', { type: 'contents', value: 20, currency: 'USD', event_id: sent[0].properties.event_id }],
      ['measure', 'purchase', { value: 49, currency: 'USD', event_id: sent[1].properties.event_id }]
    ])
    const bridge = run({ 'data-key': 'pk-live-abc', 'data-tags-only': '1' }, { choice: 'analytics,marketing', tags: cfg })
    await settle(); await settle()
    assert.strictEqual(bridge.sent.beacon.length, 0, 'tags-only mode emits no duplicate /v1/event beacons')
    assert.strictEqual(bridge.sandbox.__hanzoTags.reach(), 'openai,reddit')
    bridge.sandbox.__hanzoTags.mirror('checkout_started', { value: 20, currency: 'USD' }, 'evt-bridge-1')
    assert.deepStrictEqual(Array.from(bridge.sandbox.oaiq.q, (a) => plain(Array.from(a))).slice(-1)[0], ['measure', 'checkout_started', { type: 'contents', value: 20, currency: 'USD', event_id: 'evt-bridge-1' }])
  }
  check().then(() => console.log('openai ok'), (e) => { console.error(e); process.exit(1) })
}
