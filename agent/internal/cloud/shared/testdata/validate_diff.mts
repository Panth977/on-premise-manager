/**
 * Corpus + TS results for the validateConfig differential (validate_config_test.go): every validateConfig call of the
 * shared/src/*.test.ts suites (recorded by running them with the import wrapped), the shared vectors, and seeded mutations.
 * argv[2] = output file.
 */
import { readFileSync, writeFileSync, rmSync } from 'node:fs';
import { pathToFileURL, fileURLToPath } from 'node:url';
import { Script } from 'node:vm';
import * as V from '../../../../../shared/src/validate.ts';
import { mutate } from './mutate.mts';

const SRC = new URL('../../../../../shared/src/', import.meta.url);
const load = (f: string) => JSON.parse(readFileSync(new URL(`../testdata/${f}`, SRC), 'utf8'));
const HELLO = load('hello-config.json');
const CAT = load('hello-catalog.json');
type Rec = { kind: string; config: unknown; ctx: unknown };
const recs: Rec[] = [];
const seen = new Set<string>();
const add = (kind: string, config: unknown, ctx: V.ValidationContext) => {
  const k = JSON.stringify([config, ctx]);
  if (k === undefined || seen.has(k)) return;
  seen.add(k);
  recs.push({ kind, config: JSON.parse(JSON.stringify(config ?? null)), ctx: JSON.parse(JSON.stringify(ctx)) });
};
(globalThis as any).__recordValidate = (c: unknown, x: V.ValidationContext) => add('suite', c, x);

const tmp: string[] = [];
for (const name of ['validate', 'depends', 'devtools-validate', 'required-vars', 'volume-content', 'volume-kind']) {
  const url = new URL(`${name}.test.ts`, SRC);
  let s = readFileSync(url, 'utf8');
  s = s.replaceAll("from './", `from '${new URL('./', SRC).href}`).replaceAll("from '../", `from '${new URL('../', SRC).href}`);
  s = s.replaceAll('import.meta.url', JSON.stringify(url.href));
  s = s.replace(/\bvalidateConfig\b(?=[^;]*from '[^']*validate\.js')/, 'validateConfig as __vc');
  s = `const validateConfig = (c: any, x: any) => { (globalThis as any).__recordValidate(c, x); return __vc(c, x); };\n` + s;
  // imports stay hoisted: the const line above runs first, __vc is a live binding
  const f = new URL(`./.tmp-${name}.test.mts`, import.meta.url);
  writeFileSync(f, s);
  tmp.push(fileURLToPath(f));
}
try {
  for (const f of tmp) await import(pathToFileURL(f).href);
  for (let n = -1; n !== recs.length; ) {
    n = recs.length;
    await new Promise((r) => setTimeout(r, 2000));
  }
} finally {
  for (const f of tmp) rmSync(f, { force: true });
}
const suite = recs.length;

const ctx = (mode: V.ValidateMode, extra: object = {}): V.ValidationContext => ({ mode, deploymentId: HELLO.deploymentId, services: CAT.services, images: CAT.images, notFoundPageIds: CAT.notFoundPageIds, ...extra });
for (const m of ['template', 'draft', 'publish', 'agent'] as const) add('hello', HELLO.config, ctx(m));
add('hello', HELLO.config, ctx('publish', { secretsSet: ['a'] }));
add('hello', HELLO.config, ctx('draft', { detectedHosts: { app: '10.0.0.9', db: 'db.example.com' }, fileContents: {}, blobContents: {} }));
add('hello', HELLO.config, ctx('publish', { notFoundPageIds: undefined }));
for (const f of ['required-vars-vectors.json', 'depends-vectors.json', 'volume-kind-vectors.json', 'devtools-validate-vectors.json']) {
  const walk = (v: any, d = 0) => {
    if (!v || typeof v !== 'object' || d > 6) return;
    if (v.config?.sites && v.config?.variables) add('vector', v.config, ctx('publish'));
    else if (v.sites && v.variables) add('vector', v, ctx('publish'));
    for (const x of Object.values(v)) walk(x, d + 1);
  };
  walk(load(f));
}
const modes = ['publish', 'draft', 'template', 'agent'] as const;
const bases: Rec[] = recs.filter((r) => r.kind !== 'hello').slice(0, 60);
for (let s = 1; s <= 1500; s++) add('typed', mutate(HELLO.config, s, 'typed'), ctx(modes[s % 4]!));
for (let s = 1; s <= 500; s++) {
  const b = bases[s % bases.length];
  if (b) add('typed', mutate(b.config, 10000 + s, 'typed'), b.ctx as V.ValidationContext);
}
for (let s = 1; s <= 1500; s++) add('any', mutate(HELLO.config, 20000 + s, 'any'), ctx(modes[s % 4]!));
for (let s = 1; s <= 1500; s++) {
  const b = bases[s % bases.length];
  if (b) add('any', mutate(b.config, 40000 + s, 'any'), b.ctx as V.ValidationContext);
}
const files = Object.fromEntries((HELLO.config.files as { fileSlug: string }[]).map((f) => [f.fileSlug, '{{ var.greeting | default("x") }} {"a":1} {{ site.db.host }}']));
for (let s = 1; s <= 300; s++) add('typed', mutate(HELLO.config, 50000 + s, 'typed'), ctx(modes[s % 4]!, { fileContents: files, blobContents: {}, detectedHosts: { app: '10.1.1.1' }, secretsSet: ['api-token'] }));
const norm = (c: any) => { try { return V.normalizeConfig(c); } catch { return c; } };
for (let s = 1; s <= 500; s++) add('any', norm(mutate(HELLO.config, 30000 + s, 'any')), ctx(modes[s % 4]!));

const guarded = new Script('__validateNow()');
const out = recs.map((r) => {
  try {
    (globalThis as any).__validateNow = () => V.validateConfig(r.config as never, r.ctx as never).list;
    return { ...r, list: guarded.runInThisContext({ timeout: 1500 }) };
  } catch (e) {
    // validateConfig never returns on some malformed configs (an endless loop in TS)
    if ((e as { code?: string }).code === 'ERR_SCRIPT_EXECUTION_TIMEOUT') return { ...r, hang: true };
    return { ...r, throw: (e as Error).message };
  }
});
writeFileSync(process.argv[2]!, JSON.stringify({ suite, cases: out }));
process.exit(0);
