/**
 * The daily job (SPEC-final §2.2 UptimeDay, §6.1 uploads, §14.7): uptime docs > 30 days (transfers never expire),
 * expired uploads (parts deleted, status failed "expired"), expired agent nonces, install
 * tokens expired more than 7 days ago, activity bundles older than 90 days. §21 #84: the plaintext of expired install
 * tokens is nulled; reveal rate-limit counters (rateLimits, TTL policy too) older than their expireAt are deleted.
 * §21 #90: agent upgrade requests nobody picked up within 24 h are removed (agent.upgrade-cancelled reason expired) and
 * the "Upgrade all" run they belong to fails. §21 #101: AUTO volume-content snapshots beyond the newest 200 versions per
 * volume are pruned (never versions referenced by a push or another version; content blobs are never deleted).
 */
import { Timestamp } from '../ports/index.js';
import { col, db, nowIso } from '../core/fire.js';
import { blobs } from '../ports/index.js';
import { remove, removePrefix } from '../core/storage.js';
import { ymd } from '../core/util.js';
import type { StoredUpload } from '../core/uploads.js';
import { auditSystem } from '../core/audit.js';
import { cleanupStages } from '../core/stages.js';
import { expireAgentUpgrades } from '../routes/agent-upgrade.js';
import { pruneVolumeContent } from '../routes/volume-content.js';
import type { DocRef } from '../ports/index.js';

export interface DailyResult {
  uptimeDeleted: number;
  uploadsExpired: number;
  noncesDeleted: number;
  tokensDeleted: number;
  bundlesDeleted: number;
  tokensCleared: number;
  rateLimitsDeleted: number;
  agentUpgradesExpired: number;
  /** §21 #101 AUTO snapshot versions beyond the newest VOLUME_CONTENT_LIMITS.versionsKept per volume (blobs stay) */
  volumeVersionsPruned: number;
  /** §21 #107 expired commit stages removed */
  stagesExpired: number;
}

async function deleteRefs(refs: DocRef[]) {
  for (let i = 0; i < refs.length; i += 500) {
    const b = db().batch();
    for (const r of refs.slice(i, i + 500)) b.delete(r);
    await b.commit();
  }
}

export async function runDaily(now = new Date()): Promise<DailyResult> {
  const t0 = Date.now();
  const res: DailyResult = { uptimeDeleted: 0, uploadsExpired: 0, noncesDeleted: 0, tokensDeleted: 0, bundlesDeleted: 0, tokensCleared: 0, rateLimitsDeleted: 0, agentUpgradesExpired: 0, volumeVersionsPruned: 0, stagesExpired: 0 };
  // uptime: keep 30 days
  const cutoff = ymd(new Date(now.getTime() - 30 * 86400_000));
  const up = await db().collectionGroup('uptime').where('date', '<', cutoff).get();
  await deleteRefs(up.docs.map((d) => d.ref));
  res.uptimeDeleted = up.size;
  // transfers never expire (only Delete removes one): nothing to do here
  const iso = now.toISOString();
  // uploads
  const ups = await col.uploads().where('expiresAt', '<=', iso).get();
  for (const d of ups.docs) {
    const u = d.data() as StoredUpload;
    if (u.status === 'open') {
      await d.ref.update({ status: 'failed', error: 'expired' });
      res.uploadsExpired++;
    }
    await removePrefix(`uploads/${u.uploadId}/`).catch(() => undefined);
  }
  // §21 #107 abandoned commit stages (doc + staging/<id>/ objects; the Storage lifecycle rule removes stray objects too)
  res.stagesExpired = await cleanupStages(now.getTime()).catch(() => 0);
  // nonces (TTL policy also removes them)
  const nn = await col.agentNonces().where('expiresAt', '<', Timestamp.fromDate(now)).limit(5000).get();
  await deleteRefs(nn.docs.map((d) => d.ref));
  res.noncesDeleted = nn.size;
  // install tokens: expired UNUSED ones keep no plaintext (§21 #84). A used install token is its enrollment's upgrade
  // command (§21 #92, no expiry; nulled by revokeAgent / Regenerate); maintenance tokens have expiresAt null (never matched).
  const expired = await db().collectionGroup('installTokens').where('expiresAt', '<=', iso).get();
  const withToken = expired.docs.filter((d) => typeof d.data().token === 'string' && !d.data().usedAt && d.data().kind !== 'maintenance');
  for (let i = 0; i < withToken.length; i += 500) {
    const b = db().batch();
    for (const d of withToken.slice(i, i + 500)) b.update(d.ref, { token: null });
    await b.commit();
  }
  res.tokensCleared = withToken.length;
  // reveal rate-limit counters (the TTL policy on rateLimits.expireAt removes them too)
  const rl = await db().collection('rateLimits').where('expireAt', '<', Timestamp.fromDate(now)).limit(5000).get();
  await deleteRefs(rl.docs.map((d) => d.ref));
  res.rateLimitsDeleted = rl.size;
  // install tokens: unused tokens expired > 7 days ago
  const tokCut = new Date(now.getTime() - 7 * 86400_000).toISOString();
  const toks = await db().collectionGroup('installTokens').where('expiresAt', '<', tokCut).get();
  const stale = toks.docs.filter((d) => !d.data().usedAt);
  await deleteRefs(stale.map((d) => d.ref));
  res.tokensDeleted = stale.length;
  // §21 #90 agent upgrade requests (unstarted, past expiresAt) + running "Upgrade all" runs waiting on one
  const reqs = await db().collectionGroup('agents').where('upgradeRequest.expiresAt', '<=', iso).get();
  const runs = await col.deployments().where('agentUpgradeRun.status', '==', 'running').get();
  const deps = new Set<string>([...reqs.docs.map((d) => d.ref.parent.parent!.id), ...runs.docs.map((d) => d.id)]);
  for (const id of deps) res.agentUpgradesExpired += await expireAgentUpgrades(id, now.getTime());
  // §21 #101 volume content: prune old AUTO snapshots (never versions a push / another version references; blobs are never deleted)
  res.volumeVersionsPruned = await pruneVolumeContent();
  // activity bundles: 90 days
  const files = await blobs().list('activity-bundles/');
  const bCut = now.getTime() - 90 * 86400_000;
  for (const f of files) {
    const created = f.createdMs;
    if (created > 0 && created < bCut) {
      await blobs().delete(f.path);
      res.bundlesDeleted++;
    }
  }
  void nowIso;
  await auditSystem({ job: 'daily', action: 'system.daily-cleanup', targetType: 'system', target: 'system/daily', ok: true, data: { ...res, startedAt: now.toISOString(), durationMs: Date.now() - t0 } });
  return res;
}
