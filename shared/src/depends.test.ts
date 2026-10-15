import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dnsFailureHint, dnsFailureMessage, inferDepends, literalHostMentions, startOrder, waitDepends } from './depends.js';
import { validateConfig, type ValidationContext } from './validate.js';
import type { Config } from './types.js';

// shared/testdata/depends-vectors.json (§21 #93) — the Go mirror reads the same file (agent/internal/validate/depends_test.go).
const load = (f: string) => JSON.parse(readFileSync(fileURLToPath(new URL(`../testdata/${f}`, import.meta.url)), 'utf8'));
const V = load('depends-vectors.json');

test('§21 #93 literalHostMentions vectors', () => {
  for (const c of V.literal) assert.deepEqual(literalHostMentions(c.text, c.slugs), c.expect, JSON.stringify(c.text));
});

test('§21 #93 inferDepends + startOrder vectors', () => {
  for (const c of V.infer) {
    const inf = inferDepends({ site: c.site, images: c.images, files: c.files, fileContents: c.fileContents });
    const got = Object.fromEntries(inf.map((d) => [d.containerSlug, { explicit: d.explicit, implicit: d.implicit, suggested: d.suggested }]));
    assert.deepEqual(got, c.expect, c.name);
    const so = startOrder(c.site, inf);
    assert.deepEqual(so.order, c.order, c.name);
    assert.deepEqual(so.cycle, c.cycle, c.name);
  }
});

test('§21 #93 waitDepends = explicit (enabled) + implicit', () => {
  const inf = inferDepends({ site: V.infer[2].site });
  const app = inf.find((d) => d.containerSlug === 'app')!;
  assert.deepEqual(waitDepends(app, ['app', 'db', 'cache']), ['db']);
  const inf2 = inferDepends({ site: V.infer[1].site, images: V.infer[1].images, files: V.infer[1].files, fileContents: V.infer[1].fileContents });
  assert.deepEqual(waitDepends(inf2.find((d) => d.containerSlug === 'api')!, ['api', 'web', 'worker', 'mqtt', 'db']), ['mqtt']);
});

test('§21 #93 dnsFailureHint vectors', () => {
  for (const c of V.dns) {
    const h = dnsFailureHint(c.lines, c.slugs, c.subject);
    assert.deepEqual(h, c.expect, c.name);
    assert.equal(h ? dnsFailureMessage(c.subject, h) : null, c.message, c.name);
  }
});

test('§21 #93 validateConfig: V_DEPENDS_SUGGESTED (info) + V_START_ORDER_CYCLE (warning)', () => {
  const HELLO = load('hello-config.json') as { deploymentId: string; config: Config };
  const CAT = load('hello-catalog.json') as Omit<ValidationContext, 'mode' | 'deploymentId'>;
  const ctx: ValidationContext = { mode: 'draft', deploymentId: HELLO.deploymentId, ...CAT };
  const cfg = structuredClone(HELLO.config);
  const site = cfg.sites.find((s) => s.config.containers.length >= 1)!;
  const [first] = site.config.containers;
  const others = cfg.sites.flatMap((s) => s.config.containers).filter((c) => c !== first);
  // add a second container of the same image on the same site that mentions the first one by host:port
  const twin = structuredClone(first!);
  twin.containerSlug = `${first!.containerSlug}-2`;
  twin.ports = [];
  twin.depends = [];
  twin.env = [...twin.env.filter((e) => e.name !== 'UPSTREAM_URL'), { name: 'UPSTREAM_URL', value: `http://${first!.containerSlug}:8080`, secretRef: null }];
  site.config.containers.push(twin);
  void others;
  const is = validateConfig(cfg, ctx).list;
  const sug = is.filter((x) => x.code === 'V_DEPENDS_SUGGESTED');
  assert.equal(sug.length, 1, JSON.stringify(is));
  assert.equal(sug[0]!.severity, 'info');
  assert.match(sug[0]!.message, new RegExp(`${twin.containerSlug} mentions \`${first!.containerSlug}\` \\(env UPSTREAM_URL\\) — add it to Depends on\\?`));
  // a cycle through references → warning, never an error
  first!.env = [...first!.env, { name: 'PEER_URL', value: `http://{{ containers.${twin.containerSlug}.host }}`, secretRef: null }];
  const is2 = validateConfig(cfg, ctx).list;
  const cyc = is2.filter((x) => x.code === 'V_START_ORDER_CYCLE');
  assert.equal(cyc.length, 1, JSON.stringify(is2.filter((x) => x.severity !== 'info')));
  assert.equal(cyc[0]!.severity, 'warning');
});
