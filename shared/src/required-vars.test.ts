import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { validateConfig, containerExprContext, resolveSiteHosts, type ValidationContext } from './validate.js';
import { resolve, DEPLOY_PLACEHOLDER, tryResolve } from './expr.js';
import type { Config } from './types.js';

const load = (f: string) => JSON.parse(readFileSync(fileURLToPath(new URL(`../testdata/${f}`, import.meta.url)), 'utf8'));
const HELLO = load('hello-config.json') as { deploymentId: string; config: Config };
const CAT = load('hello-catalog.json') as Omit<ValidationContext, 'mode' | 'deploymentId'>;
const V = load('required-vars-vectors.json') as {
  codes: string[];
  cases: { name: string; mode: ValidationContext['mode']; variable: { required: boolean; value: string | null }; secretValue: string | null; expect: unknown[] }[];
};
function config(required: boolean, value: string | null, secretValue: string | null): Config {
  const c = structuredClone(HELLO.config);
  c.variables.push({ variableSlug: 'server-ip', label: 'Server IP', description: '', type: 'string', value, required, options: null, validation: null });
  c.secrets.push({ secretSlug: 'jwt-key', label: 'JWT key', description: '', required: true, generate: null, value: secretValue, rotatedAt: null });
  const app = c.sites.find((s) => s.siteSlug === 'app')!.config;
  app.host = '{{ var.server-ip }}';
  for (const k of app.containers) k.env.push({ name: 'SERVER_URL', value: 'http://{{ var.server-ip | upper }}:8080', secretRef: null });
  app.containers[0]!.env.push({ name: 'JWT', value: 'Bearer {{ secret.jwt-key }}', secretRef: null });
  return c;
}
const ctx = (mode: ValidationContext['mode']): ValidationContext => ({ mode, deploymentId: HELLO.deploymentId, services: CAT.services, images: CAT.images, notFoundPageIds: CAT.notFoundPageIds });

for (const c of V.cases) {
  test(`required vars vector: ${c.name}`, () => {
    const is = validateConfig(config(c.variable.required, c.variable.value, c.secretValue), ctx(c.mode));
    assert.deepEqual(
      is.list.filter((x) => V.codes.includes(x.code)).map(({ path, code, severity }) => ({ path, code, severity })),
      c.expect,
    );
  });
}

test('previews: a required variable without value shows ‹set at deployment› and keeps evaluating', () => {
  const cfg = config(true, null, null);
  const hosts = resolveSiteHosts(cfg, 'template');
  assert.equal(hosts.app!.host, DEPLOY_PLACEHOLDER);
  assert.equal(hosts.app!.fallback, null);
  const c = cfg.sites.find((s) => s.siteSlug === 'app')!.config.containers[0]!;
  const ec = containerExprContext(cfg, ctx('template'), 'app', c);
  const r = resolve('http://{{ var.server-ip }}:8080/{{ var.greeting | lower }}', ec, 'env');
  assert.equal(r.value, `http://${DEPLOY_PLACEHOLDER}:8080/hello from opm`);
  assert.equal(r.deferred, true);
  const t = tryResolve('{{ var.server-ip | int }}', ec, 'env');
  assert.equal(!t.ok && t.error.code, 'E_DEFERRED', 'a filter error caused by the stand-in is deferred, not E_TYPE');
  assert.equal(resolve('Bearer {{ secret.jwt-key }}', ec, 'env').masked, 'Bearer ••••');
  // agent mode is strict: no stand-in
  const ea = containerExprContext(cfg, ctx('agent'), 'app', c);
  assert.equal(tryResolve('{{ var.server-ip }}', ea, 'env').ok, false);
});
