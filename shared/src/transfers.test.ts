// SPEC-final §21 #100 transfers: server-filesystem safety policy (vectors shared with Go) + GUI state.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { AGENT_API } from './agent-api.js';
import { fsClean, fsCrumbs, fsParent, hostFsBlockReason, transferState } from './transfers.js';
import type { Transfer } from './types.js';

const vec = JSON.parse(readFileSync(new URL('../testdata/hostfs-vectors.json', import.meta.url), 'utf8')) as { protected: string[]; cases: { path: string; blocked: string | null }[] };
const kind = (r: string | null) => (r === null ? null : r.startsWith('OPM agent') ? 'agent' : r.includes('kernel') ? 'pseudo' : r === 'not an absolute path' ? 'invalid' : 'secret');

test('hostFsBlockReason vectors', () => {
  for (const c of vec.cases) assert.equal(kind(hostFsBlockReason(c.path, vec.protected)), c.blocked, c.path);
});

test('fs path helpers', () => {
  assert.equal(fsClean('//a/./b/../c/'), '/a/c');
  assert.equal(fsClean('/../..'), '/');
  assert.equal(fsClean('a/b'), null);
  assert.equal(fsParent('/'), null);
  assert.equal(fsParent('/var'), '/');
  assert.equal(fsParent('/var/log/'), '/var');
  assert.deepEqual(fsCrumbs('/var/log').map((c) => c.path), ['/', '/var', '/var/log']);
  assert.deepEqual(fsCrumbs('/').map((c) => c.name), ['/']);
});

const base: Transfer = { transferId: 'T', deploymentId: 'D', siteSlug: 's', direction: 'to-cloud', fileName: 'f', sizeBytes: 100, sha256: 'sha256:' + '0'.repeat(64), storagePath: '', uploadId: null, createdAt: '2026-10-08T00:00:00.000Z', createdBy: 'a@b.c', note: '', expiresAt: '2026-11-08T00:00:00.000Z', status: 'uploading', savedPath: null, error: null };

test('transferState: to-cloud on the agent (store-and-forward)', () => {
  assert.equal(transferState(base, 'agent').key, 'queued');
  const q = transferState({ ...base, delivery: { phase: 'queued', bytesDone: 0, attempts: 2, lastError: 'dial tcp: timeout', lastAttemptAt: null, nextAttemptAt: null } }, 'agent');
  assert.equal(q.key, 'queued');
  assert.match(q.hint!, /retry 2 — dial tcp/);
  const u = transferState({ ...base, delivery: { phase: 'uploading', bytesDone: 50, attempts: 0, lastError: null, lastAttemptAt: null, nextAttemptAt: null } }, 'agent');
  assert.equal(u.key, 'uploading');
  assert.equal(u.progress, 0.5);
  assert.equal(transferState({ ...base, status: 'ready' }, 'agent').key, 'stored');
  assert.equal(transferState({ ...base, status: 'downloaded' }, 'agent').key, 'downloaded');
  assert.equal(transferState({ ...base, status: 'uploading' }, 'cloud').label, 'arriving from the site');
});

test('transferState: to-site', () => {
  const t: Transfer = { ...base, direction: 'to-site', status: 'ready' };
  assert.equal(transferState(t, 'cloud').key, 'pending');
  assert.equal(transferState({ ...t, fetchedAt: '2026-10-08T00:00:01.000Z' }, 'cloud').key, 'on-site');
  assert.equal(transferState({ ...t, status: 'uploading' }, 'cloud').key, 'browser-uploading');
  assert.equal(transferState({ ...t, delivery: { phase: 'pending', bytesDone: 0, attempts: 0, lastError: null, lastAttemptAt: null, nextAttemptAt: null } }, 'agent').key, 'pending');
  assert.equal(transferState({ ...t, delivery: { phase: 'on-site', bytesDone: 100, attempts: 0, lastError: null, lastAttemptAt: null, nextAttemptAt: null } }, 'agent').key, 'on-site');
  assert.equal(transferState({ ...t, status: 'saved-on-server', savedPath: '/srv/x' }, 'agent').hint, '/srv/x');
  assert.equal(transferState({ ...t, status: 'expired' }, 'agent').key, 'expired');
});

test('hostFs endpoint contract', () => {
  assert.equal(AGENT_API.hostFs.path, '/_opm/api/sites/{siteSlug}/transfers/fs');
  assert.equal(AGENT_API.hostFs.auth, 'session');
  assert.equal(AGENT_API.sendFromPath.auth, 'session+actor');
  assert.ok((AGENT_API.sendFromPath.errors as readonly string[]).includes('forbidden'));
});
