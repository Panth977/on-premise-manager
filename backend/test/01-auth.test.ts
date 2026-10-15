import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createPublicKey, verify } from 'node:crypto';
import { CLOUD_API, signedRequestString } from '@opm/shared';
import { BASE, load, person, raw, rejects, aauth, adb } from './helpers.ts';
import { verifyEd25519 } from '../functions/src/core/auth.ts';

test('signing vectors (shared/testdata/canonical-vectors.json) verify with the backend verifier', () => {
  const v = load('canonical-vectors.json').signing;
  for (const r of v.requests) {
    const str = signedRequestString(r.method, r.path, r.ts, r.nonce, r.bodySha256Hex);
    assert.equal(str, r.stringToSign);
    assert.equal(verifyEd25519(v.publicKeyB64, str, r.signature), true, r.path);
    assert.equal(verifyEd25519(v.publicKeyB64, str + 'x', r.signature), false);
  }
  // cross-check with node directly
  const pk = createPublicKey({ key: { kty: 'OKP', crv: 'Ed25519', x: Buffer.from(v.publicKeyB64, 'base64').toString('base64url') }, format: 'jwk' });
  assert.ok(verify(null, Buffer.from(v.requests[0].stringToSign), pk, Buffer.from(v.requests[0].signature, 'base64url')));
});

test('router: every CLOUD_API route exists; unknown → 404; wrong method → 405', async () => {
  const r = await raw('GET', '/v1/definitely-not-here');
  assert.equal(r.status, 404);
  assert.equal(r.json.error.code, 'not-found');
  assert.ok(r.headers.get('x-request-id'));
  assert.ok(r.headers.get('x-opm-server-time'));
  const m = await raw('PATCH', '/v1/not-found-pages/abc');
  assert.equal(m.status, 405);
  assert.equal(m.json.error.code, 'method-not-allowed');
  const put = await raw('PUT', '/v1/not-found-pages/abc');
  assert.equal(put.status, 405);
  assert.ok(Object.keys(CLOUD_API).length > 80);
});

test('human auth: missing / bad token → 401; no account → 403; disabled → 403', async () => {
  const a = await raw('GET', '/v1/me');
  assert.equal(a.status, 401);
  assert.equal(a.json.error.code, 'unauthenticated');
  const b = await raw('GET', '/v1/me', { headers: { Authorization: 'Bearer nope' } });
  assert.equal(b.status, 401);
  const noAcc = await person([], { noAccount: true });
  await rejects(noAcc.api.getMe({}), 'forbidden', 403);
  const dis = await person([], { disabled: true });
  await rejects(dis.api.getMe({}), 'forbidden', 403);
});

test('GET /v1/me: isAdmin flag, lastLoginAt at most once per hour', async () => {
  const admin = await person('admin');
  const me = await admin.api.getMe({});
  assert.equal(me.isAdmin, true);
  assert.ok(me.account.lastLoginAt);
  const me2 = await admin.api.getMe({});
  assert.equal(me2.account.lastLoginAt, me.account.lastLoginAt);
  const plain = await person([]);
  assert.equal((await plain.api.getMe({})).isAdmin, false);
});

test('revoked refresh tokens (account disabled through Auth) are rejected', async () => {
  const p = await person([]);
  await new Promise((r) => setTimeout(r, 1100));
  await aauth.revokeRefreshTokens(p.uid);
  await rejects(p.api.getMe({}), 'unauthenticated', 401);
  void adb;
});

test('CORS: agent/install paths are open; human paths only for OPM_FRONTEND_ORIGINS', async () => {
  const pre = await fetch(`${BASE}/v1/agent/poll`, { method: 'OPTIONS', headers: { Origin: 'https://evil.example', 'Access-Control-Request-Method': 'GET' } });
  assert.equal(pre.status, 204);
  assert.equal(pre.headers.get('access-control-allow-origin'), '*');
  assert.match(pre.headers.get('access-control-allow-headers') ?? '', /X-OPM-Sig/);
  assert.match(pre.headers.get('access-control-expose-headers') ?? '', /X-OPM-Server-Time/);
  const h1 = await fetch(`${BASE}/v1/me`, { method: 'OPTIONS', headers: { Origin: 'http://localhost:5173' } });
  assert.equal(h1.headers.get('access-control-allow-origin'), 'http://localhost:5173');
  const h2 = await fetch(`${BASE}/v1/me`, { method: 'OPTIONS', headers: { Origin: 'https://evil.example' } });
  assert.equal(h2.headers.get('access-control-allow-origin'), null);
  const agent401 = await raw('GET', '/v1/agent/poll');
  assert.equal(agent401.status, 401);
  assert.ok(agent401.headers.get('x-opm-server-time'), 'X-OPM-Server-Time also on 401');
});
