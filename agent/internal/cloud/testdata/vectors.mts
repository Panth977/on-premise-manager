/** Prints inputs + TS outputs of the pure logic the Go port must match (router_test.go TestVectors). Run with tsx from shared/. */
import { auditActionKeys, auditDiff, capAuditValue, redactSecrets } from '../../../../shared/src/audit.ts';
import { can, canSeeDeployment, fullAdminRules, isAdmin, validateAccessRules } from '../../../../shared/src/access.ts';
import { slugifyLabel } from '../../../../shared/src/github-mirror.ts';

const big = 'x'.repeat(3000);
const docs: [unknown, unknown][] = [
  [null, { b: 1, a: { secret: 's', ok: 1 }, password: 'p', updatedAt: 'x', tokenHash: 'h', list: [1, 2] }],
  [{ b: 1, password: 'p', x: { lastSetPassword: { value: 'v', setAt: 't' } } }, null],
  [{ a: 1, b: { c: 2, d: [1, 2, 3] }, password: 'old', keep: 'same', updatedAt: 'a' }, { a: 2, b: { c: 2, d: [1, 3] }, password: 'new', keep: 'same', updatedAt: 'b' }],
  [{ sites: [{ siteSlug: 'a', v: 1 }, { siteSlug: 'b', v: 2 }] }, { sites: [{ siteSlug: 'b', v: 2 }, { siteSlug: 'a', v: 3 }, { siteSlug: 'c', v: 0 }] }],
  [{ secrets: [{ secretSlug: 's', value: 'aa', plainValue: 'x' }] }, { secrets: [{ secretSlug: 's', value: 'bb', plainValue: 'y' }] }],
  [{ env: [{ name: 'A', value: '1', secret: true }] }, { env: [{ name: 'A', value: '2', secret: true }] }],
  [{ lastResetLink: { value: 'l1', setAt: 'a', expiresAt: 'x' } }, { lastResetLink: { value: 'l2', setAt: 'b', expiresAt: 'y' } }],
  [{ big }, { big: big + 'y', other: { big } }],
  [{ a: [1, { x: 1 }] }, { a: [1, { x: 2 }, 'n'] }],
  [{ a: null, s: '' }, { a: 1, s: 'q', 'weird key': 1, 'b-c': 2 }],
  [{ x: 1 }, { x: '1' }],
  [{ token: null, accessKey: '', n: 5 }, { token: 'abc', accessKey: 'k', n: 5 }],
  [1, 2],
  [[1], [2]],
];
const account = (uid: string, access: unknown[], disabled = false) => ({ uid, access, disabled });
const rules = [
  { resource: 'deployments', actions: ['read'], scope: { kind: 'tags', tagSlugs: ['t1'] } },
  { resource: 'deployment.config', actions: ['read', 'write'], scope: { kind: 'deployments', deploymentIds: ['D1'] } },
  { resource: 'templates', actions: ['read'], scope: { kind: 'tags', tagSlugs: ['t1'] } },
  { resource: 'tags', actions: ['write'], scope: { kind: 'all' } },
];
const targets = [null, { deploymentId: 'D1', tagSlugs: [], managerAccountUids: [] }, { deploymentId: 'D2', tagSlugs: ['t1'], managerAccountUids: ['u'] }];
const checks: unknown[] = [];
for (const acc of [account('u', rules), account('v', rules), account('u', rules, true), account('a', fullAdminRules())])
  for (const t of targets)
    for (const [r, a] of [['services', 'read'], ['templates', 'read'], ['tags', 'write'], ['deployments', 'read'], ['deployments', 'write'], ['deployment.config', 'write'], ['deployment.secrets', 'read'], ['accounts', 'admin']] as const)
      checks.push({ acc, t, r, a, out: can(acc as never, r, a, t as never), see: t ? canSeeDeployment(acc as never, t as never) : null, admin: isAdmin(acc as never) });
const badRules: unknown[] = [
  'x', [1], [{ resource: 'nope' }], [{ resource: 'tags', actions: [], scope: { kind: 'all' } }], [{ resource: 'tags', actions: ['read', 'read', 'admin'], scope: { kind: 'tags', tagSlugs: ['a'] } }],
  [{ resource: 'deployments', actions: ['read'], scope: { kind: 'tags', tagSlugs: ['x'] } }], [{ resource: 'deployments', actions: ['read'], scope: { kind: 'deployments', deploymentIds: [] } }], [{ resource: 'deployments', actions: ['read'] }],
  [{ resource: 'deployments', actions: ['read'], scope: { kind: 'deployments', deploymentIds: ['D9', 'D1'] } }],
];
console.log(
  JSON.stringify({
    diffs: docs.map(([a, b]) => ({ a, b, out: auditDiff(a, b) })),
    redact: docs.flat().map((v) => ({ v, out: redactSecrets(v) })),
    cap: [big, { a: big }, 1, 'ü'.repeat(1500)].map((v) => ({ v, out: capAuditValue(v) })),
    keys: ['a', 'a.b', 'a.b.c'].map((v) => ({ v, out: auditActionKeys(v) })),
    checks,
    rules: badRules.map((v) => ({ v, out: validateAccessRules(v, ['a'], ['D1']) })),
    admin: fullAdminRules(),
    slugs: ['Hello World', 'Ünï çødé', '123 go', '', '!!!', 'a', 'x'.repeat(60), 'trail---'].map((v) => ({ v, t: slugifyLabel(v, 't-'), d: slugifyLabel(v, 'd-') })),
  }),
);
