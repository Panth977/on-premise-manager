/**
 * The TS side of driver/cross_test.go, run with tsx from backend/:
 *   write <dir>        apply the fixture + the write sequence through the TS driver, create a user, a blob; print facts
 *   read  <dir> <json> check what Go wrote (argument: the facts Go printed); print checks
 *   incr  <dir> <n> <path…>   N parallel read-modify-write transactions per path
 *   info               the registry + trigger specs
 */
import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { decodeDoc, encodeDoc } from '../../../../../backend/src/driver/codec.ts';
import { FieldValue } from '../../../../../backend/src/driver/types/docstore.ts';
import { handleAuthHttp, handleBlobHttp, INDEX_REGISTRY, localPorts } from '../../../../../backend/src/driver/local.ts';
import { TRIGGER_SPECS } from '../../../../../backend/src/core/trigger-specs.ts';

const [mode, dir, ...rest] = process.argv.slice(2);
const FIX: Record<string, unknown> = JSON.parse(readFileSync(new URL('./fixture.json', import.meta.url), 'utf8'));
const open = () => localPorts({ dataDir: dir!, publicUrl: 'http://cross.test', consoleUrl: 'http://console.test', strict: true });
const hdr = {};
async function main() {
  if (mode === 'info') {
    const hash = (o: unknown) => createHash('sha256').update(JSON.stringify(o)).digest('hex').slice(0, 16);
    console.log(JSON.stringify({ registry: Object.fromEntries(Object.entries(INDEX_REGISTRY).map(([k, v]) => [k, { json: JSON.stringify(v), hash: hash(v) }])), triggers: TRIGGER_SPECS.map((t) => ({ name: t.name, kind: t.kind, pattern: t.pattern })) }));
    return;
  }
  const p = open();
  if (mode === 'write') {
    for (const [path, stored] of Object.entries(FIX)) await p.store.doc(path).set(decodeDoc(JSON.stringify(stored)));
    // the same sequence as cross_test.go goSequence
    await p.store.doc('templates/T1').update({ 'a.y': 3, 'a.n.m': 1, label: FieldValue.delete(), cnt: FieldValue.increment(2), arr: FieldValue.arrayUnion('x', { k: 1 }, 'x') });
    await p.store.doc('templates/T1').set({ a: { z: 9, q: { r: 1 } }, arr: FieldValue.arrayRemove('x') }, { merge: true });
    await p.store.doc('services/s1/images/v1').update({ 'import.status': 'done' });
    await p.store.doc('plain/gone').set({ v: 1 });
    await p.store.doc('plain/gone').delete();
    await p.store.doc('deployments/D2').delete();
    await p.blobs.put('files/a b/ü.txt', Buffer.from('hello cross'), { contentType: 'text/x-cross', cacheControl: 'private, max-age=9', meta: { opmSha256: 'abc', z: '1', a: '2' } });
    await p.blobs.put('staging/plain', Buffer.from('x'));
    await p.blobs.setMeta('staging/plain', { k: 'v' });
    const u = await p.auth.createUser({ email: 'Ts.User@Example.com', password: 'ts-password-1', displayName: 'TS User' });
    const login = await handleAuthHttp({ method: 'POST', url: '/auth/login', headers: hdr, body: Buffer.from(JSON.stringify({ email: 'ts.user@example.com', password: 'ts-password-1' })), ip: '1.1.1.1' }, p.auth, { consoleUrl: 'http://console.test' });
    const link = await p.auth.generatePasswordResetLink('ts.user@example.com');
    const url = await p.blobs.signedReadUrl('files/a b/ü.txt', { expiresMs: 600_000, disposition: 'attachment; filename="ü b.txt"' });
    console.log(JSON.stringify({ uid: u.uid, login: JSON.parse(login.body!), link, url }));
  } else if (mode === 'read') {
    const f = JSON.parse(rest[0]!) as { uid: string; idToken: string; refreshToken: string; link: string; url: string };
    const out: Record<string, unknown> = {};
    for (const path of Object.keys(FIX).filter((x) => x !== 'deployments/D2')) {
      const s = await p.store.doc(path).get();
      out[path] = s.exists ? encodeDoc(s.data()!) : null;
    }
    const dec = await p.auth.verifyIdToken(f.idToken, true);
    out.tokenUid = dec.uid;
    const refreshed = JSON.parse((await handleAuthHttp({ method: 'POST', url: '/auth/refresh', headers: hdr, body: Buffer.from(JSON.stringify({ refreshToken: f.refreshToken })), ip: '1' }, p.auth, { consoleUrl: 'x' })).body!);
    out.refreshed = refreshed.uid;
    const login = await handleAuthHttp({ method: 'POST', url: '/auth/login', headers: hdr, body: Buffer.from(JSON.stringify({ email: 'go.user@example.com', password: 'go-password-1' })), ip: '1.1.1.1' }, p.auth, { consoleUrl: 'x' });
    out.login = login.status;
    const token = new URL(f.link).searchParams.get('token')!;
    out.reset = (await p.auth.checkReset(token) as { email?: string }).email;
    const r = await handleBlobHttp({ method: 'GET', url: f.url.slice('http://cross.test'.length), headers: hdr }, p.blobs, { allowedOrigins: [] });
    const chunks: Buffer[] = [];
    for await (const c of r.body as AsyncIterable<Buffer>) chunks.push(Buffer.from(c));
    out.blob = { status: r.status, ct: r.headers['Content-Type'], cd: r.headers['Content-Disposition'], cc: r.headers['Cache-Control'], body: Buffer.concat(chunks).toString(), meta: (await p.blobs.head('files/a b/ü.txt'))!.meta };
    out.queries = {
      group: (await p.store.collectionGroup('commits').orderBy('seq').get()).docs.map((d) => d.ref.path),
      dep: (await p.store.collection('deployments').where('deleted', '==', false).get()).docs.map((d) => d.id),
      ts: (await p.store.collection('uploads').where('expiresAt', '>', new Date(0)).get()).size,
      arr: (await p.store.collection('auditLog').where('_actionKeys', 'array-contains', 'x.').get()).size,
    };
    console.log(JSON.stringify(out));
  } else if (mode === 'incr') {
    const [n, ...paths] = rest;
    await Promise.all(
      Array.from({ length: Number(n) }, async () => {
        for (const path of paths) {
          await p.store.runTransaction(
            async (tx) => {
              const ref = p.store.doc(path);
              const s = await tx.get(ref);
              tx.set(ref, { v: ((s.data()?.v as number) ?? 0) + 1 }, { merge: true });
            },
            { maxAttempts: 500 },
          );
        }
      }),
    );
    console.log('done');
  }
  await p.close();
}
await main();
