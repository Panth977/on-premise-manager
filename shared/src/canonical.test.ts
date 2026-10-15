import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { createHash, createPrivateKey, createPublicKey, sign, verify } from 'node:crypto';
import { fileURLToPath } from 'node:url';
import { canonicalize, activityHashInput, signedRequestString, signedResponseString, formatNumber } from './canonical.js';
import { ulid, ULID_RE, ulidTime } from './ulid.js';

const V = JSON.parse(readFileSync(fileURLToPath(new URL('../testdata/canonical-vectors.json', import.meta.url)), 'utf8'));

for (const c of V.canonical as { json: string; output: string }[]) {
  test(`JCS ${c.json}`, () => assert.equal(canonicalize(JSON.parse(c.json)), c.output));
}

test('activity hash chain', () => {
  for (const a of V.activities as { activity: Parameters<typeof activityHashInput>[0] & { hash: string }; hashInput: string; hash: string }[]) {
    const input = activityHashInput(a.activity);
    assert.equal(input, a.hashInput);
    assert.equal('sha256:' + createHash('sha256').update(input, 'utf8').digest('hex'), a.hash);
  }
  assert.equal(V.activities[1].activity.prevHash, V.activities[0].hash);
});

test('signed request vectors', () => {
  const seed = Buffer.from(V.signing.seedHex, 'hex');
  const priv = createPrivateKey({ key: Buffer.concat([Buffer.from('302e020100300506032b657004220420', 'hex'), seed]), format: 'der', type: 'pkcs8' });
  const pub = createPublicKey(priv);
  assert.equal(Buffer.from(pub.export({ format: 'der', type: 'spki' }).subarray(12)).toString('base64'), V.signing.publicKeyB64);
  for (const r of V.signing.requests) {
    assert.equal(createHash('sha256').update(r.body).digest('hex'), r.bodySha256Hex);
    const s = signedRequestString(r.method, r.path, r.ts, r.nonce, r.bodySha256Hex);
    assert.equal(s, r.stringToSign);
    assert.equal(sign(null, Buffer.from(s), priv).toString('base64url'), r.signature);
    assert.ok(verify(null, Buffer.from(s), pub, Buffer.from(r.signature, 'base64url')));
  }
  for (const r of V.signing.responses) {
    const s = signedResponseString(r.status, r.requestNonce, r.ts, r.bodySha256Hex);
    assert.equal(s, r.stringToSign);
    assert.equal(sign(null, Buffer.from(s), priv).toString('base64url'), r.signature);
  }
});

test('formatNumber', () => {
  assert.equal(formatNumber(-0), '0');
  assert.equal(formatNumber(1e21), '1e+21');
  assert.throws(() => formatNumber(NaN));
});

test('ulid', () => {
  const a = ulid(1700000000000);
  const b = ulid(1700000000000);
  assert.match(a, ULID_RE);
  assert.ok(b > a, 'monotonic within the same ms');
  assert.equal(ulidTime(a), 1700000000000);
  assert.ok(ulid() > a);
});
