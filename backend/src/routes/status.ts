/** Status + uptime, activities list, manual .opmlog upload (SPEC-final §6.1 Status / Activities upload, §13.3). */
import { randomBytes } from 'node:crypto';
import type { Activity, AgentDoc, OpmlogManifest, SiteCloudStatus, StatusResponse, UploadActivitiesResponse, UptimeDay } from '@opm/shared';
import { col, db } from '../core/fire.js';
import { E } from '../core/errors.js';
import { loadDep, needDep } from '../core/perm.js';
import { keyFingerprint, sha256Hex, ymd } from '../core/util.js';
import { decodeActivity } from '../core/codec.js';
import { appendCloud, ingestSiteLog } from '../core/activities.js';
import { agentView } from '../core/views.js';
import { readTar } from '../core/tar.js';
import { paths, writeBytes } from '../core/storage.js';
import { verifyEd25519 } from '../core/auth.js';
import { clampLimit } from './accounts.js';
import { deploymentHeadConfig, siteRunningCommit } from '../core/branches.js';
import type { Handlers } from '../core/router.js';
import { loadDepsManifest } from './install.js';
import { agentReleaseInfo, upgradeViewOf } from './agent-upgrade.js';
import type { DocSnap, Query, WhereOp } from '../driver/index.js';

export const BUNDLE_MAX = 24 * 1024 * 1024;

function last30Days(now = new Date()): string[] {
  const out: string[] = [];
  for (let i = 29; i >= 0; i--) out.push(ymd(new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate() - i))));
  return out;
}

export const statusHandlers: Pick<Handlers, 'getStatus' | 'listActivities' | 'uploadActivities'> = {
  async getStatus(c) {
    const dep = await loadDep(c);
    // O2: the deployment BRANCH head (§21 #104; the legacy dep.head only before migration), and each agent's reported
    // head resolved through its agent branch (branch-mode agents report an agent-branch commit id, not a revision)
    const [agents, headConfig, release] = await Promise.all([col.agents(dep.deploymentId).get(), deploymentHeadConfig(dep), loadDepsManifest().then((m) => agentReleaseInfo(m))]);
    const head = headConfig ? { config: headConfig } : null;
    const now = Date.now();
    const agentBy = new Map(agents.docs.map((d) => [d.id, agentView(d.data() as AgentDoc)]));
    const labels = new Map<string, string>();
    for (const s of head?.config.sites ?? []) labels.set(s.siteSlug, s.label);
    for (const s of dep.draft.sites) if (!labels.has(s.siteSlug)) labels.set(s.siteSlug, s.label);
    const slugs = [...new Set([...(head?.config.sites ?? []).map((s) => s.siteSlug), ...dep.draft.sites.map((s) => s.siteSlug), ...agentBy.keys()])].sort();
    // head seq of every site: the deployment seq it runs
    const seqOf = new Map<string, number | null>();
    await Promise.all(
      [...agentBy.entries()].map(async ([slug, a]) => {
        if (!a.headRevId) return;
        const r = await siteRunningCommit(dep.deploymentId, slug, a.headRevId);
        seqOf.set(slug, r?.deploymentSeq ?? null);
      }),
    );
    const sites: SiteCloudStatus[] = slugs.map((slug) => {
      const a = agentBy.get(slug);
      return {
        siteSlug: slug,
        label: labels.get(slug) ?? slug,
        inHead: !!head?.config.sites.some((s) => s.siteSlug === slug),
        enrolled: !!a,
        online: a?.online ?? false,
        lastSeenAt: a?.lastSeenAt ?? null,
        relayVia: a?.relayVia ?? null,
        agentVersion: a?.agentVersion ?? null,
        headRevId: a?.headRevId ?? null,
        headSeq: a?.headRevId ? (seqOf.get(slug) ?? null) : null,
        staging: a?.staging ?? null,
        keyVersion: a?.keyVersion ?? null,
        metrics: a?.metrics ?? null,
        edge: a?.edge ?? null,
        // round C extension (not yet in shared SiteCloudStatus): the site's mesh / auto host + detected addresses
        advertiseHost: a?.advertiseHost ?? null,
        detectedAddresses: a?.detectedAddresses ?? [],
        upgrade: upgradeViewOf(a, release, now),
        clockSkew: a?.clockSkew ?? null,
      } as SiteCloudStatus;
    });
    const days = last30Days();
    const today = days.at(-1)!;
    const curHour = new Date().getUTCHours();
    const refs = slugs.flatMap((s) => days.map((d) => col.uptime(dep.deploymentId).doc(`${s}_${d}`)));
    const docs = new Map<string, UptimeDay>();
    for (let i = 0; i < refs.length; i += 300) {
      const snaps = await db().getAll(...refs.slice(i, i + 300));
      for (const s of snaps) if (s.exists) docs.set(s.id, s.data() as UptimeDay);
    }
    const uptime: StatusResponse['uptime'] = slugs.map((slug) => ({
      siteSlug: slug,
      days: days.map((date) => {
        const u = docs.get(`${slug}_${date}`);
        const hours = u?.hours?.length === 24 ? u.hours : Array(24).fill(0);
        const n = date === today ? curHour + 1 : 24;
        const onlineFraction = hours.slice(0, n).reduce((a: number, b: number) => a + b, 0) / n;
        return { date, onlineFraction: Math.round(onlineFraction * 10000) / 10000, hours };
      }),
    }));
    return { serverTime: new Date().toISOString(), sites, uptime } satisfies StatusResponse;
  },

  async listActivities(c) {
    const dep = await loadDep(c);
    needDep(c, dep, 'deployment.activities', 'read');
    const q0 = c.query;
    const limit = clampLimit(c, 100, 500);
    const site = q0.get('site') || null;
    const origin = q0.get('origin') || null;
    const type = q0.get('type') || null;
    const actor = q0.get('actor') || null;
    const via = q0.get('via') || null;
    const from = q0.get('from') || null;
    const to = q0.get('to') || null;
    const errs: string[] = [];
    if (origin && !['agent', 'cloud', 'system'].includes(origin)) errs.push('origin must be agent | cloud | system');
    if (via && !['pipe', 'manual'].includes(via)) errs.push('via must be pipe | manual');
    if (from && !Number.isFinite(Date.parse(from))) errs.push('from must be an ISO time');
    if (to && !Number.isFinite(Date.parse(to))) errs.push('to must be an ISO time');
    if (type && !/^[a-z]+(-[a-z]+)*(\.[a-z-]+)*\.?$/.test(type)) errs.push('type must be an activity type or a prefix ending in "."');
    if (errs.length) throw E.invalid(errs.map((m) => ({ path: 'query', code: 'V_FORMAT', message: m, severity: 'error' as const })));
    const acts = col.activities(dep.deploymentId);
    let q: Query = acts;
    // one equality filter runs in Firestore (indexes: firestore.indexes.json); the others are applied here
    const mem: ((a: Activity) => boolean)[] = [];
    const typeMatch = (a: Activity) => (type!.endsWith('.') ? a.type.startsWith(type!) : a.type === type);
    let used = false;
    const use = (field: string, val: unknown, op: WhereOp, pred: (a: Activity) => boolean) => {
      if (!used) {
        q = q.where(field, op, val);
        used = true;
      } else mem.push(pred);
    };
    if (site) use('siteSlug', site, '==', (a) => a.siteSlug === site);
    if (type) use('_typeKeys', type, 'array-contains', typeMatch);
    if (actor) use('actor', actor, '==', (a) => a.actor === actor);
    if (origin) use('origin', origin, '==', (a) => a.origin === origin);
    if (via) use('uploadedVia', via, '==', (a) => a.uploadedVia === via);
    if (from) q = q.where('_tsMs', '>=', Date.parse(from));
    if (to) q = q.where('_tsMs', '<=', Date.parse(to));
    q = q.orderBy('_tsMs', 'desc').orderBy('seq', 'desc');
    const cursor = q0.get('cursor');
    let after: DocSnap | null = null;
    if (cursor) {
      after = await acts.doc(cursor).get();
      if (!after.exists) throw E.bad('cursor', 'V_FORMAT', 'bad cursor');
    }
    const items: Activity[] = [];
    let exhausted = false;
    const pageSize = mem.length ? 500 : limit + 1;
    for (let round = 0; round < 20 && items.length <= limit && !exhausted; round++) {
      const page = await (after ? q.startAfter(after) : q).limit(pageSize).get();
      if (page.docs.length < pageSize) exhausted = true;
      for (const d of page.docs) {
        after = d;
        const a = decodeActivity(d.data());
        if (mem.every((f) => f(a))) items.push(a);
        if (items.length > limit) break;
      }
    }
    const shown = items.slice(0, limit);
    return { items: shown, cursor: items.length > limit ? shown.at(-1)!.activityId : null };
  },

  async uploadActivities(c) {
    const dep = await loadDep(c);
    needDep(c, dep, 'deployment.activities', 'write');
    if (c.body.length > BUNDLE_MAX) throw E.tooLarge(`the bundle is larger than ${BUNDLE_MAX} bytes`);
    let entries;
    try {
      entries = readTar(c.body);
    } catch (e) {
      throw E.bad('body', 'V_FORMAT', `not a .opmlog (ustar) file: ${(e as Error).message}`);
    }
    const get = (n: string) => entries.find((e) => e.name === n)?.data;
    const manBytes = get('manifest.json');
    const sig = get('signature.sig');
    const nd = get('activities.ndjson');
    if (!manBytes || !sig || !nd) throw E.bad('body', 'V_REQUIRED', '.opmlog needs manifest.json, activities.ndjson and signature.sig');
    let man: OpmlogManifest;
    try {
      man = JSON.parse(manBytes.toString('utf8'));
    } catch {
      throw E.bad('manifest.json', 'V_FORMAT', 'manifest.json is not JSON');
    }
    if (man.format !== 'opmlog/1') throw E.bad('manifest.format', 'V_ENUM', 'format must be opmlog/1');
    if (man.deploymentId !== dep.deploymentId) throw E.bad('manifest.deploymentId', 'V_REF', 'the bundle belongs to another deployment');
    const signer = man.signedBy;
    if (!signer?.siteSlug || !signer.publicKey) throw E.bad('manifest.signedBy', 'V_REQUIRED', 'signedBy is required');
    const [agentSnap, revokedSnap] = await Promise.all([col.agents(dep.deploymentId).doc(signer.siteSlug).get(), col.revokedKeys(dep.deploymentId).doc(keyFingerprint(signer.publicKey)).get()]);
    const known =
      (agentSnap.exists && agentSnap.data()!.publicKey === signer.publicKey) ||
      (revokedSnap.exists && revokedSnap.data()!.siteSlug === signer.siteSlug && revokedSnap.data()!.publicKey === signer.publicKey);
    if (!known) throw E.bad('manifest.signedBy', 'V_REF', `the signer key is not a key of site ${signer.siteSlug} (enrolled or revoked)`);
    if (!verifyEd25519(signer.publicKey, manBytes, sig.toString('ascii').trim())) throw E.bad('signature.sig', 'V_FORMAT', 'bad signature: the manifest is not signed by the site key');
    if (man.activities?.sizeBytes !== nd.length || man.activities?.sha256 !== `sha256:${sha256Hex(nd)}`) throw E.bad('activities.ndjson', 'V_FORMAT', 'activities.ndjson does not match the manifest (size / sha256)');
    const bySite = new Map<string, Activity[]>();
    const errors: UploadActivitiesResponse['errors'] = [];
    let rejected = 0;
    const lines = nd.toString('utf8').split('\n').filter((l) => l.trim() !== '');
    for (const [i, line] of lines.entries()) {
      let a: Activity;
      try {
        a = JSON.parse(line);
      } catch {
        rejected++;
        errors.push({ activityId: `line:${i + 1}`, reason: 'not JSON' });
        continue;
      }
      if (!a || typeof a.siteSlug !== 'string') {
        rejected++;
        errors.push({ activityId: `line:${i + 1}`, reason: 'no siteSlug' });
        continue;
      }
      (bySite.get(a.siteSlug) ?? bySite.set(a.siteSlug, []).get(a.siteSlug)!).push(a);
    }
    let inserted = 0;
    let skipped = 0;
    const sites: UploadActivitiesResponse['sites'] = [];
    for (const [slug, list] of [...bySite.entries()].sort(([a], [b]) => (a < b ? -1 : 1))) {
      const r = await ingestSiteLog(dep.deploymentId, slug, list, 'manual');
      inserted += r.inserted;
      skipped += r.skipped;
      rejected += r.rejected;
      errors.push(...r.errors);
      sites.push({ siteSlug: slug, fromSeq: r.fromSeq, toSeq: r.toSeq });
    }
    const name = `${new Date().toISOString().replace(/[:.]/g, '-')}-${randomBytes(4).toString('hex')}.opmlog`;
    await writeBytes(paths.bundle(dep.deploymentId, name), c.body);
    await appendCloud(dep.deploymentId, [
      {
        type: 'activities.uploaded',
        summary: `.opmlog signed by site ${signer.siteSlug} uploaded: ${inserted} inserted, ${skipped} skipped, ${rejected} rejected`,
        data: { signedBy: signer.siteSlug, inserted, skipped, rejected, sites, bundle: name, sizeBytes: c.body.length },
        actor: c.account!.uid,
      },
    ]);
    return { inserted, skipped, rejected, errors: errors.slice(0, 1000), sites, signatureOk: true } satisfies UploadActivitiesResponse;
  },
};
