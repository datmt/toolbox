// Logic tests for app.js without a browser: DOM is stubbed with a Proxy.
// Run: node test/ui_logic.test.mjs
import fs from 'node:fs';
import vm from 'node:vm';
import assert from 'node:assert/strict';

const stub = new Proxy(function () {}, { get: () => stub, apply: () => stub, set: () => true });
const ctx = vm.createContext({ document: stub, window: {}, fetch: () => Promise.reject(new Error('offline')),
  EventSource: stub, console, setTimeout, clearTimeout, requestAnimationFrame: () => 0, confirm: () => true });
const src = fs.readFileSync(new URL('../internal/web/static/app.js', import.meta.url), 'utf8');
vm.runInContext(src + '\n;globalThis.T = { parseEntry, scopes, baseDomain, denyName, entryName, classifyName: pk };', ctx);
const T = ctx.T;

// domains
let e = T.parseEntry('GitHub.com', true);
assert.equal(e.op.type, 'regexp'); assert.equal(e.op.data, '^(.*\\.)?github\\.com$'); assert.equal(e.text, '*.github.com');
e = T.parseEntry('github.com', false);
assert.equal(e.op.type, 'simple'); assert.equal(e.op.operand, 'dest.host'); assert.equal(e.tail, 'github.com-exact');
e = T.parseEntry('*.example.org', false);                       // wildcard forces subdomains
assert.equal(e.op.type, 'regexp'); assert.equal(e.tail, 'example.org');
// IPs and networks
e = T.parseEntry('93.184.216.34', true);
assert.equal(e.op.operand, 'dest.ip'); assert.equal(e.op.data, '93.184.216.34');
e = T.parseEntry('10.0.0.0/8', true);
assert.equal(e.op.type, 'network'); assert.equal(e.op.operand, 'dest.network');
e = T.parseEntry('2a07:a8c0::', true); assert.equal(e.op.operand, 'dest.ip');
e = T.parseEntry('2001:db8::/32', true); assert.equal(e.op.type, 'network');
// rejects
for (const bad of ['999.1.1.1', '10.0.0.0/40', 'not a domain', 'localhost', 'http://x.com', 'a..com']) {
  assert.throws(() => T.parseEntry(bad, true), undefined, bad);
}
assert.equal(T.parseEntry('   ', true), null);

// names: allow entries must sort before the deny rule (daemon returns first Deny/Precedence in name order)
const prog = '/usr/bin/curl';
const names = ['github.com', 'api.anthropic.com', 'zzz.example.com', 'a.io'].map(d => T.entryName(prog, T.parseEntry(d, true).tail));
for (const n of names) assert.ok(n < T.denyName(prog), `${n} must sort before ${T.denyName(prog)}`);
assert.match(T.denyName(prog), /^[A-Za-z0-9._-]+$/);
assert.equal(T.entryName(prog, 'x.com', '443'), 'wl-usr-bin-curl-a-x.com-443');
assert.notEqual(T.entryName('/usr/bin/python3', 'x'), T.entryName('/opt/python3', 'x'));

// prompt scopes
const sc = T.scopes({ path: '/usr/bin/curl', dstHost: 'api.github.com', dstIp: '1.2.3.4', dstPort: 443 });
assert.equal(sc[0].id, 'host-port'); assert.equal(sc[0].operator.list.length, 3);
assert.ok(sc.find(s => s.id === 'domain').operator.list[1].data.includes('github\\.com'));
assert.ok(sc.find(s => s.id === 'proc').broad);
assert.equal(T.baseDomain('www.bbc.co.uk'), 'bbc.co.uk');
assert.equal(T.scopes({ path: '/x', dstHost: '', dstIp: '1.1.1.1', dstPort: 53 })[0].id, 'ip-port');
console.log('ui logic: all passed');
