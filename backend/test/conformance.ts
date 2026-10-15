/**
 * Adapter conformance suite (SPEC §21 #108): the DocStore + BlobStore semantics every adapter guarantees
 * (ports/docstore.ts, ports/blobstore.ts). Registered by test/40-adapter-conformance.test.ts for each adapter.
 * Data lives under conformance/{run}/… and deployments/cf{run}/… so runs never collide.
 */
import assert from 'node:assert/strict';
import { randomBytes } from 'node:crypto';
import { after, before, describe, it } from 'node:test';
import type { Readable } from 'node:stream';
// named runtime imports from the module itself: functions/ is CJS under tsx, `export *` re-exports are invisible to ESM
import { FieldValue, Timestamp, storeErrorCode, type DocStore } from '../functions/src/ports/docstore.js';
import type { BlobStore } from '../functions/src/ports/blobstore.js';

/** index-registry entries of the conformance collections (local adapter, strict mode) */
export const CONFORMANCE_INDEXES = {
  conformance__items: { scalar: ['n', 's', 'b', 'ts', 'grp', 'mixed', 'nested.x'], array: ['tags'] },
  conformance__a__cgItems: { scalar: ['run', 'n'] },
  conformance__b__cgItems: { scalar: ['run', 'n'] },
  cgItems: { scalar: ['run', 'n'] },
};

async function readAll(s: Readable): Promise<Buffer> {
  const chunks: Buffer[] = [];
  for await (const c of s) chunks.push(Buffer.from(c as Buffer));
  return Buffer.concat(chunks);
}
async function rejects(p: Promise<unknown>, code: string) {
  try {
    await p;
  } catch (e) {
    assert.equal(storeErrorCode(e), code, `expected ${code}, got ${String(e)}`);
    return;
  }
  assert.fail(`expected ${code}`);
}

export function conformance(name: string, mk: () => Promise<{ store: DocStore; blobs: BlobStore; done(): Promise<void> }>) {
  describe(`adapter conformance: ${name}`, () => {
    let store: DocStore;
    let blobs: BlobStore;
    let done: () => Promise<void>;
    const run = `r${randomBytes(5).toString('hex')}`;
    const items = () => store.collection(`conformance/${run}/items`);
    const ids = (s: { docs: { id: string }[] }) => s.docs.map((d) => d.id);
    before(async () => {
      ({ store, blobs, done } = await mk());
    });
    after(async () => {
      await done();
    });

    describe('documents', () => {
      it('set / get / exists / delete, auto ids, refs', async () => {
        const ref = store.doc(`conformance/${run}/docs/a`);
        assert.equal((await ref.get()).exists, false);
        assert.equal((await ref.get()).data(), undefined);
        await ref.set({ x: 1, u: undefined, m: { k: 'v', gone: undefined }, arr: [1, 'two', { three: 3 }] });
        const s = await ref.get();
        assert.equal(s.exists, true);
        assert.equal(s.id, 'a');
        assert.deepEqual(s.data(), { x: 1, m: { k: 'v' }, arr: [1, 'two', { three: 3 }] });
        assert.equal(s.get('m.k'), 'v');
        assert.equal(ref.parent.path, `conformance/${run}/docs`);
        assert.equal(ref.parent.parent?.path, `conformance/${run}`);
        assert.equal(store.collection('conformance').parent, null);
        assert.equal(ref.collection('sub').path, `conformance/${run}/docs/a/sub`);
        const auto = store.collection(`conformance/${run}/docs`).doc();
        assert.match(auto.id, /^[A-Za-z0-9]{20}$/);
        await ref.set({ y: 2 });
        assert.deepEqual((await ref.get()).data(), { y: 2 }, 'set replaces');
        await ref.delete();
        assert.equal((await ref.get()).exists, false);
        await ref.delete(); // deleting a missing document is fine
      });
      it('set merge deep-merges maps, arrays replaced', async () => {
        const ref = store.doc(`conformance/${run}/docs/merge`);
        await ref.set({ a: { x: 1, y: 1, deep: { k: 1 } }, arr: [1, 2], keep: true });
        await ref.set({ a: { y: 2, z: 3, deep: { j: 2 } }, arr: [3] }, { merge: true });
        assert.deepEqual((await ref.get()).data(), { a: { x: 1, y: 2, z: 3, deep: { k: 1, j: 2 } }, arr: [3], keep: true });
        await store.doc(`conformance/${run}/docs/merge-new`).set({ fresh: 1 }, { merge: true });
        assert.deepEqual((await store.doc(`conformance/${run}/docs/merge-new`).get()).data(), { fresh: 1 });
      });
      it('update with dotted field paths; a map value replaces the field', async () => {
        const ref = store.doc(`conformance/${run}/docs/upd`);
        await ref.set({ a: { b: { c: 1, d: 2 } }, m: { p: 1, q: 2 }, top: 0 });
        await ref.update({ 'a.b.c': 5, 'a.new.leaf': 'x', m: { r: 3 }, top: 'y' });
        assert.deepEqual((await ref.get()).data(), { a: { b: { c: 5, d: 2 }, new: { leaf: 'x' } }, m: { r: 3 }, top: 'y' });
      });
      it('transforms: increment, arrayUnion, arrayRemove, delete, serverTimestamp', async () => {
        const ref = store.doc(`conformance/${run}/docs/tr`);
        await ref.set({ n: 1, tags: ['a', 'b'], gone: 1, nested: { c: 1 } });
        await ref.update({ n: FieldValue.increment(2), tags: FieldValue.arrayUnion('b', 'c'), gone: FieldValue.delete(), 'nested.c': FieldValue.increment(1), fresh: FieldValue.increment(5), at: FieldValue.serverTimestamp() });
        let d = (await ref.get()).data()!;
        assert.equal(d.n, 3);
        assert.deepEqual(d.tags, ['a', 'b', 'c']);
        assert.equal('gone' in d, false);
        assert.equal(d.nested.c, 2);
        assert.equal(d.fresh, 5);
        assert.ok(d.at instanceof Timestamp, 'serverTimestamp → Timestamp');
        assert.ok(Math.abs(d.at.toMillis() - Date.now()) < 60_000);
        await ref.set({ tags: FieldValue.arrayRemove('a'), n: FieldValue.increment(1) }, { merge: true });
        d = (await ref.get()).data()!;
        assert.deepEqual(d.tags, ['b', 'c']);
        assert.equal(d.n, 4);
      });
      it('create conflicts with already-exists, update of a missing doc is not-found', async () => {
        const ref = store.doc(`conformance/${run}/docs/once`);
        await ref.create({ v: 1 });
        await rejects(ref.create({ v: 2 }), 'already-exists');
        assert.equal((await ref.get()).data()!.v, 1);
        await rejects(store.doc(`conformance/${run}/docs/missing`).update({ v: 1 }), 'not-found');
        assert.equal((await store.doc(`conformance/${run}/docs/missing`).get()).exists, false);
      });
      it('Timestamp and Buffer round-trip', async () => {
        const ref = store.doc(`conformance/${run}/docs/types`);
        const ts = new Timestamp(1_700_000_000, 123_000_000);
        await ref.set({ ts, bytes: Buffer.from([0, 1, 2, 255]), nested: { list: [ts, Buffer.from('hi')] }, n: null, f: 1.5, neg: -3, s: 'ü€' });
        const d = (await ref.get()).data()!;
        assert.ok(d.ts instanceof Timestamp);
        assert.ok(d.ts.isEqual(ts));
        assert.ok(Buffer.isBuffer(d.bytes) || d.bytes instanceof Uint8Array);
        assert.deepEqual([...d.bytes], [0, 1, 2, 255]);
        assert.ok(d.nested.list[0] instanceof Timestamp);
        assert.equal(Buffer.from(d.nested.list[1]).toString(), 'hi');
        assert.equal(d.n, null);
        assert.equal(d.f, 1.5);
        assert.equal(d.neg, -3);
        assert.equal(d.s, 'ü€');
      });
      it('getAll keeps order and reports missing documents', async () => {
        await store.doc(`conformance/${run}/docs/g1`).set({ i: 1 });
        const r = await store.getAll(store.doc(`conformance/${run}/docs/g1`), store.doc(`conformance/${run}/docs/nope`));
        assert.deepEqual(
          r.map((s) => s.exists),
          [true, false],
        );
      });
    });

    describe('queries', () => {
      before(async () => {
        const b = store.batch();
        for (let i = 0; i < 10; i++) {
          b.set(items().doc(`d${i}`), {
            n: i,
            s: `s${i}`,
            b: i % 2 === 0,
            ts: Timestamp.fromMillis(1_700_000_000_000 + i * 1000),
            grp: i < 5 ? 'lo' : 'hi',
            tags: i % 3 === 0 ? ['three', 'all'] : ['all'],
            mixed: i < 5 ? i : String(i),
            nested: { x: 10 - i },
          });
        }
        await b.commit();
      });
      it('== / in / array-contains / range', async () => {
        assert.deepEqual(ids(await items().where('n', '==', 3).get()), ['d3']);
        assert.deepEqual(ids(await items().where('s', '==', 's4').get()), ['d4']);
        assert.deepEqual(ids(await items().where('b', '==', true).get()), ['d0', 'd2', 'd4', 'd6', 'd8']);
        assert.deepEqual(ids(await items().where('n', 'in', [1, 5, 42]).get()), ['d1', 'd5']);
        assert.deepEqual(ids(await items().where('tags', 'array-contains', 'three').get()), ['d0', 'd3', 'd6', 'd9']);
        assert.deepEqual(ids(await items().where('n', '<', 2).get()), ['d0', 'd1']);
        assert.deepEqual(ids(await items().where('n', '<=', 2).get()), ['d0', 'd1', 'd2']);
        assert.deepEqual(ids(await items().where('n', '>', 7).get()), ['d8', 'd9']);
        assert.deepEqual(ids(await items().where('n', '>=', 7).get()), ['d7', 'd8', 'd9']);
        assert.deepEqual(ids(await items().where('nested.x', '>=', 9).get()), ['d1', 'd0'], 'implicit order by the range field');
        assert.deepEqual(ids(await items().where('ts', '>', Timestamp.fromMillis(1_700_000_007_500)).get()), ['d8', 'd9']);
        assert.deepEqual(ids(await items().where('grp', '==', 'lo').where('n', '>', 2).get()), ['d3', 'd4']);
      });
      it('typed comparisons: a string filter never matches numbers and vice versa', async () => {
        assert.deepEqual(ids(await items().where('mixed', '==', 1).get()), ['d1']);
        assert.deepEqual(ids(await items().where('mixed', '==', '1').get()), []);
        assert.deepEqual(ids(await items().where('mixed', '==', '7').get()), ['d7']);
        assert.deepEqual(ids(await items().where('mixed', '>', 2).get()), ['d3', 'd4']);
        assert.deepEqual(ids(await items().where('mixed', '>=', '').get()), ['d5', 'd6', 'd7', 'd8', 'd9']);
        assert.deepEqual(ids(await items().where('n', '>', '').get()), []);
        assert.deepEqual(ids(await items().where('b', '==', 1).get()), []);
      });
      it('orderBy (multi) + implicit document order in the direction of the last orderBy', async () => {
        assert.deepEqual(ids(await items().orderBy('n', 'desc').limit(3).get()), ['d9', 'd8', 'd7']);
        assert.deepEqual(ids(await items().orderBy('grp').orderBy('n', 'desc').get()), ['d9', 'd8', 'd7', 'd6', 'd5', 'd4', 'd3', 'd2', 'd1', 'd0']);
        assert.deepEqual(ids(await items().orderBy('grp', 'asc').get()), ['d5', 'd6', 'd7', 'd8', 'd9', 'd0', 'd1', 'd2', 'd3', 'd4']);
        assert.deepEqual(ids(await items().orderBy('grp', 'desc').get()), ['d4', 'd3', 'd2', 'd1', 'd0', 'd9', 'd8', 'd7', 'd6', 'd5']);
        assert.deepEqual(ids(await items().get()), ['d0', 'd1', 'd2', 'd3', 'd4', 'd5', 'd6', 'd7', 'd8', 'd9']);
        assert.deepEqual(ids(await items().where('tags', 'array-contains', 'all').orderBy('ts', 'desc').limit(2).get()), ['d9', 'd8']);
      });
      it('startAfter(snapshot) and startAfter(values)', async () => {
        const q = items().orderBy('grp').orderBy('n', 'desc');
        const p1 = await q.limit(4).get();
        assert.deepEqual(ids(p1), ['d9', 'd8', 'd7', 'd6']);
        const p2 = await q.startAfter(p1.docs[3]!).limit(4).get();
        assert.deepEqual(ids(p2), ['d5', 'd4', 'd3', 'd2']);
        const snap = await items().doc('d2').get();
        assert.deepEqual(ids(await q.startAfter(snap).get()), ['d1', 'd0'], 'a DocSnap from get() works as cursor');
        assert.deepEqual(ids(await items().orderBy('n').startAfter(6).get()), ['d7', 'd8', 'd9']);
        assert.deepEqual(ids(await items().orderBy('n', 'desc').startAfter(2).get()), ['d1', 'd0']);
        assert.deepEqual(ids(await q.startAfter('hi', 7).limit(2).get()), ['d6', 'd5']);
        assert.deepEqual(ids(await items().orderBy('grp').startAfter('hi').get()), ['d0', 'd1', 'd2', 'd3', 'd4']);
        assert.deepEqual(ids(await items().orderBy('ts').startAfter(Timestamp.fromMillis(1_700_000_008_000)).get()), ['d9']);
        // ties broken by document path
        const g = items().orderBy('grp');
        const a = await g.limit(2).get();
        assert.deepEqual(ids(await g.startAfter(a.docs[1]!).limit(2).get()), ['d7', 'd8']);
      });
      it('select(fields) projection; select() = existence only', async () => {
        const s = await items().where('n', '==', 4).select('n', 'nested.x').get();
        assert.deepEqual(s.docs[0]!.data(), { n: 4, nested: { x: 6 } });
        const e = await items().where('n', '<', 2).select().get();
        assert.equal(e.size, 2);
        assert.deepEqual(e.docs[0]!.data(), {});
        assert.equal(e.empty, false);
        let n = 0;
        e.forEach(() => n++);
        assert.equal(n, 2);
      });
      it('collectionGroup across parents (and across files)', async () => {
        const w = store.batch();
        w.set(store.doc(`conformance/${run}/a/p1/cgItems/x1`), { run, n: 3 });
        w.set(store.doc(`conformance/${run}/a/p2/cgItems/x2`), { run, n: 1 });
        w.set(store.doc(`conformance/${run}/b/p3/cgItems/x3`), { run, n: 2 });
        w.set(store.doc(`deployments/cf${run}/cgItems/x4`), { run, n: 0 });
        w.set(store.doc(`deployments/cf${run}x/cgItems/x5`), { run, n: 4 });
        w.set(store.doc(`conformance/${run}/a/p1/notCg/x9`), { run, n: 9 });
        await w.commit();
        const all = await store.collectionGroup('cgItems').where('run', '==', run).get();
        assert.deepEqual(ids(all).sort(), ['x1', 'x2', 'x3', 'x4', 'x5']);
        assert.deepEqual(ids(await store.collectionGroup('cgItems').where('run', '==', run).orderBy('n').limit(3).get()), ['x4', 'x2', 'x3']);
        const page1 = await store.collectionGroup('cgItems').where('run', '==', run).orderBy('n', 'desc').limit(2).get();
        assert.deepEqual(ids(page1), ['x5', 'x1']);
        assert.deepEqual(ids(await store.collectionGroup('cgItems').where('run', '==', run).orderBy('n', 'desc').startAfter(page1.docs[1]!).get()), ['x3', 'x2', 'x4']);
        assert.equal((await store.collectionGroup('cgItems').where('run', '==', run).where('n', '>=', 2).get()).size, 3);
        assert.equal(all.docs.find((d) => d.id === 'x4')!.ref.path, `deployments/cf${run}/cgItems/x4`);
      });
    });

    describe('transactions + batches', () => {
      it('N parallel read-modify-write increments end exactly at N', async () => {
        const ref = store.doc(`conformance/${run}/counters/c`);
        await ref.set({ v: 0 });
        const N = 10;
        await Promise.all(
          Array.from({ length: N }, () =>
            store.runTransaction(
              async (tx) => {
                const s = await tx.get(ref);
                tx.update(ref, { v: (s.data()!.v as number) + 1 });
              },
              { maxAttempts: 50 },
            ),
          ),
        );
        assert.equal((await ref.get()).data()!.v, N);
      });
      it('reads after writes are an error', async () => {
        const ref = store.doc(`conformance/${run}/counters/rw`);
        await assert.rejects(
          store.runTransaction(async (tx) => {
            tx.set(ref, { v: 1 });
            await tx.get(ref);
          }),
        );
        assert.equal((await ref.get()).exists, false);
      });
      it('tx.get(query) + getAll + a failing write aborts the whole transaction', async () => {
        const r = await store.runTransaction(async (tx) => {
          const q = await tx.get(items().where('grp', '==', 'hi'));
          const [a, b] = await tx.getAll(items().doc('d0'), items().doc('nope'));
          tx.set(store.doc(`conformance/${run}/counters/fromTx`), { size: q.size, a: a!.exists, b: b!.exists });
          return q.size;
        });
        assert.equal(r, 5);
        assert.deepEqual((await store.doc(`conformance/${run}/counters/fromTx`).get()).data(), { size: 5, a: true, b: false });
        await store.doc(`conformance/${run}/counters/exists`).set({ v: 1 });
        await assert.rejects(
          store.runTransaction(async (tx) => {
            tx.set(store.doc(`conformance/${run}/counters/notWritten`), { v: 1 });
            tx.create(store.doc(`conformance/${run}/counters/exists`), { v: 2 });
          }),
        );
        assert.equal((await store.doc(`conformance/${run}/counters/notWritten`).get()).exists, false);
      });
      it('batch commits atomically', async () => {
        const b = store.batch();
        b.set(store.doc(`conformance/${run}/batch/one`), { v: 1 });
        b.update(store.doc(`conformance/${run}/batch/missing`), { v: 1 });
        await rejects(b.commit() as Promise<unknown>, 'not-found');
        assert.equal((await store.doc(`conformance/${run}/batch/one`).get()).exists, false);
        const ok = store.batch();
        ok.set(store.doc(`conformance/${run}/batch/one`), { v: 1 }).set(store.doc(`deployments/cf${run}/batch/two`), { v: 2 });
        ok.delete(store.doc(`conformance/${run}/batch/none`));
        await ok.commit();
        assert.equal((await store.doc(`deployments/cf${run}/batch/two`).get()).data()!.v, 2);
      });
      it('bulkWriter writes independently', async () => {
        const w = store.bulkWriter();
        w.onWriteError(() => false);
        const ok = w.create(store.doc(`conformance/${run}/bulk/a`), { v: 1 });
        await store.doc(`conformance/${run}/bulk/b`).set({ v: 0 });
        const dup = w.create(store.doc(`conformance/${run}/bulk/b`), { v: 1 });
        const dupDone = assert.rejects(dup);
        // Firestore's BulkWriter sends on close/flush: settle the writes only after close (activities.ts does the same)
        await w.close();
        await ok;
        await dupDone;
        assert.equal((await store.doc(`conformance/${run}/bulk/a`).get()).data()!.v, 1);
        assert.equal((await store.doc(`conformance/${run}/bulk/b`).get()).data()!.v, 0);
      });
    });

    describe('blobs', () => {
      const P = `conformance/${run}`;
      it('put / get / head / meta / stream range / list / copy / delete / deletePrefix', async () => {
        const data = Buffer.from('0123456789abcdef');
        await blobs.put(`${P}/a.bin`, data, { contentType: 'application/x-test', meta: { opmSha256: 'abc' } });
        assert.deepEqual(await blobs.get(`${P}/a.bin`), data);
        assert.equal(await blobs.get(`${P}/missing`), null);
        assert.equal(await blobs.head(`${P}/missing`), null);
        const h = (await blobs.head(`${P}/a.bin`))!;
        assert.equal(h.size, 16);
        assert.equal(h.contentType, 'application/x-test');
        assert.equal(h.meta.opmSha256, 'abc');
        assert.ok(h.createdMs > Date.now() - 600_000);
        await blobs.setMeta(`${P}/a.bin`, { extra: '1' });
        assert.equal((await blobs.head(`${P}/a.bin`))!.meta.extra, '1');
        assert.equal((await readAll(blobs.stream(`${P}/a.bin`, { start: 2, end: 5 }))).toString(), '2345');
        assert.equal((await readAll(blobs.stream(`${P}/a.bin`))).toString(), data.toString());
        await new Promise<void>((resolve, reject) => {
          const w = blobs.writeStream(`${P}/dir/s.txt`, { contentType: 'text/plain' });
          w.on('error', reject);
          w.on('finish', () => resolve());
          w.write('hello ');
          w.end('world');
        });
        assert.equal((await blobs.get(`${P}/dir/s.txt`))!.toString(), 'hello world');
        await blobs.put(`${P}/dir/part-1`, Buffer.from('1'));
        await blobs.put(`${P}/dir/part-2`, Buffer.from('22'));
        await blobs.copy(`${P}/a.bin`, `${P}/copy.bin`);
        assert.equal((await blobs.head(`${P}/copy.bin`))!.contentType, 'application/x-test');
        const listed = (await blobs.list(`${P}/`)).map((b) => b.path.slice(P.length + 1));
        assert.deepEqual(listed.sort(), ['a.bin', 'copy.bin', 'dir/part-1', 'dir/part-2', 'dir/s.txt']);
        const parts = await blobs.list(`${P}/dir/part-`);
        assert.deepEqual(
          parts.map((b) => [b.path.slice(P.length + 1), b.size]),
          [
            ['dir/part-1', 1],
            ['dir/part-2', 2],
          ],
        );
        await blobs.deletePrefix(`${P}/dir/`);
        assert.deepEqual((await blobs.list(`${P}/dir/`)).length, 0);
        await blobs.delete(`${P}/copy.bin`);
        await blobs.delete(`${P}/copy.bin`);
        assert.equal(await blobs.get(`${P}/copy.bin`), null);
      });
      it('signedReadUrl is fetchable', async (t) => {
        await blobs.put(`${P}/signed.txt`, Buffer.from('signed body'), { contentType: 'text/plain' });
        let url: string;
        try {
          url = await blobs.signedReadUrl(`${P}/signed.txt`, { expiresMs: 60_000, disposition: 'attachment; filename="s.txt"' });
        } catch (e) {
          if (blobs.kind === 'firebase') return t.skip(`emulator cannot sign: ${String(e)}`);
          throw e;
        }
        if (blobs.kind === 'local') {
          const { handleBlobHttp, LocalBlobStore } = await import('../functions/src/adapters/local/index.js');
          const r = await handleBlobHttp({ method: 'GET', url: new URL(url).pathname + new URL(url).search, headers: {} }, blobs as InstanceType<typeof LocalBlobStore>, { allowedOrigins: [] });
          assert.equal(r.status, 200);
          assert.equal(r.headers['Content-Disposition'], 'attachment; filename="s.txt"');
          const body = r.body && typeof r.body === 'object' && !Buffer.isBuffer(r.body) ? await readAll(r.body) : Buffer.from(r.body as Buffer);
          assert.equal(body.toString(), 'signed body');
          return;
        }
        try {
          const r = await fetch(url);
          if (!r.ok) return t.skip(`emulator signed URL answered ${r.status}`);
          assert.equal(await r.text(), 'signed body');
        } catch (e) {
          t.skip(`signed URL not fetchable on the emulator: ${String(e)}`);
        }
      });
      after(async () => {
        await blobs?.deletePrefix(`${P}/`);
      });
    });
  });
}
