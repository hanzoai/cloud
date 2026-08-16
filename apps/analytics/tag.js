/*! Hanzo event tag — one paste, one wire.
 *
 *   <script defer src="https://api.hanzo.ai/v1/event.js" data-key="pk-…"></script>
 *
 * Autocaptures pageviews (initial + SPA) and uncaught errors onto the canonical
 * {batch:[…]} wire at /v1/event. data-product and data-key are the only knobs.
 *
 * NO KEY ⇒ INERT. A keyless beacon is accepted 200 into $public, a reserved
 * tenant the owning org cannot read — a silence that looks like success. Sending
 * nothing is the honest failure, and it is the one this tag picks.
 *
 * `hzAnonId` is not defined here: it comes from anon.js, the shared identity
 * chain vendored from @hanzo/event, which tag.go serves ahead of this file
 * inside one wrapper. Both halves are the asset; neither runs alone.
 */
(function () {
  if (window.__hanzoEvent) return

  var el = document.currentScript
  if (!el || !el.src) return
  var src
  try {
    src = new URL(el.src)
  } catch (e) {
    return
  }

  var key = (el.getAttribute('data-key') || src.searchParams.get('key') || '').trim()
  if (key.indexOf('pk-') !== 0) return

  var url = src.origin + '/v1/event'
  var product = (el.getAttribute('data-product') || '').trim()

  // Identity is hzAnonId, the ONE chain — anon.js, vendored verbatim from
  // @hanzo/event and served ahead of this file by tag.go. Resolving it here as
  // well is what made an origin carrying only this tag a separate population:
  // this read localStorage alone, so it never saw the cookie the other two
  // clients share and never adopted the id hz.js left behind.
  //
  // The session keeps its own resolution: a session is deliberately origin-local
  // and 30-minute idle-bounded, and nothing about it crosses a surface.
  var SESSION = 'hz_session'
  var SESSION_TTL = 30 * 60 * 1000

  function uid() {
    try {
      return crypto.randomUUID()
    } catch (e) {}
    return 'a-' + Date.now().toString(36) + Math.random().toString(36).slice(2, 10)
  }

  function store() {
    try {
      return window.localStorage
    } catch (e) {} // Safari private mode / blocked storage
  }

  function sessionId() {
    var s = store()
    if (!s) return ''
    var now = Date.now()
    var st = null
    try {
      st = JSON.parse(s.getItem(SESSION) || 'null')
    } catch (e) {}
    if (!st || now - st.last > SESSION_TTL) st = { id: uid(), last: now }
    else st.last = now
    s.setItem(SESSION, JSON.stringify(st))
    return st.id
  }

  var queue = []
  var timer = null
  var person = ''

  function flush(beacon) {
    if (timer) {
      clearTimeout(timer)
      timer = null
    }
    if (!queue.length) return
    var body = JSON.stringify({ batch: queue })
    queue = []
    // sendBeacon cannot set a header, so the key rides the query — the carrier
    // publishable.go ingestKey already reads.
    //
    // The body is text/plain because that type is CORS-safelisted, which makes the
    // POST a SIMPLE request: no preflight, and an unloading document gets no second
    // round trip. This tag runs on a customer's own domain, so the beacon is always
    // cross-origin. handle reads the raw body and dispatches on its first non-space
    // byte, so the type names the CORS class and nothing else.
    if (beacon && navigator.sendBeacon) {
      try {
        var blob = new Blob([body], { type: 'text/plain' })
        if (navigator.sendBeacon(url + '?ingest_key=' + encodeURIComponent(key), blob)) return
      } catch (e) {}
    }
    try {
      // No credentials mode. The publishable key IS the credential, and asking
      // for cookies costs the send: a credentialed cross-origin request is only
      // read once the response carries Access-Control-Allow-Credentials, which
      // is granted to exact first-party origins alone. This tag's whole job is
      // to run on a customer's own domain, which is not one of them — the
      // preflight would fail and every event would be dropped, on their site
      // and on ours. Cookies also have no business riding along on a page that
      // is not ours.
      fetch(url, {
        method: 'POST',
        keepalive: true,
        headers: { 'content-type': 'application/json', authorization: 'Bearer ' + key },
        body: body
      }).catch(noop)
    } catch (e) {}
  }

  function noop() {}

  // Every event carries url — the warehouse derives host from it
  // (event.fact.host DEFAULT domain(url)), so an event without one is a row
  // nobody can attribute to a site.
  function push(ev) {
    var anon = hzAnonId()
    ev.messageId = uid()
    ev.timestamp = new Date().toISOString()
    ev.distinctId = person || anon
    ev.anonymousId = anon
    ev.sessionId = sessionId()
    ev.url = location.href
    ev.path = location.pathname
    ev.referrer = document.referrer || ''
    if (product) ev.product = product
    queue.push(ev)
    if (queue.length >= 20) flush(false)
    else if (!timer) timer = setTimeout(function () { flush(false) }, 5000)
  }

  // event is left empty on pageview/error: resolveEventName (capture.go) names
  // them $pageview/$error server-side, so naming lives in one place.
  function page() { push({ type: 'pageview' }) }

  function track(name, props) {
    if (!name) return
    push({ type: 'event', event: String(name), properties: props || {} })
  }

  function identify(id, traits) {
    person = id ? String(id) : person
    push({ type: 'identify', properties: traits || {} })
  }

  function error(e, handled) {
    var err = e instanceof Error ? e : new Error(String(e && e.message ? e.message : e))
    push({
      type: 'error',
      error: {
        type: err.name || 'Error',
        message: err.message || 'Unknown error',
        stack: err.stack || '',
        handled: !!handled
      }
    })
  }

  // SPA navigation: history is patched because pushState fires no event.
  var href = location.href
  function navigated() {
    if (location.href === href) return
    href = location.href
    page()
  }
  var push_ = history.pushState
  var replace_ = history.replaceState
  history.pushState = function () {
    push_.apply(this, arguments)
    navigated()
  }
  history.replaceState = function () {
    replace_.apply(this, arguments)
    navigated()
  }
  addEventListener('popstate', navigated)
  addEventListener('hashchange', navigated)

  addEventListener('error', function (e) { error(e.error || e.message, false) })
  addEventListener('unhandledrejection', function (e) { error(e.reason, false) })

  // pagehide is the one unload signal that fires on mobile Safari; the
  // visibilitychange flush covers a tab backgrounded and never returned to.
  addEventListener('pagehide', function () { flush(true) })
  addEventListener('visibilitychange', function () {
    if (document.visibilityState === 'hidden') flush(true)
  })

  window.__hanzoEvent = true
  window.hanzo = { track: track, identify: identify, page: page, error: error, flush: flush }

  // ── config-driven tag injection (the hosted twin of track.js) ─────────────
  // Fetch the SITE's connected browser pixels (/v1/tags, dual-resolved per site by
  // this key or the host) and inject them first-party. Every event then also fires
  // the native pixel — translated through the SAME taxonomy the server-side CAPI uses
  // (destinations/translate.go), stamped with a shared event_id carried on the
  // /v1/event row too — so the browser pixel and the server CAPI DEDUPLICATE instead
  // of double-counting. Keyless already returned above, so this only runs when keyed.
  var STD = {
    $pageview: 'page_view', pricing_viewed: 'view_content', signup_viewed: 'view_content',
    product_viewed: 'view_content', plan_clicked: 'lead', signup_submitted: 'lead',
    waitlist_joined: 'lead', referral_used: 'lead', signup_completed: 'signup',
    product_added: 'add_to_cart', add_to_cart: 'add_to_cart', checkout_started: 'start_checkout',
    begin_checkout: 'start_checkout', order_completed: 'purchase', purchase: 'purchase'
  }
  var NATIVE = {
    ga: { page_view: 'page_view', view_content: 'view_item', add_to_cart: 'add_to_cart', lead: 'generate_lead', signup: 'sign_up', start_checkout: 'begin_checkout', purchase: 'purchase' },
    meta: { page_view: 'PageView', view_content: 'ViewContent', add_to_cart: 'AddToCart', lead: 'Lead', signup: 'CompleteRegistration', start_checkout: 'InitiateCheckout', purchase: 'Purchase' },
    tiktok: { page_view: 'Pageview', view_content: 'ViewContent', add_to_cart: 'AddToCart', lead: 'SubmitForm', signup: 'CompleteRegistration', start_checkout: 'InitiateCheckout', purchase: 'CompletePayment' },
    x: { page_view: 'PageView', view_content: 'ViewContent', add_to_cart: 'AddToCart', lead: 'Lead', signup: 'SignUp', start_checkout: 'InitiateCheckout', purchase: 'Purchase' }
  }
  function nativeName(t, n) { var s = STD[n]; return s ? (NATIVE[t] || {})[s] || null : null }
  function loadJS(u) { var e = document.createElement('script'); e.async = true; e.src = u; document.head.appendChild(e) }
  function eid() { try { return crypto.randomUUID() } catch (e) { return 'e-' + Date.now().toString(36) + Math.random().toString(36).slice(2, 10) } }
  var INJECT = {
    ga: {
      load: function (id) { loadJS('https://www.googletagmanager.com/gtag/js?id=' + encodeURIComponent(id)); window.dataLayer = window.dataLayer || []; window.gtag = window.gtag || function () { dataLayer.push(arguments) }; gtag('js', new Date()); gtag('config', id) },
      fire: function (n, p, id) { if (!window.gtag) return; var name = nativeName('ga', n) || n; var o = {}; for (var k in p) o[k] = p[k]; o.event_id = id; if (STD[n] === 'purchase') o.transaction_id = id; gtag('event', name, o) }
    },
    meta: {
      load: function (id) { if (!window.fbq) { var n = window.fbq = function () { n.callMethod ? n.callMethod.apply(n, arguments) : n.queue.push(arguments) }; if (!window._fbq) window._fbq = n; n.push = n; n.loaded = true; n.version = '2.0'; n.queue = []; loadJS('https://connect.facebook.net/en_US/fbevents.js') } fbq('init', id); fbq('track', 'PageView') },
      fire: function (n, p, id) { if (!window.fbq) return; var name = nativeName('meta', n); if (name) fbq('track', name, p || {}, { eventID: id }); else fbq('trackCustom', n, p || {}) }
    },
    tiktok: {
      load: function (id) { var q = window.ttq = window.ttq || []; if (!q.methods) { q.methods = ['page', 'track', 'identify', 'instances', 'debug', 'on', 'off', 'once', 'ready', 'alias', 'group', 'enableCookie', 'disableCookie']; q.setAndDefer = function (t, e) { t[e] = function () { t.push([e].concat(Array.prototype.slice.call(arguments, 0))) } }; for (var i = 0; i < q.methods.length; i++) q.setAndDefer(q, q.methods[i]); loadJS('https://analytics.tiktok.com/i18n/pixel/events.js?sdkid=' + encodeURIComponent(id) + '&lib=ttq') } if (q.load) q.load(id); if (q.page) q.page() },
      fire: function (n, p, id) { if (!window.ttq || !window.ttq.track) return; var name = nativeName('tiktok', n) || n; window.ttq.track(name, p || {}, { event_id: id }) }
    },
    x: {
      load: function (id) { if (!window.twq) { var s = window.twq = function () { s.exe ? s.exe.apply(s, arguments) : s.queue.push(arguments) }; s.version = '1.1'; s.queue = []; loadJS('https://static.ads-twitter.com/uwt.js') } twq('config', id) },
      fire: function (n, p, id) { if (!window.twq) return; var name = nativeName('x', n) || n; var o = {}; for (var k in p) o[k] = p[k]; o.conversion_id = id; twq('event', name, o) }
    }
  }
  var active = []
  try {
    fetch(src.origin + '/v1/tags?key=' + encodeURIComponent(key))
      .then(function (r) { return r.ok ? r.json() : { tags: [] } })
      .then(function (cfg) {
        var tags = (cfg && cfg.tags) || []
        for (var i = 0; i < tags.length; i++) {
          var t = tags[i]
          if (INJECT[t.type]) { try { INJECT[t.type].load(t.id); active.push(t) } catch (e) {} }
        }
        if (active.length) {
          var base = window.hanzo.track
          window.hanzo.track = function (n, p) {
            var id = eid()
            for (var j = 0; j < active.length; j++) { try { INJECT[active[j].type].fire(n, p, id) } catch (e) {} }
            var o = {}; if (p) for (var k in p) o[k] = p[k]; o.event_id = id
            base(n, o)
          }
        }
      })
      .catch(function () {})
  } catch (e) {}

  page()
})()
