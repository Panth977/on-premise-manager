import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import { applyEnvironment, classifyRedis, classifyStatement, devToolConfirmOk, devToolSecretRefs, isOpmPlaceholder, newDevToolPreset, parseRedisCommandLine, redactHttpRequest, redactStatement, splitSqlStatements, truncateUtf8, type DevToolKind } from './devtools.js';

const here = dirname(fileURLToPath(import.meta.url));
const V = JSON.parse(readFileSync(join(here, '..', 'testdata', 'devtools-vectors.json'), 'utf8'));

test('devtools vectors: split, classify, redis, parse, redact', () => {
  for (const v of V.splitSql) assert.deepEqual(splitSqlStatements(v.in), v.out, v.in);
  for (const v of V.classify) assert.deepEqual(classifyStatement(v.kind, v.in), v.out, v.in);
  for (const v of V.redis) assert.deepEqual(classifyRedis(v.in), v.out, v.in.join(' '));
  for (const v of V.parseRedis) assert.deepEqual(parseRedisCommandLine(v.in), v.out, v.in);
  for (const v of V.redact) assert.deepEqual(redactStatement(v.kind as DevToolKind, v.in), v.out, v.in);
  for (const v of V.redactHttp) assert.deepEqual(redactHttpRequest(v.in[0], v.in[1], v.in[2], v.in[3]), v.out);
});

test('devtools helpers', () => {
  assert.equal(truncateUtf8('é'.repeat(3000), 4096).truncated, true);
  assert.ok(new TextEncoder().encode(truncateUtf8('é'.repeat(3000), 4096).text).length <= 4096);
  assert.ok(devToolConfirmOk('Main DB', ' Main DB '));
  assert.ok(!devToolConfirmOk('Main DB', 'main db'));
  assert.ok(!devToolConfirmOk('', ''));
  const p = newDevToolPreset('postgres', ['postgres']);
  assert.equal(p.presetSlug, 'postgres-2');
  p.auth.password = { kind: 'expr', expr: '{{ secret.pg-pass }}' };
  p.options.defaultHeaders = [{ name: 'X', value: '{{ secret.other }}' }];
  assert.deepEqual(devToolSecretRefs(p), ['other', 'pg-pass']);
  assert.ok(isOpmPlaceholder(' secret.x '));
  assert.ok(!isOpmPlaceholder('baseUrl'));
  assert.equal(applyEnvironment('{{host}}/a?k={{ secret.k }}&{{missing}}', { host: 'h' }), 'h/a?k={{ secret.k }}&{{missing}}');
});
