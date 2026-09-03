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

  // Property.* / PieceAuth.* are pure metadata builders: they return the
  // config object tagged with a type. Execution never inspects the schema —
  // propsValue arrives already resolved — so a shallow copy is faithful.
  function tagged(type) {
    return function (cfg) {
      return Object.assign({ type: type, valueSchema: undefined }, cfg || {});
    };
  }
  var Property = {
    ShortText: tagged('SHORT_TEXT'),
    LongText: tagged('LONG_TEXT'),
    Number: tagged('NUMBER'),
    Checkbox: tagged('CHECKBOX'),
    Json: tagged('JSON'),
    Object: tagged('OBJECT'),
    Array: tagged('ARRAY'),
    File: tagged('FILE'),
    DateTime: tagged('DATE_TIME'),
    Color: tagged('COLOR'),
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
    BasicAuth: tagged('BASIC_AUTH'),
    CustomAuth: tagged('CUSTOM_AUTH'),
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
    this._actions = {};
    (params.actions || []).forEach(function (a) { this._actions[a.name] = a; }, this);
    this._triggers = {};
    (params.triggers || []).forEach(function (t) { this._triggers[t.name] = t; }, this);
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

  var framework = {
    Property: Property,
    PieceAuth: PieceAuth,
    createAction: createAction,
    createTrigger: createTrigger,
    createPiece: createPiece,
    PieceCategory: {},
    getAuthPropertyForValue: function () { return undefined; },
  };

  // ---- @activepieces/pieces-common ------------------------------------------

  var HttpMethod = {
    GET: 'GET', POST: 'POST', PUT: 'PUT', PATCH: 'PATCH', DELETE: 'DELETE', HEAD: 'HEAD',
  };
  var AuthenticationType = {
    BEARER_TOKEN: 'BEARER_TOKEN', BASIC: 'BASIC', API_KEY: 'API_KEY',
  };

  // httpClient.sendRequest hands the request to the Go doer synchronously and
  // wraps the result in a resolved Promise so `await httpClient.sendRequest`
  // and `.then(...)` chains both work.
  var httpClient = {
    sendRequest: function (req) {
      return new Promise(function (resolve, reject) {
        try { resolve(g.__hanzoHttpSend(req)); }
        catch (e) { reject(e); }
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

        var resp = await httpClient.sendRequest({
          method: method,
          url: fullUrl,
          headers: reqHeaders,
          queryParams: reqQuery,
          body: reqBody,
        });
        return { status: resp.status, headers: resp.headers, body: resp.body };
      },
    });
  }

  var common = {
    httpClient: httpClient,
    HttpMethod: HttpMethod,
    AuthenticationType: AuthenticationType,
    getAccessTokenOrThrow: getAccessTokenOrThrow,
    createCustomApiCallAction: createCustomApiCallAction,
    // HttpError is referenced as a type by some pieces; a constructor keeps
    // `instanceof`/`new HttpError` from throwing if executed.
    HttpError: function HttpError(request, cause) { this.request = request; this.cause = cause; },
  };

  // ---- @activepieces/shared (pure helpers pieces import) --------------------

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
  var shared = {
    isNil: isNil,
    isEmpty: isEmpty,
    assertNotNullOrUndefined: assertNotNullOrUndefined,
    PieceCategory: {},
    TriggerStrategy: { POLLING: 'POLLING', WEBHOOK: 'WEBHOOK', APP_WEBHOOK: 'APP_WEBHOOK' },
  };

  // ---- module resolution -----------------------------------------------------

  g.__ap_modules = {
    '@activepieces/pieces-framework': framework,
    '@activepieces/pieces-common': common,
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
