// §21 #103: static dev-tool credential values (Value mode) + hash filters — diff masking, validation severity, old-agent needs.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import { fileURLToPath } from 'node:url';
import type { Config } from './types.js';
import { diffConfigs, DEVTOOL_VALUE_CHANGED_NOTE, maskDevToolLiterals } from './diff.js';
import { Issues, siteFilterAgentNeed, validateSiteDevTools } from './validate.js';
import { DEVTOOL_LITERAL_MIN_AGENT, devToolLiteralFields, newDevToolPreset, type DevToolPreset } from './devtools.js';
import { exprFilters, exprMinAgent, FILTER_MIN_AGENT } from './expr.js';
import { redactSecrets } from './audit.js';

const HELLO = JSON.parse(fs.readFileSync(fileURLToPath(new URL('../testdata/hello-config.json', import.meta.url)), 'utf8')).config as Config;
const clone = <T>(v: T): T => JSON.parse(JSON.stringify(v)) as T;
const mqtt = (value: string): DevToolPreset => {
  const p = newDevToolPreset('mqtt');
  p.presetSlug = 'broker';
  p.target = { kind: 'host', host: 'mqtt.local', port: 1883 };
  p.auth.username = 'northwind';
  p.auth.password = { kind: 'literal', value };
  return p;
};
const withPreset = (p: DevToolPreset | null): Config => {
  const c = clone(HELLO);
  const site = c.sites.find((s) => s.siteSlug === 'db')!;
  if (p) site.config.devTools = [p];
  else delete site.config.devTools;
  return c;
};

test('diff: a static value is compared raw and never shown in clear', () => {
  const a = withPreset(mqtt('hunter2-secret'));
  const b = withPreset(mqtt('other-value-99'));
  const d = diffConfigs(a, b);
  assert.equal(d.length, 1);
  assert.equal(d[0]!.section, 'devTools');
  assert.equal(d[0]!.kind, 'changed');
  assert.equal(d[0]!.note, DEVTOOL_VALUE_CHANGED_NOTE);
  const js = JSON.stringify(d);
  assert.ok(!js.includes('hunter2-secret') && !js.includes('other-value-99') && js.includes('••••'), js);
  const added = diffConfigs(withPreset(null), a);
  assert.equal(added.length, 1);
  assert.ok(!JSON.stringify(added).includes('hunter2-secret'));
  const removed = diffConfigs(a, withPreset(null));
  assert.ok(!JSON.stringify(removed).includes('hunter2-secret'));
  // unchanged static value → no change at all
  assert.equal(diffConfigs(a, withPreset(mqtt('hunter2-secret'))).length, 0);
  // expressions are still shown as they are
  assert.equal(maskDevToolLiterals({ ...mqtt('x'), auth: { ...mqtt('x').auth, password: { kind: 'expr', expr: '{{ secret.p }}' } } }).auth.password.kind, 'expr');
});

test('validation: a static value is a WARNING (V_DEVTOOL_LITERAL), an empty one an error', () => {
  const c = withPreset(mqtt('hunter2'));
  const is = new Issues('publish');
  validateSiteDevTools(c.sites.find((s) => s.siteSlug === 'db')!, c, {}, is);
  assert.deepEqual(is.list.map((i) => [i.code, i.severity]), [['V_DEVTOOL_LITERAL', 'warning']]);
  assert.match(is.list[0]!.message, /stored in plain text in the config — anyone who can read the config sees it; use a secret for production/);
  const e = withPreset(mqtt(''));
  const is2 = new Issues('publish');
  validateSiteDevTools(e.sites.find((s) => s.siteSlug === 'db')!, e, {}, is2);
  assert.deepEqual(is2.list.map((i) => [i.code, i.severity]), [['V_DEVTOOL_AUTH', 'error']]);
  assert.deepEqual(devToolLiteralFields(mqtt('x')), ['password']);
  assert.equal(DEVTOOL_LITERAL_MIN_AGENT, '0.13.1');
});

test('audit redaction masks a static value inside a preset password', () => {
  const r = JSON.stringify(redactSecrets(withPreset(mqtt('hunter2-secret'))));
  assert.ok(!r.includes('hunter2-secret'));
});

test('old agents: hash filters need 0.13.1 (config strings + mounted rendered files)', () => {
  assert.deepEqual(exprFilters('a {{ secret.x | mosquitto_passwd | sha256 }} {{ var.y | upper }}'), ['mosquitto_passwd', 'sha256', 'upper']);
  assert.equal(exprMinAgent('{{ var.y | upper }}'), null);
  assert.equal(exprMinAgent('{{ var.y | sha512 }}'), '0.13.1');
  assert.equal(FILTER_MIN_AGENT.mosquitto_passwd, '0.13.1');
  const c = clone(HELLO);
  assert.equal(siteFilterAgentNeed(c, 'app'), null);
  // a rendered file mounted by hello-web on site app
  const need = siteFilterAgentNeed(c, 'app', { banner: 'u:{{ var.greeting | mosquitto_passwd }}' });
  assert.deepEqual(need, { min: '0.13.1', uses: ['mosquitto_passwd (file banner)'] });
  assert.equal(siteFilterAgentNeed(c, 'db', { banner: 'u:{{ var.greeting | mosquitto_passwd }}' }), null, 'db does not mount banner');
  // container env + preset strings
  const app = c.sites.find((s) => s.siteSlug === 'app')!;
  app.config.containers[0]!.env.push({ name: 'H', value: '{{ var.greeting | sha256 }}', secretRef: null } as never);
  assert.deepEqual(siteFilterAgentNeed(c, 'app')?.uses, ['sha256 (container hello-api)']);
});
