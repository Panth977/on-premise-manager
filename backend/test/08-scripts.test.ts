import { test } from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { mkdirSync, writeFileSync, mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { abucket, adb, here, PROJECT, sha256Hex, aauth } from './helpers.ts';

test('scripts: upload-agent writes agent/<v>/<os>-<arch>/opm-agent + .sha256 + .sig + latest; create-admin is idempotent', () => {
  const dir = mkdtempSync(join(tmpdir(), 'opm-bins-'));
  writeFileSync(join(dir, 'opm-agent-linux-amd64'), 'linux-amd64-bin');
  mkdirSync(join(dir, 'darwin-arm64'));
  writeFileSync(join(dir, 'darwin-arm64', 'opm-agent'), 'darwin-arm64-bin');
  const env = { ...process.env, GCLOUD_PROJECT: PROJECT };
  const out = execFileSync('npx', ['tsx', resolve(here, '../scripts/upload-agent.ts'), '--dir', dir, '--version', '9.8.7-test', '--latest', '--project', PROJECT], { env, encoding: 'utf8' });
  assert.match(out, /agent\/latest = 9\.8\.7-test/);
  const a = execFileSync('npx', ['tsx', resolve(here, '../scripts/create-admin.ts'), '--project', PROJECT, '--email', 'script-admin@opm.test', '--password', 'pw-123456', '--name', 'Script Admin'], { env, encoding: 'utf8' });
  const b = execFileSync('npx', ['tsx', resolve(here, '../scripts/create-admin.ts'), '--project', PROJECT, '--email', 'script-admin@opm.test', '--password', 'pw-654321', '--name', 'Script Admin'], { env, encoding: 'utf8' });
  assert.equal(JSON.parse(a).uid, JSON.parse(b).uid);
  return Promise.all([
    abucket.file('agent/9.8.7-test/linux-amd64/opm-agent').download(),
    abucket.file('agent/9.8.7-test/linux-amd64/opm-agent.sha256').download(),
    abucket.file('agent/9.8.7-test/darwin-arm64/opm-agent.sig').download(),
    abucket.file('agent/latest').download(),
    adb.collection('accounts').doc(JSON.parse(a).uid).get(),
    aauth.getUserByEmail('script-admin@opm.test'),
  ]).then(([[bin], [sha], [sig], [latest], acc, user]) => {
    assert.equal(bin.toString(), 'linux-amd64-bin');
    assert.equal(sha.toString().trim(), sha256Hex('linux-amd64-bin'));
    assert.match(sig.toString().trim(), /^[A-Za-z0-9_-]{86}$/);
    assert.equal(latest.toString().trim(), '9.8.7-test');
    assert.equal(acc.data()!.createdBy, 'bootstrap');
    assert.equal(user.uid, JSON.parse(a).uid);
  });
});
