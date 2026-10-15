// §21 #105 browser change sets: storage form (no secret values), restore state, rebase, secret value changes.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { parseStoredPending, pendingState, pendingStorageKey, stripSecretValues, toStoredPending, draftsExpired, draftValidity, staleDraftNotice, draftKey, draftBlobKey, DRAFT_TTL_MS } from './pending.js';
import { hasSecretValueChanges, rebaseChanges, secretValueChanges } from './branch.js';
import type { Config, ConfigInput } from './types.js';

const hello: Config = JSON.parse(readFileSync(new URL('../testdata/hello-config.json', import.meta.url), 'utf8')).config;
const clone = <T>(x: T): T => JSON.parse(JSON.stringify(x));

test('stored pending: typed secret values never stored, clear + generate kept', () => {
  const input: ConfigInput = { ...clone(hello), secrets: [
    { secretSlug: 'a', label: 'A', description: '', required: false, generate: null, plainValue: 'hunter2' },
    { secretSlug: 'b', label: 'B', description: '', required: false, generate: null, plainValue: null },
    { secretSlug: 'c', label: 'C', description: '', required: false, generate: { charset: 'alnum', length: 20 }, generateNow: true },
    { secretSlug: 'd', label: 'D', description: '', required: false, generate: null },
  ] };
  const s = toStoredPending({ branch: 'deployment:X', baseCommitId: '01H', baseSeq: 3, config: input, count: 4, now: '2026-10-09T00:00:00.000Z' });
  const raw = JSON.stringify(s);
  assert.ok(!raw.includes('hunter2'));
  assert.deepEqual(s.droppedValues, ['a']);
  assert.equal(s.config.secrets[1]!.plainValue, null);
  assert.equal(s.config.secrets[2]!.generateNow, true);
  assert.equal('plainValue' in s.config.secrets[0]!, false);
  assert.equal(input.secrets[0]!.plainValue, 'hunter2', 'the input is not mutated');
  const back = parseStoredPending(raw, 'deployment:X');
  assert.ok(back);
  assert.equal(parseStoredPending(raw, 'deployment:Y'), null, 'other entity');
  assert.equal(parseStoredPending('{bad', 'deployment:X'), null);
  assert.equal(pendingState(back!, '01H'), 'same');
  assert.equal(pendingState(back!, '01J'), 'stale');
  assert.equal(pendingState({ baseCommitId: null }, null), 'same');
  assert.equal(pendingStorageKey('cloud', 'template:t1'), 'opm.pending.v1.cloud.template:t1');
  assert.deepEqual(stripSecretValues(input).dropped, ['a']);
});

test('rebase: clean auto, conflicts reported, choices honoured', () => {
  const base = clone(hello);
  const mine = clone(hello);
  mine.variables[0]!.value = 'yellow';
  const head = clone(hello);
  head.variables[1]!.value = 'Hi';
  const r = rebaseChanges(base, mine, head);
  assert.equal(r.clean, true);
  assert.equal(r.config.variables[0]!.value, 'yellow');
  assert.equal(r.config.variables[1]!.value, 'Hi');
  const head2 = clone(hello);
  head2.variables[0]!.value = 'blue';
  const c = rebaseChanges(base, mine, head2);
  assert.equal(c.clean, false);
  assert.deepEqual(c.conflicts.map((i) => i.key), ['variables/theme/value']);
  assert.equal(rebaseChanges(base, mine, head2, [{ key: 'variables/theme/value', take: 'source' }]).config.variables[0]!.value, 'yellow');
  // no base (the branch had no commit): mine wins on every difference, head-only things stay
  const nb = rebaseChanges(null, mine, head);
  assert.equal(nb.clean, true);
  assert.equal(nb.config.variables[0]!.value, 'yellow');
});

test('secret value changes: slugs only', () => {
  const before = [{ secretSlug: 'a', value: 'x' }, { secretSlug: 'b', value: 'y' }, { secretSlug: 'c', value: null }, { secretSlug: 'd', value: 'z' }];
  const after = [{ secretSlug: 'a', value: 'x2' }, { secretSlug: 'b', value: null }, { secretSlug: 'c', value: 'gen' }, { secretSlug: 'd', value: 'z' }, { secretSlug: 'e', value: 'new' }];
  const ch = secretValueChanges(before, after, ['c']);
  assert.deepEqual(ch, { set: ['a', 'e'], generated: ['c'], cleared: ['b'] });
  assert.equal(hasSecretValueChanges(ch), true);
  assert.equal(hasSecretValueChanges(secretValueChanges(before, before)), false);
  assert.ok(!JSON.stringify(ch).includes('x2'));
});

test('§21 #107 drafts: expiry per app heartbeat, validity = same head, notice, keys per user', () => {
  const now = Date.parse('2026-10-09T12:00:00.000Z');
  assert.equal(draftsExpired(null, now), true);
  assert.equal(draftsExpired(new Date(now - DRAFT_TTL_MS + 1000).toISOString(), now), false);
  assert.equal(draftsExpired(new Date(now - DRAFT_TTL_MS - 1000).toISOString(), now), true);
  assert.equal(draftValidity({ baseCommitId: 'A' }, 'A'), 'valid');
  assert.equal(draftValidity({ baseCommitId: 'A' }, 'B'), 'stale');
  assert.equal(draftValidity({ baseCommitId: null }, null), 'valid');
  assert.equal(staleDraftNotice(3, 5), 'Your local draft was based on commit 3; the latest is commit 5, so it was discarded.');
  assert.notEqual(draftKey('cloud', 'u1', 'template:T'), draftKey('cloud', 'u2', 'template:T'));
  assert.ok(draftBlobKey('agent', 'a@x', 'agent:d/s', 'sha256:00').startsWith(draftKey('agent', 'a@x', 'agent:d/s') + '|'));
});
