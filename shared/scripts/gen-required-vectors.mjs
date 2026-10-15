// Generates testdata/required-vars-vectors.json (round E, user feedback on template "Single": required variables are
// filled at deployment, so a template must not warn about them). Base = hello-config with a variable server-ip used by
// the app site host and by an env SERVER_URL of hello-api + hello-web, and a required secret used by an env.
// The `expect` lists are the issues whose code is in CODES (path, code, severity), in validateConfig order.
import { readFileSync, writeFileSync } from 'node:fs';
import assert from 'node:assert/strict';
import { validateConfig } from '../dist/validate.js';

const load = (f) => JSON.parse(readFileSync(new URL(`../testdata/${f}`, import.meta.url), 'utf8'));
const HELLO = load('hello-config.json');
const CAT = load('hello-catalog.json');
const CODES = ['V_VAR_REQUIRED', 'V_VAR_OPTIONAL_NULL', 'V_SECRET_REQUIRED', 'V_HOST_AUTO', 'V_HOST', 'E_NULL', 'E_DEFERRED', 'E_TYPE', 'V_OPTION', 'V_PATH'];

function config(required, value, secretValue) {
  const c = structuredClone(HELLO.config);
  c.variables.push({ variableSlug: 'server-ip', label: 'Server IP', description: '', type: 'string', value, required, options: null, validation: null });
  c.secrets.push({ secretSlug: 'jwt-key', label: 'JWT key', description: '', required: true, generate: null, value: secretValue, rotatedAt: null });
  const app = c.sites.find((s) => s.siteSlug === 'app').config;
  app.host = '{{ var.server-ip }}';
  for (const k of app.containers) k.env.push({ name: 'SERVER_URL', value: 'http://{{ var.server-ip | upper }}:8080', secretRef: null });
  app.containers[0].env.push({ name: 'JWT', value: 'Bearer {{ secret.jwt-key }}', secretRef: null });
  return c;
}
const cases = [
  { name: 'template + required null → no issue', mode: 'template', required: true, value: null, secret: null, want: [] },
  { name: 'template + optional null → one info on the variable (+ E_NULL where null is invalid)', mode: 'template', required: false, value: null, secret: null, want: ['V_VAR_OPTIONAL_NULL'] },
  { name: 'draft + required null → one V_VAR_REQUIRED (publish blocker) + V_SECRET_REQUIRED', mode: 'draft', required: true, value: null, secret: null, want: ['V_VAR_REQUIRED', 'V_SECRET_REQUIRED'] },
  { name: 'publish + required null → errors on the variable / secret only', mode: 'publish', required: true, value: null, secret: null, want: ['V_VAR_REQUIRED', 'V_SECRET_REQUIRED'] },
  { name: 'draft + required set → clean', mode: 'draft', required: true, value: '10.0.0.5', secret: 's3cret', want: [] },
  { name: 'draft + optional null → V_HOST_AUTO + null errors at usages', mode: 'draft', required: false, value: null, secret: 's3cret', want: ['V_HOST_AUTO', 'E_TYPE', 'E_TYPE'] },
];
const out = [];
for (const c of cases) {
  const cfg = config(c.required, c.value, c.secret);
  const is = validateConfig(cfg, { mode: c.mode, deploymentId: HELLO.deploymentId, services: CAT.services, images: CAT.images, notFoundPageIds: CAT.notFoundPageIds });
  const expect = is.list.filter((x) => CODES.includes(x.code)).map(({ path, code, severity }) => ({ path, code, severity }));
  const codes = expect.filter((x) => !(c.mode === 'template' && c.required === false && ['E_NULL', 'E_TYPE'].includes(x.code))).map((x) => x.code);
  assert.deepEqual(codes, c.want, `${c.name}: ${JSON.stringify(expect)}`);
  out.push({ name: c.name, mode: c.mode, variable: { required: c.required, value: c.value }, secretValue: c.secret, expect });
}
writeFileSync(
  new URL('../testdata/required-vars-vectors.json', import.meta.url),
  JSON.stringify(
    {
      _doc: 'Round E required-variable rules. Base: hello-config + variable server-ip (case.variable) used by sites[app].config.host and env SERVER_URL of every app container, + required secret jwt-key (case.secretValue) used by env JWT of the first app container (see shared/scripts/gen-required-vectors.mjs). validateConfig(mode) issues filtered to `codes` must equal expect (path, code, severity).',
      codes: CODES,
      cases: out,
    },
    null,
    2,
  ) + '\n',
);
console.log(JSON.stringify(out.map((x) => [x.name, x.expect]), null, 1));
