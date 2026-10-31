/** Setup: access key, install tokens, agent enrollment revoke (SPEC-final §4.2, §4.6, §6.1 Setup). */
import { REVEAL_SECONDS, installTokenState, type AgentDoc, type CreateInstallTokenResponse, type GuiPort, type InstallToken, type InstallTokenView, type RotateAccessKeyResponse, type SetupResponse, type SiteUpgradeCommandView, type UpgradeCommandResponse } from '@opm/shared';
import { col, db, isoPlus, nowIso } from '../core/fire.js';
import { E } from '../core/errors.js';
import { loadDep, needDep } from '../core/perm.js';
import { asObject, keyFingerprint, onlyKeys, randomToken, sha256Hex } from '../core/util.js';
import { decodeDeployment } from '../core/codec.js';
import { CloudLog } from '../core/activities.js';
import { newAccessKey } from '../core/accesskey.js';
import { agentView } from '../core/views.js';
import { env } from '../core/env.js';
import { loadRevision } from './deployments.js';
import { loadDepsManifest } from './install.js';
import { agentReleaseInfo, applyRunExpiry, upgradeViewOf } from './agent-upgrade.js';
import type { Ctx, Handlers } from '../core/router.js';

const KIND_ORDER: Record<GuiPort['kind'], number> = { edge: 0, 'gui-alias': 1, 'agent-port': 2 };
/** §21 #86: edge ports the agent cannot bind (heartbeat edge.bindErrors, purpose edge-http / edge-https). */
export function portAlertsOf(a: AgentDoc): SetupResponse['sites'][number]['portAlerts'] {
  const errs = Array.isArray(a.edge?.bindErrors) ? a.edge!.bindErrors : [];
  return errs
    .filter((b) => b.purpose === 'edge-http' || b.purpose === 'edge-https')
    .map((b) => {
      const owner = typeof b.owner === 'string' && b.owner ? b.owner : null;
      return { port: b.port, purpose: b.purpose as 'edge-http' | 'edge-https', owner, message: `port ${b.port} is used by ${owner ?? 'another program'} — stop it or change edge.httpPort` };
    });
}
/** §21 #86: agent GUI URLs from the last heartbeat — addresses (IPv4 of detectedAddresses, max 3; else the first entry;
 *  else agent.ip) × guiPorts, port-major (http 80 first, then https 443, then edge, gui-alias, agent-port); deduped. */
export function guiUrlsOf(a: AgentDoc): SetupResponse['sites'][number]['guiUrls'] {
  const ports = Array.isArray(a.guiPorts) ? [...a.guiPorts] : [];
  if (!ports.length) return [];
  const da = Array.isArray(a.detectedAddresses) ? a.detectedAddresses : [];
  const v4 = da.filter((x) => /^\d{1,3}(\.\d{1,3}){3}$/.test(x)).slice(0, 3);
  const addrs = v4.length ? v4 : da.length ? [da[0]!] : a.ip ? [a.ip] : [];
  const rank = (g: GuiPort) => (g.scheme === 'http' && g.port === 80 ? 0 : g.scheme === 'https' && g.port === 443 ? 1 : 2 + KIND_ORDER[g.kind]);
  ports.sort((x, y) => rank(x) - rank(y) || x.port - y.port);
  const out: SetupResponse['sites'][number]['guiUrls'] = [];
  const seen = new Set<string>();
  for (const g of ports) {
    for (const addr of addrs) {
      const host = addr.includes(':') ? `[${addr}]` : addr;
      const dflt = (g.scheme === 'http' && g.port === 80) || (g.scheme === 'https' && g.port === 443);
      const url = `${g.scheme}://${host}${dflt ? '' : `:${g.port}`}/_opm/`;
      if (seen.has(url)) continue;
      seen.add(url);
      out.push({ url, kind: g.kind });
    }
  }
  return out;
}

export const INSTALL_TOKEN_TTL_MS = 24 * 3600_000;

export function apiBaseOf(c: Ctx): string {
  if (env.apiBase) return env.apiBase;
  const host = c.headers['x-forwarded-host'] ?? c.headers['host'] ?? 'localhost';
  const local = /^(127\.0\.0\.1|localhost|\[::1\])(:\d+)?$/.test(host);
  const proto = (c.headers['x-forwarded-proto'] ?? (local ? 'http' : 'https')).split(',')[0]!.trim();
  // the functions emulator strips /<project>/<region>/<function> before the handler sees the URL
  const mount = c.mount || (env.emulator && /:5001$/.test(host) ? `/${process.env.GCLOUD_PROJECT}/asia-south1/api` : '');
  return `${proto}://${host}${mount}`;
}
export function tokenState(t: InstallToken): InstallTokenView['state'] {
  return installTokenState(t);
}
const kindOf = (t: InstallToken) => (t.kind === 'maintenance' ? 'maintenance' : 'install');
const hasPlain = (t: InstallToken | null | undefined): t is InstallToken & { token: string } => !!t && typeof t.token === 'string' && t.token.length > 0;

/** InstallToken (stored, with the plaintext) → view: never the token (§21 #84). `upgradeHash` = the site's current
 *  upgrade command token (§21 #92). */
export function installTokenView(t: InstallToken, upgradeHash: string | null = null): InstallTokenView {
  const { token, ...rest } = t as InstallToken & Record<string, unknown>;
  void token;
  const state = tokenState(t);
  const kind = kindOf(t);
  return {
    ...(rest as Omit<InstallToken, 'token'>),
    kind,
    expiresAt: t.expiresAt ?? null,
    state,
    stored: kind === 'install' && state === 'active' && hasPlain(t),
    upgradeCommand: upgradeHash !== null && t.tokenHash === upgradeHash,
  } as InstallTokenView;
}
/** §21 #92 the token of an enrolled site's "Upgrade / reinstall command": the newest unrevoked maintenance token bound to the
 *  current enrollment, else the enrollment's own install token (usedAt = agent.enrolledAt, not revoked); null = none. */
export function upgradeTokenOf(toks: InstallToken[], a: Pick<AgentDoc, 'siteSlug' | 'enrolledAt'> | null | undefined): InstallToken | null {
  if (!a) return null;
  const mine = toks.filter((t) => t.siteSlug === a.siteSlug && !t.revokedAt);
  const m = mine.filter((t) => kindOf(t) === 'maintenance' && t.boundEnrolledAt === a.enrolledAt).sort((x, y) => (x.createdAt < y.createdAt ? 1 : -1))[0];
  return m ?? mine.find((t) => kindOf(t) === 'install' && t.usedAt === a.enrolledAt) ?? null;
}
/** tokens the upgrade command of this enrollment consists of (what Regenerate / revokeAgent end) */
function enrollmentTokens(toks: InstallToken[], a: Pick<AgentDoc, 'enrolledAt'>): InstallToken[] {
  return toks.filter((t) => !t.revokedAt && ((kindOf(t) === 'maintenance' && t.boundEnrolledAt === a.enrolledAt) || (kindOf(t) === 'install' && t.usedAt === a.enrolledAt)));
}
export function upgradeCommandView(t: InstallToken | null): SiteUpgradeCommandView {
  return { tokenHash: t?.tokenHash ?? null, kind: t ? kindOf(t) : null, stored: hasPlain(t), createdAt: t?.createdAt ?? null, createdBy: t?.createdBy ?? null };
}
export const installCommand = (c: Ctx, token: string) => `curl -fsSL -H "Authorization: Bearer ${token}" ${apiBaseOf(c)}/v1/install/script | sudo sh`;
function installTokenResponse(c: Ctx, t: InstallToken & { token: string }): CreateInstallTokenResponse {
  const command = installCommand(c, t.token);
  return { value: command, hideAfterSec: REVEAL_SECONDS, command, token: t.token, tokenHash: t.tokenHash, siteSlug: t.siteSlug, expiresAt: t.expiresAt ?? '' };
}
function upgradeCommandResponse(c: Ctx, t: InstallToken & { token: string }, enrolledAt: string): UpgradeCommandResponse {
  const command = installCommand(c, t.token);
  return { value: command, hideAfterSec: REVEAL_SECONDS, command, token: t.token, tokenHash: t.tokenHash, siteSlug: t.siteSlug, kind: kindOf(t), enrolledAt };
}

export const setupHandlers: Pick<Handlers, 'getSetup' | 'rotateAccessKey' | 'revealAccessKey' | 'createInstallToken' | 'revealInstallCommand' | 'revealUpgradeCommand' | 'regenerateUpgradeCommand' | 'revokeInstallToken' | 'revokeAgent'> = {
  async getSetup(c) {
    const dep = await loadDep(c);
    needDep(c, dep, 'deployment.setup', 'read');
    const [agents, tokens, head, deps] = await Promise.all([
      col.agents(dep.deploymentId).get(),
      col.installTokens(dep.deploymentId).get(),
      dep.head ? loadRevision(dep.deploymentId, dep.head.revId) : null,
      loadDepsManifest(),
    ]);
    const agentBy = new Map(agents.docs.map((d) => [d.id, agentView(d.data() as AgentDoc)]));
    const release = await agentReleaseInfo(deps);
    const now = Date.now();
    const labels = new Map<string, string>();
    for (const s of head?.config.sites ?? []) labels.set(s.siteSlug, s.label);
    for (const s of dep.draft.sites) labels.set(s.siteSlug, s.label);
    const slugs = [...new Set([...dep.draft.sites.map((s) => s.siteSlug), ...(head?.config.sites ?? []).map((s) => s.siteSlug), ...agentBy.keys()])].sort();
    const toks = tokens.docs.map((d) => d.data() as InstallToken).sort((a, b) => (a.createdAt < b.createdAt ? 1 : -1));
    const res: SetupResponse = {
      accessKey: { version: dep.accessKeyVersion, rotatedAt: dep.accessKeyRotatedAt, rotatedBy: dep.accessKeyRotatedBy, stored: typeof dep.accessKey === 'string' && dep.accessKey.length > 0 },
      apiBase: apiBaseOf(c),
      installDeps: deps ? { dockerVersion: deps.docker.version, minDockerApi: deps.minDockerApi, agentVersion: deps.agent?.version ?? null } : null,
      agentRelease: release,
      // an expired, unstarted request of the run's current site shows the run as failed (the daily job persists it)
      agentUpgradeRun: applyRunExpiry(dep.agentUpgradeRun, agentBy, now).run,
      serverTime: new Date(now).toISOString(),
      sites: slugs.map((slug) => {
        const a = agentBy.get(slug);
        const up = a ? upgradeTokenOf(toks, a) : null;
        return {
          siteSlug: slug,
          label: labels.get(slug) ?? slug,
          inDraft: dep.draft.sites.some((s) => s.siteSlug === slug),
          inHead: !!head?.config.sites.some((s) => s.siteSlug === slug),
          agent: a ? { agentVersion: a.agentVersion, enrolledAt: a.enrolledAt, lastSeenAt: a.lastSeenAt, online: a.online, hostname: a.hostname, ip: a.ip, keyVersion: a.keyVersion, os: a.os, arch: a.arch, advertiseHost: a.advertiseHost, detectedAddresses: a.detectedAddresses, clockSkew: a.clockSkew ?? null } as SetupResponse['sites'][number]['agent'] : null,
          upgradeCommand: a ? upgradeCommandView(up) : null,
          guiUrls: a ? guiUrlsOf(a) : [],
          defaultEdge: typeof a?.edge?.defaultEdge === 'boolean' ? a.edge.defaultEdge : null,
          portAlerts: a ? portAlertsOf(a) : [],
          upgrade: upgradeViewOf(a, release, now),
          tokens: toks
            .filter((t) => t.siteSlug === slug)
            .slice(0, 10)
            .map((t) => installTokenView(t, up?.tokenHash ?? null)),
        };
      }),
    };
    return res;
  },
  async rotateAccessKey(c) {
    const dep0 = await loadDep(c);
    needDep(c, dep0, 'deployment.setup', 'write');
    const key = await newAccessKey();
    const ref = col.deployments().doc(dep0.deploymentId);
    return db().runTransaction(async (tx) => {
      const dep = decodeDeployment((await tx.get(ref)).data()!);
      const version = dep.accessKeyVersion + 1;
      const rotatedAt = nowIso();
      const log = new CloudLog(dep.deploymentId, dep);
      log.add(tx, { type: 'access-key.rotated', summary: `Access key rotated (version ${version})`, data: { version }, actor: c.account!.uid });
      tx.update(ref, { accessKey: key.grouped, accessKeyHash: key.hash, accessKeyVersion: version, accessKeyRotatedAt: rotatedAt, accessKeyRotatedBy: c.account!.uid, agentEpoch: dep.agentEpoch + 1, ...log.depUpdate() });
      c.audit = { data: { version } };
      const res: RotateAccessKeyResponse = { value: key.grouped, hideAfterSec: REVEAL_SECONDS, accessKey: key.grouped, version, rotatedAt };
      return res;
    });
  },
  /** §21 #84: the stored key again (60 s in RevealValue); logged as access-key.revealed + audit. */
  async revealAccessKey(c) {
    const dep0 = await loadDep(c);
    needDep(c, dep0, 'deployment.setup', 'write');
    const ref = col.deployments().doc(dep0.deploymentId);
    return db().runTransaction(async (tx) => {
      const dep = decodeDeployment((await tx.get(ref)).data()!);
      const key = typeof dep.accessKey === 'string' && dep.accessKey ? dep.accessKey : null;
      if (!key) throw E.gone('the access key of this deployment is not stored (created before keys were kept) — regenerate it to view', { reason: 'not-stored' });
      const log = new CloudLog(dep.deploymentId, dep);
      log.add(tx, { type: 'access-key.revealed', summary: `Access key viewed (version ${dep.accessKeyVersion})`, data: { version: dep.accessKeyVersion }, actor: c.account!.uid });
      tx.update(ref, log.depUpdate());
      c.audit = { data: { version: dep.accessKeyVersion } };
      const res: RotateAccessKeyResponse = { value: key, hideAfterSec: REVEAL_SECONDS, accessKey: key, version: dep.accessKeyVersion, rotatedAt: dep.accessKeyRotatedAt };
      return res;
    });
  },
  async createInstallToken(c) {
    const dep0 = await loadDep(c);
    needDep(c, dep0, 'deployment.setup', 'write');
    const b = asObject(c.json());
    onlyKeys(b, ['siteSlug', 'replaceActive']);
    const slug = b.siteSlug;
    if (typeof slug !== 'string' || !slug) throw E.bad('siteSlug', 'V_REQUIRED', 'siteSlug is required');
    if (b.replaceActive !== undefined && typeof b.replaceActive !== 'boolean') throw E.bad('replaceActive', 'V_TYPE', 'replaceActive must be true / false');
    const replace = b.replaceActive === true;
    if (!dep0.draft.sites.some((s) => s.siteSlug === slug)) throw E.notFound(`site ${slug} is not in the draft`);
    const token = `opi_${randomToken(32)}`;
    const tokenHash = sha256Hex(token);
    const ref = col.deployments().doc(dep0.deploymentId);
    const out = await db().runTransaction(async (tx) => {
      const dep = decodeDeployment((await tx.get(ref)).data()!);
      const agent = await tx.get(col.agents(dep.deploymentId).doc(slug));
      if (agent.exists) throw E.conflict(`site ${slug} is enrolled (revoke the enrollment first)`);
      // Regenerate (replaceActive): revoke this site's active tokens in the same transaction
      const active = replace ? (await tx.get(col.installTokens(dep.deploymentId).where('siteSlug', '==', slug))).docs.filter((d) => kindOf(d.data() as InstallToken) === 'install' && tokenState(d.data() as InstallToken) === 'active') : [];
      const now = nowIso();
      const t: InstallToken = { tokenHash, siteSlug: slug, token, expiresAt: isoPlus(INSTALL_TOKEN_TTL_MS), usedAt: null, revokedAt: null, createdAt: now, createdBy: c.account!.uid };
      const log = new CloudLog(dep.deploymentId, dep);
      for (const d of active) {
        tx.update(d.ref, { revokedAt: now, token: null });
        log.add(tx, { type: 'install-token.revoked', summary: `Install token for site ${slug} revoked (replaced by a new install command)`, data: { siteSlug: slug, tokenHash: d.id }, actor: c.account!.uid });
      }
      tx.create(col.installTokens(dep.deploymentId).doc(tokenHash), t);
      log.add(tx, { type: 'install-token.created', summary: `Install command created for site ${slug}`, data: { siteSlug: slug, tokenHash, expiresAt: t.expiresAt, ...(replace ? { replaced: active.length } : {}) }, actor: c.account!.uid });
      tx.update(ref, log.depUpdate());
      c.audit = { data: { siteSlug: slug, tokenHash, ...(replace ? { replaced: active.map((d) => d.id) } : {}) } };
      return t;
    });
    return installTokenResponse(c, { ...out, token });
  },
  /** §21 #84: the install command of an ACTIVE token again; gone reason used | revoked | expired | not-stored. */
  async revealInstallCommand(c) {
    const dep0 = await loadDep(c);
    needDep(c, dep0, 'deployment.setup', 'write');
    const ref = col.deployments().doc(dep0.deploymentId);
    const tRef = col.installTokens(dep0.deploymentId).doc(c.params.tokenHash!);
    return db().runTransaction(async (tx) => {
      const [ds, ts] = await Promise.all([tx.get(ref), tx.get(tRef)]);
      if (!ts.exists) throw E.notFound('install token not found');
      const t = ts.data() as InstallToken;
      c.audit = { data: { siteSlug: t.siteSlug, tokenHash: t.tokenHash } };
      const state = tokenState(t);
      if (kindOf(t) === 'maintenance') throw E.gone('this is the upgrade command of an enrolled site — view it with the site\'s "Upgrade / reinstall command"', { reason: 'maintenance' });
      if (state === 'used') throw E.gone('the install token was already used (the site enrolled) — create a new install command', { reason: 'used' });
      if (state === 'revoked') throw E.gone('the install token was revoked — create a new install command', { reason: 'revoked' });
      if (state === 'expired') throw E.gone('the install token expired — create a new install command', { reason: 'expired' });
      if (typeof t.token !== 'string' || !t.token) throw E.gone('the install command of this token is not stored (created before commands were kept) — regenerate it to view', { reason: 'not-stored' });
      const dep = decodeDeployment(ds.data()!);
      const log = new CloudLog(dep.deploymentId, dep);
      log.add(tx, { type: 'install-token.revealed', summary: `Install command for site ${t.siteSlug} viewed`, data: { siteSlug: t.siteSlug, tokenHash: t.tokenHash }, actor: c.account!.uid });
      tx.update(ref, log.depUpdate());
      return installTokenResponse(c, t as InstallToken & { token: string });
    });
  },
  /** §21 #92: the enrolled site's upgrade / reinstall command (60 s); install-command.revealed + audit. */
  async revealUpgradeCommand(c) {
    const dep0 = await loadDep(c);
    needDep(c, dep0, 'deployment.setup', 'write');
    const slug = c.params.siteSlug!;
    c.audit = { data: { siteSlug: slug } };
    const ref = col.deployments().doc(dep0.deploymentId);
    const aRef = col.agents(dep0.deploymentId).doc(slug);
    return db().runTransaction(async (tx) => {
      const [ds, as, tq] = await Promise.all([tx.get(ref), tx.get(aRef), tx.get(col.installTokens(dep0.deploymentId).where('siteSlug', '==', slug))]);
      if (!as.exists) throw E.notFound(`site ${slug} is not enrolled (create an install command instead)`);
      const a = as.data() as AgentDoc;
      const t = upgradeTokenOf(tq.docs.map((d) => d.data() as InstallToken), a);
      if (t) c.audit = { data: { siteSlug: slug, tokenHash: t.tokenHash, kind: kindOf(t) } };
      if (!hasPlain(t)) throw E.gone('the upgrade command of this site is not stored (its token was created before commands were kept) — Regenerate to view a new one', { reason: 'not-stored' });
      const dep = decodeDeployment(ds.data()!);
      const log = new CloudLog(dep.deploymentId, dep);
      log.add(tx, { type: 'install-command.revealed', summary: `Upgrade / reinstall command for site ${slug} viewed`, data: { siteSlug: slug, tokenHash: t.tokenHash, kind: kindOf(t) }, actor: c.account!.uid });
      tx.update(ref, log.depUpdate());
      return upgradeCommandResponse(c, t, a.enrolledAt);
    });
  },
  /** §21 #92 Regenerate: a new site-bound maintenance token; the previous upgrade command (the enrollment's install token and
   *  older maintenance tokens) is revoked in the same transaction. install-command.regenerated + audit. */
  async regenerateUpgradeCommand(c) {
    const dep0 = await loadDep(c);
    needDep(c, dep0, 'deployment.setup', 'write');
    const slug = c.params.siteSlug!;
    c.audit = { data: { siteSlug: slug } };
    const token = `opi_${randomToken(32)}`;
    const tokenHash = sha256Hex(token);
    const ref = col.deployments().doc(dep0.deploymentId);
    const aRef = col.agents(dep0.deploymentId).doc(slug);
    return db().runTransaction(async (tx) => {
      const [ds, as, tq] = await Promise.all([tx.get(ref), tx.get(aRef), tx.get(col.installTokens(dep0.deploymentId).where('siteSlug', '==', slug))]);
      if (!as.exists) throw E.notFound(`site ${slug} is not enrolled (create an install command instead)`);
      const a = as.data() as AgentDoc;
      const dep = decodeDeployment(ds.data()!);
      const now = nowIso();
      const old = enrollmentTokens(tq.docs.map((d) => d.data() as InstallToken), a);
      for (const o of old) tx.update(col.installTokens(dep.deploymentId).doc(o.tokenHash), { revokedAt: now, token: null });
      const t: InstallToken = { tokenHash, siteSlug: slug, kind: 'maintenance', token, expiresAt: null, boundEnrolledAt: a.enrolledAt, usedAt: null, revokedAt: null, createdAt: now, createdBy: c.account!.uid };
      tx.create(col.installTokens(dep.deploymentId).doc(tokenHash), t);
      const revoked = old.map((o) => o.tokenHash);
      const log = new CloudLog(dep.deploymentId, dep);
      log.add(tx, { type: 'install-command.regenerated', summary: `Upgrade / reinstall command for site ${slug} regenerated${revoked.length ? ' (the previous command no longer works)' : ''}`, data: { siteSlug: slug, tokenHash, revoked }, actor: c.account!.uid });
      tx.update(ref, log.depUpdate());
      c.audit = { data: { siteSlug: slug, tokenHash, kind: 'maintenance', revoked } };
      return upgradeCommandResponse(c, t as InstallToken & { token: string }, a.enrolledAt);
    });
  },
  async revokeInstallToken(c) {
    const dep0 = await loadDep(c);
    needDep(c, dep0, 'deployment.setup', 'write');
    const ref = col.deployments().doc(dep0.deploymentId);
    const tRef = col.installTokens(dep0.deploymentId).doc(c.params.tokenHash!);
    await db().runTransaction(async (tx) => {
      const [ds, ts] = await Promise.all([tx.get(ref), tx.get(tRef)]);
      if (!ts.exists) throw E.notFound('install token not found');
      const t = ts.data() as InstallToken;
      if (t.usedAt) throw E.gone('the install token was already used');
      if (t.revokedAt) return;
      const dep = decodeDeployment(ds.data()!);
      tx.update(tRef, { revokedAt: nowIso(), token: null });
      const log = new CloudLog(dep.deploymentId, dep);
      log.add(tx, { type: 'install-token.revoked', summary: `Install token for site ${t.siteSlug} revoked`, data: { siteSlug: t.siteSlug, tokenHash: t.tokenHash }, actor: c.account!.uid });
      tx.update(ref, log.depUpdate());
    });
    return { revoked: true };
  },
  async revokeAgent(c) {
    const dep0 = await loadDep(c);
    needDep(c, dep0, 'deployment.setup', 'write');
    const slug = c.params.siteSlug!;
    const b = asObject(c.json());
    if (b.confirm !== slug) throw E.bad('confirm', 'V_FORMAT', 'confirm must equal the site slug');
    const ref = col.deployments().doc(dep0.deploymentId);
    const aRef = col.agents(dep0.deploymentId).doc(slug);
    await db().runTransaction(async (tx) => {
      const [ds, as, tq] = await Promise.all([tx.get(ref), tx.get(aRef), tx.get(col.installTokens(dep0.deploymentId).where('siteSlug', '==', slug))]);
      if (!as.exists) throw E.notFound(`site ${slug} is not enrolled`);
      const a = as.data() as AgentDoc;
      const dep = decodeDeployment(ds.data()!);
      const fp = keyFingerprint(a.publicKey);
      const now = nowIso();
      tx.set(col.revokedKeys(dep.deploymentId).doc(fp), { siteSlug: slug, publicKey: a.publicKey, revokedAt: now });
      tx.delete(aRef);
      // §21 #92: the enrollment's upgrade command dies with it (maintenance tokens revoked; the used install token loses its plaintext)
      const ended = enrollmentTokens(tq.docs.map((d) => d.data() as InstallToken), a);
      for (const t of ended) tx.update(col.installTokens(dep.deploymentId).doc(t.tokenHash), kindOf(t) === 'maintenance' ? { revokedAt: now, token: null } : { token: null });
      const log = new CloudLog(dep.deploymentId, dep);
      log.add(tx, { type: 'agent.revoked', summary: `Enrollment of site ${slug} revoked`, data: { siteSlug: slug, publicKeyFingerprint: fp, upgradeCommandsEnded: ended.length }, actor: c.account!.uid });
      tx.update(ref, { ...log.depUpdate(), agentEpoch: dep.agentEpoch + 1 });
    });
    return { revoked: true };
  },
};
