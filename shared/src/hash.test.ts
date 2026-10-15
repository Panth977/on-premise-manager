// §21 #103: the pure-TS hashes behind the sha256 / sha512 / mosquitto_passwd filters.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import crypto from 'node:crypto';
import fs from 'node:fs';
import { fileURLToPath } from 'node:url';
import { SHA256, SHA512, fromBase64, hmac, mosquittoHash, mosquittoSalt, pbkdf2, sha256, sha512, toBase64, toHex } from './hash.js';
import { resolve } from './expr.js';

const vectors = JSON.parse(fs.readFileSync(fileURLToPath(new URL('../testdata/mosquitto-passwd-vectors.json', import.meta.url)), 'utf8')) as {
  lines: { tool: string; password: string; line: string }[];
};

test('sha256 / sha512 / hmac match node:crypto for 0–300 byte inputs (block boundaries included)', () => {
  for (let n = 0; n <= 300; n++) {
    const m = crypto.randomBytes(n);
    assert.equal(toHex(sha256(m)), crypto.createHash('sha256').update(m).digest('hex'), `sha256 ${n}`);
    assert.equal(toHex(sha512(m)), crypto.createHash('sha512').update(m).digest('hex'), `sha512 ${n}`);
    const k = crypto.randomBytes(n % 160);
    assert.equal(toHex(hmac(SHA256, k, m)), crypto.createHmac('sha256', k).update(m).digest('hex'), `hmac256 ${n}`);
    assert.equal(toHex(hmac(SHA512, k, m)), crypto.createHmac('sha512', k).update(m).digest('hex'), `hmac512 ${n}`);
  }
});

test('pbkdf2-hmac-sha512 matches node:crypto (1, 101, 1000 iterations; 64 and 100 byte keys)', () => {
  for (const it of [1, 2, 101, 1000]) {
    const pw = crypto.randomBytes(19);
    const salt = crypto.randomBytes(12);
    for (const len of [64, 100]) assert.equal(toHex(pbkdf2(SHA512, pw, salt, it, len)), crypto.pbkdf2Sync(pw, salt, it, len, 'sha512').toString('hex'));
  }
});

test('mosquittoHash reproduces lines written by the real mosquitto_passwd (2.0.18 and 2.1.x, $7 and $6)', () => {
  assert.ok(vectors.lines.length >= 8);
  for (const v of vectors.lines) {
    const enc = v.line.slice(v.line.indexOf(':') + 1);
    const p = enc.split('$'); // ['', '7', iterations, salt, hash] | ['', '6', salt, hash]
    const format = Number(p[1]) as 6 | 7;
    const salt = fromBase64(format === 7 ? p[3]! : p[2]!);
    const iterations = format === 7 ? Number(p[2]) : 1;
    assert.equal(mosquittoHash(v.password, salt, format, iterations), enc, v.tool);
  }
});

test('the user line format: $7$101$<16 base64 chars = 12 bytes>$<88 base64 chars = 64 bytes>', () => {
  const r = resolve('northwind:{{ secret.p | mosquitto_passwd }}', { deployment: { id: 'd' }, self: { slug: 's', host: null }, var: {}, secret: { p: 'pw' } }, 'file');
  assert.match(r.value, /^northwind:\$7\$101\$[A-Za-z0-9+/]{16}\$[A-Za-z0-9+/]{86}==$/);
  assert.equal(r.masked, 'northwind:••••');
  assert.equal(r.secret, true);
});

test('mosquitto salt: deterministic, keyed by the password, differs per site and deployment', () => {
  const a = toBase64(mosquittoSalt('pw', 'dep', 'site-01'));
  assert.equal(a, toBase64(mosquittoSalt('pw', 'dep', 'site-01')));
  assert.notEqual(a, toBase64(mosquittoSalt('pw', 'dep', 'site-02')));
  assert.notEqual(a, toBase64(mosquittoSalt('pw', 'dep2', 'site-01')));
  assert.notEqual(a, toBase64(mosquittoSalt('pw2', 'dep', 'site-01')));
  // no ambiguity between "a" + "b\0c" and "a\0b" + "c": NUL separators and slugs / ids cannot hold NUL
  assert.equal(mosquittoSalt('pw', 'dep', 'site-01').length, 12);
  const want = crypto.createHmac('sha256', 'pw').update('opm-mosquitto-salt\u0000dep\u0000site-01').digest().subarray(0, 12).toString('base64');
  assert.equal(a, want);
});

test('cloud preview: a masked secret value still renders (hash of the stand-in), shown masked; deferred stays the placeholder', () => {
  const ctx = { deployment: { id: 'template' }, self: { slug: 'site-01', host: null }, var: {}, secret: { p: '•••' } };
  const r = resolve('northwind:{{ secret.p | mosquitto_passwd }}', ctx, 'file');
  assert.equal(r.masked, 'northwind:••••');
  const d = resolve('northwind:{{ secret.p | mosquitto_passwd }}', { ...ctx, secret: { p: '‹set at deployment›' } }, 'file');
  assert.equal(d.value, 'northwind:‹set at deployment›');
  assert.equal(d.deferred, true);
});

test('secretHashed: a secret only through mosquitto_passwd is still secret but not in clear (file mode 0444); any clear use → 0400', () => {
  const ctx = { deployment: { id: 'd' }, self: { slug: 's', host: null }, var: { v: 'x' }, secret: { p: 'pw', q: 'pw2' } };
  const hashed = resolve('a:{{ secret.p | mosquitto_passwd }}\nb:{{ secret.q | default("z") | mosquitto_passwd | upper }}', ctx, 'file');
  assert.equal(hashed.secret, true);
  assert.equal(hashed.secretHashed, true);
  assert.equal(resolve('a:{{ secret.p | mosquitto_passwd }} # {{ secret.p }}', ctx, 'file').secretHashed, undefined);
  assert.equal(resolve('{{ secret.p | sha256 }}', ctx, 'file').secretHashed, undefined, 'sha256 is not a password hash');
  assert.equal(resolve('{{ var.v | mosquitto_passwd }}', ctx, 'file').secretHashed, undefined, 'no secret at all');
});
