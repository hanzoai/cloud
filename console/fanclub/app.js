'use strict';

const $ = (s) => document.querySelector(s);
const STORAGE_TOKEN_KEY = 'fanclub_iam_access_token';
const STORAGE_PKCE_VERIFIER = 'fanclub_pkce_verifier';
const STORAGE_PKCE_STATE = 'fanclub_pkce_state';

const state = {
  config: null,
  me: null,
  token: sessionStorage.getItem(STORAGE_TOKEN_KEY) || '',
  view: 'overview',
  members: [],
  proposals: [],
  audit: [],
  filter: 'all',
  search: '',
  chat: [],
  mcpProbe: null,
};

const html = (text) =>
  String(text ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
const num = (n) => Number(n || 0).toLocaleString('en-US');
const money = (n) => `${num(n)} cr`;
const dt = (iso) =>
  iso ? new Date(iso).toLocaleString('en-US', { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' }) : '—';
const icons = { retention: '◎', return: '↻', growth: '↗' };
const navLabels = {
  overview: 'OVERVIEW',
  members: 'PEOPLE & RECORDS',
  journeys: 'JOURNEYS',
  approvals: 'APPROVALS',
  assistant: 'AI STUDIO',
  evaluation: 'MODEL LAB',
  stack: 'HANZO CLOUD STACK',
  audit: 'AUDIT TRAIL',
};

let toTimer;
function toast(message) {
  const el = $('#toast');
  el.textContent = message;
  el.classList.add('show');
  clearTimeout(toTimer);
  toTimer = setTimeout(() => el.classList.remove('show'), 3200);
}

function authHeaders(extra = {}) {
  const h = { ...extra };
  if (state.token) h.Authorization = `Bearer ${state.token}`;
  return h;
}

async function api(path, { method = 'GET', body } = {}) {
  const headers = authHeaders(body !== undefined ? { 'Content-Type': 'application/json' } : {});
  const res = await fetch(path, {
    method,
    credentials: 'same-origin',
    headers,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  let payload = null;
  try {
    payload = await res.json();
  } catch {
    payload = null;
  }
  if (!res.ok) {
    const msg =
      payload?.detail ||
      payload?.message ||
      payload?.error?.message ||
      (typeof payload?.error === 'string' ? payload.error : '') ||
      `HTTP ${res.status}`;
    const err = new Error(msg);
    err.status = res.status;
    throw err;
  }
  return payload;
}

async function mcpCall(toolName, args = {}) {
  const res = await fetch('/mcp', {
    method: 'POST',
    credentials: 'same-origin',
    headers: authHeaders({ 'Content-Type': 'application/json' }),
    body: JSON.stringify({
      jsonrpc: '2.0',
      id: Date.now(),
      method: 'tools/call',
      params: { name: toolName, arguments: args },
    }),
  });
  const frame = await res.json();
  if (frame.error) {
    throw new Error(`MCP ${frame.error.code}: ${frame.error.message}`);
  }
  return frame.result;
}

function b64url(bytes) {
  let s = '';
  for (const b of bytes) s += String.fromCharCode(b);
  return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/g, '');
}

async function startPKCELogin() {
  if (!state.config?.authorizationEndpoint) {
    toast('IAM authorization endpoint unavailable');
    return;
  }
  const rand = new Uint8Array(48);
  crypto.getRandomValues(rand);
  const verifier = b64url(rand);
  const stateBytes = new Uint8Array(16);
  crypto.getRandomValues(stateBytes);
  const oauthState = b64url(stateBytes);

  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(verifier));
  const challenge = b64url(new Uint8Array(digest));

  const redirectUri = `${location.origin}/fanclub/index.html`;
  sessionStorage.setItem(STORAGE_PKCE_VERIFIER, verifier);
  sessionStorage.setItem(STORAGE_PKCE_STATE, oauthState);

  const authUrl = new URL(state.config.authorizationEndpoint);
  authUrl.searchParams.set('response_type', 'code');
  authUrl.searchParams.set('client_id', state.config.clientId);
  authUrl.searchParams.set('redirect_uri', redirectUri);
  authUrl.searchParams.set('scope', 'openid profile email');
  authUrl.searchParams.set('code_challenge', challenge);
  authUrl.searchParams.set('code_challenge_method', 'S256');
  authUrl.searchParams.set('state', oauthState);
  location.assign(authUrl.toString());
}

async function handleOAuthCallback() {
  const params = new URLSearchParams(location.search);
  const code = params.get('code');
  const returnedState = params.get('state');
  if (!code) return;

  const expectedState = sessionStorage.getItem(STORAGE_PKCE_STATE);
  const verifier = sessionStorage.getItem(STORAGE_PKCE_VERIFIER);
  sessionStorage.removeItem(STORAGE_PKCE_STATE);
  sessionStorage.removeItem(STORAGE_PKCE_VERIFIER);

  history.replaceState({}, document.title, location.pathname);

  if (!expectedState || returnedState !== expectedState || !verifier) {
    toast('IAM PKCE state mismatch — please sign in again');
    return;
  }
  const redirectUri = `${location.origin}/fanclub/index.html`;
  const exchanged = await api('/v1/fanclub/oauth/exchange', {
    method: 'POST',
    body: { code, codeVerifier: verifier, redirectUri },
  });
  state.token = exchanged.accessToken;
  sessionStorage.setItem(STORAGE_TOKEN_KEY, state.token);
  toast(`Signed in as ${exchanged.identity.org}/${exchanged.identity.username || exchanged.identity.user}`);
}

function signOut() {
  state.token = '';
  state.me = null;
  state.chat = [];
  state.mcpProbe = null;
  sessionStorage.removeItem(STORAGE_TOKEN_KEY);
  updateIdentityChrome();
  renderAuthGate();
  toast('Signed out');
}

function updateIdentityChrome() {
  const issuer = state.config?.iamIssuer || 'Hanzo IAM';
  $('#iam-issuer-label').textContent = issuer.replace(/^https?:\/\//, '');
  if (state.me) {
    $('#org-name').textContent = state.me.org;
    const roleLabel = state.me.isSuperAdmin
      ? 'SuperAdmin · can approve'
      : state.me.isOrgAdmin
        ? 'Org Admin · can approve'
        : 'Org Member · read/plan only';
    $('#org-role').textContent = `${state.me.username || state.me.user} · ${roleLabel}`;
    $('#user-avatar').textContent = (state.me.username || state.me.user || 'OP').slice(0, 2).toUpperCase();
    $('#auth-actions').innerHTML = `<button type="button" class="btn btn-ghost" data-signout="1">Sign out</button>`;
  } else {
    $('#org-name').textContent = 'Sign in required';
    $('#org-role').textContent = 'No unauthenticated tenant access';
    $('#user-avatar').textContent = '--';
    $('#auth-actions').innerHTML = `<button type="button" class="btn btn-primary" data-pkce-login="1">Sign in with Hanzo IAM</button>`;
  }
}

function section(title, description = '', actions = '') {
  const eyebrow = state.me ? `IAM ORG · ${state.me.org}` : 'HANZO IAM AUTHENTICATION';
  return `<div class="section-header"><div><div class="eyebrow">${html(eyebrow)}</div><h1 class="section-title">${html(title)}</h1><p class="section-description">${html(description)}</p></div>${actions ? `<div class="section-cta">${actions}</div>` : ''}</div>`;
}

function badge(text) {
  const cls =
    text === 'High'
      ? 'pill-high'
      : text === 'Medium'
        ? 'pill-medium'
        : text === 'Low'
          ? 'pill-low'
          : text?.startsWith('Approved')
            ? 'pill-approved'
            : text === 'Pending'
              ? 'pill-pending'
              : 'pill-rejected';
  return `<span class="pill ${cls}">${html(text)}</span>`;
}

function metric(title, value, footer, icon = '◈', warning = false) {
  return `<div class="metric-card"><div class="metric-caption">${html(title)}<span class="metric-ikon">${icon}</span></div><div class="metric-value">${html(value)}</div><div class="metric-foot ${warning ? 'warning' : ''}">${footer}</div></div>`;
}

function topMembers(ms, limit = 5) {
  return `<div class="table-scroller"><table class="data-table"><thead><tr><th>Person</th><th>Tier</th><th>Risk</th><th>Next step</th></tr></thead><tbody>${ms
    .slice(0, limit)
    .map(
      (m) =>
        `<tr><td><div class="member-name">${html(m.name)}</div><div class="member-id">${html(m.member_id)}</div></td><td><span class="tag">${html(m.tier)}</span></td><td>${badge(m.score.priority)}</td><td class="muted">${html(m.score.nextBest)}</td></tr>`
    )
    .join('')}</tbody></table></div>`;
}

function renderAuthGate(errorMsg = '') {
  const cfg = state.config;
  $('#content').innerHTML = `
    <div class="auth-gate">
      ${section('Sign in with Hanzo IAM', 'Organization scope is derived strictly from a cryptographically verified Hanzo IAM JWT. There is no browser tenant switcher or local unauthenticated database.')}
      ${errorMsg ? `<div class="note warning"><strong>Authentication required:</strong> ${html(errorMsg)}</div>` : ''}
      <section class="panel">
        <div class="panel-head"><div><h2>OIDC PKCE sign-in</h2><p class="panel-sub">Issuer: ${html(cfg?.iamIssuer || 'Unconfigured')} · Client: ${html(cfg?.clientId || 'hanzo-fanclub')}</p></div><span class="tag">S256 PKCE</span></div>
        <p class="explain">Redirects to your configured Hanzo IAM issuer and exchanges the authorization code at <code>POST /v1/fanclub/oauth/exchange</code> with strict redirect URI verification.</p>
        <div style="margin-top:16px"><button type="button" class="btn btn-primary" data-pkce-login="1">Sign in with Hanzo IAM ↗</button></div>
      </section>
      <section class="panel" style="margin-top:16px">
        <div class="panel-head"><div><h2>Local CLI Bearer token</h2><p class="panel-sub">Paste a valid JWT from <code>hanzo auth token</code> for local verification</p></div><span class="tag">JWT VERIFIED</span></div>
        <form id="token-form" style="display:flex;gap:8px;flex-wrap:wrap;margin-top:8px">
          <label for="bearer-input" class="sr-only">Hanzo IAM JWT</label>
          <input id="bearer-input" class="token-input" type="password" placeholder="eyJhbGciOiJSUzI1NiIs…" autocomplete="off" required>
          <button type="submit" class="btn">Verify token</button>
        </form>
        <p class="explain" style="margin-top:10px">Cloud's SanitizeIdentity middleware verifies signature against <code>${html(cfg?.jwksUri || '/v1/iam/.well-known/jwks')}</code>, issuer, and expiry on every request.</p>
      </section>
      <div class="note" style="margin-top:16px"><strong>Storage &amp; inference posture:</strong> ${html(cfg?.disclosures?.syntheticData || '')} ${html(cfg?.disclosures?.approvals || '')}</div>
    </div>`;
}

function emptySeedPanel() {
  const profiles = state.config?.profiles || [];
  return `<div class="panel"><div class="empty-state"><div class="empty-icon">◫</div><h3>Your IAM org's Cloud Base collections are empty</h3><p>No synthetic data is pre-populated automatically. Opt in below to seed 90 reproducible fictional records into Cloud Base collection <code>fc_members</code> for organization <b>${html(state.me.org)}</b>.</p><div class="seed-grid">${profiles
    .map(
      (p) =>
        `<button type="button" class="btn btn-primary" data-seed="${html(p.id)}">Seed ${html(p.name)} (${html(p.industry)})</button>`
    )
    .join('')}</div><p class="explain" style="margin-top:16px">All seeded records are explicitly flagged <code>synthetic: true</code> and never represent real customers.</p></div></div>`;
}

async function renderOverview() {
  const [summary, people] = await Promise.all([api('/v1/fanclub/summary'), api('/v1/fanclub/members')]);
  const m = summary.metrics;
  state.members = people.members || [];
  $('#approval-count').textContent = String(m.pending);
  if (!state.members.length) {
    $('#content').innerHTML = `${section('Intelligence overview', 'Hosted natively in Hanzo Cloud over IAM, Cloud Base, Cloud Tasks, MCP, and Hanzo AI.')}${emptySeedPanel()}`;
    return;
  }
  const high = [...state.members].sort((a, b) => b.score.risk - a.score.risk);
  const chartMax = Math.max(1, ...m.weeklyActivity.map((x) => x.value));
  $('#content').innerHTML = `${section('Intelligence overview', 'Authenticated Cloud Base records scoped strictly to your validated Hanzo IAM organization.', `<button class="btn btn-primary" data-go="journeys">Explore journeys &nbsp; ↗</button>`)}
    <div class="hero"><div class="hero-copy"><div class="eyebrow">HANZO CLOUD · CANONICAL REFERENCE APP</div><h1>Own your audience.<br><em>Activate your intelligence.</em></h1><p>Identity from Hanzo IAM, durable records in Cloud Base, human-approved simulation tasks in Cloud Tasks, typed tools over MCP, and fail-closed Hanzo AI inference.</p></div><div class="hero-visual"><div class="hero-step"><b>◫</b>Base</div><span class="hero-connector"></span><div class="hero-step"><b>◈</b>Rules</div><span class="hero-connector"></span><div class="hero-step"><b>✓</b>Tasks</div><span class="hero-connector"></span><div class="hero-step"><b>✧</b>AI</div></div></div>
    <div class="grid-four">
      ${metric('People in Cloud Base', num(m.members), summary.syntheticDataPresent ? '<b>Synthetic fixtures</b> in fc_members' : '<b>fc_members</b> in Cloud Base', '♙')}
      ${metric('Renewal attention', num(m.atRisk), '<b>Rules baseline</b> · elevated risk', '↗', true)}
      ${metric('Value in risk cohort', money(m.valueAtRisk), '<b>Demo credits</b>, not booked revenue', '◈')}
      ${metric('Pending approvals', num(m.pending), '<b>Org-admin gated</b> · simulation only', '✓')}
    </div>
    <div class="two-column">
      <section class="panel"><div class="panel-head"><div><h2>Participation activity</h2><p class="panel-sub">Illustrative weekly counts · deterministic fixtures</p></div><span class="tag">SYNTHETIC</span></div><div class="chart">${m.weeklyActivity.map((x) => `<div class="chartbar"><i style="height:${Math.round((x.value / chartMax) * 100)}%"></i><span>${html(x.day)}</span></div>`).join('')}</div><div class="chart-caption">Illustrative chart only — never live commercial analytics or measured uplift.</div></section>
      <section class="panel"><div class="panel-head"><div><h2>Next best decisions</h2><p class="panel-sub">Deterministic rules baseline · never auto-sent</p></div><button class="link" data-go="approvals">Review actions ↗</button></div>${topMembers(high, 4)}<div style="margin-top:14px"><button class="btn btn-sm btn-ghost" data-go="members">Explore people →</button></div></section>
    </div>
    <div class="note" style="margin-top:18px"><strong>Disclosures:</strong> ${html(summary.disclosures.syntheticData)} ${html(summary.disclosures.rulesBaseline)}</div>`;
}

async function renderMembers() {
  const res = await api('/v1/fanclub/members');
  state.members = res.members || [];
  if (!state.members.length) {
    $('#content').innerHTML = `${section('People & records', 'Backed exclusively by Cloud Base collection fc_members.')}${emptySeedPanel()}`;
    return;
  }
  $('#content').innerHTML = `${section('People & records', 'Authoritative records stored in Cloud Base collection fc_members under your validated IAM org.', `<button class="btn btn-ghost" data-go="audit">View data audit →</button>`)}
    <div class="note"><strong>Cloud Base persistence:</strong> Every row below is a real JSON document in <code>/v1/base/collections/fc_members</code> scoped to org <b>${html(state.me.org)}</b>. Risk scores come from the transparent deterministic baseline (<code>${html(res.baseline)}</code>).</div>
    <div class="panel"><div class="filter-row"><input id="member-search" class="search-field" type="search" value="${html(state.search)}" placeholder="Search name, tier or segment" aria-label="Search members"><div class="toggle-group"><button data-filter="all" class="${state.filter === 'all' ? 'on' : ''}">All</button><button data-filter="high" class="${state.filter === 'high' ? 'on' : ''}">At risk</button><button data-filter="consented" class="${state.filter === 'consented' ? 'on' : ''}">Consented</button></div><span class="counts" id="member-count">${state.members.length} records</span></div><div class="table-scroller"><table class="data-table"><thead><tr><th>Person</th><th>Membership</th><th>Engagement</th><th>Renewal</th><th>Risk</th><th>Outreach</th></tr></thead><tbody id="member-body"></tbody></table></div></div>`;
  updateMemberRows();
}

function updateMemberRows() {
  const filtered = state.members.filter(
    (m) =>
      `${m.name} ${m.tier} ${m.segment} ${m.member_id}`.toLowerCase().includes(state.search.toLowerCase()) &&
      (state.filter === 'all' || (state.filter === 'high' && m.score.priority === 'High') || (state.filter === 'consented' && m.consent))
  );
  $('#member-count').textContent = `${filtered.length} / ${state.members.length} records`;
  $('#member-body').innerHTML =
    filtered
      .map(
        (m) =>
          `<tr><td><div class="member-name">${html(m.name)}</div><div class="member-id">${html(m.member_id)} · ${html(m.segment)}${m.synthetic ? ' · synthetic' : ''}</div></td><td>${html(m.tier)}</td><td><span>${html(m.engagement)} / 100</span><div class="statbar"><span style="width:${m.engagement}%"></span></div></td><td>${m.days_to_renewal} days</td><td>${badge(m.score.priority)} <span class="small">${m.score.risk}</span></td><td><span class="tag">${m.consent ? '✓ Allowed' : '× Not allowed'}</span></td></tr>`
      )
      .join('') || `<tr><td colspan="6" class="muted" style="padding:30px;text-align:center">No matching records</td></tr>`;
}

async function renderJourneys() {
  const summary = await api('/v1/fanclub/summary');
  const journeys = summary.journeys || [];
  $('#content').innerHTML = `${section('Decision journeys', 'Reusable workflows that write pending human-review proposals into Cloud Base fc_proposals.', `<button class="btn btn-ghost" data-go="approvals">Approval queue →</button>`)}
    <div class="note"><strong>Simulation only.</strong> Generating a journey writes pending records to Cloud Base. Approving requires an IAM org-admin role and enqueues a simulation-only Cloud Task with <code>external_side_effects: false</code>.</div>
    <div class="journey-grid">${journeys
      .map(
        (j, i) =>
          `<div class="journey-card"><div class="journey-symbol">${icons[j.id] || '◈'}</div><div class="eyebrow">JOURNEY 0${i + 1}</div><h3>${html(j.title)}</h3><p>${html(j.subtitle)}</p><div class="journey-steps"><span>CLOUD BASE</span><span class="step-arrow">→</span><span>RULES</span><span class="step-arrow">→</span><span>ADMIN APPROVAL</span></div><button class="btn btn-primary" data-plan="${html(j.id)}">Generate review actions ↗</button></div>`
      )
      .join('')}</div>`;
}

async function renderApprovals() {
  const res = await api('/v1/fanclub/proposals');
  state.proposals = res.proposals || [];
  const pendingCount = state.proposals.filter((x) => x.status === 'Pending').length;
  $('#approval-count').textContent = String(pendingCount);
  const canApprove = Boolean(state.me?.canApprove);
  $('#content').innerHTML = `${section('Human approvals', 'Role-authorized review queue stored in Cloud Base fc_proposals and dispatched to Cloud Tasks.', `<button class="btn btn-primary" data-go="journeys">Generate proposals +</button>`)}
    <div class="note ${canApprove ? '' : 'warning'}"><strong>${canApprove ? 'Simulation boundary:' : 'Read-only role:'}</strong> ${
      canApprove
        ? 'Approving atomically updates Cloud Base, enqueues a durable Cloud Task (<code>fanclub_action_simulation</code>, <code>external_side_effects=false</code>), and logs an audit receipt.'
        : 'Your IAM principal is not an org admin. Approval and rejection requests are enforced server-side and return 403 Forbidden.'
    }</div>
    <div class="panel">${
      !state.proposals.length
        ? `<div class="empty-state"><div class="empty-icon">✓</div><h3>No decisions waiting</h3><p>Generate a journey to create human-review proposals in Cloud Base.</p><button class="btn btn-primary" data-go="journeys">Explore journeys</button></div>`
        : `<div class="table-scroller"><table class="data-table"><thead><tr><th>Person &amp; action</th><th>Why</th><th>Journey</th><th>Status</th><th>Task / Receipt</th><th>Review</th></tr></thead><tbody>${state.proposals
            .map(
              (p) =>
                `<tr><td><div class="member-name">${html(p.member_name || p.member_id)}</div><div class="member-id">${html(p.action)}</div></td><td class="muted">${html(p.reason)}</td><td>${html(p.journey)}</td><td>${badge(p.status)}</td><td class="small">${p.task_id ? html(p.task_id.slice(0, 12)) : '—'}</td><td>${
                  p.status === 'Pending'
                    ? `<div style="display:flex;gap:7px"><button class="btn btn-sm btn-primary" data-decision="${html(p.id)}" data-action="approve" ${canApprove ? '' : 'disabled'}>Approve</button><button class="btn btn-sm btn-danger" data-decision="${html(p.id)}" data-action="reject" ${canApprove ? '' : 'disabled'}>Reject</button></div>`
                    : `<span class="small">${html(p.decided_by || 'Reviewed')}</span>`
                }</td></tr>`
            )
            .join('')}</tbody></table></div>`
    }</div>`;
}

async function renderAssistant() {
  const [inf, summary] = await Promise.all([api('/v1/fanclub/inference/status'), api('/v1/fanclub/summary')]);
  if (!state.chat.length) {
    state.chat = [
      {
        role: 'assistant',
        text: 'Ask about aggregate workspace metrics (risk counts, consent counts, pending approvals). Only aggregate counts from Cloud Base are sent to Hanzo AI — never individual member records. When inference is unconfigured or unavailable, this studio fails closed with 503 rather than faking AI.',
        provider: inf.configured ? `Configured (${inf.transport} · ${inf.model})` : 'Unconfigured · fail-closed 503',
      },
    ];
  }
  $('#content').innerHTML = `${section('AI Studio', 'Authenticated Hanzo Cloud AIClient inference over aggregate org metrics only.', `<button class="btn btn-ghost" data-go="stack">Inspect Cloud Stack ↗</button>`)}
    <div class="note ${inf.configured ? '' : 'warning'}"><strong>Inference configuration (${inf.configured ? 'configured — not a readiness claim' : 'unconfigured'}):</strong> ${html(inf.note)}</div>
    <div class="chat-shell">
      <section class="panel conversation">
        <div class="panel-head"><div><h2>Ask Hanzo Cloud AI</h2><p class="panel-sub">POST /v1/fanclub/chat · aggregates only · zero external fallback</p></div><span class="tag">${html(inf.configured ? inf.model : 'FAIL-CLOSED')}</span></div>
        <div class="message-stream" id="chat-messages"></div>
        <form class="chat-input" id="chat-form"><label for="chat-question" class="sr-only">Your question</label><input id="chat-question" autocomplete="off" maxlength="400" placeholder="Ask about retention risk, consent, or pending approvals…" required><button class="btn btn-primary" type="submit">Ask ↗</button></form>
      </section>
      <aside class="panel">
        <div class="panel-head"><h2>Try a question</h2></div>
        <button class="suggestion" data-question="How many members are at risk of not renewing?">How many members are at risk of not renewing?</button>
        <button class="suggestion" data-question="What needs human approval right now?">What needs human approval right now?</button>
        <button class="suggestion" data-question="Summarize outreach consent coverage.">Summarize outreach consent coverage.</button>
        <div class="note" style="margin-top:20px"><strong>${summary.metrics.members} records in Cloud Base.</strong> Individual names and IDs are never sent to the model.</div>
      </aside>
    </div>`;
  const list = $('#chat-messages');
  list.innerHTML = state.chat
    .map(
      (m) =>
        `<div class="message ${html(m.role)}">${html(m.text)}${m.provider && m.role !== 'user' ? `<div class="small" style="margin-top:8px;color:#9fbc9a">${html(m.provider)}</div>` : ''}</div>`
    )
    .join('');
  list.scrollTop = list.scrollHeight;
}

async function renderEvaluation() {
  const { evaluation: e } = await api('/v1/fanclub/evaluation');
  $('#content').innerHTML = `${section('Model evaluation', 'Transparent deterministic rules baseline metrics computed live over Cloud Base fc_members.', `<button class="btn btn-ghost" data-go="stack">See qualification gates →</button>`)}
    <div class="note warning"><strong>Evidence disclosure:</strong> ${html(e.caution)}</div>
    <section class="panel"><div class="panel-head"><div><h2>${html(e.label)}</h2><p class="panel-sub">${html(e.data)} · n = ${e.size}</p></div><span class="tag">RULES BASELINE</span></div>
      <div class="model-stats">
        <div class="model-stat"><span>Classification accuracy</span><b>${e.accuracy}%</b><span>Held-out rows (index % 3)</span></div>
        <div class="model-stat"><span>Positive precision</span><b>${e.precision}%</b><span>Elevated-risk flags matching holdout</span></div>
        <div class="model-stat"><span>Positive recall</span><b>${e.recall}%</b><span>Holdout lapses captured</span></div>
      </div>
      <p class="explain" style="margin-top:18px">Confusion matrix: ${e.confusion.tp} TP · ${e.confusion.fp} FP · ${e.confusion.fn} FN · ${e.confusion.tn} TN.</p>
    </section>
    <div class="evaluation-box">
      <section class="panel"><div class="panel-head"><h2>Model selection ladder</h2></div><div class="explain" style="font-size:12px;line-height:2.15"><b style="color:#d9f4d2">01 &nbsp; Policy rules</b> — active deterministic baseline<br><b style="color:#d9f4d2">02 &nbsp; Statistical baseline</b> — regression / gradient boosting<br><b style="color:#d9f4d2">03 &nbsp; Licensed private PEFT/LoRA</b> — separate qualification gate<br><b style="color:#d9f4d2">04 &nbsp; Hanzo AI / Engine</b> — active aggregate Q&amp;A via Cloud AIClient</div></section>
      <section class="panel"><div class="panel-head"><h2>Train-to-serve qualification gate</h2></div><div class="explain" style="font-size:12px;line-height:2.15">Data authority → training job → signed checkpoint<br>Offline holdout eval → model custody &amp; license audit<br>Independent Engine serving → drift &amp; rollback tests</div><div class="note warning" style="margin-top:16px"><strong>Unqualified gate:</strong> No custom weights are trained or substituted here.</div></section>
    </div>`;
}

async function renderStack() {
  const [st, baseCols] = await Promise.all([api('/v1/fanclub/stack'), api('/v1/base/collections')]);
  $('#content').innerHTML = `${section('Hanzo Cloud architecture', 'Every primitive below runs natively inside the unified hanzoai/cloud binary — zero duplicate Node/SQLite servers.', `<button class="btn btn-primary" data-mcp-probe="1">Verify /mcp read-only tool ↗</button>`)}
    <div class="tech-map">${st.components
      .map(
        (x) =>
          `<div class="tech-card"><h3>${html(x.name)}</h3><span class="tech-state ${x.status === 'Active' || x.status === 'Configured' ? '' : 'dim'}">${html(x.status)}</span><p>${html(x.detail)}</p></div>`
      )
      .join('')}</div>
    <div class="two-column">
      <section class="panel"><div class="panel-head"><div><h2>Live Cloud Base collections</h2><p class="panel-sub">GET /v1/base/collections (scoped to org ${html(state.me.org)})</p></div><span class="tag">LIVE BASE</span></div>
        <div class="code-box">${html(JSON.stringify(baseCols.collections || [], null, 2))}</div>
      </section>
      <section class="panel"><div class="panel-head"><div><h2>Live MCP read-only check</h2><p class="panel-sub">POST /mcp → tools/call fanclub_summary</p></div><span class="tag">JSON-RPC 2.0</span></div>
        <div class="code-box" id="mcp-output">${html(state.mcpProbe ? JSON.stringify(state.mcpProbe, null, 2) : 'Click "Verify /mcp read-only tool" above to call fanclub_summary over /mcp with your IAM bearer.')}</div>
      </section>
    </div>`;
}

async function renderAudit() {
  const res = await api('/v1/fanclub/audit');
  state.audit = res.events || [];
  $('#content').innerHTML = `${section('Audit & provenance', 'Workspace provenance receipts stored in Cloud Base fc_audit alongside Cloud tamper-evident audit logging.', `<button class="btn btn-ghost" data-go="approvals">Approval queue →</button>`)}
    <div class="note"><strong>Dual audit layer:</strong> Every mutating REST and MCP request is recorded by Cloud's tamper-evident <code>AuditTrail</code> middleware, and every workspace decision writes a tenant-scoped receipt into Cloud Base <code>fc_audit</code>.</div>
    <section class="panel"><div class="table-scroller"><table class="data-table"><thead><tr><th>Recorded</th><th>Event</th><th>Actor</th><th>Subject</th><th>Details</th></tr></thead><tbody>${
      state.audit
        .map(
          (e) =>
            `<tr><td>${dt(e.created_at)}</td><td><b>${html(e.event)}</b></td><td>${html(e.actor)}</td><td class="muted">${html(e.subject || '—')}</td><td class="muted">${html(JSON.stringify(e.details || {}))}</td></tr>`
        )
        .join('') || `<tr><td colspan="5" class="muted" style="padding:26px;text-align:center">No audit events recorded yet</td></tr>`
    }</tbody></table></div></section>`;
}

const views = {
  overview: renderOverview,
  members: renderMembers,
  journeys: renderJourneys,
  approvals: renderApprovals,
  assistant: renderAssistant,
  evaluation: renderEvaluation,
  stack: renderStack,
  audit: renderAudit,
};

async function navigate(view) {
  if (!views[view]) return;
  state.view = view;
  state.search = '';
  state.filter = 'all';
  for (const b of document.querySelectorAll('[data-view]')) b.classList.toggle('selected', b.dataset.view === view);
  $('#top-current').textContent = navLabels[view];
  if (!state.me) {
    renderAuthGate();
    return;
  }
  $('#content').innerHTML = '<div class="loading-state">Loading Hanzo Cloud workspace…</div>';
  try {
    await views[view]();
  } catch (e) {
    if (e.status === 401 || e.status === 403) {
      state.me = null;
      updateIdentityChrome();
      renderAuthGate(e.message);
      return;
    }
    $('#content').innerHTML = `<div class="note warning"><strong>Unable to load workspace.</strong> ${html(e.message)}</div>`;
  }
  window.scrollTo?.(0, 0);
}

async function ask(question) {
  if (!question?.trim()) return;
  state.chat.push({ role: 'user', text: question });
  await renderAssistant();
  try {
    const answer = await api('/v1/fanclub/chat', { method: 'POST', body: { question } });
    state.chat.push({
      role: 'assistant',
      text: answer.answer,
      provider: `${answer.provider} · ${answer.model} (${answer.transport}) · ${answer.groundedOn}`,
    });
  } catch (e) {
    state.chat.push({
      role: 'error',
      text: `Inference failed closed: ${e.message}`,
      provider: `HTTP ${e.status || 503} · fail-closed (no synthetic fallback)`,
    });
  }
  await renderAssistant();
}

document.addEventListener('click', async (e) => {
  const login = e.target.closest('[data-pkce-login]');
  if (login) {
    await startPKCELogin();
    return;
  }
  const logout = e.target.closest('[data-signout]');
  if (logout) {
    signOut();
    return;
  }
  const nav = e.target.closest('[data-view],[data-go]');
  if (nav) {
    await navigate(nav.dataset.view || nav.dataset.go);
    return;
  }
  const filter = e.target.closest('[data-filter]');
  if (filter) {
    state.filter = filter.dataset.filter;
    document.querySelectorAll('[data-filter]').forEach((x) => x.classList.toggle('on', x === filter));
    updateMemberRows();
    return;
  }
  const seedBtn = e.target.closest('[data-seed]');
  if (seedBtn) {
    seedBtn.disabled = true;
    try {
      const out = await api('/v1/fanclub/seed', { method: 'POST', body: { profile: seedBtn.dataset.seed } });
      toast(out.seeded > 0 ? `Seeded ${out.seeded} synthetic fixtures in Cloud Base` : `Already seeded (${out.existing} records)`);
      await navigate(state.view);
    } catch (er) {
      toast(er.message);
      seedBtn.disabled = false;
    }
    return;
  }
  const plan = e.target.closest('[data-plan]');
  if (plan) {
    plan.disabled = true;
    try {
      const out = await api('/v1/fanclub/proposals/plan', { method: 'POST', body: { journey: plan.dataset.plan, limit: 6 } });
      toast(`${out.created} proposals written to Cloud Base`);
      await navigate('approvals');
    } catch (er) {
      toast(er.message);
    } finally {
      plan.disabled = false;
    }
    return;
  }
  const decision = e.target.closest('[data-decision]');
  if (decision) {
    decision.disabled = true;
    try {
      const out = await api(`/v1/fanclub/proposals/${encodeURIComponent(decision.dataset.decision)}/decision`, {
        method: 'POST',
        body: { action: decision.dataset.action },
      });
      toast(`Recorded: ${out.status}${out.taskId ? ` (task ${out.taskId.slice(0, 8)})` : ''}`);
      await navigate('approvals');
    } catch (er) {
      toast(er.message);
      decision.disabled = false;
    }
    return;
  }
  const question = e.target.closest('[data-question]');
  if (question) {
    await ask(question.dataset.question);
    return;
  }
  const mcpBtn = e.target.closest('[data-mcp-probe]');
  if (mcpBtn) {
    mcpBtn.disabled = true;
    try {
      state.mcpProbe = await mcpCall('fanclub_summary', {});
      toast('MCP fanclub_summary verified');
      await renderStack();
    } catch (er) {
      state.mcpProbe = { error: er.message };
      toast(er.message);
      await renderStack();
    } finally {
      mcpBtn.disabled = false;
    }
  }
});

document.addEventListener('input', (e) => {
  if (e.target.id === 'member-search') {
    state.search = e.target.value;
    updateMemberRows();
  }
});

document.addEventListener('submit', async (e) => {
  if (e.target.id === 'chat-form') {
    e.preventDefault();
    const input = $('#chat-question');
    const q = input.value;
    input.value = '';
    await ask(q);
    return;
  }
  if (e.target.id === 'token-form') {
    e.preventDefault();
    const tok = $('#bearer-input').value.trim();
    if (!tok) return;
    state.token = tok;
    try {
      state.me = await api('/v1/fanclub/me');
      sessionStorage.setItem(STORAGE_TOKEN_KEY, tok);
      updateIdentityChrome();
      toast(`Authenticated as ${state.me.org}/${state.me.username || state.me.user}`);
      await navigate('overview');
    } catch (er) {
      state.token = '';
      state.me = null;
      sessionStorage.removeItem(STORAGE_TOKEN_KEY);
      updateIdentityChrome();
      renderAuthGate(er.message);
    }
  }
});

async function init() {
  try {
    state.config = await api('/v1/fanclub/config');
  } catch (e) {
    $('#content').innerHTML = `<div class="note warning"><strong>FanClub subsystem not enabled.</strong> ${html(e.message)}. Start Hanzo Cloud with <code>CLOUD_ENABLE_STAGED=fanclub</code>.</div>`;
    return;
  }
  try {
    await handleOAuthCallback();
  } catch (e) {
    toast(`OAuth exchange failed: ${e.message}`);
  }
  try {
    state.me = await api('/v1/fanclub/me');
    updateIdentityChrome();
    await navigate('overview');
  } catch (e) {
    state.me = null;
    updateIdentityChrome();
    renderAuthGate();
  }
}

init();
