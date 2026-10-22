/**
 * §21 #123 mock of the shared files (FILES_AGENT_API): ONE listing for the whole deployment; `here`, `job` and
 * `sources` are per site. Copies are simulated: fetchToSite downloads over ~4 s from a reachable peer that holds one,
 * else from the cloud; when the cloud has no copy either it first waits ~2 s for the cloud to get one from a holder.
 *
 * Seeds (as seen by site app): here (cloud + app), here only (app is the only copy), on a peer only (db), cloud only,
 * uploading (a browser upload into the cloud), a failed job, unavailable (no copy anywhere), here on several sites,
 * an unpublished tombstone.
 */
import { FILE_MAX, FILE_NOTE_MAX, type SharedFile, type SharedFileSiteJob, type SiteSharedFile } from '@opm/shared/files';
import type { ErrorCode } from '@opm/shared/types';
import * as D from './data';
import { hostFsFile } from './hostfs';

type Json = Record<string, unknown>;
interface Ctx {
  params: Record<string, string>;
  query: URLSearchParams;
  body: unknown;
  raw: Uint8Array | null;
}
export interface FilesDeps {
  fail: (code: ErrorCode, message: string, details?: Json) => never;
  needSite: (site: string, allowStar: boolean) => void;
  needReach: (site: string) => void;
  reachable: (site: string) => boolean;
  sites: string[];
  addActivity: (site: string, type: string, summary: string, actor: string | null, data?: Json) => void;
  fileResponse: (body: BlobPart, name: string) => Response;
}
type FilesHandler = (c: Ctx, s: unknown, actor: string | null) => unknown;

const nowMs = () => Date.now();
const DL_MS = 4000; //   a copy downloads in 4 s
const WAIT_MS = 2000; // the cloud gets a copy from a holder in 2 s

/** a running copy onto a site */
interface Job {
  job: SharedFileSiteJob;
  t0: number;
  /** waiting-cloud: when the cloud's copy is there (then downloading from the cloud) */
  waitUntil: number | null;
}

function seed(): SharedFile[] {
  const f = (fileId: string, fileName: string, sizeBytes: number, ageSec: number, publishedBy: string, origin: SharedFile['origin'], cloudStored: boolean, sites: string[], extra: Partial<SharedFile> = {}): SharedFile => ({
    fileId,
    fileName,
    sizeBytes,
    sha256: D.fakeSha(fileId),
    note: null,
    publishedAt: D.ago(ageSec),
    publishedBy,
    origin,
    state: 'ready',
    unpublishedAt: null,
    unpublishedBy: null,
    cloud: { stored: cloudStored, fetch: null },
    sites,
    ...extra,
  });
  const cloud = { kind: 'cloud' } as const;
  const sitePath = (siteSlug: string, sourcePath: string) => ({ kind: 'site', siteSlug, via: 'path', sourcePath }) as const;
  const siteBrowser = (siteSlug: string) => ({ kind: 'site', siteSlug, via: 'browser', sourcePath: null }) as const;
  return [
    f('FilPlantLayout0000000001', 'plant-layout.pdf', 2_481_334, 86400 * 2, 'ops@acme.example', cloud, true, ['app'], { note: 'Updated floor plan' }),
    f('FilAppLogs00000000000002', 'app-logs-2026-10-05.tar.gz', 18_204_113, 86400, 'ravi@acme.example', sitePath('app', '/var/log/app/app-logs-2026-10-05.tar.gz'), false, ['app'], { note: 'Logs for ticket OPS-1182' }),
    f('FilRedisDump000000000003', 'redis-dump.rdb', 4_102_331, 86400 * 4, 'mira@acme.example', sitePath('db', '/var/lib/redis/dump.rdb'), false, ['db'], { note: 'Backup for support' }),
    f('FilLicense00000000000004', 'license-2027.key', 1_812, 3600 * 3, 'ops@acme.example', cloud, true, [], { note: 'New license, valid from Jan 1' }),
    f('FilFirmware0000000000005', 'sensor-firmware-v3.2.bin', 48_211_904, 600, 'ops@acme.example', cloud, false, [], { state: 'uploading', note: 'Flash during the next window' }),
    f('FilCalib00000000000000006', 'calibration-2026-10.csv', 92_118, 1800, 'ops@acme.example', cloud, true, []),
    f('FilGatewayCfg00000000007', 'old-gateway-config.tar', 311_002, 86400 * 9, 'mira@acme.example', siteBrowser('db'), false, [], { note: 'The site that held it dropped its copy' }),
    f('FilManual000000000000008', 'operator-manual.pdf', 5_220_331, 86400 * 6, 'ops@acme.example', cloud, true, ['app', 'db']),
    f('FilScreenshot00000000009', 'hmi-screenshot.png', 811_220, 86400 * 3, 'mira@acme.example', siteBrowser('app'), true, ['app']),
    f('FilQ2Report0000000000010', 'q2-report.xlsx', 311_002, 86400 * 12, 'ops@acme.example', cloud, false, [], { state: 'unpublished', unpublishedAt: D.ago(86400 * 2), unpublishedBy: 'ops@acme.example' }),
  ];
}

export function createFilesMock(d: FilesDeps): Record<string, FilesHandler> {
  const files: SharedFile[] = seed();
  const jobs = new Map<string, Job>(); // `${site}/${fileId}`
  const saved = new Set<string>(); //    `${site}:${path}`
  const key = (site: string, fileId: string) => `${site}/${fileId}`;
  // a failed copy on app (from the cloud)
  jobs.set(key('app', 'FilCalib00000000000000006'), { t0: nowMs() - 60_000, waitUntil: null, job: { phase: 'failed', from: 'cloud', bytesDone: 30_112, error: 'connection reset by peer while reading from the cloud', startedAt: D.ago(60) } });

  const peersOf = (f: SharedFile, site: string) => f.sites.filter((s) => s !== site && d.sites.includes(s) && d.reachable(s));
  /** advance a running copy; done → the site holds one */
  function tick(site: string, f: SharedFile) {
    const k = key(site, f.fileId);
    const j = jobs.get(k);
    if (!j || j.job.phase === 'failed') return;
    const t = nowMs();
    if (j.job.phase === 'waiting-cloud') {
      if (j.waitUntil !== null && t >= j.waitUntil) {
        f.cloud = { stored: true, fetch: null };
        j.job = { ...j.job, phase: 'downloading', from: 'cloud', bytesDone: 0 };
        j.t0 = t;
      } else return;
    }
    const el = t - j.t0;
    if (el >= DL_MS) {
      jobs.delete(k);
      if (!f.sites.includes(site)) f.sites = [...f.sites, site];
      d.addActivity(site, 'file.fetched', `Got a copy of ${f.fileName}`, null, { fileId: f.fileId, where: site, from: j.job.from });
    } else j.job = { ...j.job, phase: 'downloading', bytesDone: Math.round((f.sizeBytes * el) / DL_MS) };
  }
  function view(site: string, f: SharedFile): SiteSharedFile {
    tick(site, f);
    const gone = f.state === 'unpublished';
    return {
      ...structuredClone(f),
      here: !gone && f.sites.includes(site),
      job: gone ? null : (jobs.get(key(site, f.fileId))?.job ?? null),
      sources: gone ? { peers: [], cloud: false } : { peers: peersOf(f, site), cloud: f.cloud.stored },
      listedBy: 'cloud',
    };
  }
  function site(c: Ctx): string {
    const s = c.params.siteSlug;
    d.needSite(s, false);
    d.needReach(s);
    return s;
  }
  function file(c: Ctx, live = true): SharedFile {
    const f = files.find((x) => x.fileId === c.params.fileId);
    if (!f) d.fail('not-found', 'no such file');
    if (live && f!.state === 'unpublished') d.fail('gone', `${f!.fileName} was unpublished`);
    return f!;
  }
  function publish(s: string, fileName: string, size: number, note: string | null, actor: string, via: 'browser' | 'path', sourcePath: string | null): SiteSharedFile {
    if (!fileName || /[/\\\0]/.test(fileName) || fileName.length > 255) d.fail('invalid', 'fileName must be 1–255 characters without / \\', { errors: [{ path: 'fileName', code: 'V_FORMAT', message: 'fileName must be 1–255 characters without / \\', severity: 'error' }] });
    if ((note ?? '').length > FILE_NOTE_MAX) d.fail('invalid', `note: at most ${FILE_NOTE_MAX} characters`);
    if (size > FILE_MAX) d.fail('too-large', 'at most 2 GiB');
    const f: SharedFile = { fileId: D.mockUlid(), fileName, sizeBytes: size, sha256: D.fakeSha(fileName + nowMs()), note: note || null, publishedAt: D.iso(nowMs()), publishedBy: actor, origin: { kind: 'site', siteSlug: s, via, sourcePath }, state: 'ready', unpublishedAt: null, unpublishedBy: null, cloud: { stored: false, fetch: null }, sites: [s] };
    files.push(f);
    d.addActivity(s, 'file.published', `Published ${fileName}${sourcePath ? ` (from ${sourcePath})` : ''}`, actor, { fileId: f.fileId, fileName, sizeBytes: size, origin: f.origin });
    return view(s, f);
  }

  return {
    listSiteFiles: (c) => {
      const s = site(c);
      const u = c.query.get('unpublished');
      const all = u === '1' || u === 'true';
      return { items: files.filter((f) => all || f.state !== 'unpublished').sort((a, b) => b.publishedAt.localeCompare(a.publishedAt)).map((f) => view(s, f)) };
    },
    publishFromBrowser: (c, _s, actor) => {
      const s = site(c);
      return publish(s, c.query.get('fileName') ?? '', c.raw?.length ?? 0, c.query.get('note'), actor!, 'browser', null);
    },
    publishFromPath: (c, _s, actor) => {
      const s = site(c);
      const b = (c.body ?? {}) as { path?: string; note?: string | null };
      const path = String(b.path ?? '');
      if (!path.startsWith('/')) d.fail('invalid', 'path must be absolute', { errors: [{ path: 'path', code: 'V_FORMAT', message: 'path must be absolute (start with /)', severity: 'error' }] });
      const h = hostFsFile(path);
      if ('code' in h) d.fail(h.code, h.message);
      return publish(s, path.split('/').pop() || 'file', (h as { size: number }).size, b.note ?? null, actor!, 'path', path);
    },
    fetchToSite: (c) => {
      const s = site(c);
      const f = file(c);
      if (f.state === 'uploading') d.fail('conflict', `${f.fileName} is still being uploaded to the cloud; try again when it is ready`);
      tick(s, f);
      if (f.sites.includes(s)) return view(s, f);
      const k = key(s, f.fileId);
      const cur = jobs.get(k);
      if (cur && cur.job.phase !== 'failed') return view(s, f); // idempotent
      const peers = peersOf(f, s);
      const started = D.iso(nowMs());
      if (peers.length) jobs.set(k, { t0: nowMs(), waitUntil: null, job: { phase: 'downloading', from: `peer:${peers[0]}`, bytesDone: 0, error: null, startedAt: started } });
      else if (f.cloud.stored) jobs.set(k, { t0: nowMs(), waitUntil: null, job: { phase: 'downloading', from: 'cloud', bytesDone: 0, error: null, startedAt: started } });
      else if (f.sites.length) {
        // the holders are not reachable over the mesh: the cloud gets a copy from one first
        f.cloud = { stored: false, fetch: { requestedAt: started, requestedBy: `site:${s}`, from: f.sites[0], startedAt: null, bytesDone: 0, error: null } };
        jobs.set(k, { t0: nowMs(), waitUntil: nowMs() + WAIT_MS, job: { phase: 'waiting-cloud', from: null, bytesDone: 0, error: null, startedAt: started } });
      } else d.fail('conflict', `No copy of ${f.fileName} exists anywhere (no site holds one and the cloud has none).`);
      return view(s, f);
    },
    downloadSiteFile: (c, _s, actor) => {
      const s = site(c);
      const f = file(c);
      tick(s, f);
      if (!f.sites.includes(s)) d.fail('conflict', `${f.fileName} is not on ${s} yet; get a copy first`);
      d.addActivity(s, 'file.downloaded', `Downloaded ${f.fileName}`, actor, { fileId: f.fileId, where: s });
      return d.fileResponse(`mock content of ${f.fileName}\n`, f.fileName);
    },
    saveSiteFile: (c, _s, actor) => {
      const s = site(c);
      const f = file(c);
      tick(s, f);
      const path = String((c.body as { path?: string } | null)?.path ?? '').trim();
      if (!path.startsWith('/') || path.endsWith('/')) d.fail('invalid', 'path must be an absolute file path', { errors: [{ path: 'path', code: 'V_FORMAT', message: 'path must be an absolute file path', severity: 'error' }] });
      if (!f.sites.includes(s)) d.fail('conflict', `${f.fileName} is not on ${s} yet; get a copy first`);
      if (path.startsWith('/nope/')) d.fail('invalid', `parent directory ${path.split('/').slice(0, -1).join('/')} does not exist`);
      if (saved.has(`${s}:${path}`) || path.endsWith('/exists')) d.fail('conflict', `${path} already exists`, { reason: 'path-exists' });
      saved.add(`${s}:${path}`);
      d.addActivity(s, 'file.saved', `Saved ${f.fileName} to ${path}`, actor, { fileId: f.fileId, path });
      return view(s, f);
    },
    dropSiteCopy: (c, _s, actor) => {
      const s = site(c);
      const f = file(c);
      tick(s, f);
      if (!f.sites.includes(s)) d.fail('conflict', `${s} holds no copy of ${f.fileName}`);
      const copies = f.sites.length + (f.cloud.stored ? 1 : 0);
      if (copies <= 1) d.fail('conflict', `This is the only copy of ${f.fileName}. Unpublish the file instead, or get a copy somewhere else first.`, { reason: 'only-copy' });
      f.sites = f.sites.filter((x) => x !== s);
      d.addActivity(s, 'file.copy-dropped', `Dropped the copy of ${f.fileName}`, actor, { fileId: f.fileId });
      return view(s, f);
    },
    unpublishSiteFile: (c, _s, actor) => {
      const s = site(c);
      const f = file(c, false);
      if (f.state !== 'unpublished') {
        f.state = 'unpublished';
        f.unpublishedAt = D.iso(nowMs());
        f.unpublishedBy = actor;
        f.sites = [];
        f.cloud = { stored: false, fetch: null };
        for (const k of [...jobs.keys()]) if (k.endsWith(`/${f.fileId}`)) jobs.delete(k);
        d.addActivity(s, 'file.unpublished', `Unpublished ${f.fileName}`, actor, { fileId: f.fileId, fileName: f.fileName });
      }
      return { deleted: true };
    },
  };
}
