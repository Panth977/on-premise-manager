/**
 * Prints { configs, calls } for branch_test.go: every call = { fn, args, out | err } of shared/src/branch.ts, diff.ts and
 * template-sync.ts. Args are { $c: i } (index into configs) or { $u: 1 } (undefined) or plain JSON. Run with tsx from backend/.
 */
import { readFileSync } from 'node:fs';
import * as B from '../../../../../shared/src/branch.ts';
import * as D from '../../../../../shared/src/diff.ts';
import * as S from '../../../../../shared/src/template-sync.ts';

type Any = any;
const dir = new URL('../../../../../shared/testdata/', import.meta.url);
const V = JSON.parse(readFileSync(new URL('branch-vectors.json', dir), 'utf8'));
const hello = JSON.parse(readFileSync(new URL('hello-config.json', dir), 'utf8')).config;
const copy = <T,>(v: T): T => JSON.parse(JSON.stringify(v));

// ── configs
const configs: Any[] = [];
const add = (c: Any) => (configs.push(c), configs.length - 1);
const mut = (fn: (c: Any) => void, from: Any = hello) => {
  const c = copy(from);
  fn(c);
  return add(c);
};
const reorder = (o: Any): Any => Object.fromEntries(Object.entries(o).reverse());
const H = add(copy(hello));
const E = add({ variables: [], secrets: [], files: [], sites: [] });
const site = (c: Any, i = 0) => c.sites[i];
const cont = (c: Any, s = 0, i = 0) => site(c, s).config.containers[i];
const tree = (entries: Any[], dirs: string[]) => ({ kind: 'tree', entries, dirs });
const ent = (path: string, sha: string, extra: Any = {}) => ({ path, sha256: 'sha256:' + sha, size: 3, mode: '0644', render: 'none', ...extra });

const hand = [
  H,
  E,
  mut((c) => { // unknown fields + reordered keys everywhere
    c.zeta = { b: 1, a: [2, { y: 1, x: 2 }], 10: 'n', 2: 'm' };
    c.variables = c.variables.map((v: Any) => reorder({ ...v, unknownV: { q: 1 } }));
    c.secrets = c.secrets.map((s: Any) => reorder({ ...s, unknownS: true }));
    c.files = c.files.map((f: Any) => reorder({ ...f, unknownF: null }));
    c.sites = c.sites.map((s: Any) => reorder({ ...s, unknownSite: 1, config: reorder({ ...s.config, unknownCfg: 'é😀', edge: reorder({ ...s.config.edge, unknownEdge: 1 }), containers: s.config.containers.map((x: Any) => reorder({ ...x, unknownC: [1] })) }) }));
  }),
  mut((c) => { c.variables[0].value = 'blue'; c.variables[1].label = 'Hi'; c.variables[2].type = 'string'; c.variables.push({ variableSlug: 'new-var', label: 'N', description: '', type: 'string', value: 'x {{ secret.api-token }}', required: true, options: null, validation: null }); }),
  mut((c) => { c.variables.shift(); c.variables[0].value = ''; c.variables[0].required = true; c.variables[1].value = null; c.variables.reverse(); }),
  mut((c) => { c.secrets[0].value = 'changed'; c.secrets[0].rotatedAt = '2026-10-08T00:00:00.000Z'; c.secrets.push({ secretSlug: 'nogen', label: 'NoGen', description: 'd', required: true, generate: null, value: null, rotatedAt: null }, { secretSlug: 'typed', label: 'T', description: '', required: false, value: 'plain', rotatedAt: '2026-01-01T00:00:00.000Z' }); }),
  mut((c) => { c.secrets = c.secrets.map((s: Any) => ({ secretSlug: s.secretSlug, label: s.label + '!', description: s.description, required: s.required, generate: s.generate, isSet: true, value: '••••', rotatedAt: s.rotatedAt })); }),
  mut((c) => { c.secrets = c.secrets.map((s: Any) => ({ ...s, value: 'sha256:abc' })); c.secrets.push({ secretSlug: 'nogen', label: 'NoGen', description: 'd', required: true, value: 'sha256:def', isSet: false }); }),
  mut((c) => { c.secrets.forEach((s: Any) => { s.value = null; s.generate = null; }); }),
  mut((c) => { c.files[0].content.sha256 = 'sha256:ffff'; c.files.push({ fileSlug: 'nf', label: 'L', description: '', mime: 'text/plain', editor: 'text', maxBytes: 1, content: null }); c.files[0].label = 'X'; }),
  mut((c) => { c.files[0].content = { ...c.files[0].content, sha256: 'sha256:eeee' }; }),
  mut((c) => { c.sites.push({ ...copy(site(c)), siteSlug: 'extra', label: 'Extra', config: { ...copy(site(c).config), host: '{{ var.app-host }}', volumes: [{ volumeSlug: 'vx', seed: tree([ent('a.txt', 'aa')], ['d']) }], devTools: [{ presetSlug: 'p', auth: { password: { kind: 'literal', value: 'pw' } }, target: '{{ secret.api-token }}' }] } }); }),
  mut((c) => { c.sites.pop(); }),
  mut((c) => { c.sites.reverse(); site(c).label = 'Renamed'; site(c).config.host = '{{ var.greeting }}.x'; site(c).config.agentPort = 9999; site(c).config.notFoundPageId = 'nf'; site(c).config.depends = [{ siteSlug: 'x' }]; }),
  mut((c) => {
    const x = cont(c);
    x.version = '9.9.9'; x.enabled = false; x.command = ['sh', '-c', 'echo {{ var.theme }} {{ file.banner }}']; x.entrypoint = '{{ secret.api-token. }}'; x.restart = 'always';
    x.resources = { b: 1, a: 2 }; x.healthcheck = null; x.ports = [{ a: 1 }];
    x.depends = [{ containerSlug: 'other' }, { containerSlug: 'gone' }];
    x.volumes = [{ source: { kind: 'volume', volumeSlug: 'vol-t' }, mountPath: '/a/{{ var.new-var }}' }, { source: { kind: 'secret', secretSlug: 'nogen' }, mountPath: '/s' }, { source: { kind: 'file', fileSlug: 'nf' }, mountPath: '/f' }, { mountPath: '{{ var.skip }}' }];
    x.env = [{ name: 'NEW', value: 'v {{ var.theme }}', secretRef: null }, { name: 'SEC', value: null, secretRef: 'nogen' }, { name: 'NUL', value: null, secretRef: null }];
  }),
  mut((c) => {
    const x = cont(c);
    x.env = [{ name: 'SEC', value: 'plain', secretRef: null }, { name: 'NUL', value: 'now', secretRef: null }, { name: 'ONLY', value: '1', secretRef: null }];
    delete x.command; delete x.healthcheck;
    site(c).config.containers.push({ ...copy(x), containerSlug: 'added-c', env: [{ name: 'A', value: '{{ secret.typed }}', secretRef: null }, { name: 'B', value: null, secretRef: 'nogen' }], volumes: [{ source: { kind: 'volume', volumeSlug: 'vol-new' }, mountPath: '/x' }], depends: [{ containerSlug: cont(c).containerSlug }] });
  }),
  mut((c) => { site(c).config.containers.reverse(); site(c).config.containers.shift(); }),
  mut((c) => {
    const e = site(c).config.edge;
    e.httpPort = 81; e.httpsPort = 444; e.maxBodyBytes = 5;
    e.hosts = [...e.hosts.slice(1), { hostname: 'h.example', tls: { certFileSlug: 'nf', keyFileSlug: 'banner', keySecretSlug: 'nogen' } }, { hostname: 'plain.example' }];
    e.routes = [{ routeId: 'rnew', hosts: ['h.example', 'plain.example'], action: { type: 'proxy', containerSlug: 'added-c' } }, { routeId: 'rstatic', action: { type: 'static' } }, ...e.routes.slice(1)];
  }),
  mut((c) => { site(c).config.volumes = [{ volumeSlug: 'vol-t', label: 'T', seed: tree([ent('b.txt', '01'), ent('a.txt', '02')], ['z', 'y']) }, { volumeSlug: 'vol-s', seed: { kind: 'single', sha256: 'sha256:1', size: 1, mode: '0644', render: 'none' } }, { volumeSlug: 'vol-p', persist: true, seed: null }, { volumeSlug: 'vol-q', persist: false, seed: null }, { volumeSlug: 'vol-k', kind: 'file', seed: { kind: 'url', url: 'x' } }]; }),
  mut((c) => { site(c).config.volumes = [{ volumeSlug: 'vol-p', persist: true, seed: tree([ent('p.txt', '0a'), ent('😀.txt', '0b'), ent('￮.txt', '0c')], ['pd']) }, { volumeSlug: 'vol-s', seed: { kind: 'single', sha256: 'sha256:2', size: 2, mode: '0600', render: 'tmpl', extra: 1 } }, { volumeSlug: 'vol-t', label: 'T2', extra: 'x', seed: tree([ent('a.txt', '02', { size: 9 }), ent('c.txt', '03'), { path: 'nofields' }], ['y', 'x']) }, { volumeSlug: 'vol-q', persist: false, seed: tree([], []) }, { volumeSlug: 'vol-k', seed: tree([], []) }]; }),
  mut((c) => { site(c).config.volumes = [{ volumeSlug: 'vol-t', seed: { kind: 'single', sha256: 'sha256:3', size: 3 } }, { volumeSlug: 'vol-s', seed: tree([ent('s', '09')], []) }, { volumeSlug: 'vol-p', persist: true, seed: null, x: 1 }]; }),
  mut((c) => { site(c).config.devTools = [{ presetSlug: 'db', kind: 'x', auth: { password: { kind: 'literal', value: 'one' }, token: { kind: 'expr', value: '{{ secret.api-token }}' } } }, { presetSlug: 'tok', auth: { token: { kind: 'literal', value: 't1' } } }, { presetSlug: 'plain', auth: null }]; }),
  mut((c) => { site(c).config.devTools = [{ presetSlug: 'tok', auth: { token: { kind: 'literal', value: 't2' } } }, { presetSlug: 'db', kind: 'x', auth: { password: { kind: 'literal', value: 'two' }, token: { kind: 'expr', value: '{{ secret.api-token }}' } } }, { presetSlug: 'n', auth: { password: { kind: 'literal', value: 'nn' }, a: 1 } }]; }),
  mut((c) => { site(c).config.devTools = []; }),
];
for (const c of V.cases) for (const k of ['base', 'source', 'target']) if (c[k]) hand.push(add(c[k]));
for (const h of V.hashes) hand.push(add(h.config));

// ── fuzz: seeded mutations (reorder, delete optional fields, change values, drop list items)
let seed = 12345;
const rnd = (n: number) => ((seed = (seed * 1103515245 + 12345) & 0x7fffffff), (seed >>> 8) % n);
const pick = <T,>(l: T[]): T => l[rnd(l.length)];
const IDS = new Set(['variableSlug', 'secretSlug', 'fileSlug', 'siteSlug', 'containerSlug', 'volumeSlug', 'presetSlug', 'hostname', 'routeId', 'name', 'path', 'config', 'edge', 'hosts', 'routes', 'containers', 'env', 'variables', 'secrets', 'files', 'sites', 'kind']);
const walk = (v: Any, out: Any[]) => {
  if (Array.isArray(v)) {
    out.push(v);
    v.forEach((x) => walk(x, out));
  } else if (v && typeof v === 'object') {
    out.push(v);
    Object.values(v).forEach((x) => walk(x, out));
  }
  return out;
};
const fuzzed: number[] = [];
for (let i = 0; i < 45; i++) {
  const from = configs[pick(hand)];
  fuzzed.push(
    mut((c) => {
      for (let n = rnd(6) + 1; n > 0; n--) {
        const t = pick(walk(c, []));
        if (Array.isArray(t)) {
          if (t.length > 1 && rnd(2)) t.splice(rnd(t.length), 1);
          else t.reverse();
          continue;
        }
        const keys = Object.keys(t).filter((k) => !IDS.has(k));
        const k = keys.length ? pick(keys) : null;
        switch (rnd(5)) {
          case 0:
            if (k) delete t[k];
            break;
          case 1:
            if (k) t[k] = pick<Any>(['s', 7, true, false, null, '', 0, { z: 1, a: 2 }, [1]]);
            break;
          case 2:
            t['u' + rnd(9)] = rnd(100);
            break;
          case 3: {
            const r = reorder(t);
            for (const x of Object.keys(t)) delete t[x];
            Object.assign(t, r);
            break;
          }
          default:
            if (k && typeof t[k] === 'string') t[k] += '{{ var.theme }}';
        }
      }
    }, from),
  );
}

// ── calls
const calls: Any[] = [];
let dropped = 0;
class C {
  constructor(public i: number | null) {}
}
const cfg = (i: number | null) => new C(i);
const unwrap = (a: Any) => (a instanceof C ? (a.i === null ? null : copy(configs[a.i])) : a === undefined ? undefined : copy(a));
const wire = (a: Any) => (a instanceof C ? (a.i === null ? null : { $c: a.i }) : a === undefined ? { $u: 1 } : a);
function call(fn: string, f: (...x: Any[]) => unknown, ...args: Any[]) {
  try {
    const out = f(...args.map(unwrap));
    calls.push({ fn, args: args.map(wire), out: out === undefined ? null : JSON.parse(JSON.stringify(out)) });
  } catch (e) {
    if (e instanceof TypeError) return void dropped++; // malformed fuzz input: documented mismatch (Go does not throw TypeErrors)
    calls.push({ fn, args: args.map(wire), err: (e as Error).message });
  }
}
const Topts: Any[] = [{}, { secretValues: true }, { scope: 'app' }, { scope: 'db', secretValues: true }, { preselectFastForward: false }, { preselectAll: true }];
const slugs = ['api-token', 'nogen'];
function pair(bi: number | null, si: number, ti: number, oi: number) {
  const [b, s, t] = [cfg(bi), cfg(si), cfg(ti)];
  const o = Topts[oi % Topts.length];
  const u = (x: number | null) => (x === null ? null : configs[x]);
  let items: Any[] = [];
  try {
    items = B.planMerge(u(bi), u(si), u(ti), o);
  } catch {
    return;
  }
  call('planMerge', B.planMerge, b, s, t, o);
  const allSource = items.map((i) => ({ key: i.key, take: 'source' }));
  const mixed = items.map((i, n) => (n % 4 === 0 ? { key: i.key, take: 'target' } : n % 4 === 1 ? { key: i.key, take: 'custom', value: pick<Any>(['cv', 3, null, true]) } : n % 4 === 2 ? { key: i.key, take: 'bad' } : { key: i.key, take: 'source' })).concat([{ key: 'no/such', take: 'source' }, { key: items[0]?.key ?? 'x', take: 'source' }, { key: 'nv', take: 'custom' } as Any]);
  const values = { theme: 'red', nosuch: 1, greeting: { a: 1 } };
  call('applyMerge', B.applyMerge, b, s, t, allSource, o);
  call('applyMerge', B.applyMerge, b, s, t, mixed, { ...o, values });
  call('applyMerge', B.applyMerge, b, s, t, [], o);
  call('applyMerge', B.applyMerge, b, s, t, B.cleanSourceChoices(items), o);
  call('rebaseChanges', B.rebaseChanges, b, s, t, mixed.slice(0, 3), o);
  call('rebaseChanges', B.rebaseChanges, b, s, t, [], o);
  const skips = B.nextSkips(items, [], 3, 'now', 'me');
  call('nextSkips', B.nextSkips, items, skips.slice(0, 1).map((x) => x.key).map((k) => ({ key: k, theirsHash: skips[0].theirsHash, sourceSeq: 1, skippedAt: 'then', skippedBy: 'x' })), 3, 'now', 'me');
  call('planMerge', B.planMerge, b, s, t, { ...o, skipped: skips });
  call('planMerge', B.planMerge, b, t, s, { ...o, skipped: skips.slice(0, 2) });
  call('applyMerge', B.applyMerge, b, s, t, [], { ...o, skipped: skips, preselectAll: true });
  call('isFastForward', B.isFastForward, items);
  call('countMerge', B.countMerge, items);
  call('cleanSourceChoices', B.cleanSourceChoices, items);
  call('removalChoices', B.removalChoices, items);
  call('suggestMessage', B.suggestMessage, items, undefined);
  call('suggestMessage', B.suggestMessage, items, 'pre: ');
  call('uncommittedItems', B.uncommittedItems, s, t, o);
  call('uncommittedItems', B.uncommittedItems, cfg(null), t, o);
  call('uncommittedCount', B.uncommittedCount, s, t, o);
  call('commitStats', B.commitStats, s, t, o);
  call('commitStats', B.commitStats, cfg(null), t, o);
  call('revertPlan', B.revertPlan, s, b, t, o);
  call('applyRevert', B.applyRevert, s, b, t, mixed.slice(0, 2), o);
  call('resetPlan', B.resetPlan, s, t, o);
  call('mergeMissing', B.mergeMissing, s);
  call('secretValueChanges', B.secretValueChanges, u(si).secrets, u(ti).secrets, []);
  call('secretValueChanges', B.secretValueChanges, u(si).secrets, u(ti).secrets, ['api-token', 'typed']);
  call('withReferenceSites', B.withReferenceSites, s, t, 'app');
  call('withReferenceSites', B.withReferenceSites, s, t, 'nosuch');
  call('diffConfigs', D.diffConfigs, b, t, undefined, undefined);
  call('diffConfigs', D.diffConfigs, s, t, undefined, undefined);
  call('diffConfigsFns', (a: Any, c: Any, l: string[]) => D.diffConfigs(a, c, (x) => `svc-${x}`, (x) => l.includes(x)), s, t, slugs);
  let plan: Any[] = [];
  try {
    plan = S.planTemplateSync(u(bi), u(si), u(ti));
  } catch {
    return;
  }
  call('planTemplateSync', S.planTemplateSync, b, s, t, []);
  const syncSkips = S.nextSkipRecords(plan, [], 4, 'now', 'uid');
  call('planTemplateSync', S.planTemplateSync, b, s, t, syncSkips);
  call('planTemplateSync', S.planTemplateSync, s, b ?? s, t, syncSkips.slice(0, 3));
  call('countTemplateSync', S.countTemplateSync, plan);
  call('cleanTemplateChoices', S.cleanTemplateChoices, plan);
  call('nextSkipRecords', S.nextSkipRecords, plan, syncSkips.slice(0, 2).map((x, n) => (n ? { ...x, theirsHash: 'old' } : x)), 5, 'later', 'u2');
  const smix = plan.map((c, n) => (n % 4 === 0 ? { key: c.key, take: 'ours' } : n % 4 === 1 ? { key: c.key, take: 'custom', value: pick<Any>(['cv', 3, null, false]) } : n % 4 === 2 ? { key: c.key, take: 'nah' } : { key: c.key, take: 'theirs' })).concat([{ key: 'no/such', take: 'theirs' }, { key: plan[0]?.key ?? 'x', take: 'theirs' }, null as Any, { key: 1 } as Any]);
  call('applyTemplateSync', S.applyTemplateSync, b, s, t, plan.map((c) => ({ key: c.key, take: 'theirs' })), {});
  call('applyTemplateSync', S.applyTemplateSync, b, s, t, smix, { skipped: syncSkips.slice(0, 4), values });
  call('applyTemplateSync', S.applyTemplateSync, b, s, t, [], {});
  call('templateSyncMissing', S.templateSyncMissing, s);
  call('templateConfig', S.templateConfig, s);
}
// every hand config against hello and its neighbours, 2-way and 3-way
hand.forEach((h, n) => {
  pair(null, h, H, n);
  pair(H, h, H, n + 1);
  pair(H, H, h, n + 2);
  pair(H, h, hand[(n + 1) % hand.length], n + 3);
  pair(hand[(n + 2) % hand.length], h, hand[(n + 5) % hand.length], n + 4);
});
for (const c of V.cases) {
  const i = (x: Any) => (x ? configs.findIndex((y) => JSON.stringify(y) === JSON.stringify(x)) : null);
  call('planMerge', B.planMerge, cfg(i(c.base)), cfg(i(c.source)), cfg(i(c.target)), c.opts ?? {});
  if (c.choices) call('applyMerge', B.applyMerge, cfg(i(c.base)), cfg(i(c.source)), cfg(i(c.target)), c.choices, { ...c.opts, values: c.values });
}
// fuzz triples
for (let i = 0; i < fuzzed.length; i++) {
  const f = fuzzed[i];
  pair(rnd(3) ? pick(fuzzed) : null, f, pick(fuzzed), i);
  pair(H, pick(fuzzed), f, i + 1);
}

for (const h of V.hashes) call('configHash', B.configHash, h.config);
for (const c of V.counts) call('uncommittedCount', B.uncommittedCount, c.head, c.working, c.opts);
call('commitStats', B.commitStats, V.stats.parent, V.stats.config, { secretValues: true });

// small functions
const refs: Any[] = [{ kind: 'template', templateId: 'T1' }, { kind: 'deployment', deploymentId: 'D1' }, { kind: 'agent', deploymentId: 'D1', siteSlug: 'app' }];
for (const r of refs) {
  call('branchKey', B.branchKey, r);
  call('branchKeyDocId', B.branchKeyDocId, B.branchKey(r));
  for (const q of refs) call('sameBranch', B.sameBranch, r, q);
  for (const at of ['head', 'working', { commitId: 'C1' }]) call('pointParam', B.pointParam, { branch: r, at });
}
for (const s of ['template:T1', 'deployment:D1', 'agent:D1/app', '  agent:D1/app\n', 'template:T1/app', 'deployment:D/app', 'agent:D1', 'agent:D1/A', 'agent:D1/a', 'agent:D/ab-c9', 'nope:1', '', 'template:', 'template:a b', 'template:a b', 'template:a\u000bb', 'template:a b', 'template:é😀', 'agent:D/' + 'a'.repeat(40), 'agent:D/' + 'a'.repeat(41), 'template:T1\n', 'template:a/b/c', 'template:x@y'])
  call('parseBranchKey', B.parseBranchKey, s);
for (const s of ['template:T1', 'template:T1@head', 'template:T1@working', 'template:T1@C9', 'agent:D/ab@C@D', '@x', 'x@y', 'deployment:D@', '', 'bad@C'])
  call('parsePointParam', B.parsePointParam, s);
for (const [l, q] of [['L', 3], ['L', null], ['L', undefined], ['L', 0]] as const) {
  call('pointLabel', B.pointLabel, l, q);
  call('createMessage', B.createMessage, l, q);
}
const msgs: Any[] = ['ok', '  trim  ', '', '   ', 'a\r\nb', 'a\nb\nc', '\n\nfirst', 'x'.repeat(200), 'x'.repeat(201), 'x'.repeat(2000), 'x'.repeat(2001), '😀'.repeat(100), '😀'.repeat(101), 'a\u0085', '﻿a﻿', '  a ', 'first\r\r\nsecond', 'a\rb', 'y'.repeat(150) + '\n' + 'z'.repeat(1900), 1, null, undefined, {}];
for (const m of msgs) {
  if (typeof m === 'string') call('commitSummary', B.commitSummary, m);
  call('checkCommitMessage', B.checkCommitMessage, m, undefined);
  call('checkCommitMessage', B.checkCommitMessage, m, 'p.q');
}
for (const s of ['', 'abc', 'é😀']) call('sha256HexOf', B.sha256HexOf, s);
for (const c of hand) {
  call('configHash', B.configHash, cfg(c));
  call('markSecretValues', B.markSecretValues, cfg(c));
}
for (const f of fuzzed) {
  call('configHash', B.configHash, cfg(f));
  call('markSecretValues', B.markSecretValues, cfg(f));
}
call('catalogHashOf', B.catalogHashOf, { b: 1, a: [1, 'x', null, { z: 1.5, y: 1e21 }], 'é': 'é', '😀': 1, '￮': 2 });
for (const v of [null, undefined, '', 'x', 0, 1, 1.5, true, false, 'é😀', [1, 2], { a: 1 }]) call('secretValueMarker', B.secretValueMarker, v);
for (const v of [null, undefined, 'x', 'é😀', 1, 0, -0, 1.5, 1e21, true, [], {}, [1, { b: 1, a: 2 }], { b: 1, a: [null] }, { has: true, v: 1 }, { has: false, v: 1 }, { has: true }, { v: 1 }, { has: 1, v: null }, '\ud800'])
  call('itemHash', B.itemHash, v), call('templateSyncHash', S.templateSyncHash, v);
for (const h of V.itemHashes) call('itemHash', B.itemHash, h.value);
const sl: Any[] = [{ has: false, v: null }, { has: true, v: 1 }, { has: true, v: { a: 1, b: 2 } }, { has: true, v: { b: 2, a: 1 } }, { has: true, v: null }, { has: true, v: 'x' }];
for (const a of sl)
  for (const b of sl)
    for (const c of sl) {
      call('classifySync', S.classifySync, a, b, c);
      call('classifyItem', B.classifyItem, a, b, c);
    }
for (const a of sl) for (const b of sl) call('classifyItem', B.classifyItem, null, a, b);
for (const v of ['0.14.0', '0.14.1', 'v0.14.0', '0.13.9', '0.14', '0.14.0-rc1', '0.14.0+b', '1', '0', '1.0.0', '0.15', '0.15.0', ' 0.14.0', '0.14.x', 'x', '', null, undefined, 'v0.9.9', '0.014.000', '00.14.0', '0.14.0.1']) {
  call('supportsBranches', B.supportsBranches, v);
}
call('needsAgentMessage', B.needsAgentMessage, 'app', '0.1.0');
call('needsAgentMessage', B.needsAgentMessage, 'app', null);
const author = { kind: 'person', email: 'a@b', uid: 'u', name: 'N', siteSlug: null };
const inputs: Any[] = [
  { commitId: 'C1', branch: refs[0], parent: null, kind: 'create', message: '  hello\nworld ', author, createdAt: 'T', config: hello },
  { commitId: 'C2', branch: refs[1], parent: { commitId: 'C1', seq: 7, config: configs[3] }, kind: 'change', message: 'm', author, createdAt: 'T', config: hello, catalog: { images: { z: 1, a: 2 } }, source: { label: 'x' }, revertOf: { commitId: 'C', seq: 1 }, resetTo: { commitId: 'D', seq: 2 }, rollbackOf: { snapshotId: 'S' }, data: { b: 1, a: 2 }, scope: 'app' },
  { commitId: 'C3', branch: refs[2], parent: { commitId: 'C1', seq: 1, config: hello }, kind: 'merge', message: 'm', author, createdAt: 'T', config: configs[5], catalog: null, scope: null },
];
for (const i of inputs) {
  call('makeCommit', B.makeCommit, i);
  const c = B.makeCommit(i) as Any;
  call('commitRef', B.commitRef, c);
  call('commitSummaryOf', B.commitSummaryOf, c);
  call('maskCommit', (x: Any) => B.maskCommit(x, (y: Any) => ({ masked: Object.keys(y).length })), { ...c, extra: 1, config: c.config });
  call('verifyCommitLink', B.verifyCommitLink, c, null);
  call('verifyCommitLink', B.verifyCommitLink, c, { commitId: c.parentId, seq: c.seq - 1 });
  call('verifyCommitLink', B.verifyCommitLink, c, { commitId: 'other', seq: c.seq - 1 });
  call('verifyCommitLink', B.verifyCommitLink, c, { commitId: c.parentId, seq: c.seq });
  call('verifyCommitLink', B.verifyCommitLink, { ...c, hash: 'sha256:0' }, null);
  call('verifyCommitLink', B.verifyCommitLink, { ...c, catalog: { x: 1 }, catalogHash: 'sha256:0' }, null);
  call('verifyCommitLink', B.verifyCommitLink, { ...c, catalog: { x: 1 }, catalogHash: null }, null);
}
call('sourcePicks', B.sourcePicks, ['a', 'b'], 'merge');
call('sourcePicks', B.sourcePicks, ['a', 'b'], 'cherry-pick');
call('sourcePicks', B.sourcePicks, Array.from({ length: 501 }, (_, i) => `k${i}`), 'cherry-pick');
call('sourcePicks', B.sourcePicks, [], 'cherry-pick');
call('hasSecretValueChanges', B.hasSecretValueChanges, null);
call('hasSecretValueChanges', B.hasSecretValueChanges, { set: [], generated: [], cleared: [] });
call('hasSecretValueChanges', B.hasSecretValueChanges, { set: [], generated: ['a'], cleared: [] });
call('secretValueChanges', B.secretValueChanges, [{ secretSlug: 'a', value: 'x' }, { secretSlug: 'b', value: 'y' }, { secretSlug: 'c', value: null }], [{ secretSlug: 'z', value: 'n' }, { secretSlug: 'a', value: '' }, { secretSlug: 'b', value: 'y' }, { secretSlug: 'c', value: 'g' }, { secretSlug: 'd', value: 1 }, { secretSlug: '😀', value: 'q' }, { secretSlug: '￮', value: 'q' }], ['c']);
call('suggestMessage', B.suggestMessage, Array.from({ length: 5 }, (_, i) => ({ label: `Variable v${i}` + 'é😀'.repeat(60), effect: ['change', 'add', 'remove'][i % 3] })), 'p ');
call('suggestMessage', B.suggestMessage, [], '  x ');
call('suggestMessage', B.suggestMessage, [{ label: 'Secret s', effect: 'add' }, { label: 'File f', effect: 'add' }, { label: 'Other Variable x', effect: 'add' }], undefined);
for (const p of [null, { auth: null }, { auth: { password: { kind: 'expr' } } }, { a: 1, auth: { token: { kind: 'literal', value: 'x' }, z: 1 } }, { auth: { z: 1, password: { kind: 'literal', value: 'x' }, token: null }, y: 2 }, { auth: { token: { kind: 'literal' }, password: { kind: 'env', n: 1 } } }, 'str', 5])
  call('maskDevToolLiterals', D.maskDevToolLiterals, p);
call('constants', () => ({ a: [B.BRANCHES_MIN_AGENT, B.COMMIT_MESSAGE_MAX, B.COMMIT_SUMMARY_MAX, B.COMMIT_PICKS_MAX, B.COMMIT_STATS_KEYS_MAX, B.CLOUD_RECENT_COMMITS], k: [B.COMMIT_KINDS, B.MERGE_KINDS, B.SOURCE_CHANGE_KINDS, B.SITE_SETTINGS, B.EDGE_FIELDS, B.CONTAINER_FIELDS, S.SYNC_TAKEABLE], n: [B.CONTENT_NOTE_STARTER, B.CONTENT_NOTE_PERSIST, D.DEVTOOL_VALUE_CHANGED_NOTE, D.DEVTOOL_VALUE_MASK, D.VOLUME_CONTENT_CHANGED_NOTE, D.VOLUME_SEED_CHANGED_NOTE, D.VOLUME_KIND_CHANGED_NOTE] }));
call('configHashInf', () => B.configHash({ variables: [{ n: Infinity }] } as Any));
call('makeCommitInf', () => B.makeCommit({ commitId: 'C', branch: refs[0], parent: null, kind: 'create', message: 'm', author, createdAt: 'T', config: { variables: [{ n: -Infinity }], secrets: [], files: [], sites: [] } } as Any));

process.stderr.write(`dropped ${dropped} TypeError calls\n`);
process.stdout.write(JSON.stringify({ configs, calls }));
