import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  mirrorBranchName,
  parseMirrorBranchName,
  mirrorBranchDocId,
  slugifyLabel,
  uniqueSlug,
  mirrorCommitMessage,
  parseMirrorTrailers,
  mirrorEnvName,
  maskGithubToken,
  mirrorItemId,
  mirrorOrderKey,
  mirrorBackoffSec,
  GITHUB_OWNER_RE,
  GITHUB_REPO_RE,
  GITHUB_TOKEN_RE,
  MIRROR_TRAILERS,
  GITHUB_MIRROR_CLOUD_API,
} from './github-mirror.js';
import { CLOUD_API } from './cloud-api.js';
import { AUDIT_OPS, CREDENTIAL_OPS } from './types.js';

test('branch names: the agreed layout, round trip', () => {
  assert.equal(mirrorBranchName('template', 'single'), 'template/single');
  assert.equal(mirrorBranchName('deployment', 'test-02'), 'deployment/test-02/cloud');
  assert.equal(mirrorBranchName('agent', 'test-02', 'site-01'), 'deployment/test-02/agent/site-01');
  assert.equal(mirrorBranchName('uncommitted', 'test-02', 'site-01'), 'deployment/test-02/uncommitted/site-01');
  for (const n of ['template/single', 'deployment/test-02/cloud', 'deployment/test-02/agent/site-01', 'deployment/test-02/uncommitted/site-01']) {
    const p = parseMirrorBranchName(n)!;
    assert.ok(p, n);
    assert.equal(mirrorBranchName(p.kind, p.entitySlug, p.siteSlug), n);
  }
  assert.equal(parseMirrorBranchName('main'), null);
  assert.equal(parseMirrorBranchName('deployment/x/cloud'), null); // slug needs 2+ chars
  assert.equal(parseMirrorBranchName('deployment/ab/agent'), null);
  assert.throws(() => mirrorBranchName('agent', 'ab'));
  assert.throws(() => mirrorBranchName('template', 'Bad Slug'));
  assert.equal(mirrorBranchDocId('deployment/test-02/agent/site-01'), 'deployment~test-02~agent~site-01');
});

test('git ref rules: no component is both a file and a directory', () => {
  // deployment/<s>/cloud (file) never collides with deployment/<s>/agent/<site> (dir agent): different components
  const names = [mirrorBranchName('deployment', 'aa'), mirrorBranchName('agent', 'aa', 'cloud'), mirrorBranchName('uncommitted', 'aa', 'cloud')];
  const files = new Set(names);
  for (const n of names) {
    const parts = n.split('/');
    for (let i = 1; i < parts.length; i++) assert.ok(!files.has(parts.slice(0, i).join('/')), `${n} has a prefix that is a branch`);
    assert.doesNotMatch(n, /\.\.|@\{|\.lock$|\/\.|\/$|\s|[~^:?*[\\]/);
  }
});

test('slugifyLabel / uniqueSlug', () => {
  assert.equal(slugifyLabel('Single'), 'single');
  assert.equal(slugifyLabel('Test 02'), 'test-02');
  assert.equal(slugifyLabel('Dev Server'), 'dev-server');
  assert.equal(slugifyLabel('  Ünïcödé — Plant #3 '), 'unicode-plant-3');
  assert.equal(slugifyLabel('2026 plant'), 'x-2026-plant');
  assert.equal(slugifyLabel('2026 plant', 'd-'), 'd-2026-plant');
  assert.equal(slugifyLabel(''), 'x-1');
  assert.equal(slugifyLabel('!!!'), 'x-1');
  assert.equal(slugifyLabel('a'), 'a-1');
  const long = slugifyLabel('a'.repeat(80));
  assert.equal(long.length, 40);
  assert.match(slugifyLabel('x'.repeat(39) + ' y'), /^[a-z][a-z0-9-]{1,39}$/);
  assert.equal(uniqueSlug('single', new Set()), 'single');
  assert.equal(uniqueSlug('single', new Set(['single'])), 'single-2');
  assert.equal(uniqueSlug('single', new Set(['single', 'single-2'])), 'single-3');
  const b40 = 'b'.repeat(40);
  assert.equal(uniqueSlug(b40, new Set([b40])), 'b'.repeat(38) + '-2');
});

test('commit message + trailers', () => {
  const m = mirrorCommitMessage('Add mqtt\r\n\r\nbody line  \n', [
    [MIRROR_TRAILERS.commit, '01JABCDEF0123456789ABCDEFG'],
    [MIRROR_TRAILERS.branch, 'template:abc'],
    [MIRROR_TRAILERS.seq, '3'],
  ]);
  assert.equal(m, 'Add mqtt\n\nbody line\n\nOPM-Commit: 01JABCDEF0123456789ABCDEFG\nOPM-Branch: template:abc\nOPM-Seq: 3\n');
  assert.deepEqual(parseMirrorTrailers(m), { 'OPM-Commit': '01JABCDEF0123456789ABCDEFG', 'OPM-Branch': 'template:abc', 'OPM-Seq': '3' });
  assert.deepEqual(parseMirrorTrailers('just a message'), {});
  assert.deepEqual(parseMirrorTrailers('x\n\nnot a trailer line\nOPM-Commit: y'), {});
});

test('env names', () => {
  assert.equal(mirrorEnvName('secret', 'mqtt-password'), 'OPM_SECRET_MQTT_PASSWORD');
  assert.equal(mirrorEnvName('var', 'server-ip'), 'OPM_VAR_SERVER_IP');
  assert.equal(mirrorEnvName('site-host', 'site-01'), 'OPM_SITE_SITE_01_HOST');
  assert.equal(mirrorEnvName('derived', 'backend', 'DB_URL'), 'OPM_DERIVED_BACKEND_DB_URL');
});

test('repo + token validation, token mask never shows the value', () => {
  assert.ok(GITHUB_OWNER_RE.test('northwind'));
  assert.ok(!GITHUB_OWNER_RE.test('-x'));
  assert.ok(GITHUB_REPO_RE.test('opm-backup.configs'));
  assert.ok(!GITHUB_REPO_RE.test('..'));
  assert.ok(!GITHUB_REPO_RE.test('a/b'));
  const tok = 'github_pat_11ABCDEFG0123456789_abcdefghijklmnopqrstuvwxyzABCDEFwxyz';
  assert.ok(GITHUB_TOKEN_RE.test(tok));
  assert.ok(!GITHUB_TOKEN_RE.test('short'));
  assert.ok(!GITHUB_TOKEN_RE.test('has space in it 0123456789'));
  const masked = maskGithubToken(tok)!;
  assert.equal(masked, 'github_pat_•••wxyz');
  assert.ok(!masked.includes('ABCDEFG'));
  assert.equal(maskGithubToken('ghp_0123456789abcdefghijABCD'), 'ghp_•••ABCD');
  assert.equal(maskGithubToken(null), null);
});

test('queue helpers', () => {
  const id = mirrorItemId('deployment/test-02/agent/site-01', 'commit', '01JABCDEF0123456789ABCDEFG');
  assert.equal(id, 'deployment~test-02~agent~site-01__commit__01JABCDEF0123456789ABCDEFG');
  assert.equal(mirrorItemId('template/single', 'catch-up', ''), 'template~single__catch-up__-');
  assert.doesNotMatch(mirrorItemId('template/single', 'drift', 'sha256:ab/cd'), /[/:]/);
  const a = mirrorOrderKey('2026-10-09T10:00:00.000Z', 2, 'b');
  const b = mirrorOrderKey('2026-10-09T10:00:00.000Z', 10, 'a');
  assert.ok(a < b, 'seq orders numerically at the same time');
  assert.deepEqual([1, 2, 3, 7, 8, 50].map(mirrorBackoffSec), [30, 60, 120, 3600, 3600, 3600]);
});

test('API table: rows spread into CLOUD_API, audit + credential ops', () => {
  for (const k of Object.keys(GITHUB_MIRROR_CLOUD_API)) assert.ok(k in CLOUD_API, k);
  for (const op of ['patchGithubMirror', 'testGithubMirror', 'revealGithubToken', 'syncGithubMirror', 'resyncGithubMirror'] as const) {
    assert.ok(op in AUDIT_OPS, op);
    assert.equal((GITHUB_MIRROR_CLOUD_API as Record<string, { perm: string | null }>)[op]!.perm, 'accounts.admin');
  }
  assert.ok((CREDENTIAL_OPS as readonly string[]).includes('revealGithubToken'));
  assert.equal(GITHUB_MIRROR_CLOUD_API.agentPushDrift.auth, 'agent');
});
