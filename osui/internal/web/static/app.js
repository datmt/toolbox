'use strict';
// All data shown here can be attacker-influenced (process paths, hostnames,
// args), so the DOM is built with textContent only. Never use innerHTML.

const $ = (s, r = document) => r.querySelector(s);
function h(tag, attrs, ...kids) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (k === 'class') e.className = v;
    else if (k.startsWith('on')) e.addEventListener(k.slice(2), v);
    else if (v === true) e.setAttribute(k, '');
    else if (v !== false && v != null) e.setAttribute(k, v);
  }
  for (const c of kids.flat()) if (c != null && c !== false) e.append(c.nodeType ? c : document.createTextNode(String(c)));
  return e;
}

const S = { status: {}, rules: [], prompts: [], log: [], tab: 'prompts',
            sel: new Set(), q: '', fAction: '', fRisk: '', logQ: '', choice: {} };

// ---------- api ----------
async function api(method, path, body) {
  const r = await fetch(path, { method, headers: { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body) });
  const j = await r.json().catch(() => ({}));
  if (!r.ok) throw new Error(j.error || r.statusText);
  return j;
}
let toastT;
function toast(msg) {
  const t = $('#toast'); t.textContent = msg; t.classList.remove('hidden');
  clearTimeout(toastT); toastT = setTimeout(() => t.classList.add('hidden'), 3500);
}
const guard = fn => async (...a) => { try { await fn(...a); } catch (e) { toast(e.message); } };

// ---------- scope builder ----------
const CCSLD = new Set(['co', 'com', 'org', 'net', 'gov', 'ac', 'edu']);
function baseDomain(host) {
  const p = host.split('.');
  if (p.length <= 2) return host;
  const n = (p[p.length - 1].length === 2 && CCSLD.has(p[p.length - 2])) ? 3 : 2;
  return p.slice(-n).join('.');
}
const esc = s => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
const simple = (operand, data) => ({ type: 'simple', operand, data, sensitive: false });
const all = ops => ops.length === 1 ? ops[0] : { type: 'list', operand: 'list', data: '', list: ops };
const base = p => (p || 'unknown').split('/').pop() || 'unknown';

function scopes(c) {
  const proc = c.path ? simple('process.path', c.path) : null;
  const out = [];
  const add = (id, label, hint, ops, nameTail, broad) => {
    ops = ops.filter(Boolean);
    if (!ops.length) return;
    out.push({ id, label, hint, broad: !!broad, operator: all(ops), nameTail });
  };
  const pn = base(c.path);
  if (c.dstHost) {
    const dom = baseDomain(c.dstHost);
    add('host-port', `${pn} → ${c.dstHost}:${c.dstPort}`, 'recommended: exact host and port', [proc, simple('dest.host', c.dstHost), simple('dest.port', String(c.dstPort))], `${c.dstHost}-${c.dstPort}`);
    add('host', `${pn} → ${c.dstHost} (any port)`, '', [proc, simple('dest.host', c.dstHost)], c.dstHost);
    add('domain', `${pn} → *.${dom}`, 'domain and all its subdomains', [proc, { type: 'regexp', operand: 'dest.host', data: '^(.*\\.)?' + esc(dom) + '$', sensitive: false }], dom);
  }
  add('ip-port', `${pn} → ${c.dstIp}:${c.dstPort}`, c.dstHost ? 'by IP (may change)' : 'recommended: no hostname known, use IP and port', [proc, simple('dest.ip', c.dstIp), simple('dest.port', String(c.dstPort))], `${c.dstIp}-${c.dstPort}`);
  add('port', `${pn} → any host on port ${c.dstPort}`, 'looser', [proc, simple('dest.port', String(c.dstPort))], `port-${c.dstPort}`);
  if (c.dstHost) add('any-prog-host', `any program → ${c.dstHost}`, 'every process may reach this host', [simple('dest.host', c.dstHost)], c.dstHost);
  add('proc', `${pn} → anything`, 'BROAD: any destination, any port', [proc], 'any', true);
  return out;
}
const ruleName = (action, dur, c, sc) => `${action}-${dur.replace(/\s+/g, '-')}-${base(c.path)}-${sc.nameTail}`;

// ---------- summaries ----------
function leaves(o) { return o.type === 'list' ? o.list.flatMap(leaves) : [o]; }
function summarize(o) {
  return leaves(o).map(l => `${l.operand} ${l.type === 'regexp' ? '~' : l.type === 'network' ? '∈' : '='} ${l.data}`).join('  AND  ');
}

// ---------- rendering ----------
function render() {
  renderHeader();
  if (S.tab === 'prompts') renderPrompts();
  if (S.tab === 'rules') renderRules();
  if (S.tab === 'log') renderLog();
}

function renderHeader() {
  const st = S.status;
  $('#conn').className = 'dot ' + (st.connected ? 'on' : 'off');
  $('#connText').textContent = st.connected ? `${st.clientName || 'daemon'} ${st.version || ''}` : 'daemon not connected';
  const b = $('#promptBadge'); b.textContent = S.prompts.length; b.classList.toggle('hidden', !S.prompts.length);
  $('#ruleCount').textContent = S.rules.length ? `(${S.rules.length})` : '';
  const s = st.stats;
  $('#stats').textContent = s ? `conns ${s.connections} · accepted ${s.accepted} · dropped ${s.dropped} · rule hits ${s.ruleHits}` : '';
  const broad = S.rules.filter(r => r.risk === 'broad').length;
  document.title = (S.prompts.length ? `(${S.prompts.length}) ` : '') + 'osui' + (broad ? '' : '');
}

function renderPrompts() {
  const root = $('#tab-prompts');
  const keep = root.querySelectorAll('.card').length === S.prompts.length &&
    [...root.querySelectorAll('.card')].every((c, i) => c.dataset.id === S.prompts[i].id);
  if (keep) return; // don't wipe a form the user is editing
  root.replaceChildren();
  if (!S.prompts.length) {
    root.append(h('div', { class: 'empty' }, S.status.connected ? 'No connections waiting for a decision.' : 'Waiting for the OpenSnitch daemon to connect…'));
    return;
  }
  S.prompts.forEach(p => root.append(promptCard(p)));
}

function promptCard(p) {
  const c = p.conn;
  const sc = scopes(c);
  const st = S.choice[p.id] ||= { scope: sc[0].id, dur: 'always' };
  const host = c.dstHost || c.dstIp;
  const timer = h('div', {}), bar = h('div', { class: 'timer' }, timer);
  if (!p.deadline) bar.classList.add('hidden');
  const durSel = h('select', { onchange: e => st.dur = e.target.value },
    ['once', '30s', '5m', '1h', '12h', '1d', 'until restart', 'always'].map(d => h('option', { value: d, selected: d === st.dur }, d)));

  const send = guard(async action => {
    const chosen = sc.find(s => s.id === st.scope);
    await api('POST', `/api/prompts/${p.id}/answer`, {
      name: ruleName(action, st.dur, c, chosen), action, duration: st.dur, operator: chosen.operator });
  });
  const card = h('div', { class: 'card', 'data-id': p.id },
    h('h2', {}, base(c.path), ' → ', host, ':', c.dstPort),
    h('div', { class: 'muted mono' }, c.path),
    bar,
    h('dl', { class: 'kv' },
      h('dt', {}, 'Destination'), h('dd', { class: 'mono' }, `${c.dstHost ? c.dstHost + ' ' : ''}(${c.dstIp}) ${c.protocol} port ${c.dstPort}`),
      h('dt', {}, 'Process'), h('dd', { class: 'mono' }, `pid ${c.pid}, uid ${c.uid}`),
      c.args && c.args.length ? [h('dt', {}, 'Arguments'), h('dd', { class: 'mono' }, c.args.join(' '))] : null,
      c.cwd ? [h('dt', {}, 'Working dir'), h('dd', { class: 'mono' }, c.cwd)] : null),
    h('div', { class: 'scopes' }, sc.map(s => h('label', { class: 'scope' + (s.broad ? ' broad' : '') },
      h('input', { type: 'radio', name: 'sc-' + p.id, checked: s.id === st.scope, onchange: () => st.scope = s.id }),
      h('span', {}, s.label, s.hint ? h('small', {}, s.hint) : null)))),
    h('div', { class: 'row' },
      h('button', { class: 'allow', onclick: () => send('allow') }, 'Allow'),
      h('button', { class: 'deny', onclick: () => send('deny') }, 'Deny'),
      h('span', { class: 'muted' }, 'for'), durSel));
  if (p.deadline) tickTimer(p, timer);
  return card;
}

function tickTimer(p, el) {
  const total = Math.max(1, p.deadline - p.created);
  const step = () => {
    if (!el.isConnected) return;
    const left = p.deadline - Date.now();
    el.style.width = Math.max(0, 100 * left / total) + '%';
    if (left > 0) requestAnimationFrame(step);
  };
  step();
}

function matchRule(r) {
  const q = S.q.toLowerCase();
  if (S.fAction && r.action !== S.fAction) return false;
  if (S.fRisk && r.risk !== S.fRisk) return false;
  if (!q) return true;
  return (r.name + ' ' + r.description + ' ' + summarize(r.operator)).toLowerCase().includes(q);
}

function renderRules() {
  const root = $('#tab-rules');
  const active = document.activeElement;
  const hadFocus = active && active.id === 'ruleSearch';
  const rows = S.rules.filter(matchRule).sort((a, b) => a.name.localeCompare(b.name));
  const broad = S.rules.filter(r => r.risk === 'broad').length;
  const bulk = guard(async op => {
    const names = [...S.sel].filter(n => S.rules.some(r => r.name === n));
    if (op === 'delete' && !confirm(`Delete ${names.length} rule(s)?`)) return;
    for (const n of names) await api(op === 'delete' ? 'DELETE' : 'POST', `/api/rules/${encodeURIComponent(n)}` + (op === 'delete' ? '' : '/' + op));
    S.sel.clear(); toast(`${op}: ${names.length} rule(s)`);
  });

  root.replaceChildren(
    broad ? h('div', { class: 'card', style: 'border-color:var(--deny)' },
      h('b', {}, `${broad} allow rule(s) permit any destination. `),
      h('a', { href: '#', onclick: e => { e.preventDefault(); S.fRisk = 'broad'; S.fAction = 'allow'; render(); } }, 'Show them')) : null,
    h('div', { class: 'toolbar' },
      h('input', { id: 'ruleSearch', type: 'search', placeholder: 'Search name, process, host…', value: S.q, oninput: e => { S.q = e.target.value; renderRules(); } }),
      h('select', { onchange: e => { S.fAction = e.target.value; renderRules(); } },
        [['', 'all actions'], ['allow', 'allow'], ['deny', 'deny']].map(([v, t]) => h('option', { value: v, selected: v === S.fAction }, t))),
      h('select', { onchange: e => { S.fRisk = e.target.value; renderRules(); } },
        [['', 'any risk'], ['broad', 'broad allow'], ['medium', 'medium'], ['narrow', 'narrow']].map(([v, t]) => h('option', { value: v, selected: v === S.fRisk }, t))),
      h('button', { class: 'primary', onclick: () => openEditor(newRule()) }, 'New rule'),
      S.sel.size ? [h('span', { class: 'muted' }, `${S.sel.size} selected`),
        h('button', { class: 'small', onclick: () => bulk('disable') }, 'Disable'),
        h('button', { class: 'small', onclick: () => bulk('enable') }, 'Enable'),
        h('button', { class: 'small danger', onclick: () => bulk('delete') }, 'Delete')] : null),
    h('table', {},
      h('thead', {}, h('tr', {},
        h('th', {}, h('input', { type: 'checkbox', checked: rows.length && rows.every(r => S.sel.has(r.name)),
          onchange: e => { rows.forEach(r => e.target.checked ? S.sel.add(r.name) : S.sel.delete(r.name)); renderRules(); } })),
        ['On', 'Name', 'Action', 'Duration', 'Match', 'Risk', ''].map(t => h('th', {}, t)))),
      h('tbody', {}, rows.map(ruleRow))),
    rows.length ? null : h('div', { class: 'empty' }, 'No rules match.'));
  if (hadFocus) { const i = $('#ruleSearch'); i.focus(); i.setSelectionRange(i.value.length, i.value.length); }
}

function ruleRow(r) {
  return h('tr', { class: r.enabled ? '' : 'off' },
    h('td', {}, h('input', { type: 'checkbox', checked: S.sel.has(r.name), onchange: e => { e.target.checked ? S.sel.add(r.name) : S.sel.delete(r.name); renderRules(); } })),
    h('td', {}, h('input', { type: 'checkbox', class: 'sw', checked: r.enabled,
      onchange: guard(async e => { await api('POST', `/api/rules/${encodeURIComponent(r.name)}/${e.target.checked ? 'enable' : 'disable'}`); }) })),
    h('td', { class: 'mono' }, r.name, r.precedence ? h('span', { class: 'muted' }, ' ★') : null),
    h('td', {}, h('span', { class: 'tag ' + r.action }, r.action)),
    h('td', {}, r.duration),
    h('td', { class: 'mono' }, summarize(r.operator)),
    h('td', {}, r.risk ? h('span', { class: 'tag ' + r.risk, title: r.riskWhy }, r.risk) : ''),
    h('td', { class: 'actions' },
      h('button', { class: 'small', onclick: () => openEditor(r) }, 'Edit'), ' ',
      h('button', { class: 'small danger', onclick: guard(async () => { if (confirm(`Delete ${r.name}?`)) await api('DELETE', `/api/rules/${encodeURIComponent(r.name)}`); }) }, 'Delete')));
}

function renderLog() {
  const root = $('#tab-log');
  const hadFocus = document.activeElement && document.activeElement.id === 'logSearch';
  const q = S.logQ.toLowerCase();
  const rows = S.log.slice().reverse().filter(e => !q || `${e.process} ${e.dstHost} ${e.dstIp} ${e.dstPort} ${e.rule} ${e.action}`.toLowerCase().includes(q)).slice(0, 300);
  root.replaceChildren(
    h('div', { class: 'toolbar' }, h('input', { id: 'logSearch', type: 'search', placeholder: 'Filter…', value: S.logQ, oninput: e => { S.logQ = e.target.value; renderLog(); } }),
      h('span', { class: 'muted' }, 'Recent connections reported by the daemon. "Rule from this" opens a prefilled narrow rule.')),
    h('table', {},
      h('thead', {}, h('tr', {}, ['Time', 'Process', 'Destination', 'Action', 'Rule', ''].map(t => h('th', {}, t)))),
      h('tbody', {}, rows.map(e => h('tr', {},
        h('td', { class: 'mono' }, (e.time || '').split(' ').pop()),
        h('td', { class: 'mono' }, e.process || '?'),
        h('td', { class: 'mono' }, `${e.dstHost || e.dstIp}:${e.dstPort} ${e.proto}`),
        h('td', {}, e.action ? h('span', { class: 'tag ' + e.action }, e.action) : ''),
        h('td', { class: 'mono' }, e.rule),
        h('td', { class: 'actions' }, e.process ? h('button', { class: 'small', onclick: () => ruleFromLog(e) }, 'Rule from this') : ''))))));
  if (hadFocus) { const i = $('#logSearch'); i.focus(); i.setSelectionRange(i.value.length, i.value.length); }
}

// ---------- rule editor ----------
let editing = null; // original rule being edited (null = new)
function newRule() {
  return { name: '', description: '', enabled: true, precedence: false, nolog: false, action: 'allow', duration: 'always',
    operator: simple('process.path', '/usr/bin/example') };
}
function ruleFromLog(e) {
  const c = { path: e.process, dstHost: e.dstHost, dstIp: e.dstIp, dstPort: e.dstPort };
  const s = scopes(c)[0];
  openEditor({ name: ruleName('allow', 'always', c, s), description: 'created from connection log', enabled: true, precedence: false,
    nolog: false, action: 'allow', duration: 'always', operator: s.operator }, true);
}
function openEditor(r, isNew) {
  editing = (r.name && !isNew) ? r : null;
  const { risk, riskWhy, created, ...clean } = r;
  $('#editorTitle').textContent = editing ? `Edit ${r.name}` : 'New rule';
  $('#editorText').value = JSON.stringify(clean, null, 2);
  $('#editorErr').textContent = '';
  $('#editor').showModal();
}
$('#editorSave').addEventListener('click', async ev => {
  ev.preventDefault();
  let r;
  try { r = JSON.parse($('#editorText').value); } catch (e) { $('#editorErr').textContent = 'Invalid JSON: ' + e.message; return; }
  try {
    await api('POST', '/api/rules', r);
    if (editing && editing.name !== r.name) await api('DELETE', `/api/rules/${encodeURIComponent(editing.name)}`);
    $('#editor').close(); toast('Saved ' + r.name);
  } catch (e) { $('#editorErr').textContent = e.message; }
});

// ---------- tabs / boot ----------
$('#tabs').addEventListener('click', e => {
  const b = e.target.closest('button'); if (!b) return;
  S.tab = b.dataset.tab;
  document.querySelectorAll('#tabs button').forEach(x => x.classList.toggle('active', x === b));
  for (const t of ['prompts', 'rules', 'log']) $('#tab-' + t).classList.toggle('hidden', t !== S.tab);
  render();
});

function onPromptNew(p) {
  if (!S.prompts.some(x => x.id === p.id)) S.prompts.push(p);
  if (S.tab !== 'prompts') $('#tabs button[data-tab=prompts]').click();
  else render();
  if (window.Notification && Notification.permission === 'granted') new Notification('OpenSnitch', { body: `${base(p.conn.path)} → ${p.conn.dstHost || p.conn.dstIp}:${p.conn.dstPort}` });
}

async function boot() {
  const st = await api('GET', '/api/state');
  Object.assign(S, { status: st.status, rules: st.rules, prompts: st.prompts, log: st.log || [] });
  render();
  if (window.Notification && Notification.permission === 'default') Notification.requestPermission();

  const es = new EventSource('/api/events');
  es.addEventListener('status', e => { S.status = JSON.parse(e.data); renderHeader(); });
  es.addEventListener('rules', e => { S.rules = JSON.parse(e.data); if (S.tab === 'rules') renderRules(); renderHeader(); });
  es.addEventListener('prompt', e => onPromptNew(JSON.parse(e.data)));
  es.addEventListener('prompt_gone', e => { const id = JSON.parse(e.data); S.prompts = S.prompts.filter(p => p.id !== id); delete S.choice[id]; render(); });
  es.addEventListener('log', e => { S.log.push(...JSON.parse(e.data)); if (S.log.length > 1000) S.log = S.log.slice(-1000); if (S.tab === 'log') renderLog(); });
  es.onerror = () => { $('#conn').className = 'dot off'; $('#connText').textContent = 'osui server unreachable…'; };
  es.onopen = () => api('GET', '/api/state').then(st => { Object.assign(S, { status: st.status, rules: st.rules, prompts: st.prompts }); render(); }).catch(() => {});
}
boot().catch(e => { document.body.append(h('pre', {}, 'Failed to load: ' + e.message)); });
