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
            alProg: '', chat: [], asking: false, llm: null, sel: new Set(), q: '', fAction: '', fRisk: '', logQ: '', choice: {} };

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
  if (S.tab === 'allow') renderAllow();
  if (S.tab === 'rules') renderRules();
  if (S.tab === 'log') renderLog();
  if (S.tab === 'settings') renderSettings();
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
      r.risk === 'broad' && procOf(r) ? [h('button', { class: 'small', onclick: () => gotoAllow(procOf(r)) }, 'Restrict…'), ' '] : null,
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


// ---------- allowlists ----------
// One allow rule per entry (process + destination), plus one optional
// "deny rest" rule per program. Allow rules use precedence so they win.
const pk = prog => prog.replace(/[^A-Za-z0-9]+/g, '-').replace(/^-|-$/g, '');
// The daemon walks rules in name order and returns the first Deny or Precedence
// match, so allow entries ("-a-") must sort before the deny rule ("-z-").
const denyName = prog => `wl-${pk(prog)}-z-deny-rest`;
const entryName = (prog, tail, port) => `wl-${pk(prog)}-a-${tail}${port ? '-' + port : ''}`.slice(0, 190);
const procOf = r => { const l = leaves(r.operator).find(x => x.operand === 'process.path' && x.type === 'simple'); return l ? l.data : ''; };
const isWl = (r, prog) => r.name.startsWith(`wl-${pk(prog)}-`) && procOf(r) === prog;

function knownProgs() {
  const s = new Set();
  S.rules.forEach(r => { const p = procOf(r); if (p) s.add(p); });
  S.log.forEach(e => e.process && s.add(e.process));
  S.prompts.forEach(p => p.conn.path && s.add(p.conn.path));
  return [...s].sort();
}

function gotoAllow(prog) {
  S.alProg = prog;
  $('#tabs button[data-tab=allow]').click();
}

const IPV4 = /^\d{1,3}(\.\d{1,3}){3}$/;
const IPV6 = /^[0-9a-f:]*:[0-9a-f:]*$/i;
const DOMAIN = /^([a-z0-9-]+\.)+[a-z]{2,}$/;
function parseEntry(raw, subdomains) {
  let t = raw.trim().toLowerCase();
  if (!t) return null;
  if (t.includes('/')) {
    const [ip, bits] = t.split('/');
    if (!(IPV4.test(ip) && +bits >= 0 && +bits <= 32) && !(IPV6.test(ip) && +bits >= 0 && +bits <= 128)) throw new Error(`bad network "${raw}"`);
    return { text: t, tail: t.replace(/[^a-z0-9]+/g, '-'), op: { type: 'network', operand: 'dest.network', data: t, sensitive: false } };
  }
  if (IPV4.test(t) || IPV6.test(t)) {
    if (IPV4.test(t) && t.split('.').some(n => +n > 255)) throw new Error(`bad IP "${raw}"`);
    return { text: t, tail: t.replace(/[^a-z0-9]+/g, '-'), op: simple('dest.ip', t) };
  }
  const wild = t.startsWith('*.') || t.startsWith('.');
  t = t.replace(/^\*?\./, '');
  if (!DOMAIN.test(t)) throw new Error(`"${raw}" is not a domain, IP or CIDR`);
  const sub = subdomains || wild;
  return { text: (sub ? '*.' : '') + t, tail: sub ? t : t + '-exact', op: sub ? { type: 'regexp', operand: 'dest.host', data: '^(.*\\.)?' + esc(t) + '$', sensitive: false } : simple('dest.host', t) };
}

async function addEntries(prog, text, port, subdomains) {
  const parts = text.split(/[\s,;]+/).filter(Boolean);
  if (!parts.length) throw new Error('enter at least one domain, IP or CIDR');
  if (port && !(/^\d{1,5}$/.test(port) && +port > 0 && +port < 65536)) throw new Error('bad port');
  const parsed = parts.map(x => parseEntry(x, subdomains));          // validate all before sending any
  for (const e of parsed) {
    const ops = [simple('process.path', prog), e.op, port ? simple('dest.port', port) : null].filter(Boolean);
    await api('POST', '/api/rules', { name: entryName(prog, e.tail, port),
      description: `osui allowlist: ${e.text}${port ? ':' + port : ''}`, enabled: true, precedence: true, nolog: false,
      action: 'allow', duration: 'always', operator: all(ops) });
  }
  toast(`Added ${parsed.length} entr${parsed.length === 1 ? 'y' : 'ies'}`);
}

const putDeny = prog => api('POST', '/api/rules', { name: denyName(prog), description: 'osui allowlist: block everything else',
  enabled: true, precedence: false, nolog: false, action: 'deny', duration: 'always', operator: simple('process.path', prog) });

// ---------- natural-language helper (user-configured LLM endpoint) ----------
// The model only PROPOSES. Everything is re-validated here, shown as a preview,
// and applied only when the user clicks Apply.
async function loadLLM() { try { S.llm = await api('GET', '/api/llm'); } catch { S.llm = null; } }

function describeIntent(i) {
  const parsed = [], problems = [];
  for (const d of i.destinations) { try { const e = parseEntry(d, i.include_subdomains); if (e) parsed.push(e); } catch (err) { problems.push(err.message); } }
  return { parsed, problems };
}

// ---------- settings tab ----------
function renderSettings() {
  const root = $('#tab-settings');
  const ae = document.activeElement;
  if (root.contains(ae) && ae.tagName === 'INPUT' && ae.value) return; // don't wipe typing
  const L = S.llm || {};
  const urlIn = h('input', { type: 'text', id: 'llmUrl', size: 44, value: L.url || '', placeholder: 'http://127.0.0.1:11434/v1' });
  const modelIn = h('input', { type: 'text', id: 'llmModel', size: 26, value: L.model || '', placeholder: 'e.g. qwen2.5:7b' });
  const keyIn = h('input', { type: 'password', id: 'llmKey', size: 26, autocomplete: 'off',
    placeholder: L.hasKey ? 'key saved — leave blank to keep' : 'optional' });
  const status = h('span', { class: 'muted', id: 'llmStatus' });
  const save = async clear => {
    S.llm = await api('POST', '/api/llm', { url: urlIn.value, model: modelIn.value, key: keyIn.value, clearKey: clear === true });
    keyIn.value = ''; renderChat();
  };
  const preset = (url, model) => h('button', { class: 'small', onclick: () => { urlIn.value = url; if (model) modelIn.value = model; } }, url.includes('11434') ? 'Ollama' : url.includes('openai') ? 'OpenAI' : 'llama.cpp');
  root.replaceChildren(
    h('div', { class: 'card' },
      h('h2', {}, 'LLM endpoint'),
      h('p', { class: 'muted' }, 'Used by "Describe what you want" on the Allowlists tab. Any OpenAI-compatible chat endpoint works. The model only proposes rules; you review and apply them.'),
      h('div', { class: 'row', style: 'margin:8px 0' }, h('span', { class: 'muted' }, 'Presets:'),
        preset('http://127.0.0.1:11434/v1'), preset('http://127.0.0.1:8080/v1'), preset('https://api.openai.com/v1', 'gpt-4o-mini')),
      h('dl', { class: 'kv', style: 'grid-template-columns:130px 1fr' },
        h('dt', {}, 'Base URL'), h('dd', {}, urlIn),
        h('dt', {}, 'Model'), h('dd', {}, modelIn),
        h('dt', {}, 'API key'), h('dd', {}, keyIn)),
      h('div', { class: 'row' },
        h('button', { class: 'primary', onclick: guard(async () => { await save(false); toast('Saved'); renderSettings(); }) }, 'Save'),
        h('button', { class: 'ghost', onclick: guard(async () => {
          await save(false);
          status.textContent = 'testing…';
          try {
            const r = await api('POST', '/api/llm/propose', { text: 'allow /usr/bin/curl to reach example.com' });
            status.textContent = `OK — endpoint replied (${r.intents.length} proposal${r.intents.length === 1 ? '' : 's'}${r.note ? ', note: ' + r.note : ''})`;
            status.className = 'tag narrow';
          } catch (e) { status.textContent = 'Failed: ' + e.message; status.className = 'err'; }
        }) }, 'Save & test'),
        L.hasKey ? h('button', { class: 'small danger', onclick: guard(async () => { await save(true); toast('Key removed'); renderSettings(); }) }, 'Remove key') : null,
        status),
      L.url && !L.loopback ? h('p', { class: 'err' }, 'This endpoint is not on this machine: your request text and the names of programs you have rules for are sent to it.') : null,
      h('p', { class: 'muted' }, 'The key is stored in ~/.config/osui/llm.json (mode 0600) and is never sent back to the browser. OSUI_LLM_KEY overrides it.')));
}

// ---------- assistant sidebar ----------
function chatBubble(m) {
  if (m.role === 'user') return h('div', { class: 'bubble user' }, m.text);
  if (m.error) return h('div', { class: 'bubble bot' }, h('span', { class: 'err' }, m.error));
  return h('div', { class: 'bubble bot' }, proposalView(m));
}

function proposalView(m) {
  const p = m.proposal;
  const rows = p.intents.map(i => ({ i, ...describeIntent(i) })).filter(r => r.parsed.length);
  const known = new Set(knownProgs());
  const done = m.state !== 'pending';
  const apply = guard(async () => {
    for (const r of rows) {
      await addEntries(r.i.program, r.parsed.map(e => e.text).join(' '), r.i.port, r.i.include_subdomains);
      if (r.i.block_rest) await putDeny(r.i.program);
    }
    m.state = 'applied'; S.alProg = rows[0].i.program;
    if (S.tab === 'allow') renderAllow();
    renderChat();
  });
  if (!rows.length) return h('div', {}, p.note ? h('p', {}, p.note) : null, 'I could not turn that into usable entries. Try rephrasing, or add them on the Allowlists tab.');
  return h('div', {},
    p.note ? h('p', { class: 'muted' }, p.note) : null,
    h('b', {}, done ? (m.state === 'applied' ? 'Applied:' : 'Discarded:') : 'Proposed changes (nothing applied yet):'),
    rows.map(r => h('div', { style: 'margin:6px 0' },
      h('div', {}, h('span', { class: 'mono' }, r.i.program), ' may reach' + (r.i.port ? ` port ${r.i.port} of` : ':')),
      h('ul', {}, r.parsed.map(e => h('li', { class: 'mono' }, e.text))),
      r.i.block_rest ? h('div', { class: 'tag medium' }, 'then block everything else for this program') : null,
      !done && !known.has(r.i.program) ? h('div', { class: 'err' }, 'No rules or traffic for this program yet — check the path.') : null,
      r.problems.length ? h('div', { class: 'err' }, 'Skipped: ' + r.problems.join('; ')) : null)),
    done
      ? (m.state === 'applied' ? h('button', { class: 'small', onclick: () => { S.alProg = rows[0].i.program; showTab('allow'); } }, 'View in Allowlists') : null)
      : h('div', { class: 'row' },
          h('button', { class: 'primary', onclick: apply }, 'Apply'),
          h('button', { class: 'ghost', onclick: () => { m.state = 'discarded'; renderChat(); } }, 'Discard')));
}

function renderChat() {
  const L = S.llm || {};
  const configured = !!(L.url && L.model);
  const log = $('#chatLog');
  log.replaceChildren(
    !configured ? h('div', { class: 'bubble bot' }, 'No LLM endpoint is configured yet. ',
      h('button', { class: 'small', onclick: () => showTab('settings') }, 'Open settings')) : null,
    configured && !S.chat.length ? h('div', { class: 'muted' },
      'Describe the rules you want, e.g. "let curl reach github.com and anything under anthropic.com, block everything else". ',
      'I only propose allow entries; you review them and click Apply.') : null,
    S.chat.map(chatBubble),
    S.asking ? h('div', { class: 'bubble bot muted' }, 'Thinking…') : null);
  log.scrollTop = log.scrollHeight;
  $('#askSend').disabled = !configured || S.asking;
  $('#askModel').textContent = configured ? L.model : '';
  $('#askModel').title = configured ? `${L.model} @ ${L.url}` : '';
}

async function sendAsk() {
  const ta = $('#askText');
  const text = ta.value.trim();
  if (!text || S.asking || !S.llm || !S.llm.url) return;
  ta.value = '';
  S.chat.push({ role: 'user', text });
  S.asking = true; renderChat();
  try { S.chat.push({ role: 'bot', proposal: await api('POST', '/api/llm/propose', { text }), state: 'pending' }); }
  catch (e) { S.chat.push({ role: 'bot', error: e.message }); }
  S.asking = false; renderChat();
}

function setAssistant(open) {
  $('#assistant').classList.toggle('collapsed', !open);
  $('#assistantToggle').classList.toggle('active-toggle', open);
  $('#assistantToggle').setAttribute('aria-expanded', String(open));
  try { localStorage.setItem('osui.assistant', open ? 'open' : 'closed'); } catch { /* storage unavailable */ }
  if (open) { renderChat(); $('#askText').focus(); }
}
$('#assistantToggle').addEventListener('click', () => setAssistant($('#assistant').classList.contains('collapsed')));
$('#assistantClose').addEventListener('click', () => setAssistant(false));
$('#chatClear').addEventListener('click', () => { S.chat = []; renderChat(); });
$('#askSend').addEventListener('click', sendAsk);
$('#askText').addEventListener('keydown', e => { if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) { e.preventDefault(); sendAsk(); } });

function renderAllow() {
  const root = $('#tab-allow');
  const ae = document.activeElement;
  if (root.contains(ae) && ((ae.tagName === 'INPUT' && ae.type !== 'checkbox' && ae.value) || (ae.tagName === 'TEXTAREA' && ae.value))) return; // don't wipe typing
  const progs = knownProgs();
  const prog = S.alProg;
  const kids = [
    h('p', { class: 'muted' }, 'Pick a program, list the domains / IPs / networks it may reach, then turn on "block everything else". ',
      'Each entry becomes one allow rule; blocking creates one deny rule for that program.'),
    h('div', { class: 'toolbar' },
      h('select', { onchange: e => { S.alProg = e.target.value; renderAllow(); } },
        h('option', { value: '' }, '— choose a program —'),
        (progs.includes(prog) || !prog ? progs : [prog, ...progs]).map(p => h('option', { value: p, selected: p === prog }, p))),
      h('input', { type: 'text', id: 'alCustom', placeholder: 'or type a path, e.g. /usr/bin/curl', size: 34,
        onkeydown: e => { if (e.key === 'Enter' && e.target.value.trim().startsWith('/')) { S.alProg = e.target.value.trim(); renderAllow(); } } }),
      h('button', { class: 'ghost', onclick: () => { const v = $('#alCustom').value.trim(); if (!v.startsWith('/')) return toast('path must be absolute'); S.alProg = v; renderAllow(); } }, 'Use path'))];

  if (!prog) { kids.push(h('div', { class: 'empty' }, 'Choose a program above (or use "Restrict…" on a broad rule in the Rules tab).')); root.replaceChildren(...kids); return; }

  const entries = S.rules.filter(r => isWl(r, prog) && r.name !== denyName(prog)).sort((a, b) => a.name.localeCompare(b.name));
  const deny = S.rules.find(r => r.name === denyName(prog));
  const broad = S.rules.filter(r => r.risk === 'broad' && r.enabled && procOf(r) === prog);

  if (broad.length) kids.push(h('div', { class: 'card', style: 'border-color:var(--deny)' },
    h('b', {}, `${broad.length} existing rule(s) already allow ${base(prog)} to reach anything: `), broad.map(r => h('span', { class: 'mono' }, r.name + ' ')),
    h('div', { class: 'row', style: 'margin-top:8px' },
      h('button', { class: 'small', onclick: guard(async () => { for (const r of broad) await api('POST', `/api/rules/${encodeURIComponent(r.name)}/disable`); toast('Disabled — the allowlist now applies'); }) }, 'Disable them'),
      h('span', { class: 'muted' }, 'Needed, otherwise the allowlist is bypassed.'))));

  const blockSw = h('input', { type: 'checkbox', class: 'sw', checked: !!deny && deny.enabled,
    onchange: guard(async e => {
      if (e.target.checked) {
        if (!entries.length && !confirm(`The list is empty: this will block ALL network access for ${prog}. Continue?`)) { e.target.checked = false; return; }
        await putDeny(prog);
        toast(`${base(prog)} can now reach only the listed destinations`);
      } else if (deny) {
        await api('DELETE', `/api/rules/${encodeURIComponent(deny.name)}`);
        toast('Deny rule removed');
      }
    }) });

  kids.push(h('div', { class: 'card' },
    h('div', { class: 'row' }, h('b', {}, base(prog)), h('span', { class: 'muted mono' }, prog)),
    h('div', { class: 'row', style: 'margin:10px 0' }, blockSw, h('span', {}, 'Block everything else for this program'),
      deny ? h('span', { class: 'tag narrow' }, 'enforced') : h('span', { class: 'tag medium' }, 'not enforced: only the allow rules below exist')),
    h('div', { class: 'row' },
      h('input', { type: 'text', id: 'alText', placeholder: 'github.com, *.example.org, 93.184.216.34, 10.0.0.0/8', size: 52,
        onkeydown: e => { if (e.key === 'Enter') $('#alAdd').click(); } }),
      h('input', { type: 'text', id: 'alPort', placeholder: 'port (optional)', size: 12 }),
      h('label', { class: 'row' }, h('input', { type: 'checkbox', id: 'alSub', checked: true }), 'include subdomains'),
      h('button', { id: 'alAdd', class: 'primary', onclick: guard(async () => {
        await addEntries(prog, $('#alText').value, $('#alPort').value.trim(), $('#alSub').checked);
        $('#alText').value = ''; $('#alPort').value = ''; renderAllow();
      }) }, 'Add')),
    h('p', { class: 'muted' }, 'Domains match the hostname from the DNS lookup; with DoH or hardcoded IPs the hostname is unknown, so add the IP instead.')));

  kids.push(entries.length
    ? h('table', {}, h('thead', {}, h('tr', {}, ['On', 'Allowed destination', 'Rule', ''].map(t => h('th', {}, t)))),
        h('tbody', {}, entries.map(r => h('tr', { class: r.enabled ? '' : 'off' },
          h('td', {}, h('input', { type: 'checkbox', class: 'sw', checked: r.enabled, onchange: guard(async e => { await api('POST', `/api/rules/${encodeURIComponent(r.name)}/${e.target.checked ? 'enable' : 'disable'}`); }) })),
          h('td', { class: 'mono' }, (r.description || '').replace(/^osui allowlist: /, '') || summarize(r.operator)),
          h('td', { class: 'mono muted' }, r.name),
          h('td', { class: 'actions' }, h('button', { class: 'small danger', onclick: guard(async () => { await api('DELETE', `/api/rules/${encodeURIComponent(r.name)}`); }) }, 'Remove'))))))
    : h('div', { class: 'empty' }, 'No allowed destinations yet for this program.'));
  root.replaceChildren(...kids);
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
function showTab(t) {
  S.tab = t;
  document.querySelectorAll('#tabs button').forEach(x => x.classList.toggle('active', x.dataset.tab === t));
  $('#gear').classList.toggle('active-toggle', t === 'settings');
  for (const x of ['prompts', 'allow', 'rules', 'log', 'settings']) $('#tab-' + x).classList.toggle('hidden', x !== t);
  try { history.replaceState(null, '', t === 'prompts' ? location.pathname : '#' + t); } catch { /* ignore */ }
  render();
}
$('#tabs').addEventListener('click', e => { const b = e.target.closest('button'); if (b) showTab(b.dataset.tab); });
$('#gear').addEventListener('click', () => showTab(S.tab === 'settings' ? 'prompts' : 'settings'));

function onPromptNew(p) {
  if (!S.prompts.some(x => x.id === p.id)) S.prompts.push(p);
  if (S.tab !== 'prompts') $('#tabs button[data-tab=prompts]').click();
  else render();
  if (window.Notification && Notification.permission === 'granted') new Notification('OpenSnitch', { body: `${base(p.conn.path)} → ${p.conn.dstHost || p.conn.dstIp}:${p.conn.dstPort}` });
}

async function boot() {
  const st = await api('GET', '/api/state');
  Object.assign(S, { status: st.status, rules: st.rules, prompts: st.prompts, log: st.log || [] });
  await loadLLM();
  const hash = location.hash.slice(1);
  if (['allow', 'rules', 'log', 'settings'].includes(hash)) showTab(hash); else render();
  let open = false;
  try { open = localStorage.getItem('osui.assistant') === 'open'; } catch { /* ignore */ }
  setAssistant(open);
  if (window.Notification && Notification.permission === 'default') Notification.requestPermission();

  const es = new EventSource('/api/events');
  es.addEventListener('status', e => { S.status = JSON.parse(e.data); renderHeader(); });
  es.addEventListener('rules', e => { S.rules = JSON.parse(e.data); if (S.tab === 'rules') renderRules(); if (S.tab === 'allow') renderAllow(); renderHeader(); });
  es.addEventListener('prompt', e => onPromptNew(JSON.parse(e.data)));
  es.addEventListener('prompt_gone', e => { const id = JSON.parse(e.data); S.prompts = S.prompts.filter(p => p.id !== id); delete S.choice[id]; render(); });
  es.addEventListener('log', e => { S.log.push(...JSON.parse(e.data)); if (S.log.length > 1000) S.log = S.log.slice(-1000); if (S.tab === 'log') renderLog(); });
  es.onerror = () => { $('#conn').className = 'dot off'; $('#connText').textContent = 'osui server unreachable…'; };
  es.onopen = () => api('GET', '/api/state').then(st => { Object.assign(S, { status: st.status, rules: st.rules, prompts: st.prompts }); render(); }).catch(() => {});
}
boot().catch(e => { document.body.append(h('pre', {}, 'Failed to load: ' + e.message)); });
