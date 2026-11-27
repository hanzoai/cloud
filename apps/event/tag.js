/*! Hanzo event tag — one paste, one wire.
 *   <script defer src="https://api.hanzo.ai/v1/event/tag.js" data-key="pk-…"></script>
 * Autocaptures pageviews (initial + SPA) and uncaught errors onto {batch:[…]} at /v1/event.
 * NO KEY ⇒ INERT (`hzAnonId` comes from anon.js, served ahead of this file by tag.go).
 */
(function () {
  var el = document.currentScript
  if (!el || !el.src) return
  var tagsOnly = el.getAttribute('data-tags-only') === '1'
  if (tagsOnly ? window.__hanzoTags : window.__hanzoEvent) return
  var src
  try {
    src = new URL(el.src)
  } catch (e) {
    return
  }

  var key = (el.getAttribute('data-key') || src.searchParams.get('key') || '').trim()
  if (key.indexOf('pk-') !== 0) return

  var url = src.origin + '/v1/event'
  var wire = url + '?ingest_key=' + encodeURIComponent(key)
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
    try {
      s.setItem(SESSION, JSON.stringify(st))
    } catch (e) {} // a full or blocked store: the session lives on this page alone
    return st.id
  }

  // CONSENT GOVERNS THE THIRD PARTIES, NOT THE STREAM. First-party stream events always go.
  // Third-party rules follow cloud's `consent.mode` (eu | us_privacy | global), stored in `__Host-hz_consent`.
  var HOST = location.protocol === 'https:' ? '__Host-' : ''
  var CHOICE = HOST + 'hz_consent'
  var CHOICE_V = HOST + 'hz_consent_v'
  var RULE = HOST + 'hz_consent_rule'
  // The global notice, closed. It grants nothing.
  var NOTICE = HOST + 'hz_consent_notice'
  // The policy version this tag was built under, and the oldest whose choices it counts.
  var VERSION = 3
  var FLOOR = 2
  var MODES = ['eu', 'us_privacy', 'global']
  var CACHE = 'hz_consent_policy'
  var rule = cached()
  function session() {
    try {
      return window.sessionStorage
    } catch (e) {}
  }
  function cached() {
    try {
      var c = JSON.parse(session().getItem(CACHE) || 'null')
      return c && typeof c.version === 'number' ? c : null
    } catch (e) {
      return null
    }
  }
  // serve takes cloud's answer and keeps it for the tab.
  function serve(c) {
    rule = c
    try {
      session().setItem(CACHE, JSON.stringify(c))
    } catch (e) {} // a full or blocked store: the rule lives on this page alone
  }
  function cookie(n) {
    var m = document.cookie.match(new RegExp('(?:^|; )' + n + '=([^;]*)'))
    return m ? decodeURIComponent(m[1]) : undefined
  }
  function mode() { return rule && MODES.indexOf(rule.mode) >= 0 ? rule.mode : 'eu' }
  function eu() { return mode() === 'eu' }
  // The policy version in force: cloud's, never older than the one this tag was built under.
  function version() { return Math.max(rule && rule.version || 0, VERSION) }
  // The stored choice that counts under the rule in force, and whether its version is
  // current (from the floor to the version), or undefined.
  function kept() {
    var c = cookie(CHOICE)
    var v = Number(cookie(CHOICE_V))
    var r = cookie(RULE)
    if (c === undefined || (eu() && !(r === 'eu' || (r === undefined && v === 2)))) return undefined
    return { held: grantsOf(c), current: v >= (rule && typeof rule.floor === 'number' ? rule.floor : FLOOR) && v <= version() }
  }
  function any(g) { return !!(g.analytics || g.marketing || g.ads) }
  // Whether a choice stops the asking: a current one, or one that allowed nothing.
  function stored() {
    var k = kept()
    return !!k && (k.current || !any(k.held))
  }
  // The browser's refusal, as the page sees it or as cloud saw the request.
  function refused() {
    var n = navigator
    var dnt = n.doNotTrack || window.doNotTrack || n.msDoNotTrack
    return n.globalPrivacyControl === true || dnt === '1' || dnt === 'yes' || !!(rule && rule.refused === true)
  }
  function grantsOf(c) {
    var on = c !== undefined ? c.split(',') : []
    var has = function (k) { return on.indexOf(k) >= 0 }
    return { analytics: has('analytics'), marketing: has('marketing'), ads: has('ads') }
  }
  var ALL = { analytics: true, marketing: true, ads: true }
  // What a refusal leaves of grants g: nothing, in every region.
  function limit(g) { return refused() ? {} : g }
  // The stored choice, its grants void below the floor; else the region's default:
  // nothing in eu, everything elsewhere.
  function held() {
    var base = eu() ? {} : ALL
    var k = kept()
    if (!k) return grantsOf(render(base) || undefined)
    if (k.current) return k.held
    return { analytics: !!(base.analytics && k.held.analytics), marketing: !!(base.marketing && k.held.marketing), ads: !!(base.ads && k.held.ads) }
  }
  function grants() {
    var g = limit(held())
    return { analytics: !!g.analytics, marketing: !!g.marketing, ads: !!g.ads }
  }
  // Whether the visitor is still to be asked: eu, no choice under the policy in force and
  // a browser that has not refused, exactly whom the @hanzo/ui banner asks.
  function asks() { return eu() && !refused() && !stored() }
  function noticed() { return cookie(NOTICE) === '1' }
  // Grants as the cookie and the `consent` property spell them.
  function render(g) {
    return ['analytics', 'marketing', 'ads'].filter(function (k) { return g[k] }).join(',')
  }
  function spelled() { return render(grants()) }
  // save stores a choice the way @hanzo/event's save() does — the categories on this
  // host alone, beside the policy version and the rule in force, for 13 months, a refusal
  // under GPC or DNT — forgets what the choice does not allow, and tells the page.
  function keep(n, v) {
    document.cookie = n + '=' + encodeURIComponent(v) + '; Max-Age=' + 395 * 86400 + '; Path=/; SameSite=Lax' + (HOST ? '; Secure' : '')
  }
  function save(g) {
    var out = limit(g)
    keep(CHOICE, render(out))
    keep(CHOICE_V, String(version()))
    keep(RULE, mode())
    withdraw(out)
    dispatchEvent(new Event('hzconsent'))
  }
  // notice records the global notice closed, as @hanzo/event's notice() does.
  function notice() {
    keep(NOTICE, '1')
    dispatchEvent(new Event('hzconsent'))
  }
  // What a category keeps on the device, and so what withdrawing it forgets: the
  // third-party cookies @hanzo/event's consent.ts lists for it, by exact name and
  // numbered form (`_ga_<container>`, `_gat_<tracker>`, `ttcsid_<pixel>`), and what is
  // kept for forwarding (`hz_cid`, Google's client id; `hz_touch`, the ad click).
  // Analytics: Google Analytics' cookies. Marketing: the ad platforms' browser ids and
  // the ad click. Ads: Google's ad cookies, which ad_storage governs. The site's own
  // first-party analytics — the anonymous id, the session, the touches — is essential
  // and is not here, and neither is anything else essential: a sign-in or IAM session,
  // the consent choice and notice, `hz_known`, a CSRF token.
  var KEPT = {
    analytics: /^(?:_ga|_gid|_gat|_ga_[A-Z0-9]+|_gat_[\w-]+|hz_cid)$/,
    marketing: /^(?:_fbp|_fbc|_ttp|_tt_enable_cookie|ttcsid|ttcsid_[A-Z0-9]+|_twclid|li_fat_id|li_giant|_uetsid|_uetvid|_pin_unauth|_epik|_derived_epik|_rdt_uuid|_rdt_cid|hz_touch)$/,
    ads: /^(?:_gcl_[a-z]+|_gac_[\w-]+)$/
  }
  // withdraw forgets what each category g does not allow, wherever its cookies were
  // set: on this host and on every domain above it, since a library writes to the
  // registrable domain (.example.co.uk as readily as .example.com), and a browser
  // ignores the deletion on a public suffix.
  function withdraw(g) {
    var names = document.cookie.split('; ').map(function (c) { return c.slice(0, c.indexOf('=')) })
    var labels = location.hostname.split('.')
    var scopes = ['']
    for (var l = 0; l < labels.length - 1; l++) scopes.push('; Domain=.' + labels.slice(l).join('.'))
    for (var c in KEPT) {
      if (g[c]) continue
      for (var i = 0; i < names.length; i++) {
        for (var j = 0; KEPT[c].test(names[i]) && j < scopes.length; j++) document.cookie = names[i] + '=; Max-Age=0; Path=/' + scopes[j]
      }
    }
  }
  var queue = []
  var timer = null
  var person = ''

  // An event states the grants in force when it is sent: the rule cloud answered with
  // since it was queued, and a choice made or withdrawn since, in this tab or another.
  function stated(ev) {
    ev.properties.consent = spelled()
    return ev
  }
  function flush(beacon) {
    if (timer) {
      clearTimeout(timer)
      timer = null
    }
    var batch = queue.map(stated)
    queue = []
    if (!batch.length) return
    var body = JSON.stringify({ batch: batch })
    // BOTH carriers send a CORS-SIMPLE request (`ingest_key` on query, `text/plain` body) so cross-origin sends never preflight.
    if (beacon && navigator.sendBeacon) {
      try {
        if (navigator.sendBeacon(wire, new Blob([body], { type: 'text/plain' }))) return
      } catch (e) {}
    }
    post(body)
  }

  // post sends one body by fetch. The publishable key IS the credential, so no
  // cookies: they have no business riding along on a page that is not ours.
  function post(body) {
    try {
      fetch(wire, {
        method: 'POST',
        keepalive: true,
        credentials: 'omit',
        headers: { 'content-type': 'text/plain' },
        body: body
      }).catch(noop)
    } catch (e) {}
  }

  function noop() {}

  // Every event carries url — the warehouse derives host from it
  // (event.fact.host DEFAULT domain(url)), so an event without one is a row
  // nobody can attribute to a site. Every event goes, for every visitor: the stream
  // is first party.
  function push(ev) {
    var anon = hzAnonId()
    ev.messageId = uid()
    ev.timestamp = new Date().toISOString()
    ev.distinctId = person || anon
    ev.anonymousId = anon
    ev.sessionId = sessionId()
    ev.referrer = document.referrer || ''
    ev.url = location.href
    ev.path = location.pathname
    if (product) ev.product = product
    // The tag names itself and the consent contract it keeps, so cloud can tell its
    // events from any other client's.
    ev.library = '@hanzo/tag'
    ev.libraryVersion = String(VERSION)
    // The grants it goes under are stated when it is sent (stated).
    ev.properties = ev.properties || {}
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

  if (!tagsOnly) {
    var href = location.href
    function navigated() {
      if (location.href === href) return
      href = location.href
      try { page() } catch (e) {}
    }
    var push_ = history.pushState
    var replace_ = history.replaceState
    history.pushState = function () { push_.apply(this, arguments); navigated() }
    history.replaceState = function () { replace_.apply(this, arguments); navigated() }
    addEventListener('popstate', navigated)
    addEventListener('hashchange', navigated)
    addEventListener('error', function (e) { error(e.error || e.message, false) })
    addEventListener('unhandledrejection', function (e) { error(e.reason, false) })
    addEventListener('pagehide', function () { flush(true) })
    addEventListener('visibilitychange', function () {
      if (document.visibilityState === 'hidden') flush(true)
    })
    window.__hanzoEvent = true
    window.hanzo = { track: track, identify: identify, page: page, error: error, flush: flush }
  }

  // ── config-driven tag injection (the hosted twin of track.js) ─────────────
  // Fetch the SITE's connected browser pixels (/v1/project/tags, dual-resolved per site by
  // this key or the host) and inject them first-party. Every event then also fires
  // the native pixel — translated through the SAME taxonomy the server-side CAPI uses
  // (destinations/translate.go), stamped with a shared event_id carried on the
  // /v1/event row too — so the browser pixel and the server CAPI DEDUPLICATE instead
  // of double-counting. Keyless already returned above, so this only runs when keyed.
  var STD = {
    $pageview: 'page_view', pricing_viewed: 'view_content', signup_viewed: 'view_content', checkout_viewed: 'view_content',
    product_viewed: 'view_content', search_performed: 'search', plan_clicked: 'lead', signup_submitted: 'lead',
    sales_contacted: 'lead', waitlist_joined: 'lead', referral_used: 'lead', newsletter_subscribed: 'lead',
    beta_submitted: 'lead', research_access_applied: 'lead', signup_completed: 'signup',
    plan_selected: 'add_to_cart', product_added: 'add_to_cart', add_to_cart: 'add_to_cart',
    checkout_started: 'start_checkout', begin_checkout: 'start_checkout', order_completed: 'purchase', purchase: 'purchase'
  }
  var NATIVE = {
    ga: { page_view: 'page_view', view_content: 'view_item', search: 'search', add_to_cart: 'add_to_cart', lead: 'generate_lead', signup: 'sign_up', start_checkout: 'begin_checkout', purchase: 'purchase' },
    meta: { page_view: 'PageView', view_content: 'ViewContent', search: 'Search', add_to_cart: 'AddToCart', lead: 'Lead', signup: 'CompleteRegistration', start_checkout: 'InitiateCheckout', purchase: 'Purchase' },
    tiktok: { page_view: 'Pageview', view_content: 'ViewContent', search: 'Search', add_to_cart: 'AddToCart', lead: 'SubmitForm', signup: 'CompleteRegistration', start_checkout: 'InitiateCheckout', purchase: 'CompletePayment' },
    x: { page_view: 'PageView', view_content: 'ViewContent', add_to_cart: 'AddToCart', lead: 'Lead', signup: 'SignUp', start_checkout: 'InitiateCheckout', purchase: 'Purchase' },
    // Google Ads speaks gtag, so it speaks GA4's vocabulary.
    gads: { page_view: 'page_view', view_content: 'view_item', search: 'search', add_to_cart: 'add_to_cart', lead: 'generate_lead', signup: 'sign_up', start_checkout: 'begin_checkout', purchase: 'purchase' },
    pinterest: { page_view: 'pagevisit', view_content: 'viewcategory', search: 'search', add_to_cart: 'addtocart', lead: 'lead', signup: 'signup', start_checkout: 'checkout', purchase: 'checkout' },
    reddit: { page_view: 'PageVisit', view_content: 'ViewContent', search: 'Search', add_to_cart: 'AddToCart', lead: 'Lead', signup: 'SignUp', start_checkout: 'AddToCart', purchase: 'Purchase' },
    openai: { view_content: 'view_content', search: 'search', add_to_cart: 'add_to_cart', lead: 'lead', signup: 'sign_up', start_checkout: 'checkout_started', purchase: 'purchase' }
  }
  function nativeName(t, n) { var s = STD[n]; return s ? (NATIVE[t] || {})[s] || null : null }
  function loadJS(u, onerr) { var e = document.createElement('script'); e.async = true; e.src = u; if (onerr) e.onerror = onerr; document.head.appendChild(e) }
  function eid() { try { return crypto.randomUUID() } catch (e) { return 'e-' + Date.now().toString(36) + Math.random().toString(36).slice(2, 10) } }
  // GA4 and Google Ads are ONE loader with two configs: gtag.js is a single script
  // and configuring it twice is how a site ends up fetching it twice and counting a
  // pageview twice. A site running both gets one script, two config lines.
  // Google Consent Mode v2, set before gtag.js is fetched. The default is the region's:
  // in eu all four flags denied, waiting 500 ms for an update, so Google Analytics sends
  // cookieless pings; elsewhere all granted. The update is the visitor's grants: Ads the
  // three ad flags everywhere (Ads off elsewhere is exactly the opt-out), Analytics the
  // storage flag in eu (elsewhere Google Analytics without Analytics does not load).
  var gtagged = false // this tag set Consent Mode on the page's gtag
  function gtagConsent() {
    var g = grants()
    var on = function (b) { return b ? 'granted' : 'denied' }
    var u = { ad_storage: on(g.ads), ad_user_data: on(g.ads), ad_personalization: on(g.ads) }
    if (eu()) u.analytics_storage = on(g.analytics)
    gtag('consent', 'update', u)
  }
  function gtagInit(id) {
    if (!window.gtag) {
      window.dataLayer = window.dataLayer || []
      window.gtag = function () { dataLayer.push(arguments) }
      var d = eu() ? 'denied' : 'granted'
      var dflt = { analytics_storage: d, ad_storage: d, ad_user_data: d, ad_personalization: d }
      if (eu()) dflt.wait_for_update = 500
      gtag('consent', 'default', dflt)
      gtagged = true
      gtagConsent()
      loadJS('https://www.googletagmanager.com/gtag/js?id=' + encodeURIComponent(id))
      gtag('js', new Date())
    }
    gtag('config', id)
  }
  // ids are the running ids of one platform's tags.
  function ids(type) {
    var out = []
    for (var i = 0; i < active.length; i++) if (active[i].type === type) out.push(active[i].id)
    return out
  }
  // An event is addressed to the platform's own ids: one with no send_to reaches every
  // config on the page, Google Analytics' and Google Ads' alike.
  function gtagFire(t, n, p, id) {
    if (!window.gtag) return
    var name = nativeName(t, n) || n
    var o = {}; for (var k in p) o[k] = p[k]
    o.event_id = id
    if (STD[n] === 'purchase') o.transaction_id = id
    o.send_to = ids(t)
    gtag('event', name, o)
  }
  // Meta on this page: its stub init'ed, held revoked, fbevents fetched, the PageView sent.
  var fb = { on: false, revoked: false, fetched: false, viewed: false }
  // Limited Data Use, the sale-or-sharing opt-out: on while Ads is off.
  function ldu() {
    if (grants().ads) fbq('dataProcessingOptions', [])
    else fbq('dataProcessingOptions', ['LDU'], 0, 0)
  }
  // live is Meta allowed: fbevents fetched, consent granted if it was held, the PageView sent once.
  function live() {
    if (!fb.fetched) {
      fb.fetched = true
      loadJS('https://connect.facebook.net/en_US/fbevents.js')
    }
    if (fb.revoked) {
      fbq('consent', 'grant')
      fb.revoked = false
    }
    if (!fb.viewed) {
      fb.viewed = true
      fbq('track', 'PageView')
    }
  }
  var INJECT = {
    ga: {
      load: gtagInit,
      fire: function (n, p, id) { gtagFire('ga', n, p, id) }
    },
    // Google Ads rides the same gtag. The browser half is remarketing and audience
    // building -- a conversion ACTION is keyed by a label this config cannot name,
    // and the server half already forwards conversions through the Ads API.
    gads: {
      load: gtagInit,
      fire: function (n, p, id) { gtagFire('gads', n, p, id) }
    },
    linkedin: {
      load: function (id) {
        var pid = String(id)
        window._linkedin_partner_id = pid
        window._linkedin_data_partner_ids = window._linkedin_data_partner_ids || []
        if (window._linkedin_data_partner_ids.indexOf(pid) === -1) window._linkedin_data_partner_ids.push(pid)
        if (!window.lintrk) { window.lintrk = function (a, b) { window.lintrk.q.push([a, b]) }; window.lintrk.q = []; loadJS('https://snap.licdn.com/li.lms-analytics/insight.min.js', function () { new Image(1, 1).src = 'https://px.ads.linkedin.com/collect/?pid=' + encodeURIComponent(pid) + '&fmt=gif' }) }
      },
      // The Insight Tag carries retargeting from load alone. A LinkedIn conversion is
      // addressed by a numeric id minted in Campaign Manager, which a canonical event
      // name cannot supply, so conversions ride the CAPI half rather than a guess.
      fire: function () {}
    },
    pinterest: {
      load: function (id) {
        if (!window.pintrk) { window.pintrk = function () { window.pintrk.queue.push(Array.prototype.slice.call(arguments)) }; window.pintrk.queue = []; window.pintrk.version = '3.0'; loadJS('https://s.pinimg.com/ct/core.js') }
        pintrk('load', id); pintrk('page')
      },
      fire: function (n, p, id) { if (!window.pintrk) return; pintrk('track', nativeName('pinterest', n) || 'custom', p || {}, { event_id: id }) }
    },
    reddit: {
      load: function (id) {
        if (!window.rdt) { var r = window.rdt = function () { r.sendEvent ? r.sendEvent.apply(r, arguments) : r.callQueue.push(arguments) }; r.callQueue = []; loadJS('https://www.redditstatic.com/ads/pixel.js') }
        rdt('init', id); rdt('track', 'PageVisit')
      },
      fire: function (n, p, id) { if (!window.rdt) return; var o = {}; for (var k in p) o[k] = p[k]; o.conversionId = id; rdt('track', nativeName('reddit', n) || 'Custom', o) }
    },
    openai: {
      load: function (id) {
        if (!window.oaiq) { var q = window.oaiq = function () { q.q.push(arguments) }; q.q = []; loadJS('https://bzrcdn.openai.com/sdk/oaiq.min.js') }
        var dbg = location.search && /(?:^\?|&)oaiq_debug=1(?:&|$)/.test(location.search)
        oaiq('init', dbg ? { pixelId: id, debug: true } : { pixelId: id })
      },
      fire: function (n, p, id) {
        if (!window.oaiq) return
        var name = nativeName('openai', n)
        if (!name) return
        var o = {}
        if (name === 'checkout_started') o.type = 'contents'
        if (p && typeof p.value === 'number') { o.value = p.value; o.currency = typeof p.currency === 'string' ? p.currency : 'USD' }
        else if (p && typeof p.currency === 'string') o.currency = p.currency
        if (id) o.event_id = id
        oaiq('measure', name, o)
      }
    },
    // A site may report to several Meta pixels (one per brand). The stub is built once,
    // and before the first init Meta is told its consent (revoked without Marketing) and
    // Limited Data Use (on without Ads); each pixel is init'ed. Only under Marketing
    // (live) is fbevents fetched and ONE PageView sent: fbq('track') reaches every pixel
    // init'ed on the page, and each event fires once, with one eventID every pixel and
    // the server's Conversions API dedupe on. Without Marketing nothing reaches Meta.
    meta: {
      load: function (id) {
        if (!window.fbq) { var n = window.fbq = function () { n.callMethod ? n.callMethod.apply(n, arguments) : n.queue.push(arguments) }; if (!window._fbq) window._fbq = n; n.push = n; n.loaded = true; n.version = '2.0'; n.queue = [] }
        if (!fb.on) {
          fb.on = true
          if (!grants().marketing) {
            fbq('consent', 'revoke')
            fb.revoked = true
          }
          if (!grants().ads) ldu()
        }
        fbq('init', id)
      },
      loaded: function () { if (grants().marketing) live() },
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
  // What runs under the visitor's grants (loads): under GPC or DNT nothing; else Google
  // Analytics in eu for everyone (Consent Mode keeps it cookieless) and elsewhere under
  // Analytics, Meta's stub always (held revoked without Marketing), every other platform
  // under Marketing. An event
  // reaches what runs (hears), Meta only under Marketing. A choice reaches Google and
  // Meta at once; one that withdraws any other running library reloads the page: a
  // library that has run cannot be unloaded.
  var all = []
  var active = []
  function loads(t) {
    var g = grants()
    return !refused() && (t.type === 'meta' || (t.type === 'ga' ? eu() || g.analytics : g.marketing))
  }
  function hears(t) { return t.type === 'meta' ? grants().marketing : loads(t) }
  // The grants this page last acted on, before any refusal: a browser that refuses now
  // (GPC or DNT) over a stored yes withdraws it on the first look.
  var was = null
  var waiting = false // a reload the open sheet holds
  function load() {
    var g = grants()
    if (!was) was = held()
    // A category withdrawn on this page forgets what its third party kept.
    withdraw({ analytics: !was.analytics || g.analytics, marketing: !was.marketing || g.marketing, ads: !was.ads || g.ads })
    if (render(g) !== render(was)) {
      try { if (gtagged) gtagConsent() } catch (e) {}
      try {
        if (fb.on && !!g.ads !== !!was.ads) ldu()
        if (fb.on && g.marketing) live()
        if (fb.on && !g.marketing && !fb.revoked) {
          fbq('consent', 'revoke')
          fb.revoked = true
        }
      } catch (e) {}
    }
    was = g
    for (var k = 0; k < active.length; k++) {
      if (loads(active[k])) continue
      // gtag stops measuring this page at once, before the reload lets it go.
      for (var d = 0; d < active.length; d++) if ((active[d].type === 'ga' || active[d].type === 'gads') && !loads(active[d])) window['ga-disable-' + active[d].id] = true
      // An open sheet keeps the page until it closes (close), then the page reloads once.
      if (asked) {
        waiting = true
        return
      }
      active = []
      try { location.reload() } catch (e) {}
      return
    }
    // A platform with several tags (Meta's pixels) is told once, after the pass, that
    // its tags are in (loaded).
    var fresh = {}
    for (var i = 0; i < all.length; i++) {
      var t = all[i]
      if (active.indexOf(t) >= 0 || !INJECT[t.type] || !loads(t)) continue
      try { INJECT[t.type].load(t.id); active.push(t); fresh[t.type] = true } catch (e) {}
    }
    for (var f in fresh) if (INJECT[f].loaded) try { INJECT[f].loaded() } catch (e) {}
  }
  function fireTags(n, p, id) {
    var fired = {}
    for (var j = 0; j < active.length; j++) {
      var t = active[j]
      if (fired[t.type] || !hears(t)) continue
      fired[t.type] = true
      try { INJECT[t.type].fire(n, p, id) } catch (e) {}
    }
  }
  function reachTags() {
    var out = []
    for (var j = 0; j < active.length; j++) {
      var t = active[j]
      if (hears(t) && out.indexOf(t.type) < 0) out.push(t.type)
    }
    return out.join(',')
  }
  window.__hanzoTags = { mirror: fireTags, reach: reachTags }
  if (!tagsOnly && window.hanzo) {
    var base = window.hanzo.track
    window.hanzo.track = function (n, p) {
      var id = eid()
      fireTags(n, p, id)
      var o = {}; if (p) for (var k in p) o[k] = p[k]; o.event_id = id
      base(n, o)
    }
  }
  addEventListener('hzconsent', load)
  function shown() { if (document.visibilityState !== 'hidden' && was && spelled() !== render(was)) load() }
  addEventListener('pageshow', shown)
  addEventListener('visibilitychange', shown)

  // ── the banner (data-banner) ──────────────────────────────────────────────
  // Shadow-DOM consent bar + sheet for pages that mount no @hanzo/ui Consent banner.
  var banner = tagsOnly ? null : el.getAttribute('data-banner')
  // An http(s) address the page names, or ''.
  function policy(v) {
    if (!v) return ''
    try {
      var a = new URL(v, location.href)
      return a.protocol === 'https:' || a.protocol === 'http:' ? a.href : ''
    } catch (e) {
      return ''
    }
  }
  var POLICIES = [
    ['Cookie Policy', policy(el.getAttribute('data-cookies'))],
    ['Privacy Policy', policy(banner)],
    ['Terms of Service', policy(el.getAttribute('data-terms'))]
  ].filter(function (p) { return p[1] })
  // One section per purpose: the category it grants (none for the strictly necessary,
  // which are always on), its title, and what it does as the gating above does it — in
  // eu, where each waits for the visitor, and elsewhere (on), where each runs until the
  // visitor turns it off.
  var NECESSARY = ['', 'Strictly Necessary Cookies', "Needed for the site to work, and always on: sign-in and your session, security and CSRF protection, load balancing, remembering this choice, the state of a form you are filling in, and this site's own first-party analytics, which counts its visits and errors for itself and is never sold or shared."]
  var SECTIONS = {
    eu: [
      NECESSARY,
      ['analytics', 'Performance Cookies', "Google Analytics: lets Google measure this site's visits with its own cookies. Without them it counts visits without cookies."],
      ['marketing', 'Marketing Cookies', "The Meta pixel: lets Meta measure the sign-ups and orders this site's ads led to."],
      ['ads', 'Sell, Share, or Use Your Data for Targeted Advertising', 'Lets Google and Meta use your visit to choose the ads they show you on other sites and apps.']
    ],
    on: [
      NECESSARY,
      ['analytics', 'Performance Cookies', "On by default. Google Analytics measures this site's visits with its own cookies."],
      ['marketing', 'Marketing Cookies', "On by default. The Meta pixel measures the sign-ups and orders this site's ads led to."],
      ['ads', 'Sell, Share, or Use Your Data for Targeted Advertising', 'On by default. Google and Meta may use your visits to choose the ads they show you on other sites and apps. Turn it off to opt out of the sale or sharing of your personal information and of targeted advertising.']
    ]
  }
  // The card and its buttons keep concentric radii (16px, 12px); a button's hover and
  // press are a shift of the theme's own surface, and Save preferences, the sheet's
  // primary, keeps full contrast whatever the switches say.
  var STYLE =
    ':host{all:initial}:host([data-hz-consent-foot]){display:block}' +
    '.bar,.sheet{--bg:#fff;--ink:#111;--soft:#555;--line:rgba(0,0,0,.15);--ring:#2563eb;--hover:rgba(0,0,0,.05);--press:rgba(0,0,0,.1);' +
    'position:fixed;z-index:2147483646;' +
    'left:calc(16px + env(safe-area-inset-left,0px));right:calc(16px + env(safe-area-inset-right,0px));bottom:calc(16px + env(safe-area-inset-bottom,0px));' +
    'margin:0 auto;box-sizing:border-box;max-width:480px;display:grid;gap:12px;padding:16px;border:1px solid var(--line);border-radius:16px;' +
    'background:var(--bg);color:var(--ink);font:14px/1.45 system-ui,-apple-system,"Segoe UI",Roboto,sans-serif;box-shadow:0 8px 24px rgba(0,0,0,.25)}' +
    '.sheet{overflow-y:auto;max-height:calc(100vh - 32px - env(safe-area-inset-top,0px) - env(safe-area-inset-bottom,0px));' +
    'max-height:calc(100dvh - 32px - env(safe-area-inset-top,0px) - env(safe-area-inset-bottom,0px))}' +
    'p{margin:0}.soft,.always,.body{color:var(--soft)}' +
    'a{color:inherit;text-decoration:underline}' +
    '.head,.row{display:flex;align-items:center;justify-content:space-between;gap:12px}.bar .head{align-items:flex-start}' +
    'h2{margin:0;font-size:16px;font-weight:600}' +
    '.section{border-top:1px solid var(--line);padding-top:10px}' +
    '.always{font-size:12px;white-space:nowrap}.body{margin:6px 0 0 18px;font-size:12px}' +
    'input{flex:none;width:18px;height:18px;margin:0;accent-color:var(--ink)}' +
    '.acts{display:flex;flex-wrap:wrap;justify-content:flex-end;gap:8px;margin-inline-start:auto}' +
    'button{font:inherit;font-size:13px;line-height:1.2;min-height:36px;padding:6px 14px;border:1px solid var(--line);border-radius:12px;' +
    'background:transparent;color:inherit;cursor:pointer;transition:background-color .12s,border-color .12s}' +
    'button:hover{background:var(--hover)}button:active{background:var(--press)}' +
    'button.go{background:var(--ink);border-color:var(--ink);color:var(--bg);font-weight:600}' +
    'button.go:hover{background:color-mix(in srgb,var(--ink) 86%,var(--bg));border-color:color-mix(in srgb,var(--ink) 86%,var(--bg))}' +
    'button.go:active{background:color-mix(in srgb,var(--ink) 74%,var(--bg));border-color:color-mix(in srgb,var(--ink) 74%,var(--bg))}' +
    'button.x{border-color:transparent;min-width:36px;padding:0;font-size:20px;line-height:1}' +
    'button.fold{flex:1;display:flex;align-items:center;gap:10px;border:0;margin-inline:-8px;padding:6px 8px;border-radius:8px;font-size:14px;text-align:start;font-weight:600}' +
    '.chev{flex:none;width:7px;height:7px;border-right:2px solid;border-bottom:2px solid;transform:rotate(-45deg);transition:transform .15s}' +
    '.fold[aria-expanded=true] .chev{transform:rotate(45deg)}' +
    'a:focus-visible,button:focus-visible,input:focus-visible{outline:2px solid var(--ring);outline-offset:2px}' +
    '.foot{--ring:#2563eb;display:flex;justify-content:center;padding:12px 16px calc(12px + env(safe-area-inset-bottom,0px))}' +
    'button.choices{display:inline-flex;align-items:center;gap:8px;border:0;min-height:32px;padding:4px 8px;border-radius:8px;' +
    'font:12px/1.4 system-ui,-apple-system,"Segoe UI",Roboto,sans-serif;color:#555;text-decoration:underline}' +
    'button.choices:hover{background:rgba(0,0,0,.05)}' +
    '@media (max-width:480px){.acts>button{flex:1 1 auto}}' +
    '@media (prefers-reduced-motion:reduce){.chev,button{transition:none}}' +
    '@media (prefers-color-scheme:dark){.bar,.sheet{--bg:#161616;--ink:#f2f2f2;--soft:#a3a3a3;--line:rgba(255,255,255,.18);--ring:#60a5fa;' +
    '--hover:rgba(255,255,255,.08);--press:rgba(255,255,255,.14)}.foot{--ring:#60a5fa}button.choices{color:#a3a3a3}button.choices:hover{background:rgba(255,255,255,.08)}}'
  var css = null // the banner's constructed sheet, made once
  var nonce = el.nonce || el.getAttribute('nonce') || '' // the script's CSP nonce, for a <style>
  var shell = null // the banner's host element while it is drawn
  var asked = false // the visitor opened the sheet themselves
  var back = null // what held focus before it came into the banner
  function make(tag, text, attrs) {
    var n = document.createElement(tag)
    if (text) n.textContent = text
    for (var k in attrs) n.setAttribute(k, attrs[k])
    return n
  }
  function button(text, cls, fn) {
    var b = make('button', text, { type: 'button', class: cls })
    b.addEventListener('click', fn)
    return b
  }
  // close takes the banner down, and hands focus back when it held it.
  function close() {
    var inside = shell && document.activeElement === shell
    if (shell) shell.remove()
    shell = null
    asked = false
    try {
      if (inside && back) back.focus()
    } catch (e) {}
    back = null
    if (waiting) {
      waiting = false
      load()
    }
  }
  // dismiss closes the sheet unanswered: a visitor still to be asked is shown the bar.
  function dismiss() {
    close()
    sync()
  }
  function choose(g) {
    return function () {
      close()
      save(g)
    }
  }
  // bar is the first-visit card. In eu: the line, ×, Manage preferences and Accept all.
  // In global, the notice: the line, × (which closes it for good) and Manage preferences.
  function bar(box) {
    var head = make('div', '', { class: 'head' })
    if (!eu()) {
      head.appendChild(make('p', 'We use cookies to improve your experience. By continuing to browse, you agree.'))
      var shut = button('×', 'x', function () {
        close()
        notice()
      })
      shut.setAttribute('aria-label', 'Close')
      shut.setAttribute('title', 'Close')
      head.appendChild(shut)
      box.appendChild(head)
      var more = make('div', '', { class: 'acts' })
      more.appendChild(button('Manage preferences', '', open))
      box.appendChild(more)
      return
    }
    head.appendChild(make('p', 'Essential cookies keep this site running. With your OK, optional cookies help us measure performance and ads.'))
    // × is an answer, not a dismissal: it keeps essential cookies only, so refusing is
    // one press beside the card, as easy as accepting.
    var x = button('×', 'x', choose({}))
    x.setAttribute('aria-label', 'Close and use essential cookies only')
    x.setAttribute('title', 'Close and use essential cookies only')
    head.appendChild(x)
    box.appendChild(head)
    var acts = make('div', '', { class: 'acts' })
    acts.appendChild(button('Manage preferences', '', open))
    acts.appendChild(button('Accept all', 'go', choose(ALL)))
    box.appendChild(acts)
  }
  // sheet is Cookie Preferences: the intro, a section per purpose, and the answers.
  // It answers the control focus belongs on.
  function sheet(box, root) {
    var ring = [] // the controls Tab cycles through, in order
    function stop(n) {
      ring.push(n)
      return n
    }
    var head = make('div', '', { class: 'head' })
    head.appendChild(make('h2', eu() ? 'Cookie Preferences' : 'Your Privacy Choices', { id: 'hz-consent-title' }))
    var x = stop(button('×', 'x', dismiss))
    x.setAttribute('aria-label', 'Close')
    x.setAttribute('title', 'Close')
    head.appendChild(x)
    box.appendChild(head)
    var intro = make('p')
    intro.appendChild(document.createTextNode(eu()
      ? 'We use cookies and similar technologies to provide our services, measure performance, and support advertising. ' +
        'Essential cookies are required for the site to work and cannot be turned off. You can allow or reject optional cookies below.'
      : 'We use cookies and similar technologies to provide our services, measure performance, and support advertising. ' +
        'Google Analytics and the Meta pixel run by default, and you can turn each off below. A browser that sends Global Privacy Control ' +
        'or Do Not Track runs none of them.'))
    if (POLICIES.length) {
      intro.appendChild(document.createTextNode(' Learn more in our '))
      for (var p = 0; p < POLICIES.length; p++) {
        if (p) intro.appendChild(document.createTextNode(POLICIES.length === 2 ? ' and ' : p === POLICIES.length - 1 ? ', and ' : ', '))
        intro.appendChild(stop(make('a', POLICIES[p][0], { href: POLICIES[p][1] })))
      }
      intro.appendChild(document.createTextNode('.'))
    }
    box.appendChild(intro)
    // A browser that refused locks every switch off, in every region.
    var locked = refused()
    if (locked) box.appendChild(make('p', 'Your browser asks sites not to track you, so all of these stay off.', { class: 'soft' }))
    var now = grants()
    var on = {}
    var SECTIONS_ = SECTIONS[eu() ? 'eu' : 'on']
    for (var i = 0; i < SECTIONS_.length; i++) {
      var c = SECTIONS_[i][0]
      var part = make('div', '', { class: 'section' })
      var row = make('div', '', { class: 'row' })
      var fold = stop(make('button', '', { type: 'button', class: 'fold', 'aria-expanded': 'false', 'aria-controls': 'hz-consent-' + i }))
      fold.appendChild(make('span', '', { class: 'chev', 'aria-hidden': 'true' }))
      fold.appendChild(make('span', SECTIONS_[i][1]))
      var body = make('p', SECTIONS_[i][2], { id: 'hz-consent-' + i, class: 'body' })
      body.hidden = true
      fold.addEventListener('click', unfold(fold, body))
      row.appendChild(fold)
      if (c) {
        var input = stop(make('input', '', { type: 'checkbox', role: 'switch', 'aria-label': SECTIONS_[i][1] }))
        input.checked = !!now[c]
        input.disabled = locked
        on[c] = input
        row.appendChild(input)
      } else row.appendChild(make('span', 'Always Active', { class: 'always' }))
      part.appendChild(row)
      part.appendChild(body)
      box.appendChild(part)
    }
    var acts = make('div', '', { class: 'acts' })
    // A browser that refused has nothing to accept here, and outside eu all is on until turned off.
    if (eu() && !locked) acts.appendChild(stop(button('Accept all', '', choose(ALL))))
    acts.appendChild(stop(button('Save preferences', 'go', function () {
      var g = {}
      for (var k in on) g[k] = on[k].checked
      close()
      save(g)
    })))
    box.appendChild(acts)
    // Escape lets the sheet go; Tab and Shift+Tab wrap at its ends, so focus stays in
    // the dialog while it is open.
    box.addEventListener('keydown', function (e) {
      if (e.key === 'Escape') return dismiss()
      if (e.key !== 'Tab') return
      var open = ring.filter(function (n) { return !n.disabled })
      var at = open.indexOf(root.activeElement)
      var to = e.shiftKey ? (at <= 0 ? open.length - 1 : -1) : (at < 0 || at === open.length - 1 ? 0 : -1)
      if (to < 0) return
      e.preventDefault()
      open[to].focus()
    })
    return x
  }
  function unfold(fold, body) {
    return function () {
      var expanded = fold.getAttribute('aria-expanded') !== 'true'
      fold.setAttribute('aria-expanded', String(expanded))
      body.hidden = !expanded
    }
  }
  // draw puts up the bar, or the sheet, and answers the control focus belongs on.
  var under = '' // the rule the banner was drawn under: a rule that changes redraws it
  function draw(choosing) {
    under = mode()
    if (shell) shell.remove()
    shell = make('div', '', { 'data-hz-consent': '' })
    // What held focus before it came into the banner, from outside it.
    shell.addEventListener('focusin', function (e) { if (e.relatedTarget && e.relatedTarget !== shell) back = e.relatedTarget })
    var root = styled(shell)
    var box = choosing
      ? make('div', '', { role: 'dialog', 'aria-modal': 'true', 'aria-labelledby': 'hz-consent-title', class: 'sheet' })
      : make('div', '', { role: 'region', 'aria-label': 'Cookie consent', class: 'bar' })
    var first = null
    if (choosing) first = sheet(box, root)
    else bar(box)
    root.appendChild(box)
    document.body.appendChild(shell)
    return first
  }
  // styled gives a host element its shadow root and the banner's style: a constructed
  // sheet where the browser has one, which a strict style-src does not stop; else a
  // <style> carrying the script's own nonce, which it allows.
  function styled(host) {
    var root = host.attachShadow({ mode: 'open' })
    if (root.adoptedStyleSheets && typeof CSSStyleSheet === 'function') {
      if (!css) {
        css = new CSSStyleSheet()
        css.replaceSync(STYLE)
      }
      root.adoptedStyleSheets = [css]
    } else root.appendChild(make('style', STYLE, nonce ? { nonce: nonce } : {}))
    return root
  }
  // The California Privacy Protection Agency's opt-out icon: a toggle, a check on its
  // light half and a cross on its blue one.
  var ICON = [
    ['M7.4 12.8h6.8l3.1-11.6H7.4C4.2 1.2 1.6 3.8 1.6 7s2.6 5.8 5.8 5.8z', '#fff'],
    ['M22.6 0H7.4c-3.9 0-7 3.1-7 7s3.1 7 7 7h15.2c3.9 0 7-3.1 7-7s-3.2-7-7-7zm-21 7c0-3.2 2.6-5.8 5.8-5.8h9.9l-3.1 11.6H7.4c-3.2 0-5.8-2.6-5.8-5.8z', '#06f'],
    ['M24.6 4c.2.2.2.6 0 .8L22.5 7l2.2 2.2c.2.2.2.6 0 .8s-.6.2-.8 0l-2.2-2.2-2.2 2.2c-.2.2-.6.2-.8 0s-.2-.6 0-.8L20.8 7l-2.2-2.2c-.2-.2-.2-.6 0-.8s.6-.2.8 0l2.2 2.2L23.8 4c.2-.2.6-.2.8 0z', '#fff'],
    ['M12.7 4.1c.2.2.3.6.1.8L8.6 9.8c-.1.1-.2.2-.3.2-.2.1-.5.1-.7-.1L5.4 7.7c-.2-.2-.2-.6 0-.8s.6-.2.8 0L8 8.6l3.8-4.5c.2-.2.6-.2.9 0z', '#06f']
  ]
  function icon() {
    var NS = 'http://www.w3.org/2000/svg'
    var svg = document.createElementNS(NS, 'svg')
    var at = { viewBox: '0 0 30 14', width: '30', height: '14', 'aria-hidden': 'true', focusable: 'false' }
    for (var k in at) svg.setAttribute(k, at[k])
    for (var i = 0; i < ICON.length; i++) {
      var d = document.createElementNS(NS, 'path')
      d.setAttribute('d', ICON[i][0])
      d.setAttribute('fill', ICON[i][1])
      svg.appendChild(d)
    }
    return svg
  }
  // link keeps the us_privacy "Your Privacy Choices" link at the foot of the page,
  // opening the sheet, and takes it away under any other rule or once the page carries
  // its own (data-hz-consent-link).
  var foot = null
  function link() {
    var own = document.querySelector && document.querySelector('[data-hz-consent-link]')
    if (mode() !== 'us_privacy' || own) {
      if (foot) foot.remove()
      foot = null
      return
    }
    if (foot && foot.isConnected) return
    foot = make('div', '', { 'data-hz-consent-foot': '' })
    var row = make('nav', '', { class: 'foot', 'aria-label': 'Your Privacy Choices' })
    var b = button('', 'choices', open)
    b.appendChild(icon())
    b.appendChild(make('span', 'Your Privacy Choices'))
    row.appendChild(b)
    styled(foot).appendChild(row)
    document.body.appendChild(foot)
  }
  // open shows the sheet because the visitor asked for it, and moves focus there.
  function open() {
    if (!document.body) return
    asked = true
    var f = draw(true)
    try {
      f.focus()
    } catch (e) {}
  }
  // sync puts the bar up for a visitor still to be asked in eu, or the notice for one who
  // has not closed it in global — again, if the page took it out — and takes it down
  // once they are not; a sheet the visitor opened stays until they answer or close it.
  // Nothing is drawn before the rule is known (cloud's, or the tab's last) or 1.5 s
  // have passed, after which it is eu: a banner the rule would take back never flashes.
  var late = false
  function sync() {
    if (!document.body || (!rule && !late)) return
    link()
    if (asked) return
    if (!(asks() || (mode() === 'global' && !noticed() && !refused()))) close()
    else if (!shell || !shell.isConnected || under !== mode()) draw(false)
  }
  // A browser with no shadow DOM gets no banner, and every other part of the tag.
  if (banner !== null && typeof document.createElement('div').attachShadow === 'function') {
    window.hzConsent = { open: open }
    addEventListener('hzconsent-open', open)
    addEventListener('hzconsent', sync)
    addEventListener('visibilitychange', sync)
    addEventListener('DOMContentLoaded', sync)
    setTimeout(function () {
      late = true
      sync()
    }, 1500)
    sync()
  }

  if (tagsOnly && window.__hanzoTagConfig && Array.isArray(window.__hanzoTagConfig.tags)) {
    all = window.__hanzoTagConfig.tags
    load()
  } else {
    try {
      fetch(src.origin + '/v1/project/tags?key=' + encodeURIComponent(key) + '&host=' + encodeURIComponent(location.hostname))
        .then(function (r) { return r.ok ? r.json() : { tags: [] } })
        .then(function (cfg) {
          if (cfg && cfg.consent && typeof cfg.consent.version === 'number') serve(cfg.consent)
          all = (cfg && cfg.tags) || []
          load()
          if (banner !== null) sync()
        })
        .catch(function () {})
    } catch (e) {}
  }

  if (!tagsOnly) {
    try { page() } catch (e) {}
  }
})()
