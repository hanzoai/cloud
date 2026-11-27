// shim.js is the in-process implementation of the ActivePieces authoring
// framework, evaluated once per connector VM. A connector's compiled bundle
// leaves @activepieces/pieces-framework, @activepieces/pieces-common and
// @activepieces/shared external; this file supplies them via a minimal
// require() so the SAME connector source that ran under the Node engine runs
// unmodified in goja.
//
// The only impure primitive is __hanzoHttpSend(request) — a Go function
// injected per invocation that performs the HTTP call synchronously (Go
// net/http) and returns {status,headers,body} or throws. Because it resolves
// synchronously, an action's `async run(ctx)` settles within goja's own
// microtask drain: no event loop, connectors run as plain in-process work.
(function (g) {
  'use strict';

  // ---- @activepieces/pieces-framework ---------------------------------------

  // names answers every property read with the property's own name, which is
  // what an ActivePieces string enum is: PieceCategory.COMMUNICATION is the
  // string 'COMMUNICATION'. One definition covers every enum a piece names —
  // categories, trigger strategies, store scopes — so an enum added upstream
  // never makes a piece fail to load.
  function names() {
    return new Proxy({}, {
      get: function (_, k) { return typeof k === 'string' ? k : undefined; },
    });
  }

  // Property.* / PieceAuth.* are pure metadata builders: they return the
  // config object tagged with a type. Execution never inspects the schema —
  // propsValue arrives already resolved — so a shallow copy is faithful.
  function tagged(type, fixed) {
    return function (cfg) {
      return Object.assign({ type: type, valueSchema: undefined }, cfg || {}, fixed || {});
    };
  }
  var Property = {
    ShortText: tagged('SHORT_TEXT'),
    LongText: tagged('LONG_TEXT'),
    RichText: tagged('RICH_TEXT'),
    Number: tagged('NUMBER'),
    Checkbox: tagged('CHECKBOX'),
    Json: tagged('JSON'),
    Object: tagged('OBJECT'),
    Array: tagged('ARRAY'),
    File: tagged('FILE'),
    DateTime: tagged('DATE_TIME'),
    DateRange: tagged('DATE_RANGE'),
    Color: tagged('COLOR'),
    MarkDown: tagged('MARKDOWN'),
    Markdown: tagged('MARKDOWN'),
    StaticDropdown: tagged('STATIC_DROPDOWN'),
    Dropdown: tagged('DROPDOWN'),
    StaticMultiSelectDropdown: tagged('STATIC_MULTI_SELECT_DROPDOWN'),
    MultiSelectDropdown: tagged('MULTI_SELECT_DROPDOWN'),
    DynamicProperties: tagged('DYNAMIC'),
    Custom: tagged('CUSTOM'),
    SecretText: tagged('SECRET_TEXT'),
  };
  var PieceAuth = {
    SecretText: tagged('SECRET_TEXT'),
    OAuth2: tagged('OAUTH2'),
    BasicAuth: tagged('BASIC_AUTH', { required: true }),
    CustomAuth: tagged('CUSTOM_AUTH'),
    OIDC: tagged('OIDC'),
    None: function () { return undefined; },
  };

  function createAction(p) {
    return {
      name: p.name,
      displayName: p.displayName,
      description: p.description,
      props: p.props || {},
      run: p.run,
      test: p.test || p.run,
      requireAuth: p.requireAuth !== false,
      __isAction: true,
    };
  }
  function createTrigger(p) {
    return {
      name: p.name,
      displayName: p.displayName,
      description: p.description,
      props: p.props || {},
      type: p.type,
      __isTrigger: true,
    };
  }

  function Piece(params) {
    this.displayName = params.displayName;
    this.description = params.description || '';
    this.auth = params.auth;
    this.logoUrl = params.logoUrl;
    this.categories = params.categories || [];
    this.deprecated = params.deprecated;
    this._actions = {};
    (params.actions || []).forEach(function (a) { if (a) this._actions[a.name] = a; }, this);
    this._triggers = {};
    (params.triggers || []).forEach(function (t) { if (t) this._triggers[t.name] = t; }, this);
  }
  Piece.prototype.getAction = function (n) { return this._actions[n]; };
  Piece.prototype.getTrigger = function (n) { return this._triggers[n]; };
  Piece.prototype.actions = function () { return this._actions; };
  Piece.prototype.triggers = function () { return this._triggers; };

  // createPiece registers the constructed piece on a per-VM list; the runtime
  // reads the last one after evaluating the bundle (a bundle calls createPiece
  // exactly once at module top level).
  function createPiece(params) {
    var p = new Piece(params);
    g.__ap_pieces.push(p);
    return p;
  }
  g.__ap_pieces = [];

  function isNil(v) { return v === null || v === undefined; }
  function isEmpty(v) {
    if (isNil(v)) return true;
    if (typeof v === 'string' || Array.isArray(v)) return v.length === 0;
    if (typeof v === 'object') return Object.keys(v).length === 0;
    return false;
  }
  function assertNotNullOrUndefined(v, name) {
    if (isNil(v)) throw new Error('Expected ' + (name || 'value') + ' to be defined, received ' + v);
    return v;
  }
  function words(s) {
    return String(s || '').replace(/([a-z0-9])([A-Z])/g, '$1 $2').split(/[^A-Za-z0-9]+/).filter(Boolean);
  }

  var framework = {
    Property: Property,
    PieceAuth: PieceAuth,
    ProviderAuth: PieceAuth,
    createAction: createAction,
    createTrigger: createTrigger,
    createPiece: createPiece,
    createProvider: createPiece,
    PieceCategory: names(),
    ProviderCategory: names(),
    TriggerStrategy: names(),
    StoreScope: names(),
    MarkdownVariant: names(),
    AppConnectionType: names(),
    WebhookHandshakeStrategy: names(),
    OAuth2GrantType: names(),
    OAuth2AuthorizationMethod: names(),
    ExecutionType: names(),
    PropertyType: names(),
    DEDUPE_KEY_PROPERTY: '_dedupe_key',
    getAuthPropertyForValue: function (o) {
      var a = o && o.pieceAuth;
      return Array.isArray(a) ? a[0] : a;
    },
    isNil: isNil,
    isEmpty: isEmpty,
    isString: function (v) { return typeof v === 'string'; },
    isNotUndefined: function (v) { return v !== undefined; },
    assertNotNullOrUndefined: assertNotNullOrUndefined,
    spreadIfDefined: function (k, v) { var o = {}; if (v !== undefined && v !== null) o[k] = v; return o; },
    chunk: function (a, n) { var out = []; for (var i = 0; i < (a || []).length; i += n) out.push(a.slice(i, i + n)); return out; },
    unique: function (a) { return Array.from(new Set(a || [])); },
    pickBy: function (o, f) { var out = {}; Object.keys(o || {}).forEach(function (k) { if (f(o[k], k)) out[k] = o[k]; }); return out; },
    kebabCase: function (s) { return words(s).join('-').toLowerCase(); },
    startCase: function (s) { return words(s).map(function (w) { return w.charAt(0).toUpperCase() + w.slice(1); }).join(' '); },
    tryCatch: async function (fn) { try { return { data: await fn(), error: null }; } catch (e) { return { data: null, error: e }; } },
    tryCatchSync: function (fn) { try { return { data: fn(), error: null }; } catch (e) { return { data: null, error: e }; } },
  };

  // ---- @activepieces/pieces-common ------------------------------------------

  var HttpMethod = {
    GET: 'GET', POST: 'POST', PUT: 'PUT', PATCH: 'PATCH', DELETE: 'DELETE', HEAD: 'HEAD',
  };
  var AuthenticationType = {
    BEARER_TOKEN: 'BEARER_TOKEN', BASIC: 'BASIC', API_KEY: 'API_KEY',
  };

  // HttpError is pieces-common's: thrown for any status outside 2xx, carrying
  // the response a piece's catch reads (error.response.status / .body).
  function HttpError(requestBody, params) {
    var status = (params && params.status) || 500;
    var body = params && params.responseBody;
    this.name = 'HttpError';
    this.message = JSON.stringify({ response: { status: status, body: body }, request: { body: requestBody } });
    this.status = status;
    this.responseBody = body;
    this.response = { status: status, body: body };
    this.request = { body: requestBody };
  }
  HttpError.prototype = Object.create(Error.prototype);
  HttpError.prototype.constructor = HttpError;
  HttpError.prototype.errorMessage = function () {
    return { response: this.response, request: this.request };
  };

  function formEncode(o) {
    return Object.keys(o).filter(function (k) { return o[k] !== undefined && o[k] !== null; })
      .map(function (k) { return encodeURIComponent(k) + '=' + encodeURIComponent(String(o[k])); }).join('&');
  }

  // httpClient.sendRequest is pieces-common's: Accept JSON, the request's
  // `authentication` turned into its Authorization header, a JSON body unless
  // the piece said otherwise, the piece's own headers last, and an HttpError
  // for any answer outside 2xx. The call itself goes to the Go doer, which
  // resolves synchronously, so `await` and `.then` both work.
  var httpClient = {
    sendRequest: function (req) {
      return new Promise(function (resolve, reject) {
        try {
          req = req || {};
          var headers = { Accept: 'application/json' };
          var a = req.authentication;
          if (a && a.type === 'BEARER_TOKEN') headers.Authorization = 'Bearer ' + a.token;
          if (a && a.type === 'BASIC') headers.Authorization = 'Basic ' + Buffer.from(a.username + ':' + a.password).toString('base64');
          var method = String(req.method || 'GET').toUpperCase();
          var body = method === 'GET' || method === 'HEAD' ? undefined : req.body;
          var given = req.headers || {};
          if (body !== undefined && body !== null) {
            headers['Content-Type'] = given['Content-Type'] === 'text/csv' ? 'text/csv' : 'application/json';
          }
          Object.keys(given).forEach(function (k) { if (given[k] !== undefined) headers[k] = given[k]; });
          var ctype = String(headers['Content-Type'] || headers['content-type'] || '');
          if (body && typeof body === 'object' && !body.__isBuffer && ctype.indexOf('application/x-www-form-urlencoded') >= 0) {
            body = formEncode(body);
          }
          var resp = g.__hanzoHttpSend({
            method: method,
            url: req.url,
            headers: headers,
            queryParams: req.queryParams,
            body: body && body.__isBuffer ? body.toString('latin1') : body,
            timeout: req.timeout,
          });
          if (resp.status < 200 || resp.status >= 300) {
            reject(new HttpError(req.body, { status: resp.status, responseBody: resp.body }));
            return;
          }
          if (req.responseType === 'text' && typeof resp.body !== 'string') {
            resp.body = JSON.stringify(resp.body);
          }
          resolve(resp);
        } catch (e) { reject(e); }
      });
    },
  };

  function getAccessTokenOrThrow(auth) {
    var t = auth && auth.access_token;
    if (t === undefined || t === null) throw new Error('Invalid bearer token');
    return t;
  }

  function joinUrl(base, rel) {
    base = base || '';
    if (base.charAt(base.length - 1) !== '/') base += '/';
    if (rel.charAt(0) === '/') rel = rel.slice(1);
    return base + rel;
  }

  // createCustomApiCallAction is the generic HTTP action every provider gets.
  // It is behaviourally identical to @activepieces/pieces-common's own: build
  // the request from propsValue (url/method/headers/queryParams/body[_type]),
  // fold in the provider authMapping, send. This is the exact action the KB
  // long-tail sync invokes (notion custom_api_call), so shimming it here is
  // what lets KB run native.
  function createCustomApiCallAction(opts) {
    opts = opts || {};
    var baseUrl = opts.baseUrl || function () { return ''; };
    var authMapping = opts.authMapping;
    var authLocation = opts.authLocation || 'headers';
    return createAction({
      name: opts.name || 'custom_api_call',
      displayName: opts.displayName || 'Custom API Call',
      description: opts.description || 'Make a custom API call to a specific endpoint',
      requireAuth: !!opts.auth,
      props: opts.props || {},
      run: async function (ctx) {
        var pv = ctx.propsValue || {};
        var method = pv.method;
        var headers = pv.headers || {};
        var queryParams = pv.queryParams || {};
        var body = pv.body;
        var bodyType = pv.body_type;
        var urlProp = pv.url;
        var urlValue = (urlProp && typeof urlProp === 'object') ? urlProp.url : urlProp;
        if (!method) throw new Error('Method is required');
        if (!urlValue) throw new Error('URL is required');

        var authValue = authMapping ? await authMapping(ctx.auth, pv) : {};
        var fullUrl =
          (urlValue.indexOf('http://') === 0 || urlValue.indexOf('https://') === 0)
            ? urlValue
            : joinUrl(baseUrl(ctx.auth), urlValue);

        var reqHeaders = Object.assign({}, headers, authLocation === 'headers' ? authValue : {});
        var reqQuery = Object.assign({}, authLocation === 'queryParams' ? authValue : {}, queryParams);

        var reqBody;
        if (body) {
          if (bodyType && bodyType !== 'none') reqBody = body.data;
          else if (!bodyType) reqBody = body;
        }

        try {
          var resp = await httpClient.sendRequest({
            method: method,
            url: fullUrl,
            headers: reqHeaders,
            queryParams: reqQuery,
            body: reqBody,
            timeout: pv.timeout ? pv.timeout * 1000 : 0,
          });
          return { status: resp.status, headers: resp.headers, body: resp.body };
        } catch (e) {
          // failsafe answers the failure as output instead of failing the
          // step, in pieces-common's own shape.
          if (!pv.failsafe) throw e;
          if (e instanceof HttpError) return e.errorMessage();
          return { response: { status: 0, body: e && e.message ? e.message : String(e) }, request: { body: reqBody } };
        }
      },
    });
  }

  var common = {
    httpClient: httpClient,
    HttpMethod: HttpMethod,
    AuthenticationType: AuthenticationType,
    getAccessTokenOrThrow: getAccessTokenOrThrow,
    createCustomApiCallAction: createCustomApiCallAction,
    HttpError: HttpError,
    DedupeStrategy: names(),
    // Validation of props happens before an action runs; the schemas a piece
    // passes here come from zod, which a runnable program does not carry.
    propsValidation: { validateZod: async function () {} },
  };

  // ---- @activepieces/shared (pure helpers pieces import) --------------------

  var shared = {
    isNil: isNil,
    isEmpty: isEmpty,
    assertNotNullOrUndefined: assertNotNullOrUndefined,
    PieceCategory: names(),
    TriggerStrategy: names(),
  };

  // ---- Buffer ----------------------------------------------------------------

  // Buffer is the one Node global pieces reach for without importing it:
  // Basic credentials, base64 bodies, hex digests. This is that much of it —
  // from/toString over utf8, base64, base64url, hex and latin1 — and nothing
  // that touches a file or a stream.
  var B64 = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/';
  function utf8Encode(s) {
    var out = [];
    for (var i = 0; i < s.length; i++) {
      var c = s.charCodeAt(i);
      if (c >= 0xd800 && c < 0xdc00 && i + 1 < s.length) {
        var d = s.charCodeAt(i + 1);
        if (d >= 0xdc00 && d < 0xe000) { c = 0x10000 + ((c - 0xd800) << 10) + (d - 0xdc00); i++; }
      }
      if (c < 0x80) out.push(c);
      else if (c < 0x800) out.push(0xc0 | (c >> 6), 0x80 | (c & 63));
      else if (c < 0x10000) out.push(0xe0 | (c >> 12), 0x80 | ((c >> 6) & 63), 0x80 | (c & 63));
      else out.push(0xf0 | (c >> 18), 0x80 | ((c >> 12) & 63), 0x80 | ((c >> 6) & 63), 0x80 | (c & 63));
    }
    return out;
  }
  function utf8Decode(b) {
    var s = '';
    for (var i = 0; i < b.length;) {
      var c = b[i++];
      if (c >= 0xf0) c = ((c & 7) << 18) | ((b[i++] & 63) << 12) | ((b[i++] & 63) << 6) | (b[i++] & 63);
      else if (c >= 0xe0) c = ((c & 15) << 12) | ((b[i++] & 63) << 6) | (b[i++] & 63);
      else if (c >= 0xc0) c = ((c & 31) << 6) | (b[i++] & 63);
      s += String.fromCodePoint(c);
    }
    return s;
  }
  function b64Encode(b, url) {
    var s = '';
    for (var i = 0; i < b.length; i += 3) {
      var n = (b[i] << 16) | ((b[i + 1] || 0) << 8) | (b[i + 2] || 0);
      s += B64[(n >> 18) & 63] + B64[(n >> 12) & 63] +
        (i + 1 < b.length ? B64[(n >> 6) & 63] : '=') + (i + 2 < b.length ? B64[n & 63] : '=');
    }
    return url ? s.replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '') : s;
  }
  function b64Decode(s) {
    s = String(s).replace(/-/g, '+').replace(/_/g, '/').replace(/[^A-Za-z0-9+/]/g, '');
    var out = [];
    for (var i = 0; i < s.length; i += 4) {
      var n = 0, k = 0;
      for (var j = 0; j < 4; j++) {
        var v = i + j < s.length ? B64.indexOf(s[i + j]) : -1;
        n = (n << 6) | (v < 0 ? 0 : v);
        if (v >= 0) k++;
      }
      if (k > 1) out.push((n >> 16) & 255);
      if (k > 2) out.push((n >> 8) & 255);
      if (k > 3) out.push(n & 255);
    }
    return out;
  }
  function toBuffer(bytes) {
    var u = new Uint8Array(bytes);
    u.__isBuffer = true;
    u.toString = function (enc) {
      enc = String(enc || 'utf8').toLowerCase();
      var arr = Array.prototype.slice.call(u);
      if (enc === 'base64') return b64Encode(arr, false);
      if (enc === 'base64url') return b64Encode(arr, true);
      if (enc === 'hex') return arr.map(function (x) { return (x < 16 ? '0' : '') + x.toString(16); }).join('');
      if (enc === 'latin1' || enc === 'binary' || enc === 'ascii') return String.fromCharCode.apply(null, arr);
      return utf8Decode(arr);
    };
    u.toJSON = function () { return { type: 'Buffer', data: Array.prototype.slice.call(u) }; };
    return u;
  }
  function Buffer() { throw new Error('Buffer: use Buffer.from'); }
  Buffer.from = function (v, enc) {
    if (typeof v === 'string') {
      enc = String(enc || 'utf8').toLowerCase();
      if (enc === 'base64' || enc === 'base64url') return toBuffer(b64Decode(v));
      if (enc === 'hex') {
        var h = [];
        for (var i = 0; i + 1 < v.length; i += 2) h.push(parseInt(v.substr(i, 2), 16));
        return toBuffer(h);
      }
      if (enc === 'latin1' || enc === 'binary' || enc === 'ascii') {
        return toBuffer(Array.prototype.map.call(v, function (ch) { return ch.charCodeAt(0) & 255; }));
      }
      return toBuffer(utf8Encode(v));
    }
    if (v instanceof ArrayBuffer) return toBuffer(Array.prototype.slice.call(new Uint8Array(v)));
    if (v && typeof v.length === 'number') return toBuffer(Array.prototype.slice.call(v));
    if (v && v.type === 'Buffer' && Array.isArray(v.data)) return toBuffer(v.data);
    throw new Error('Buffer.from: unsupported value');
  };
  Buffer.alloc = function (n) { return toBuffer(new Array(n).fill(0)); };
  Buffer.isBuffer = function (v) { return !!(v && v.__isBuffer); };
  Buffer.byteLength = function (v, enc) { return typeof v === 'string' ? Buffer.from(v, enc).length : (v && v.length) || 0; };
  Buffer.concat = function (list) {
    var out = [];
    (list || []).forEach(function (b) { Array.prototype.push.apply(out, Array.prototype.slice.call(b)); });
    return toBuffer(out);
  };
  if (typeof g.Buffer === 'undefined') g.Buffer = Buffer;
  if (typeof g.btoa === 'undefined') g.btoa = function (s) { return Buffer.from(String(s), 'latin1').toString('base64'); };
  if (typeof g.atob === 'undefined') g.atob = function (s) { return Buffer.from(String(s), 'base64').toString('latin1'); };

  // ---- module resolution -----------------------------------------------------

  g.__ap_modules = {
    '@activepieces/pieces-framework': framework,
    '@activepieces/pieces-common': common,
    '@activepieces/providers-framework': framework,
    '@activepieces/providers-common': common,
    '@activepieces/shared': shared,
  };
  g.require = function (name) {
    var m = g.__ap_modules[name];
    if (m) return m;
    throw new Error('connectorruntime: module not available in-process: ' + name);
  };

  // __makeContext builds the ActionContext an action's run(ctx) receives.
  // auth + propsValue are the real inputs; the rest are safe in-process stubs
  // (an in-memory store, no-op files/connections/server) so a connector that
  // touches them does not throw. HTTP-only connectors ignore all but the first
  // two.
  g.__makeContext = function (auth, propsValue) {
    var mem = {};
    // A few pieces still read a secret-text credential as the bare string
    // (`Bearer ${auth}`), from before it became {secret_text}. Both work.
    if (auth && typeof auth === 'object' && typeof auth.secret_text === 'string') {
      auth = Object.assign({}, auth);
      Object.defineProperty(auth, 'toString', { value: function () { return this.secret_text; } });
    }
    return {
      auth: auth,
      propsValue: propsValue || {},
      store: {
        get: async function (k) { return k in mem ? mem[k] : null; },
        put: async function (k, v) { mem[k] = v; return v; },
        delete: async function (k) { delete mem[k]; },
      },
      files: {
        write: async function (o) { return (o && o.data) || null; },
      },
      connections: {
        get: async function () { return null; },
      },
      server: { apiUrl: '', publicUrl: '', token: '' },
      project: { id: '', externalId: async function () { return undefined; } },
      run: { id: '', stop: function () {}, pause: function () {} },
      generateResumeUrl: function () { return ''; },
      flows: { current: { id: '', version: { id: '' } } },
      step: { name: '' },
      payload: {},
    };
  };
})(globalThis);
