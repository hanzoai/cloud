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

  // Identity uses the SAME storage keys and 30-minute session TTL as
  // @hanzo/event (ui/pkgs/event/src/storage.ts). A page carrying both clients
  // resolves to one person, not two.
  var ANON = 'hz_anon_id'
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

  function anonId() {
    var s = store()
    if (!s) return ''
    var v = s.getItem(ANON)
    if (!v) {
      v = uid()
      s.setItem(ANON, v)
    }
    return v
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
    if (beacon && navigator.sendBeacon) {
      try {
        var blob = new Blob([body], { type: 'application/json' })
        if (navigator.sendBeacon(url + '?ingest_key=' + encodeURIComponent(key), blob)) return
      } catch (e) {}
    }
    try {
      fetch(url, {
        method: 'POST',
        keepalive: true,
        credentials: 'include',
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
    var anon = anonId()
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

  page()
})()
