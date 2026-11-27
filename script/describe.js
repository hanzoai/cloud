// describe.js reads what a piece declares about itself — its name, auth,
// actions and triggers — without running any of it. It is evaluated after
// shim.js, only by Describe, which is an offline ingest step (cmd/piece).
//
// A piece's module body runs to reach createPiece, and that body touches
// whatever npm packages the piece uses (a client constructed at load, an
// enum read off an SDK). Those are not here, so require() answers every name
// the shim does not know with a stand-in that accepts any use: it can be
// called, constructed, extended and read, and a value read off it is its own
// name. The declarations a catalog needs are plain literals, so they come
// through; anything that needed the real package is a function nobody calls.
(function (g) {
  'use strict';

  var STUB = '__hanzoStub';

  // Every read answers a stand-in named for what was read, except the few
  // names the module system itself asks about. __esModule reads false so an
  // `import x from 'pkg'` binds the stand-in as the default export; the
  // prototype is itself a stand-in so a namespace import — which the bundler
  // builds as a fresh object inheriting from the module — still answers
  // `z.string` and `sdk.Client`.
  var inherited = new Proxy({}, {
    get: function (_, k) {
      if (typeof k !== 'string' || k === 'then' || k === '__esModule') return undefined;
      return stub(k);
    },
  });

  function stub(name) {
    var target = function () {};
    return new Proxy(target, {
      get: function (_, k) {
        if (k === STUB) return name;
        if (k === Symbol.toPrimitive) return function () { return name; };
        if (k === 'toString' || k === 'valueOf') return function () { return name; };
        if (k === 'then' || k === '__esModule') return undefined;
        if (k === 'prototype') return target.prototype;
        if (typeof k !== 'string') return undefined;
        return stub(k);
      },
      getPrototypeOf: function () { return inherited; },
      apply: function () { return stub(name); },
      construct: function () { return stub(name); },
    });
  }

  // A known module keeps every export it has and answers the rest with a
  // stand-in, so a helper added upstream after this shim was written does not
  // stop a piece from describing itself.
  function open(mod) {
    return new Proxy(mod, {
      get: function (t, k) {
        if (k in t) return t[k];
        if (typeof k !== 'string' || k === 'then') return undefined;
        return stub(k);
      },
    });
  }

  // The clock is stopped at the epoch. A piece that fills a default with "now"
  // at load would otherwise make every regeneration differ by the time it ran,
  // and a default of the moment the catalog was generated means nothing to the
  // person filling the form; such a default is left out (see props).
  var RealDate = Date;
  function StoppedDate() {
    if (!(this instanceof StoppedDate)) return new RealDate(0).toString();
    var args = Array.prototype.slice.call(arguments);
    return args.length ? new (Function.prototype.bind.apply(RealDate, [null].concat(args)))() : new RealDate(0);
  }
  StoppedDate.prototype = RealDate.prototype;
  StoppedDate.now = function () { return 0; };
  StoppedDate.parse = RealDate.parse;
  StoppedDate.UTC = RealDate.UTC;
  g.Date = StoppedDate;
  var EPOCH = new RealDate(0);
  function stopped(v) {
    return typeof v === 'string' && (v.indexOf('1970-01-01') === 0 || v === EPOCH.toString());
  }

  var known = g.__ap_modules;
  g.require = function (name) {
    var m = known[name];
    return m ? open(m) : stub(name);
  };

  function text(v) {
    if (v === null || v === undefined) return '';
    if (typeof v === 'string') return v;
    if (typeof v === 'number' || typeof v === 'boolean') return String(v);
    if (typeof v === 'function' && v[STUB] !== undefined) return String(v[STUB]);
    return '';
  }
  function scalar(v) {
    if (typeof v === 'string' || typeof v === 'number' || typeof v === 'boolean') return v;
    return text(v);
  }

  function options(p) {
    if (p.type !== 'STATIC_DROPDOWN' && p.type !== 'STATIC_MULTI_SELECT_DROPDOWN') return undefined;
    var o = p.options && p.options.options;
    if (!Array.isArray(o)) return undefined;
    return o.slice(0, 200).filter(Boolean).map(function (x) {
      return { label: text(x.label), value: scalar(x.value) };
    });
  }

  function props(m) {
    var out = [];
    if (!m || typeof m !== 'object') return out;
    Object.keys(m).forEach(function (k) {
      var p = m[k];
      if (!p || typeof p !== 'object') return;
      out.push({
        name: k,
        displayName: text(p.displayName),
        description: text(p.description),
        type: text(p.type),
        required: p.required === true,
        options: options(p),
        default: (typeof p.defaultValue === 'string' || typeof p.defaultValue === 'number' ||
          typeof p.defaultValue === 'boolean') && !stopped(p.defaultValue) ? p.defaultValue : undefined,
      });
    });
    return out;
  }

  function strings(a) {
    return Array.isArray(a) ? a.map(text).filter(Boolean) : [];
  }
  function stringMap(o) {
    var out = {};
    if (!o || typeof o !== 'object') return out;
    Object.keys(o).forEach(function (k) { var v = text(o[k]); if (v) out[k] = v; });
    return out;
  }

  function auth(a) {
    var list = Array.isArray(a) ? a : a ? [a] : [];
    return list.filter(function (x) { return x && typeof x === 'object'; }).map(function (x) {
      var oauth;
      if (text(x.type) === 'OAUTH2') {
        oauth = {
          authUrl: text(x.authUrl),
          tokenUrl: text(x.tokenUrl),
          scopes: strings(x.scope),
          pkce: x.pkce === true,
          pkceMethod: text(x.pkceMethod),
          prompt: text(x.prompt),
          method: text(x.authorizationMethod),
          grant: text(x.grantType),
          extra: stringMap(x.extra),
        };
      }
      return {
        type: text(x.type),
        displayName: text(x.displayName),
        description: text(x.description),
        required: x.required !== false,
        oauth: oauth,
        props: props(x.props),
        validate: typeof x.validate === 'function',
      };
    });
  }

  function ops(m, trigger) {
    return Object.keys(m || {}).map(function (k) {
      var o = m[k];
      return {
        name: text(o.name) || k,
        displayName: text(o.displayName),
        description: text(o.description),
        props: props(o.props),
        strategy: trigger ? text(o.type) : undefined,
      };
    });
  }

  g.__describe = function () {
    var list = g.__ap_pieces;
    if (!list || list.length === 0) throw new Error('the module never called createPiece');
    var p = list[list.length - 1];
    return JSON.stringify({
      name: text(p.displayName),
      description: text(p.description),
      logoUrl: text(p.logoUrl),
      categories: strings(p.categories),
      deprecated: !!p.deprecated,
      auth: auth(p.auth),
      actions: ops(p._actions, false),
      triggers: ops(p._triggers, true),
    });
  };
})(globalThis);
