/**
 * §21 #96 volume manager in the mock agent: volume operations (empty / copy / restore) run as timed step machines on the
 * mock volume trees of lib/mock/tools.ts; the copies list; restore uploads. lib/mock/index.ts wires the AGENT_API
 * handlers (intents, snapshots, activities) to these functions.
 *
 * Knob: opm.mock.volFail=<step>  the next volume action fails in that step (stop | snapshot | copy | empty | restore).
 */
import type { FastForwardUse, VolumeCopy, VolumeOp, VolumeOpStepName, VolumeOpSource, VolumeOpTarget } from '@opm/shared/types';
import { cloneNode, mockHome, mockNodeSize, mockVolume, volBusy, type MockNode } from './tools';

const knob = (k: string) => {
  try {
    return localStorage.getItem(k);
  } catch {
    return null;
  }
};
const iso = (ms: number) => new Date(ms).toISOString();
let seq = 0;
const rid = () => `01J9VOLOP${(++seq).toString(36).toUpperCase().padStart(4, '0')}MOCK00000`.slice(0, 26);

const ops = new Map<string, VolumeOp>();
/** copies: the data of each copy (a frozen tree) */
const copies: { c: VolumeCopy; data: MockNode }[] = [];
const uploads = new Map<string, { site: string; volumeSlug: string; data: MockNode; size: number }>();

(function seed() {
  const v = mockVolume('app', 'hello-api-data');
  if (!v) return;
  const data = cloneNode(v.root);
  const z = mockNodeSize(data);
  copies.push({
    c: { copyId: '01J9COPYSEED0000000000000A', siteSlug: 'app', volumeSlug: 'hello-api-data', volumeKind: 'dir', path: '/var/lib/opm/volume-copies/hello-api-data-20261006-091500', inCopiesDir: true, exists: true, sizeBytes: z.size, files: z.files, createdAt: iso(Date.now() - 2 * 86_400_000), createdBy: 'mira@acme.example', opId: null },
    data,
  });
})();

export interface VolDeps {
  fail: (code: 'not-found' | 'invalid' | 'conflict' | 'locked' | 'too-large', message: string, details?: Record<string, unknown>) => never;
  addActivity: (site: string, type: string, summary: string, actor: string | null, data: Record<string, unknown>) => void;
  /** a manual snapshot of one volume → snapshotId */
  snapshot: (site: string, volumeSlug: string, actor: string, note: string) => string;
  /** containers that mount the volume (head config), in start order, and whether they run */
  users: (site: string, volumeSlug: string) => { slug: string; running: boolean; db: boolean }[];
  updateRunning: () => boolean;
}

export function volumeOpsList(site: string, volume?: string | null): VolumeOp[] {
  return [...ops.values()]
    .filter((o) => o.siteSlug === site && (!volume || o.volumeSlug === volume))
    .sort((a, b) => (a.finishedAt === null) !== (b.finishedAt === null) ? (a.finishedAt === null ? -1 : 1) : b.startedAt.localeCompare(a.startedAt));
}
export function volumeOp(site: string, id: string, d: VolDeps): VolumeOp {
  const o = ops.get(id);
  if (!o || o.siteSlug !== site) d.fail('not-found', 'no such volume operation');
  return o!;
}
export function volumeCopies(site: string): VolumeCopy[] {
  return copies.filter((x) => x.c.siteSlug === site).map((x) => x.c).sort((a, b) => b.createdAt.localeCompare(a.createdAt));
}

export function guard(site: string, slugs: string[], d: VolDeps) {
  for (const s of slugs) {
    const b = volBusy.get(`${site}/${s}`);
    if (b) d.fail('conflict', `volume ${s} is busy: ${b}`);
  }
}
function vol(site: string, slug: string, d: VolDeps) {
  const v = mockVolume(site, slug);
  if (!v) d.fail('not-found', `site ${site} declares no volume ${slug}`);
  return v!;
}
const stamp = (ms: number) => iso(ms).replace(/[-:]/g, '').replace('T', '-').slice(0, 15);
const human = (n: number) => (n < 1024 ? `${n} B` : n < 1024 ** 2 ? `${(n / 1024).toFixed(1)} KiB` : n < 1024 ** 3 ? `${(n / 1024 ** 2).toFixed(1)} MiB` : `${(n / 1024 ** 3).toFixed(2)} GiB`);

const STEP_MS: Record<VolumeOpStepName, number> = { stop: 700, snapshot: 900, copy: 1600, empty: 700, restore: 1600, start: 700 };

function run(op: VolumeOp, locks: string[], d: VolDeps, work: Partial<Record<VolumeOpStepName, () => string | void>>, done: () => { type: string; summary: string; data: Record<string, unknown> }) {
  for (const l of locks) volBusy.set(`${op.siteSlug}/${l}`, `${op.kind} running (operation ${op.opId})`);
  const fail = knob('opm.mock.volFail');
  if (fail) localStorage.removeItem('opm.mock.volFail');
  const t0 = Date.now();
  let i = 0;
  const finish = (err: string | null, step: string) => {
    for (const l of locks) volBusy.delete(`${op.siteSlug}/${l}`);
    op.finishedAt = iso(Date.now());
    op.durationMs = Date.now() - t0;
    op.ok = !err;
    op.phase = err ? 'failed' : 'done';
    op.error = err;
    for (const s of op.steps) if (s.state === 'pending') s.state = 'skipped';
    if (err) {
      d.addActivity(op.siteSlug, 'volume.op-failed', `${op.kind} of volume ${op.volumeSlug} failed at ${step}: ${err}`, op.actor, { volumeSlug: op.volumeSlug, opId: op.opId, kind: op.kind, step, error: err, stopped: op.stopped, durationMs: op.durationMs });
      return;
    }
    // the mock's data-directory mismatch (opm.mock.pgMismatch) is fixed once a volume of that site is emptied / restored
    if (op.kind !== 'copy' && (knob('opm.mock.pgMismatch') ?? '').startsWith(`${op.siteSlug}/`)) localStorage.removeItem('opm.mock.pgMismatch');
    const r = done();
    const ff = op.fastForward ?? null;
    d.addActivity(op.siteSlug, r.type, r.summary + (ff ? ` · fast-forwarded (×${ff.maxSpeed}, ${Math.round(ff.realSeconds)} s real)` : ''), op.actor, { ...r.data, volumeSlug: op.volumeSlug, opId: op.opId, stopped: op.stopped, durationMs: op.durationMs, ...(ff ? { fastForward: ff } : {}) });
  };
  const next = () => {
    const s = op.steps[i];
    if (!s) return finish(null, '');
    s.state = 'running';
    s.startedAt = iso(Date.now());
    if (s.name !== 'start') op.phase = s.name;
    const ms = STEP_MS[s.name];
    const tick = setInterval(() => {
      if (op.progress.bytesTotal && (s.name === 'copy' || s.name === 'restore')) op.progress.bytesDone = Math.min(op.progress.bytesTotal, op.progress.bytesDone + op.progress.bytesTotal / 8);
    }, ms / 8);
    setTimeout(() => {
      clearInterval(tick);
      s.finishedAt = iso(Date.now());
      if (fail === s.name) {
        s.state = 'failed';
        s.detail = `mock failure in ${s.name}`;
        // the containers are started again even after a failure
        const st = op.steps.find((x) => x.name === 'start');
        if (st && op.stopped.length) ((st.state = 'done'), (st.detail = `${op.stopped.join(', ')} started`));
        return finish(`mock failure in ${s.name}`, s.name);
      }
      try {
        const det = work[s.name]?.();
        if (det === 'skip') s.state = 'skipped';
        else s.state = 'done';
        if (typeof det === 'string' && det !== 'skip') s.detail = det;
      } catch (e) {
        s.state = 'failed';
        s.detail = String((e as Error).message ?? e);
        return finish(s.detail, s.name);
      }
      i++;
      next();
    }, ms);
  };
  setTimeout(next, 150);
}

function newOp(site: string, slug: string, kind: VolumeOp['kind'], actor: string, steps: VolumeOpStepName[], kindOf: 'dir' | 'file'): VolumeOp {
  const op: VolumeOp = {
    opId: rid(),
    siteSlug: site,
    volumeSlug: slug,
    volumeKind: kindOf,
    kind,
    actor,
    startedAt: iso(Date.now()),
    finishedAt: null,
    phase: 'queued',
    ok: null,
    error: null,
    steps: steps.map((name) => ({ name, state: 'pending', detail: null, startedAt: null, finishedAt: null })),
    progress: { bytesDone: 0, bytesTotal: null, files: 0 },
    stopped: [],
    target: null,
    source: null,
    snapshotId: null,
    copyId: null,
    resultBytes: null,
    durationMs: null,
    warnings: [],
  };
  ops.set(op.opId, op);
  return op;
}

function stopStep(op: VolumeOp, d: VolDeps, slugs: string[]) {
  return () => {
    const run = slugs.filter((x, i, a) => a.indexOf(x) === i);
    op.stopped = run;
    return run.length ? `stopped ${[...run].reverse().join(', ')} (reverse start order)` : 'skip';
  };
}
const startStep = (op: VolumeOp) => () => (op.stopped.length ? `${op.stopped.join(', ')} started` : 'skip');

export function emptyVolume(site: string, slug: string, actor: string, snapshot: boolean, ff: FastForwardUse | null, d: VolDeps): VolumeOp {
  const v = vol(site, slug, d);
  guard(site, [slug], d);
  const before = mockNodeSize(v.root).size;
  const op = newOp(site, slug, 'empty', actor, snapshot ? ['stop', 'snapshot', 'empty', 'start'] : ['stop', 'empty', 'start'], v.v.kind);
  op.fastForward = ff;
  op.progress.bytesTotal = before;
  const users = d.users(site, slug).filter((u) => u.running).map((u) => u.slug);
  run(
    op,
    [slug],
    d,
    {
      stop: stopStep(op, d, users),
      snapshot: () => {
        op.snapshotId = d.snapshot(site, slug, actor, `before emptying ${slug}`);
        return `snapshot ${op.snapshotId}`;
      },
      empty: () => {
        if (v.root.type === 'dir') v.root.children = {};
        else if (v.root.type === 'file') ((v.root.size = 0), (v.root.text = ''));
        op.progress.bytesDone = before;
        op.resultBytes = before;
        return `${human(before)} deleted`;
      },
      start: startStep(op),
    },
    () => ({ type: 'volume.emptied', summary: `volume ${slug} emptied on ${site} (${human(before)} deleted)`, data: { snapshotId: op.snapshotId, bytesBefore: before } }),
  );
  return op;
}

export function copyVolume(site: string, slug: string, actor: string, req: { target: { kind: 'path'; path?: string | null } | { kind: 'volume'; volumeSlug: string; overwrite?: string | null }; stopContainers: boolean; preserve: boolean }, d: VolDeps): VolumeOp {
  const v = vol(site, slug, d);
  const home = mockHome(site);
  let target: VolumeOpTarget;
  let tv: ReturnType<typeof vol> | null = null;
  if (req.target.kind === 'path') {
    let p = (req.target.path ?? '').trim();
    if (!p) p = `${home}/volume-copies/${slug}-${stamp(Date.now())}`;
    else if (p.startsWith('./')) p = `${home}/${p.slice(2)}`;
    if (!p.startsWith('/')) d.fail('invalid', 'target path: use an absolute path, or ./… relative to the agent home', { errors: [{ path: 'target.path', code: 'V_PATH', message: 'absolute or ./', severity: 'error' }] });
    const bad = /^\/(bin|boot|dev|etc|lib|proc|run|sbin|sys|usr)(\/|$)|^\/var\/lib\/docker(\/|$)|^\/$/.test(p) || (p.startsWith(home) && !p.startsWith(`${home}/volume-copies/`));
    if (bad) d.fail('invalid', `target path: ${p} is not allowed (system folders, the agent home outside ./volume-copies/ and volume folders are refused)`, { errors: [{ path: 'target.path', code: 'V_PATH', message: 'not allowed', severity: 'error' }] });
    if (copies.some((x) => x.c.path === p && x.c.exists)) d.fail('invalid', `target path: ${p} already exists and is not empty`);
    target = { kind: 'path', path: p, volumeSlug: null };
  } else {
    if (req.target.volumeSlug === slug) d.fail('invalid', 'the target is the same volume');
    tv = vol(site, req.target.volumeSlug, d);
    if (tv.v.kind !== v.v.kind) d.fail('invalid', `the target volume is a ${tv.v.kind}, the source a ${v.v.kind}`);
    const z = mockNodeSize(tv.root);
    if ((z.size > 0 || (tv.root.type === 'dir' && Object.keys(tv.root.children).length > 0)) && req.target.overwrite !== tv.v.volumeSlug) d.fail('conflict', `volume ${tv.v.volumeSlug} is not empty: type its name to overwrite it`, { sizeBytes: z.size, files: z.files });
    target = { kind: 'volume', path: tv.v.hostPath, volumeSlug: tv.v.volumeSlug };
  }
  guard(site, tv ? [slug, tv.v.volumeSlug] : [slug], d);
  const z = mockNodeSize(v.root);
  const op = newOp(site, slug, 'copy', actor, tv ? ['stop', 'empty', 'copy', 'start'] : ['stop', 'copy', 'start'], v.v.kind);
  op.target = target;
  op.progress.bytesTotal = z.size;
  const users = [...(req.stopContainers ? d.users(site, slug) : []), ...(tv ? d.users(site, tv.v.volumeSlug) : [])].filter((u) => u.running).map((u) => u.slug);
  const data = cloneNode(v.root);
  run(
    op,
    tv ? [slug, tv.v.volumeSlug] : [slug],
    d,
    {
      stop: req.stopContainers || tv ? stopStep(op, d, users) : () => 'containers keep running (copy while in use)',
      empty: () => {
        if (tv && tv.root.type === 'dir') tv.root.children = {};
        return '';
      },
      copy: () => {
        op.progress.bytesDone = z.size;
        op.progress.files = z.files;
        op.resultBytes = z.size;
        if (tv) {
          tv.root = data;
          return `${human(z.size)} in ${z.files} files → volume ${tv.v.volumeSlug}`;
        }
        const copyId = rid();
        op.copyId = copyId;
        copies.push({ c: { copyId, siteSlug: site, volumeSlug: slug, volumeKind: v.v.kind, path: target.path, inCopiesDir: target.path.startsWith(`${home}/volume-copies/`), exists: true, sizeBytes: z.size, files: z.files, createdAt: iso(Date.now()), createdBy: actor, opId: op.opId }, data });
        return `${human(z.size)} in ${z.files} files → ${target.path}`;
      },
      start: startStep(op),
    },
    () => ({ type: 'volume.copied', summary: `volume ${slug} copied to ${tv ? `volume ${tv.v.volumeSlug}` : target.path} on ${site} (${human(z.size)})`, data: { target, bytes: z.size, files: z.files, preserve: req.preserve, copyId: op.copyId } }),
  );
  return op;
}

export function restoreUpload(site: string, slug: string, size: number, name: string, d: VolDeps) {
  vol(site, slug, d);
  if (!/\.(tar\.gz|tgz)$/i.test(name) || size === 0) d.fail('invalid', 'not a .tar.gz: upload an archive made by "Download .tar.gz" (or tar czf)', { errors: [{ path: 'body', code: 'V_FORMAT', message: 'not a .tar.gz', severity: 'error' }] });
  if (size > 2 * 1024 ** 3) d.fail('too-large', 'at most 2 GiB; copy bigger data to the server and restore from a copy');
  const id = rid();
  uploads.set(id, { site, volumeSlug: slug, size, data: { type: 'dir', children: { [name.replace(/\.(tar\.gz|tgz)$/i, '')]: { type: 'dir', children: { 'restored.dat': { type: 'file', size, binary: true } } } } } });
  return { uploadId: id, sizeBytes: size, entries: 3, expiresAt: iso(Date.now() + 3_600_000) };
}

export function restoreVolume(site: string, slug: string, actor: string, source: { kind: 'copy'; copyId: string } | { kind: 'upload'; uploadId: string } | { kind: 'snapshot'; snapshotId: string }, ff: FastForwardUse | null, d: VolDeps): VolumeOp {
  const v = vol(site, slug, d);
  let data: MockNode;
  let src: VolumeOpSource;
  if (source.kind === 'snapshot') {
    // §21 #112 the mock keeps no archive bytes: the restore keeps the current tree (the flow and the op are what is shown)
    data = cloneNode(v.root);
    src = { kind: 'snapshot', ref: source.snapshotId, path: `${mockHome(site)}/snapshots/${source.snapshotId}/sites/${site}/volumes/${slug}.tar.zst` };
  } else if (source.kind === 'copy') {
    const c = copies.find((x) => x.c.copyId === source.copyId && x.c.siteSlug === site);
    if (!c) d.fail('not-found', 'no such copy');
    if (!c!.c.exists) d.fail('not-found', `the copy at ${c!.c.path} no longer exists`);
    if (c!.c.volumeKind !== v.v.kind) d.fail('invalid', `the copy is a ${c!.c.volumeKind}, the volume a ${v.v.kind}`);
    data = cloneNode(c!.data);
    src = { kind: 'copy', ref: c!.c.copyId, path: c!.c.path };
  } else {
    const u = uploads.get(source.uploadId);
    if (!u || u.site !== site) d.fail('not-found', 'the upload expired; upload the archive again');
    data = cloneNode(u!.data);
    src = { kind: 'upload', ref: source.uploadId, path: `${mockHome(site)}/tmp/volume-restore/${source.uploadId}.tar.gz` };
  }
  guard(site, [slug], d);
  const z = mockNodeSize(data);
  const op = newOp(site, slug, 'restore', actor, ['stop', 'snapshot', 'restore', 'start'], v.v.kind);
  op.source = src;
  op.fastForward = ff;
  op.progress.bytesTotal = z.size;
  const users = d.users(site, slug).filter((u) => u.running).map((u) => u.slug);
  run(
    op,
    [slug],
    d,
    {
      stop: stopStep(op, d, users),
      snapshot: () => {
        op.snapshotId = d.snapshot(site, slug, actor, `before restore of ${slug}`);
        return `snapshot ${op.snapshotId}`;
      },
      restore: () => {
        v.root = data;
        op.progress.bytesDone = z.size;
        op.progress.files = z.files;
        op.resultBytes = z.size;
        if (source.kind === 'upload') uploads.delete(source.uploadId);
        return `${human(z.size)} in ${z.files} files`;
      },
      start: startStep(op),
    },
    () => ({ type: 'volume.restored', summary: `volume ${slug} restored from ${src.kind === 'copy' ? `copy ${src.path}` : 'an uploaded archive'} on ${site} (${human(z.size)})`, data: { source: src, snapshotId: op.snapshotId, bytes: z.size, files: z.files } }),
  );
  return op;
}

export function deleteCopy(site: string, copyId: string, confirm: string, actor: string, d: VolDeps) {
  const i = copies.findIndex((x) => x.c.copyId === copyId && x.c.siteSlug === site);
  if (i < 0) d.fail('not-found', 'no such copy');
  if (confirm !== copyId) d.fail('invalid', 'type the copy id to confirm', { errors: [{ path: 'confirm', code: 'V_CONFIRM', message: 'must be the copy id', severity: 'error' }] });
  if ([...ops.values()].some((o) => !o.finishedAt && o.source?.ref === copyId)) d.fail('conflict', 'a restore from this copy is running');
  const [c] = copies.splice(i, 1);
  d.addActivity(site, 'volume.copy-deleted', `copy of volume ${c.c.volumeSlug} deleted on ${site} (${c.c.path}, ${human(c.c.sizeBytes ?? 0)})`, actor, { copyId, volumeSlug: c.c.volumeSlug, path: c.c.path, sizeBytes: c.c.sizeBytes });
  return { deleted: true as const };
}
