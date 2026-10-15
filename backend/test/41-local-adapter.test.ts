/**
 * §21 #108 local adapter specifics: file layout, concurrency across deployment files and processes, cross-file
 * transactions, outbox + dispatcher, strict index registry, blob URLs / ranges / lifecycle, TTL sweep, local auth
 * (+ OIDC against a fake provider), backups.
 */
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { createHash, generateKeyPairSync, sign } from 'node:crypto';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { createServer, type Server } from 'node:http';
import { join, resolve } from 'node:path';
import type { Readable } from 'node:stream';
import { DatabaseSync } from 'node:sqlite';
import { after, before, describe, it } from 'node:test';
import { FieldValue, Timestamp, storeErrorCode } from '../functions/src/ports/docstore.js';
import type { DocEvent, DocTrigger } from '../functions/src/ports/events.js';
import { backupSqlite, handleAuthHttp, handleBlobHttp, localPorts, startOutboxDispatcher, sweepDocTtl, type LocalConfig } from '../functions/src/adapters/local/index.js';

const TMP = join(import.meta.dirname, '.tmp');
const ADAPTER = resolve(import.meta.dirname, '../functions/src/adapters/local/index.ts');
mkdirSync(TMP, { recursive: true });
const dirs: string[] = [];
const fresh = () => {
  const d = mkdtempSync(join(TMP, 'local-'));
  dirs.push(d);
  return d;
};
const open = (dataDir: string, o: Partial<LocalConfig> = {}) => localPorts({ dataDir, publicUrl: 'http://opm.test/base', consoleUrl: 'http://console.test', strict: true, ...o });
after(() => {
  for (const d of dirs) rmSync(d, { recursive: true, force: true });
});
async function readAll(s: Readable): Promise<Buffer> {
  const c: Buffer[] = [];
  for await (const x of s) c.push(Buffer.from(x as Buffer));
  return Buffer.concat(c);
}
const bodyOf = async (b: Buffer | string | Readable | null) => (b === null ? '' : typeof b === 'string' ? b : Buffer.isBuffer(b) ? b.toString() : (await readAll(b)).toString());
function child(script: string, args: string[], env: Record<string, string> = {}): Promise<{ code: number; out: string }> {
  return new Promise((res, rej) => {
    const p = spawn(process.execPath, ['--import', 'tsx', script, ...args], { cwd: resolve(import.meta.dirname, '..'), stdio: ['ignore', 'pipe', 'pipe'], env: { ...process.env, ...env } });
    let out = '';
    p.stdout.on('data', (d) => (out += d));
    p.stderr.on('data', (d) => (out += d));
    p.on('error', rej);
    p.on('exit', (code) => res({ code: code ?? -1, out }));
  });
}
/** a tsx script (in the temp dir) that increments counters through its own LocalDocStore */
function childScript(dir: string): string {
  const f = join(dir, 'child.ts');
  writeFileSync(
    f,
    `import { localPorts } from ${JSON.stringify(ADAPTER)};
const [dataDir, n, ...rest] = process.argv.slice(2);
const paths = rest.filter((a) => !a.startsWith('--'));
const p = localPorts({ dataDir, publicUrl: 'http://x', strict: true, ...(process.env.CHILD_TRIGGERS ? { triggers: JSON.parse(process.env.CHILD_TRIGGERS) } : {}) });
await Promise.all(Array.from({ length: Number(n) }, async () => {
  for (const path of paths) {
    await p.store.runTransaction(async (tx) => {
      const ref = p.store.doc(path);
      const s = await tx.get(ref);
      tx.set(ref, { v: ((s.data()?.v as number) ?? 0) + 1 }, { merge: true });
    }, { maxAttempts: 200 });
  }
}));
if (process.argv.includes('--outbox')) await p.store.doc('deployments/Dx/things/fromChild').create({ by: 'child' });
await p.close();
console.log('child done');
`,
  );
  return f;
}

describe('local adapter: layout + concurrency', () => {
  it('one global file + one file per deployment; deployment doc in table "deployment"', async () => {
    const d = fresh();
    const p = open(d);
    await p.store.doc('templates/T1').set({ slug: 't' });
    await p.store.doc('deployments/D1').set({ deleted: false, updatedAt: '2026-01-01' });
    await p.store.doc('deployments/D1/commits/c1').set({ seq: 1 });
    await p.store.doc('deployments/D1/agentBranches/s1/commits/c2').set({ seq: 2 });
    await p.store.doc('githubMirror/config/queue/q1').set({ state: 'pending' });
    await p.close();
    assert.ok(existsSync(join(d, 'db/global.sqlite')));
    assert.deepEqual(readdirSync(join(d, 'db/deployments')).filter((f) => f.endsWith('.sqlite')), ['D1.sqlite']);
    const g = new DatabaseSync(join(d, 'db/global.sqlite'));
    const gt = (g.prepare("SELECT name FROM sqlite_master WHERE type='table' ORDER BY name").all() as { name: string }[]).map((r) => r.name);
    assert.deepEqual(gt.filter((n) => !n.startsWith('_') && !n.startsWith('sqlite')), ['githubMirror__queue', 'templates']);
    const row = g.prepare('SELECT * FROM templates').get() as Record<string, unknown>;
    assert.deepEqual({ id: row.id, parent: row.parent, ix: row.ix_slug }, { id: 'templates/T1', parent: 'templates', ix: 't' });
    g.close();
    const dep = new DatabaseSync(join(d, 'db/deployments/D1.sqlite'));
    const dt = (dep.prepare("SELECT name FROM sqlite_master WHERE type='table' ORDER BY name").all() as { name: string }[]).map((r) => r.name).filter((n) => !n.startsWith('_') && !n.startsWith('sqlite'));
    assert.deepEqual(dt, ['agentBranches__commits', 'commits', 'deployment']);
    assert.equal((dep.prepare('PRAGMA journal_mode').get() as { journal_mode: string }).journal_mode, 'wal');
    const dr = dep.prepare('SELECT id, parent, ix_deleted, ix_updatedAt FROM deployment').get() as Record<string, unknown>;
    assert.deepEqual({ ...dr }, { id: 'deployments/D1', parent: 'deployments', ix_deleted: 0, ix_updatedAt: '2026-01-01' });
    dep.close();
    assert.throws(() => open(d).store.doc('deployments/bad-id/x/y'), /deployment id/);
  });

  it('parallel transactions on 2 deployments + a cross-file one + a second OS process: no lost updates', async () => {
    const d = fresh();
    const p = open(d);
    const paths = ['deployments/DA/counters/c', 'deployments/DB/counters/c', 'counters/g'];
    const script = childScript(d);
    const N = 25;
    const inc = (path: string) =>
      p.store.runTransaction(
        async (tx) => {
          const ref = p.store.doc(path);
          const s = await tx.get(ref);
          tx.set(ref, { v: ((s.data()?.v as number) ?? 0) + 1 }, { merge: true });
        },
        { maxAttempts: 200 },
      );
    // cross-file: reads + writes DA and global in one transaction
    const cross = () =>
      p.store.runTransaction(
        async (tx) => {
          const a = p.store.doc('deployments/DA/counters/x');
          const g = p.store.doc('counters/x');
          const [sa, sg] = await tx.getAll(a, g);
          tx.set(a, { v: ((sa!.data()?.v as number) ?? 0) + 1 });
          tx.set(g, { v: ((sg!.data()?.v as number) ?? 0) + 1 });
        },
        { maxAttempts: 200 },
      );
    const [c] = await Promise.all([child(script, [d, String(N), ...paths]), ...Array.from({ length: N }, () => Promise.all([inc(paths[0]!), inc(paths[1]!), inc(paths[2]!), cross()]))]);
    assert.equal(c.code, 0, c.out);
    for (const path of paths) assert.equal((await p.store.doc(path).get()).data()!.v, 2 * N, path);
    assert.equal((await p.store.doc('deployments/DA/counters/x').get()).data()!.v, N);
    assert.equal((await p.store.doc('counters/x').get()).data()!.v, N);
    await p.close();
  });

  it('a transaction over the whole deployments collection is serializable (slug uniqueness)', async () => {
    const p = open(fresh());
    const claim = (id: string) =>
      p.store.runTransaction(async (tx) => {
        const taken = await tx.get(p.store.collection('deployments').where('slug', '==', 'same'));
        if (!taken.empty) return false;
        tx.set(p.store.doc(`deployments/${id}`), { slug: 'same', deleted: false });
        return true;
      });
    const r = await Promise.all(['N1', 'N2', 'N3', 'N4'].map(claim));
    assert.equal(r.filter(Boolean).length, 1, 'exactly one deployment got the slug');
    assert.equal((await p.store.collection('deployments').where('slug', '==', 'same').get()).size, 1);
    await p.close();
  });

  it('cross-file transaction: all-or-nothing on errors, traced with OPM_LOCAL_TRACE_XTX=1', async () => {
    const p = open(fresh());
    await p.store.doc('counters/exists').set({ v: 1 });
    await assert.rejects(
      p.store.runTransaction(async (tx) => {
        tx.set(p.store.doc('deployments/DX/counters/a'), { v: 1 });
        tx.create(p.store.doc('counters/exists'), { v: 2 });
      }),
      (e) => storeErrorCode(e) === 'already-exists',
    );
    assert.equal((await p.store.doc('deployments/DX/counters/a').get()).exists, false, 'deployment part rolled back');
    const lines: string[] = [];
    const w = process.stdout.write.bind(process.stdout);
    process.env.OPM_LOCAL_TRACE_XTX = '1';
    const p2 = open(p.blobs.dataDir);
    process.stdout.write = ((s: string) => (lines.push(String(s)), true)) as typeof process.stdout.write;
    try {
      const b = p2.store.batch();
      b.set(p2.store.doc('deployments/DX/counters/a'), { v: 1 }).set(p2.store.doc('counters/b'), { v: 1 });
      await b.commit();
    } finally {
      process.stdout.write = w;
      delete process.env.OPM_LOCAL_TRACE_XTX;
    }
    const tr = lines.map((l) => JSON.parse(l)).find((l) => l.message === 'local cross-db tx');
    assert.deepEqual(tr?.files, ['DX', '@global'], 'deployment files first, global last');
    assert.equal((await p.store.doc('counters/b').get()).data()!.v, 1);
    await p.close();
    await p2.close();
  });

  it('strict mode throws on an unindexed field; non-strict falls back to json_extract', async () => {
    const d = fresh();
    const p = open(d);
    await p.store.doc('templates/a').set({ label: 'x', slug: 's', ts: Timestamp.fromMillis(5000), flag: true });
    await assert.rejects(p.store.collection('templates').where('label', '==', 'x').get(), /strict/);
    await assert.rejects(p.store.collection('templates').orderBy('label').get(), /strict/);
    await assert.rejects(p.store.collection('templates').where('tags', 'array-contains', 'x').get(), /strict/);
    assert.equal((await p.store.collection('templates').where('slug', '==', 's').get()).size, 1);
    const loose = open(d, { strict: false });
    assert.equal((await loose.store.collection('templates').where('label', '==', 'x').get()).size, 1);
    assert.equal((await loose.store.collection('templates').where('ts', '<', Timestamp.fromMillis(6000)).get()).size, 1);
    assert.equal((await loose.store.collection('templates').where('flag', '==', true).get()).size, 1);
    await p.close();
    await loose.close();
  });

  it('a grown registry adds + backfills the index column on the next open', async () => {
    const d = fresh();
    const p = open(d);
    await p.store.doc('templates/a').set({ label: 'L1' });
    await p.store.doc('templates/b').set({ label: 'L2' });
    await p.close();
    const p2 = open(d, { indexes: { templates: { scalar: ['label'] } } });
    assert.deepEqual(
      (await p2.store.collection('templates').where('label', '==', 'L2').get()).docs.map((x) => x.id),
      ['b'],
    );
    const g = new DatabaseSync(join(d, 'db/global.sqlite'));
    assert.deepEqual(
      (g.prepare('SELECT ix_label FROM templates ORDER BY id').all() as { ix_label: string }[]).map((r) => r.ix_label),
      ['L1', 'L2'],
    );
    g.close();
    await p2.close();
  });
});

describe('local adapter: outbox', () => {
  const triggers = [
    { name: 'tCreated', kind: 'created' as const, pattern: 'deployments/{d}/things/{t}' },
    { name: 'tWritten', kind: 'written' as const, pattern: 'gthings/{t}' },
  ];
  const rows = (file: string) => {
    const db = new DatabaseSync(file);
    try {
      return (db.prepare('SELECT trigger, path, before IS NOT NULL AS b, after IS NOT NULL AS a FROM _outbox ORDER BY seq').all() as { trigger: string; path: string; b: number; a: number }[]).map((r) => ({ ...r }));
    } finally {
      db.close();
    }
  };
  it('created vs written rows, in the same file + transaction as the write', async () => {
    const d = fresh();
    const p = open(d, { triggers });
    await p.store.doc('deployments/D1/things/a').set({ v: 1 });
    await p.store.doc('deployments/D1/things/a').update({ v: 2 });
    await p.store.doc('deployments/D1/other/a').set({ v: 1 });
    await p.store.doc('gthings/x').set({ v: 1 });
    await p.store.doc('gthings/x').set({ v: 1 }); // no-op: no row
    await p.store.doc('gthings/x').update({ v: FieldValue.increment(1) });
    await p.store.doc('gthings/x').delete();
    await assert.rejects(
      p.store.runTransaction(async (tx) => {
        tx.create(p.store.doc('deployments/D1/things/b'), { v: 1 });
        tx.set(p.store.doc('gthings/y'), { v: 1 });
        tx.update(p.store.doc('gthings/missing'), { v: 1 });
      }),
    );
    assert.deepEqual(rows(join(d, 'db/deployments/D1.sqlite')), [{ trigger: 'tCreated', path: 'deployments/D1/things/a', b: 0, a: 1 }]);
    assert.deepEqual(rows(join(d, 'db/global.sqlite')), [
      { trigger: 'tWritten', path: 'gthings/x', b: 0, a: 1 },
      { trigger: 'tWritten', path: 'gthings/x', b: 1, a: 1 },
      { trigger: 'tWritten', path: 'gthings/x', b: 1, a: 0 },
    ]);
    await p.close();
  });
  it('the dispatcher delivers every row once (incl. rows written by another process); idle()', async () => {
    const d = fresh();
    const p = open(d, { triggers });
    const seen: DocEvent[] = [];
    const mk = (t: (typeof triggers)[number], fail = false): DocTrigger => ({
      ...t,
      timeoutSeconds: 60,
      memory: '256MiB',
      handler: async (e) => {
        seen.push(e);
        await new Promise((r) => setTimeout(r, 5));
        if (fail) throw new Error('handler failure is logged and dropped');
      },
    });
    await p.store.doc('deployments/Dx/things/before-start').create({ v: 0 });
    const disp = startOutboxDispatcher(p.store, [mk(triggers[0]!), mk(triggers[1]!, true)], { pollMs: 50, concurrency: 3 });
    for (let i = 0; i < 6; i++) await p.store.doc(`deployments/Dx/things/t${i}`).create({ i, at: Timestamp.fromMillis(1000 * i) });
    await p.store.doc('gthings/g').set({ v: 1 });
    const errs: string[] = [];
    const ew = process.stderr.write.bind(process.stderr);
    process.stderr.write = ((s: string) => (errs.push(String(s)), true)) as typeof process.stderr.write;
    try {
      await disp.idle();
    } finally {
      process.stderr.write = ew;
    }
    assert.ok(errs.some((l) => l.includes('trigger tWritten failed')));
    const c = await child(childScript(d), [d, '1', 'counters/c', '--outbox'], { CHILD_TRIGGERS: JSON.stringify(triggers) });
    assert.equal(c.code, 0, c.out);
    for (let i = 0; i < 100 && !seen.some((e) => e.path.endsWith('fromChild')); i++) await new Promise((r) => setTimeout(r, 20));
    await disp.idle();
    const paths = seen.map((e) => e.path).sort();
    assert.deepEqual(paths, ['deployments/Dx/things/before-start', 'deployments/Dx/things/fromChild', ...[0, 1, 2, 3, 4, 5].map((i) => `deployments/Dx/things/t${i}`), 'gthings/g'].sort());
    const t3 = seen.find((e) => e.path.endsWith('/t3'))!;
    assert.deepEqual(t3.params, { d: 'Dx', t: 't3' });
    assert.equal(t3.before, null);
    assert.ok(t3.after!.at instanceof Timestamp);
    assert.deepEqual(rows(join(d, 'db/deployments/Dx.sqlite')), [], 'rows deleted after the handler settled');
    assert.deepEqual(rows(join(d, 'db/global.sqlite')), []);
    await disp.stop();
    await p.close();
  });
  it('an expired lease is re-run (at-least-once)', async () => {
    const d = fresh();
    const p = open(d, { triggers });
    await p.store.doc('gthings/l').set({ v: 1 });
    const db = new DatabaseSync(join(d, 'db/global.sqlite'));
    db.prepare('UPDATE _outbox SET leaseUntil = ?').run(Date.now() - 1);
    db.close();
    let n = 0;
    const disp = startOutboxDispatcher(p.store, [{ ...triggers[1]!, timeoutSeconds: 60, memory: '256MiB', handler: async () => void n++ }], { pollMs: 20 });
    await disp.idle();
    assert.equal(n, 1);
    await disp.stop();
    await p.close();
  });
});

describe('local adapter: blobs', () => {
  it('signed URLs: 200 / HEAD / Range 206 / 416 / tamper + expiry 403 / CORS', async () => {
    const p = open(fresh());
    const data = Buffer.from('abcdefghijklmnopqrstuvwxyz');
    await p.blobs.put('files/abc', data, { contentType: 'text/plain', cacheControl: 'private, max-age=60' });
    const url = await p.blobs.signedReadUrl('files/abc', { expiresMs: 60_000, disposition: 'attachment; filename="a b.txt"' });
    assert.match(url, /^http:\/\/opm\.test\/base\/_blob\/files\/abc\?exp=\d+&sig=[A-Za-z0-9_-]+&rd=/);
    const rel = url.slice('http://opm.test'.length);
    const get = (u: string, headers: Record<string, string> = {}, method = 'GET') => handleBlobHttp({ method, url: u, headers }, p.blobs, { allowedOrigins: ['http://console.test'] });
    let r = await get(rel, { origin: 'http://console.test' });
    assert.equal(r.status, 200);
    assert.equal(await bodyOf(r.body), data.toString());
    assert.equal(r.headers['Content-Length'], '26');
    assert.equal(r.headers['Content-Type'], 'text/plain');
    assert.equal(r.headers['Content-Disposition'], 'attachment; filename="a b.txt"');
    assert.equal(r.headers['Cache-Control'], 'private, max-age=60');
    assert.equal(r.headers['Access-Control-Allow-Origin'], 'http://console.test');
    assert.match(r.headers['Access-Control-Expose-Headers']!, /Content-Range/);
    r = await get(rel, { origin: 'http://evil.test' });
    assert.equal(r.headers['Access-Control-Allow-Origin'], undefined);
    r = await get(rel, {}, 'HEAD');
    assert.equal(r.status, 200);
    assert.equal(r.body, null);
    r = await get(rel, { range: 'bytes=2-4' });
    assert.equal(r.status, 206);
    assert.equal(r.headers['Content-Range'], 'bytes 2-4/26');
    assert.equal(await bodyOf(r.body), 'cde');
    r = await get(rel, { range: 'bytes=-3' });
    assert.equal(await bodyOf(r.body), 'xyz');
    r = await get(rel, { range: 'bytes=20-' });
    assert.equal(await bodyOf(r.body), 'uvwxyz');
    r = await get(rel, { range: 'bytes=26-30' });
    assert.equal(r.status, 416);
    assert.equal(r.headers['Content-Range'], 'bytes */26');
    assert.equal((await get(rel.replace('files/abc', 'files/abd'))).status, 403, 'other path');
    assert.equal((await get(rel.replace(/rd=[^&]*/, 'rd=inline'))).status, 403, 'other disposition');
    assert.equal((await get(rel.replace(/exp=(\d+)/, (_, e) => `exp=${Number(e) + 1}`))).status, 403, 'other expiry');
    const old = await p.blobs.signedReadUrl('files/abc', { expiresMs: -1000 });
    assert.equal((await get(old.slice('http://opm.test'.length))).status, 403, 'expired');
    assert.equal((await get(rel, { origin: 'http://console.test' }, 'OPTIONS')).status, 204);
    assert.equal((await get('/base/_blob/..%2Fsecrets?exp=1&sig=x')).status, 400);
    await p.close();
  });
  it('paths are validated; list("") lists every object; lifecycle sweep; failed streams leave nothing', async () => {
    const d = fresh();
    const p = open(d);
    for (const bad of ['../x', '/abs', 'a//b', 'a/./b', '']) await assert.rejects(p.blobs.put(bad, Buffer.from('x')));
    await p.blobs.put('staging/old', Buffer.from('1'));
    await p.blobs.put('staging/new', Buffer.from('2'));
    await p.blobs.put('blobs/sha256/aa', Buffer.from('3'));
    const side = join(d, 'blobmeta/staging/old.json');
    const m = JSON.parse(readFileSync(side, 'utf8'));
    writeFileSync(side, JSON.stringify({ ...m, createdMs: Date.now() - 2 * 86_400_000 }));
    assert.deepEqual(
      (await p.blobs.list('')).map((b) => b.path),
      ['blobs/sha256/aa', 'staging/new', 'staging/old'],
    );
    assert.equal(await p.blobs.sweep([{ prefixes: ['staging/'], ageDays: 1 }]), 1);
    assert.deepEqual(
      (await p.blobs.list('staging/')).map((b) => b.path),
      ['staging/new'],
    );
    const w = p.blobs.writeStream('transfers/D/t1');
    w.write('partial');
    await new Promise<void>((res) => {
      w.on('error', () => res());
      w.on('close', () => res());
      w.destroy(new Error('client went away'));
    });
    assert.equal(await p.blobs.head('transfers/D/t1'), null);
    await new Promise((r) => setTimeout(r, 20));
    assert.deepEqual(readdirSync(join(d, 'tmp')), [], 'temp file removed');
    await assert.rejects(p.blobs.compose(['a'], 'b'));
    assert.equal(p.blobs.canCompose, false);
    await p.blobs.signedReadUrl('staging/new', { expiresMs: 1000 });
    const key = join(d, 'secrets/blob-hmac.key');
    assert.equal(statSync(key).mode & 0o777, 0o600);
    await p.close();
  });
});

describe('local adapter: TTL sweep + backups', () => {
  it('sweepDocTtl deletes expired agentNonces / rateLimits only', async () => {
    const p = open(fresh());
    const now = Date.now();
    await p.store.doc('agentNonces/a').set({ expiresAt: Timestamp.fromMillis(now - 1000) });
    await p.store.doc('agentNonces/b').set({ expiresAt: Timestamp.fromMillis(now + 60_000) });
    await p.store.doc('rateLimits/r1').set({ count: 1, expireAt: Timestamp.fromMillis(now - 5) });
    await p.store.doc('rateLimits/r2').set({ count: 1, expireAt: Timestamp.fromMillis(now + 5000) });
    assert.equal(await sweepDocTtl(p.store, now), 2);
    assert.deepEqual(
      (await p.store.collection('agentNonces').get()).docs.map((x) => x.id),
      ['b'],
    );
    assert.deepEqual(
      (await p.store.collection('rateLimits').get()).docs.map((x) => x.id),
      ['r2'],
    );
    await p.close();
  });
  it('backupSqlite copies every file consistently; enumeration helpers', async () => {
    const d = fresh();
    const p = open(d);
    await p.store.doc('templates/a').set({ slug: 'a' });
    await p.store.doc('deployments/D1').set({ slug: 'd' });
    await p.store.doc('deployments/D1/commits/c').set({ seq: 1, at: Timestamp.fromMillis(1) });
    await p.store.doc('zeta/z').set({ v: 1 });
    const restored = fresh();
    const out = join(restored, 'db');
    const files = await backupSqlite(d, out);
    assert.deepEqual(files.map((f) => f.slice(out.length + 1)).sort(), ['deployments/D1.sqlite', 'global.sqlite']);
    // the copy is a working data dir
    const b = open(restored);
    assert.equal((await b.store.doc('deployments/D1/commits/c').get()).data()!.seq, 1);
    assert.equal((await b.store.collection('templates').where('slug', '==', 'a').get()).size, 1);
    await b.close();
    await assert.rejects(backupSqlite(d, out), /exists/);
    assert.deepEqual(
      [...p.store.allDocuments()].map((x) => x.path),
      ['deployments/D1', 'deployments/D1/commits/c', 'templates/a', 'zeta/z'],
    );
    assert.ok([...p.store.allDocuments()][1]!.data.at instanceof Timestamp);
    assert.equal(p.store.countDocuments(), 4);
    await p.close();
  });
});

describe('local adapter: auth', () => {
  let p: ReturnType<typeof open>;
  const mails: { to: string; text: string }[] = [];
  const opts = () => ({ consoleUrl: 'http://console.test', sendMail: async (to: string, _s: string, text: string) => void mails.push({ to, text }) });
  const call = (method: string, url: string, body?: unknown, ip = '10.0.0.1') => handleAuthHttp({ method, url, headers: {}, body: Buffer.from(body === undefined ? '' : JSON.stringify(body)), ip }, p.auth, opts());
  const j = (r: { body: string | null }) => JSON.parse(r.body ?? 'null');
  before(() => {
    p = open(fresh());
  });
  after(async () => {
    await p.close();
  });

  it('createUser (unique e-mail, explicit uid, argon2id hash), getUser / getUserByEmail / getUsers / listAllUsers', async () => {
    const u = await p.auth.createUser({ email: 'Alice@Example.com', password: 'correct horse', displayName: 'Alice' });
    assert.match(u.uid, /^[A-Za-z0-9]{28}$/);
    assert.equal(u.email, 'alice@example.com');
    assert.equal(u.hasPassword, true);
    assert.deepEqual(u.providers, ['password']);
    assert.ok(u.tokensValidAfterTime && Number.isFinite(Date.parse(u.tokensValidAfterTime)));
    const raw = (await p.store.doc(`authUsers/${u.uid}`).get()).data()!;
    assert.match(raw.passwordHash, /^\$argon2id\$v=19\$m=19456,t=2,p=1\$/);
    assert.ok(!JSON.stringify(raw).includes('correct horse'), 'never plaintext');
    await assert.rejects(p.auth.createUser({ email: 'alice@example.com' }), (e: { code?: string }) => e.code === 'auth/email-already-exists');
    await assert.rejects(p.auth.createUser({ email: 'not-an-email' }), (e: { code?: string }) => e.code === 'auth/invalid-email');
    await assert.rejects(p.auth.createUser({ email: 'x@example.com', password: '123' }), (e: { code?: string }) => e.code === 'auth/invalid-password');
    const fixed = await p.auth.createUser({ uid: 'FirebaseUid123', email: 'bob@example.com' });
    assert.equal(fixed.uid, 'FirebaseUid123');
    assert.equal(fixed.hasPassword, false);
    await assert.rejects(p.auth.createUser({ uid: 'FirebaseUid123', email: 'carol@example.com' }), (e: { code?: string }) => e.code === 'auth/uid-already-exists');
    assert.equal((await p.auth.getUserByEmail('ALICE@example.com')).uid, u.uid);
    await assert.rejects(p.auth.getUser('nope'), (e: { code?: string }) => e.code === 'auth/user-not-found');
    assert.deepEqual(
      (await p.auth.getUsers([{ uid: u.uid }, { uid: 'nope' }])).users.map((x) => x.uid),
      [u.uid],
    );
    assert.equal((await p.auth.listAllUsers()).length, 2);
    await p.auth.updateUser(fixed.uid, { email: 'bobby@example.com' });
    await assert.rejects(p.auth.getUserByEmail('bob@example.com'), (e: { code?: string }) => e.code === 'auth/user-not-found');
    assert.equal((await p.auth.getUserByEmail('bobby@example.com')).uid, fixed.uid);
    await p.auth.deleteUser(fixed.uid);
    await assert.rejects(p.auth.getUserByEmail('bobby@example.com'));
    await p.auth.createUser({ email: 'bobby@example.com' }); // the e-mail is free again
  });

  it('login / verifyIdToken / limiter / disabled / revoke', async () => {
    const cfg = await call('GET', '/auth/config');
    assert.deepEqual(j(cfg), { provider: 'local', passwordReset: true, oidc: null });
    const bad = await call('POST', '/auth/login', { email: 'alice@example.com', password: 'wrong' });
    assert.equal(bad.status, 400);
    assert.equal(j(bad).error.code, 'auth/invalid-credential');
    assert.equal(j(await call('POST', '/auth/login', { email: 'nobody@example.com', password: 'x' })).error.code, 'auth/invalid-credential');
    const ok = await call('POST', '/auth/login', { email: 'ALICE@example.com', password: 'correct horse' });
    assert.equal(ok.status, 200, ok.body ?? '');
    const t = j(ok);
    assert.equal(t.expiresIn, 3600);
    assert.equal(t.email, 'alice@example.com');
    const dec = await p.auth.verifyIdToken(t.idToken, true);
    assert.equal(dec.uid, t.uid);
    assert.equal(dec.exp - dec.iat, 3600);
    await assert.rejects(p.auth.verifyIdToken(t.idToken.slice(0, -2) + 'xx'), (e: { code?: string }) => e.code === 'auth/argument-error');
    const [h, b] = t.idToken.split('.');
    const forged = `${h}.${Buffer.from(JSON.stringify({ ...JSON.parse(Buffer.from(b, 'base64url').toString()), uid: 'other' })).toString('base64url')}.${t.idToken.split('.')[2]}`;
    await assert.rejects(p.auth.verifyIdToken(forged));
    for (let i = 0; i < 10; i++) await call('POST', '/auth/login', { email: 'alice@example.com', password: 'nope' }, '10.9.9.9');
    assert.equal(j(await call('POST', '/auth/login', { email: 'alice@example.com', password: 'correct horse' }, '10.9.9.9')).error.code, 'auth/too-many-requests');
    assert.equal((await call('POST', '/auth/login', { email: 'alice@example.com', password: 'correct horse' }, '10.9.9.10')).status, 200, 'per ip + e-mail');
    await p.auth.updateUser(t.uid, { disabled: true });
    await assert.rejects(p.auth.verifyIdToken(t.idToken), (e: { code?: string }) => e.code === 'auth/user-disabled');
    assert.equal(j(await call('POST', '/auth/login', { email: 'alice@example.com', password: 'correct horse' })).error.code, 'auth/user-disabled');
    assert.equal(j(await call('POST', '/auth/refresh', { refreshToken: t.refreshToken })).error.code, 'auth/user-disabled');
    await p.auth.updateUser(t.uid, { disabled: false });
    // revocation is at second precision (like Firebase): wait for the next second
    await new Promise((r) => setTimeout(r, 1100 - (Date.now() % 1000)));
    await p.auth.revokeRefreshTokens(t.uid);
    await assert.rejects(p.auth.verifyIdToken(t.idToken, true), (e: { code?: string }) => e.code === 'auth/id-token-revoked');
    await p.auth.verifyIdToken(t.idToken, false);
    assert.equal(j(await call('POST', '/auth/refresh', { refreshToken: t.refreshToken })).error.code, 'auth/invalid-refresh-token', 'refresh tokens deleted on revoke');
    const u = await p.auth.getUser(t.uid);
    assert.ok(Date.parse(u.tokensValidAfterTime!) > Date.now() - 5000);
  });

  it('refresh rotation; reuse of a rotated token revokes the family; logout', async () => {
    const t = j(await call('POST', '/auth/login', { email: 'alice@example.com', password: 'correct horse' }));
    const r1 = await call('POST', '/auth/refresh', { refreshToken: t.refreshToken });
    assert.equal(r1.status, 200);
    const t1 = j(r1);
    assert.notEqual(t1.refreshToken, t.refreshToken);
    await p.auth.verifyIdToken(t1.idToken, true);
    const t2 = j(await call('POST', '/auth/refresh', { refreshToken: t1.refreshToken }));
    assert.ok(t2.refreshToken);
    // replay of t1 (already rotated) → family revoked: t2 stops working too
    assert.equal(j(await call('POST', '/auth/refresh', { refreshToken: t1.refreshToken })).error.code, 'auth/invalid-refresh-token');
    assert.equal(j(await call('POST', '/auth/refresh', { refreshToken: t2.refreshToken })).error.code, 'auth/invalid-refresh-token');
    const s = j(await call('POST', '/auth/login', { email: 'alice@example.com', password: 'correct horse' }));
    assert.equal((await call('POST', '/auth/logout', { refreshToken: s.refreshToken })).status, 204);
    assert.equal(j(await call('POST', '/auth/refresh', { refreshToken: s.refreshToken })).error.code, 'auth/invalid-refresh-token');
    const stored = (await p.store.collection('authRefresh').get()).docs.map((d) => d.id);
    assert.ok(!stored.includes(t2.refreshToken), 'refresh tokens stored hashed');
  });

  it('password reset: link, request mail, check, confirm (≥ 8 chars), one-time, revokes sessions', async () => {
    await assert.rejects(p.auth.generatePasswordResetLink('nobody@example.com'), (e: { code?: string }) => e.code === 'auth/user-not-found');
    const link = await p.auth.generatePasswordResetLink('alice@example.com');
    assert.match(link, /^http:\/\/console\.test\/reset-password\?token=[A-Za-z0-9_-]{43}$/);
    const token = new URL(link).searchParams.get('token')!;
    const session = j(await call('POST', '/auth/login', { email: 'alice@example.com', password: 'correct horse' }));
    assert.deepEqual(j(await call('GET', `/auth/reset/check?token=${token}`)), { email: 'alice@example.com' });
    assert.equal((await call('GET', '/auth/reset/check?token=nope')).status, 410);
    const weak = await call('POST', '/auth/reset/confirm', { token, password: 'short' });
    assert.equal(weak.status, 400);
    assert.equal(j(weak).error.code, 'auth/weak-password');
    await new Promise((r) => setTimeout(r, 1100 - (Date.now() % 1000)));
    assert.equal((await call('POST', '/auth/reset/confirm', { token, password: 'a new password' })).status, 204);
    const again = await call('POST', '/auth/reset/confirm', { token, password: 'another password' });
    assert.equal(again.status, 410);
    assert.equal(j(again).error.code, 'auth/invalid-reset-token');
    assert.equal(j(await call('POST', '/auth/login', { email: 'alice@example.com', password: 'correct horse' })).error.code, 'auth/invalid-credential');
    assert.equal((await call('POST', '/auth/login', { email: 'alice@example.com', password: 'a new password' })).status, 200);
    await assert.rejects(p.auth.verifyIdToken(session.idToken, true), (e: { code?: string }) => e.code === 'auth/id-token-revoked');
    assert.equal(j(await call('POST', '/auth/refresh', { refreshToken: session.refreshToken })).error.code, 'auth/invalid-refresh-token');
    // expired token
    const l2 = new URL(await p.auth.generatePasswordResetLink('alice@example.com')).searchParams.get('token')!;
    await p.store.doc(`authReset/${createHash('sha256').update(l2).digest('hex')}`).update({ expiresAt: new Date(Date.now() - 1000).toISOString() });
    assert.equal(j(await call('POST', '/auth/reset/confirm', { token: l2, password: 'whatever123' })).error.code, 'auth/expired');
    // the request endpoint always answers 204 and mails in the background
    assert.equal((await call('POST', '/auth/reset/request', { email: 'nobody@example.com' })).status, 204);
    assert.equal((await call('POST', '/auth/reset/request', { email: 'alice@example.com' })).status, 204);
    for (let i = 0; i < 100 && !mails.length; i++) await new Promise((r) => setTimeout(r, 20));
    assert.equal(mails.length, 1);
    assert.equal(mails[0]!.to, 'alice@example.com');
    assert.match(mails[0]!.text, /http:\/\/console\.test\/reset-password\?token=/);
    assert.equal((await call('GET', '/auth/nope')).status, 404);
    assert.equal((await call('OPTIONS', '/auth/login')).status, 204);
  });

  it('OIDC: start → callback (PKCE, nonce, RS256 id_token) → one-time code → exchange', async () => {
    const { privateKey, publicKey } = generateKeyPairSync('rsa', { modulusLength: 2048 });
    const jwk = { ...(publicKey.export({ format: 'jwk' }) as object), kid: 'k1', alg: 'RS256', use: 'sig' };
    let srv: Server | null = null;
    let lastNonce = '';
    let lastChallenge = '';
    let email = 'oidc.user@corp.test';
    const base = await new Promise<string>((res) => {
      srv = createServer((req, rs) => {
        const u = new URL(req.url!, 'http://x');
        const issuer = `http://127.0.0.1:${(srv!.address() as { port: number }).port}`;
        if (u.pathname === '/.well-known/openid-configuration') {
          rs.setHeader('Content-Type', 'application/json');
          return rs.end(JSON.stringify({ issuer, authorization_endpoint: `${issuer}/authorize`, token_endpoint: `${issuer}/token`, jwks_uri: `${issuer}/jwks` }));
        }
        if (u.pathname === '/jwks') return rs.end(JSON.stringify({ keys: [jwk] }));
        if (u.pathname === '/token') {
          let b = '';
          req.on('data', (c) => (b += c));
          req.on('end', () => {
            const f = new URLSearchParams(b);
            const verifierOk = createHash('sha256').update(f.get('code_verifier')!).digest('base64url') === lastChallenge;
            if (f.get('code') !== 'the-code' || !verifierOk || f.get('client_secret') !== 'sec') {
              rs.statusCode = 400;
              return rs.end('{}');
            }
            const head = Buffer.from(JSON.stringify({ alg: 'RS256', kid: 'k1' })).toString('base64url');
            const body = Buffer.from(JSON.stringify({ iss: issuer, aud: 'cid', sub: 'sub-1', email, email_verified: true, hd: 'corp.test', nonce: lastNonce, exp: Math.floor(Date.now() / 1000) + 300, name: 'O User' })).toString('base64url');
            const sig = sign('RSA-SHA256', Buffer.from(`${head}.${body}`), privateKey).toString('base64url');
            rs.end(JSON.stringify({ id_token: `${head}.${body}.${sig}` }));
          });
          return;
        }
        rs.statusCode = 404;
        rs.end();
      }).listen(0, '127.0.0.1', () => res(`http://127.0.0.1:${(srv!.address() as { port: number }).port}`));
    });
    try {
      const oidc = { issuer: base, clientId: 'cid', clientSecret: 'sec', label: 'Corp SSO', allowedDomain: 'corp.test', redirectUri: 'http://opm.test/auth/oidc/callback' };
      const o = { consoleUrl: 'http://console.test', oidc };
      const req = (method: string, url: string, body?: unknown) => handleAuthHttp({ method, url, headers: {}, body: Buffer.from(body ? JSON.stringify(body) : ''), ip: '1.1.1.1' }, p.auth, o);
      assert.deepEqual(JSON.parse((await req('GET', '/auth/config')).body!).oidc, { label: 'Corp SSO' });
      const flow = async () => {
        const st = await req('GET', '/auth/oidc/start?next=/app/deployments');
        assert.equal(st.status, 302);
        const loc = new URL(st.headers.Location!);
        assert.equal(loc.origin + loc.pathname, `${base}/authorize`);
        assert.equal(loc.searchParams.get('code_challenge_method'), 'S256');
        assert.equal(loc.searchParams.get('hd'), 'corp.test');
        lastNonce = loc.searchParams.get('nonce')!;
        lastChallenge = loc.searchParams.get('code_challenge')!;
        return req('GET', `/auth/oidc/callback?code=the-code&state=${encodeURIComponent(loc.searchParams.get('state')!)}`);
      };
      const cb = await flow();
      assert.equal(cb.status, 302);
      const back = new URL(cb.headers.Location!);
      assert.equal(back.origin + back.pathname, 'http://console.test/login');
      assert.equal(back.searchParams.get('next'), '/app/deployments');
      const code = new URLSearchParams(back.hash.slice(1)).get('oidc')!;
      assert.ok(code, cb.headers.Location);
      const ex = await req('POST', '/auth/oidc/exchange', { code });
      assert.equal(ex.status, 200, ex.body!);
      const t = JSON.parse(ex.body!);
      assert.equal((await p.auth.verifyIdToken(t.idToken, true)).email, email);
      const u = await p.auth.getUserByEmail(email);
      assert.deepEqual(u.providers, ['127.0.0.1:' + new URL(base).port]);
      assert.equal(u.hasPassword, false);
      assert.equal(JSON.parse((await req('POST', '/auth/oidc/exchange', { code })).body!).error.code, 'auth/invalid-oidc-code', 'one-time');
      // the state is one-time too
      const replay = await req('GET', `/auth/oidc/callback?code=the-code&state=nope`);
      assert.match(replay.headers.Location!, /#oidcError=invalid-state$/);
      // an existing password user gets the identity linked
      email = 'alice@example.com';
      const cb2 = await flow();
      const code2 = new URLSearchParams(new URL(cb2.headers.Location!).hash.slice(1)).get('oidc')!;
      const t2 = JSON.parse((await req('POST', '/auth/oidc/exchange', { code: code2 })).body!);
      assert.equal(t2.email, 'alice@example.com');
      assert.ok((await p.auth.getUserByEmail('alice@example.com')).providers.includes('127.0.0.1:' + new URL(base).port));
    } finally {
      await new Promise((r) => (srv as Server | null)?.close(r));
    }
  });
});
