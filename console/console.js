// Dev console for the local cloud binary. Plain browser JavaScript: no build,
// no dependencies, and no request that leaves this origin.
//
// Everything on this page is derived from ONE source, /v1/openapi.json, which is
// the live router projected as a document. So the console cannot show a route
// this process does not serve, and a primitive that was never mounted shows up
// as missing instead of as a broken button.
'use strict';

const $ = (sel, root = document) => root.querySelector(sel);

// el builds a node. Text always goes in as text and never as markup: every
// string here came off the wire, and none of it is trusted to be HTML.
function el(tag, attrs, ...kids) {
  const n = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v == null || v === false) continue;
    if (k === 'class') n.className = v;
    else if (k.startsWith('on')) n.addEventListener(k.slice(2), v);
    else n.setAttribute(k, v === true ? '' : String(v));
  }
  for (const kid of kids.flat(9)) if (kid != null) n.append(kid);
  return n;
}

// key is the identity a route is looked up by. Parameter NAMES are dropped:
// whether the store calls it {key} or {id} is its business, and the console only
// needs to know that a route of this shape exists.
const key = (method, path) => method.toUpperCase() + ' ' + path.replace(/\{[^}]*\}/g, '{}');

const argsOf = (path) => [...path.matchAll(/\{([^}]+)\}/g)].map((m) => m[1]);

const state = {
  routes: [],      // {method, path, tag, args}
  have: new Set(), // key(method, path) for every live route
  tags: new Set(), // products this deployment actually mounted
  template: '/v1/openapi.json',
  args: {},        // path argument values, kept across route switches
  panels: [],
};

// The primitives the dev edition can mount. Each action names the route SHAPE it
// wants and, when it sends one, the body that shape expects; whether the route
// exists is answered by the live document, never assumed. A panel whose product
// is absent says so and stays out of the way.
const PRIMITIVES = [
  {
    tag: 'kv', title: 'kv', blurb: 'Key/value in the caller namespace.',
    fields: ['key'],
    actions: [
      { label: 'list', method: 'GET', path: '/v1/kv' },
      { label: 'get', method: 'GET', path: '/v1/kv/{key}' },
      { label: 'put', method: 'PUT', path: '/v1/kv/{key}', body: '{\n  "value": "hello"\n}' },
      { label: 'delete', method: 'DELETE', path: '/v1/kv/{key}' },
    ],
  },
  {
    tag: 'base', title: 'base', blurb: 'Collections of JSON documents.',
    fields: ['collection', 'id'],
    actions: [
      { label: 'collections', method: 'GET', path: '/v1/base/collections' },
      { label: 'records', method: 'GET', path: '/v1/base/collections/{collection}' },
      { label: 'create', method: 'POST', path: '/v1/base/collections/{collection}', body: '{\n  "doc": { "hello": "world" }\n}' },
      { label: 'read', method: 'GET', path: '/v1/base/collections/{collection}/{id}' },
      { label: 'update', method: 'PUT', path: '/v1/base/collections/{collection}/{id}', body: '{\n  "doc": { "hello": "again" }\n}' },
      { label: 'delete', method: 'DELETE', path: '/v1/base/collections/{collection}/{id}' },
    ],
  },
  {
    tag: 'tasks', title: 'tasks', blurb: 'Submit work, list it, lease it as a worker would.',
    fields: ['id'],
    actions: [
      { label: 'submit', method: 'POST', path: '/v1/tasks', body: '{\n  "kind": "demo",\n  "payload": {},\n  "maxAttempts": 3\n}' },
      { label: 'list', method: 'GET', path: '/v1/tasks' },
      { label: 'lease', method: 'POST', path: '/v1/tasks/lease', body: '{\n  "kind": "demo",\n  "seconds": 30\n}' },
      { label: 'read', method: 'GET', path: '/v1/tasks/{id}' },
      { label: 'complete', method: 'POST', path: '/v1/tasks/{id}/complete', body: '{\n  "result": {}\n}' },
      { label: 'fail', method: 'POST', path: '/v1/tasks/{id}/fail', body: '{\n  "error": "boom"\n}' },
      { label: 'cancel', method: 'DELETE', path: '/v1/tasks/{id}' },
    ],
  },
  {
    tag: 'sql', title: 'sql', blurb: 'Run one query against the local database.',
    fields: [],
    actions: [
      { label: 'query', method: 'POST', path: '/v1/sql/query', body: '{\n  "sql": "SELECT 1"\n}' },
      { label: 'tables', method: 'GET', path: '/v1/sql/tables' },
    ],
  },
];

// ---- the wire ---------------------------------------------------------------

function headersFrom(text) {
  const out = {};
  for (const line of (text || '').split('\n')) {
    const at = line.indexOf(':');
    if (at > 0) out[line.slice(0, at).trim()] = line.slice(at + 1).trim();
  }
  return out;
}

// request never throws. A dead socket and a 500 are both results a developer
// wants to read, so both come back as one shape and the caller renders it.
async function request(method, path, body, headers) {
  const init = { method, headers: Object.assign({}, headers) };
  if (body && body.trim() && method !== 'GET' && method !== 'HEAD') {
    init.body = body;
    if (!init.headers['Content-Type']) init.headers['Content-Type'] = 'application/json';
  }
  const t0 = performance.now();
  try {
    const res = await fetch(path, init);
    const text = await res.text();
    return { res, text, ms: Math.round(performance.now() - t0) };
  } catch (e) {
    return { error: String(e && e.message ? e.message : e), ms: Math.round(performance.now() - t0) };
  }
}

function classOf(status) {
  if (status < 300) return 'ok';
  if (status < 400) return 'info';
  if (status < 500) return 'warn';
  return 'down';
}

function render(out, method, path, r) {
  out.replaceChildren();
  if (r.error) {
    out.append(el('p', { class: 'note' }, 'no response: ' + r.error));
    return;
  }
  const type = r.res.headers.get('content-type') || '';
  let shown = r.text;
  if (type.includes('json') || (r.text.startsWith('{') || r.text.startsWith('['))) {
    try { shown = JSON.stringify(JSON.parse(r.text), null, 2); } catch { /* leave it raw */ }
  }
  out.append(el('div', { class: 'status' },
    el('span', { class: 'code ' + classOf(r.res.status) }, String(r.res.status)),
    el('span', { class: 'muted' }, method + ' ' + path),
    el('span', { class: 'chip' }, r.ms + ' ms'),
    el('span', { class: 'chip' }, new Blob([r.text]).size + ' B'),
    type ? el('span', { class: 'chip' }, type.split(';')[0]) : null,
  ));
  if (r.res.status === 404) {
    out.append(el('p', { class: 'note' },
      'nothing serves this path in this build — the deployment mounts only what it was configured to.'));
  }
  out.append(el('pre', {}, shown || '(empty body)'));
}

// ---- the route table --------------------------------------------------------

async function loadSpec() {
  const r = await request('GET', '/v1/openapi.json', '', {});
  state.routes = [];
  state.have = new Set();
  state.tags = new Set();
  if (r.error || !r.res.ok) {
    $('#version').textContent = r.error ? 'unreachable' : 'spec ' + r.res.status;
    return;
  }
  let doc;
  try { doc = JSON.parse(r.text); } catch { $('#version').textContent = 'spec unreadable'; return; }

  for (const [path, item] of Object.entries(doc.paths || {})) {
    for (const [method, op] of Object.entries(item)) {
      const m = method.toUpperCase();
      const tag = (op && op.tags && op.tags[0]) || 'other';
      state.routes.push({ method: m, path, tag, args: argsOf(path) });
      state.have.add(key(m, path));
      state.tags.add(tag);
    }
  }
  state.routes.sort((a, b) => a.path.localeCompare(b.path) || a.method.localeCompare(b.method));

  $('#title').textContent = (doc.info && doc.info.title) || 'cloud';
  document.title = $('#title').textContent + ' console';
  const version = r.res.headers.get('x-api-version') || (doc.info && doc.info.version) || 'unknown';
  $('#version').textContent = 'version ' + version;
}

// ---- header, health ---------------------------------------------------------

function pill(label, cls, title) {
  return el('span', { class: 'pill', title: title || '' }, el('i', { class: 'dot ' + cls }), el('b', {}, label));
}

// Health is probed, not claimed: every product that serves /v1/<product>/health
// gets asked, and the answer is what the pill shows.
function renderHealth() {
  const strip = $('#health');
  strip.replaceChildren();
  const up = state.routes.length > 0;
  strip.append(pill(up ? state.routes.length + ' routes' : 'no route table', up ? 'ok' : 'down',
    up ? 'read from /v1/openapi.json' : 'the router did not answer'));

  const probes = [...state.tags].filter((t) => state.have.has('GET /v1/' + t + '/health')).sort();
  if (!probes.length) {
    strip.append(el('span', { class: 'muted' }, 'no product publishes a health route'));
    return;
  }
  for (const tag of probes) {
    const p = pill(tag, '', 'GET /v1/' + tag + '/health');
    strip.append(p);
    request('GET', '/v1/' + tag + '/health', '', {}).then((r) => {
      const dot = $('.dot', p);
      dot.className = 'dot ' + (r.error ? 'down' : r.res.ok ? 'ok' : 'warn');
      p.title = r.error ? r.error : r.res.status + ' in ' + r.ms + ' ms';
    });
  }
}

// ---- route tree -------------------------------------------------------------

// routeButton renders one route. Path arguments are tinted so the shape of a
// route reads at a glance.
function routeButton(r) {
  const parts = r.path.split(/(\{[^}]*\})/).filter(Boolean).map((s) =>
    s.startsWith('{') ? el('span', { class: 'arg' }, s) : s);
  return el('button', {
    class: 'route', 'data-key': key(r.method, r.path), title: r.method + ' ' + r.path,
    onclick: () => select(r),
  }, el('span', { class: 'm m-' + r.method }, r.method), el('span', { class: 'p' }, parts));
}

function renderTree() {
  const q = $('#filter').value.trim().toLowerCase();
  const hits = state.routes.filter((r) => !q || (r.method + ' ' + r.path).toLowerCase().includes(q));
  const groups = new Map();
  for (const r of hits) {
    if (!groups.has(r.tag)) groups.set(r.tag, []);
    groups.get(r.tag).push(r);
  }

  const tree = $('#tree');
  tree.replaceChildren(...[...groups.keys()].sort().map((tag) =>
    el('details', { class: 'group', open: q !== '' || groups.size < 8 },
      el('summary', {}, tag, el('span', { class: 'n' }, groups.get(tag).length)),
      groups.get(tag).map(routeButton))));

  $('#count').textContent = hits.length + ' of ' + state.routes.length + ' routes · ' + state.tags.size + ' products';
  markCurrent();
}

function markCurrent() {
  const cur = key($('#method').value, state.template);
  for (const b of document.querySelectorAll('.route')) {
    b.setAttribute('aria-current', String(b.dataset.key === cur));
  }
}

// ---- the runner -------------------------------------------------------------

function applyTemplate(t) {
  state.template = t;
  const box = $('#params');
  box.replaceChildren(...argsOf(t).map((name) =>
    el('label', {}, name, el('input', {
      value: state.args[name] || '', placeholder: name, spellcheck: 'false', autocomplete: 'off',
      oninput: (e) => { state.args[name] = e.target.value; e.target.classList.remove('bad'); },
    }))));
  markCurrent();
}

function setTemplate(t) { $('#path').value = t; applyTemplate(t); }

function select(r) {
  const known = [...$('#method').options].some((o) => o.value === r.method);
  $('#method').value = known ? r.method : 'GET';
  setTemplate(r.path);
  show('api');
  $('#path').focus();
}

// resolve fills the template from the argument inputs. A blank argument is
// flagged where it is missing rather than sent as an empty segment.
function resolve(template, box) {
  let missing = false;
  const path = template.replace(/\{([^}]+)\}/g, (_, name) => {
    const input = [...box.querySelectorAll('input')].find((i) => i.placeholder === name);
    const v = (input ? input.value : state.args[name] || '').trim();
    if (!v) { missing = true; if (input) input.classList.add('bad'); }
    return encodeURIComponent(v);
  });
  return missing ? null : path;
}

async function send() {
  const out = $('#out');
  const method = $('#method').value;
  const path = resolve(state.template, $('#params'));
  if (!path) { out.replaceChildren(el('p', { class: 'note' }, 'fill in the path arguments first.')); return; }
  out.replaceChildren(el('p', { class: 'muted' }, 'sending…'));
  render(out, method, path, await request(method, path, $('#body').value, headersFrom($('#headers').value)));
}

function curl() {
  const method = $('#method').value;
  const path = resolve(state.template, $('#params')) || state.template;
  const headers = headersFrom($('#headers').value);
  const body = $('#body').value.trim();
  const parts = ['curl -sS -X ' + method + " '" + location.origin + path + "'"];
  if (body && method !== 'GET') headers['Content-Type'] = headers['Content-Type'] || 'application/json';
  for (const [k, v] of Object.entries(headers)) parts.push("-H '" + k + ': ' + v + "'");
  if (body && method !== 'GET') parts.push("-d '" + body.replace(/'/g, "'\\''") + "'");
  const line = parts.join(' \\\n  ');
  const done = () => { $('#curl').textContent = 'copied'; setTimeout(() => { $('#curl').textContent = 'copy curl'; }, 1200); };
  if (navigator.clipboard) navigator.clipboard.writeText(line).then(done, () => $('#out').replaceChildren(el('pre', {}, line)));
  else $('#out').replaceChildren(el('pre', {}, line));
}

// ---- primitive panels -------------------------------------------------------

function buildPanel(p) {
  const out = el('div', { class: 'out' });
  const fields = el('div', { class: 'params', hidden: p.fields.length === 0 }, p.fields.map((f) =>
    el('label', {}, f, el('input', {
      placeholder: f, spellcheck: 'false', autocomplete: 'off',
      oninput: (e) => e.target.classList.remove('bad'),
    }))));
  // Actions in a panel want different bodies, so each carries its own and the
  // box shows the one about to be sent — until the developer types, after which
  // what they wrote is what goes.
  let written = false;
  const body = el('textarea', { rows: '5', spellcheck: 'false', oninput: () => { written = true; } });
  const shape = p.actions.find((a) => a.body);
  body.value = shape ? shape.body : '';

  const btns = p.actions.map((a) => ({
    a,
    b: el('button', { class: 'ghost', title: a.method + ' ' + a.path, onclick: () => run(a) }, a.label),
  }));

  async function run(a) {
    const path = resolve(a.path, fields);
    if (!path) { out.replaceChildren(el('p', { class: 'note' }, 'fill in the fields this action needs.')); return; }
    if (a.body && !written) body.value = a.body;
    out.replaceChildren(el('p', { class: 'muted' }, 'sending…'));
    render(out, a.method, path, await request(a.method, path, a.body ? body.value : '', {}));
  }

  const badge = el('span', { class: 'chip' }, 'checking');
  const links = el('details', { class: 'links' }, el('summary', {}, 'live routes'));
  const view = el('section', { class: 'view', id: 'view-' + p.tag, hidden: true },
    el('section', { class: 'panel' },
      el('h2', {}, p.title, el('code', {}, '/v1/' + p.tag), badge),
      el('p', { class: 'muted' }, p.blurb),
      fields,
      el('div', { class: 'line thin' }, el('span', {}, 'body')),
      body,
      el('div', { class: 'actions' }, btns.map((x) => x.b)),
      out, links));

  state.panels.push({ p, badge, btns, links, out });
  return view;
}

function refreshPanels() {
  for (const panel of state.panels) {
    const mounted = state.tags.has(panel.p.tag);
    panel.badge.textContent = mounted ? 'mounted' : 'not mounted';
    for (const { a, b } of panel.btns) {
      const live = state.have.has(key(a.method, a.path));
      b.disabled = !live;
      b.title = a.method + ' ' + a.path + (live ? '' : ' — this deployment does not serve it');
    }
    const rs = state.routes.filter((r) => r.tag === panel.p.tag);
    panel.links.replaceChildren(el('summary', {}, 'live routes'), ...rs.map(routeButton));
    panel.links.hidden = rs.length === 0;
    panel.out.replaceChildren(mounted
      ? el('p', { class: 'muted' }, 'pick an action.')
      : el('p', { class: 'note' }, 'this build did not mount ' + panel.p.tag +
        ' — nothing under /v1/' + panel.p.tag + ' is served, so every action here would answer 404.'));
    const tab = $('#tab-' + panel.p.tag);
    if (tab) tab.className = mounted ? '' : 'off';
  }
}

// ---- views ------------------------------------------------------------------

function show(name) {
  for (const v of document.querySelectorAll('.view')) v.hidden = v.id !== 'view-' + name;
  for (const t of document.querySelectorAll('.tabs button')) {
    t.setAttribute('aria-selected', String(t.id === 'tab-' + name));
  }
  if (location.hash.slice(1) !== name) history.replaceState(null, '', '#' + name);
}

// ---- theme ------------------------------------------------------------------

const THEMES = ['auto', 'light', 'dark'];

function theme(next) {
  localStorage.setItem('console.theme', next);
  if (next === 'auto') document.documentElement.removeAttribute('data-theme');
  else document.documentElement.setAttribute('data-theme', next);
  $('#theme').textContent = next;
}

// ---- boot -------------------------------------------------------------------

async function refresh() {
  await loadSpec();
  renderHealth();
  renderTree();
  refreshPanels();
}

function boot() {
  theme(localStorage.getItem('console.theme') || 'auto');
  $('#theme').addEventListener('click', () =>
    theme(THEMES[(THEMES.indexOf($('#theme').textContent) + 1) % THEMES.length]));
  $('#origin').textContent = location.host;

  const main = $('main');
  const tabs = $('#tabs');
  tabs.append(el('button', { id: 'tab-api', onclick: () => show('api') }, 'api'));
  for (const p of PRIMITIVES) {
    main.append(buildPanel(p));
    tabs.append(el('button', { id: 'tab-' + p.tag, onclick: () => show(p.tag) }, p.title));
  }

  $('#send').addEventListener('click', send);
  $('#reload').addEventListener('click', refresh);
  $('#curl').addEventListener('click', curl);
  $('#format').addEventListener('click', () => {
    try { $('#body').value = JSON.stringify(JSON.parse($('#body').value), null, 2); } catch (e) {
      $('#out').replaceChildren(el('p', { class: 'note' }, 'body is not JSON: ' + e.message));
    }
  });
  $('#filter').addEventListener('input', renderTree);
  addEventListener('hashchange', () => show(location.hash.slice(1) || 'api'));
  $('#path').addEventListener('input', (e) => applyTemplate(e.target.value));
  $('#method').addEventListener('change', markCurrent);
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) { e.preventDefault(); send(); }
  });

  applyTemplate(state.template);
  show(location.hash.slice(1) || 'api');
  refresh();
}

boot();
