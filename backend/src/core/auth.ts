/**
 * Authentication for the four trust paths that reach the cloud (SPEC-final §4, §5.1):
 *   human   — Firebase ID token (verifyIdToken checkRevoked) + accounts/{uid}
 *   install — install token "opi_…" (doc id = sha256 hex of the token)
 *   agent   — Ed25519 signed request (X-OPM-Site / X-OPM-Ts / X-OPM-Nonce / X-OPM-Sig)
 */
import { createPublicKey, verify as edVerify, type KeyObject } from 'node:crypto';
import { Timestamp } from '../driver/index.js';
import { signedRequestString, type Account, type AgentDoc, type InstallToken, type InstallTokenKind } from '@opm/shared';
import { auth, col, isAlreadyExists, db } from './fire.js';
import { ApiError, E } from './errors.js';
import { sha256Hex } from './util.js';
import { decodeDeployment, type StoredDeployment } from './codec.js';
import { accountView } from './views.js';

// ───────────────────────────────────────── human
export async function verifyHuman(authorization: string | undefined): Promise<Account> {
  const m = /^Bearer\s+(.+)$/i.exec(authorization ?? '');
  if (!m || m[1]!.startsWith('opi_')) throw E.unauth('missing Authorization: Bearer <Firebase ID token>');
  let uid: string;
  let email: string | null = null;
  try {
    const decoded = await auth().verifyIdToken(m[1]!, true);
    uid = decoded.uid;
    email = decoded.email ?? null;
  } catch (e) {
    const code = (e as { code?: string }).code ?? '';
    if (code === 'auth/user-disabled') throw E.forbidden('account disabled');
    throw E.unauth(`invalid ID token (${code || 'verify failed'})`);
  }
  const snap = await col.accounts().doc(uid).get();
  // a signed-in person without a usable account: 403, still audited with this actor (audit.ts ActorOnError)
  if (!snap.exists) throw Object.assign(E.forbidden('no account'), { auditActor: { uid, email } });
  const acc = accountView(snap.data()!); // never carries stored credential values (§21 #84)
  if (acc.disabled) throw Object.assign(E.forbidden('account disabled'), { auditActor: { uid, email: acc.email ?? email } });
  return acc;
}

// ───────────────────────────────────────── install token
export interface InstallAuth {
  token: string;
  tokenHash: string;
  deploymentId: string;
  siteSlug: string;
  tokenDoc: InstallToken;
  /** §21 #86: install = unused active token; upgrade = the token that enrolled the site's CURRENT enrollment, or (§21 #92)
   *  a maintenance token bound to it */
  mode: 'install' | 'upgrade';
  /** §21 #92 'install' (default) | 'maintenance' */
  kind: InstallTokenKind;
}
export function bearerInstallToken(authorization: string | undefined): string {
  const m = /^Bearer\s+(opi_[A-Za-z0-9_-]+)$/i.exec(authorization ?? '');
  if (!m) throw E.unauth('missing Authorization: Bearer opi_… (install token)');
  return m[1]!;
}
/** Token check (script, binary, deps, release): exists, deployment not deleted, and
 *  - install token: unused + not revoked + not expired (install mode), or used for the CURRENT enrollment (upgrade mode, §21 #86);
 *  - maintenance token (§21 #92): not revoked + bound to the CURRENT enrollment (upgrade mode only; never install). */
export async function verifyInstallToken(authorization: string | undefined): Promise<InstallAuth> {
  const token = bearerInstallToken(authorization);
  const tokenHash = sha256Hex(token);
  const q = await db().collectionGroup('installTokens').where('tokenHash', '==', tokenHash).limit(1).get();
  if (q.empty) throw E.unauth('unknown install token');
  const snap = q.docs[0]!;
  const t = snap.data() as InstallToken;
  const deploymentId = snap.ref.parent.parent!.id;
  const kind: InstallTokenKind = t.kind === 'maintenance' ? 'maintenance' : 'install';
  let mode: InstallAuth['mode'] = 'install';
  if (kind === 'maintenance') {
    checkMaintenanceState(t, await currentEnrolledAt(deploymentId, t.siteSlug));
    mode = 'upgrade';
  } else if (t.usedAt && !t.revokedAt && (await currentEnrolledAt(deploymentId, t.siteSlug)) === t.usedAt) mode = 'upgrade';
  else checkTokenState(t);
  const dep = await col.deployments().doc(deploymentId).get();
  if (!dep.exists || dep.data()!.deleted) throw E.gone('the deployment was deleted', { reason: 'deleted' });
  return { token, tokenHash, deploymentId, siteSlug: t.siteSlug, tokenDoc: t, mode, kind };
}
/** §21 #86 upgrade rule: a used token stays valid (script / binary / deps, no expiry) while the enrollment it created is
 *  the site's current one — the enroll transaction writes the same timestamp to token.usedAt and agent.enrolledAt.
 *  revokeAgent (agent doc deleted) or a re-enroll with a newer token ends it. null = the site is not enrolled. */
async function currentEnrolledAt(deploymentId: string, siteSlug: string): Promise<string | null> {
  const a = await col.agents(deploymentId).doc(siteSlug).get();
  return a.exists ? ((a.data() as AgentDoc).enrolledAt ?? null) : null;
}
/** §21 #92 a maintenance token works only while not revoked and bound to the site's current enrollment. */
export function checkMaintenanceState(t: InstallToken, enrolledAt: string | null) {
  if (t.revokedAt) throw E.gone('this upgrade command was regenerated or revoked', { reason: 'revoked', siteSlug: t.siteSlug });
  if (!enrolledAt || !t.boundEnrolledAt || t.boundEnrolledAt !== enrolledAt) throw E.gone('the enrollment this upgrade command belongs to was revoked', { reason: 'enrollment-ended', siteSlug: t.siteSlug });
}
export function checkTokenState(t: InstallToken) {
  if (t.kind === 'maintenance') throw E.forbidden('an upgrade (maintenance) command cannot enroll a site — create an install command for a new site', { reason: 'maintenance', siteSlug: t.siteSlug });
  if (t.revokedAt) throw E.gone('the install token was revoked', { reason: 'revoked', siteSlug: t.siteSlug });
  if (t.usedAt) throw E.gone('the install token was already used', { reason: 'used', siteSlug: t.siteSlug });
  if (!t.expiresAt || Date.parse(t.expiresAt) <= Date.now()) throw E.gone('the install token expired', { reason: 'expired', siteSlug: t.siteSlug });
}

// ───────────────────────────────────────── agent signed requests (§4.4)
export interface AgentAuth {
  deploymentId: string;
  siteSlug: string;
  agent: AgentDoc;
  dep: StoredDeployment;
  /** set when the request arrived through POST /v1/agent/relay */
  relayedBy: string | null;
}
const CLOCK_SKEW_SEC = 60;
const NONCE_TTL_MS = 5 * 60 * 1000;

const keyCache = new Map<string, KeyObject>();
export function ed25519Key(publicKeyB64: string): KeyObject {
  let k = keyCache.get(publicKeyB64);
  if (!k) {
    const raw = Buffer.from(publicKeyB64, 'base64');
    if (raw.length !== 32) throw new Error('public key must be 32 bytes');
    k = createPublicKey({ key: { kty: 'OKP', crv: 'Ed25519', x: raw.toString('base64url') }, format: 'jwk' });
    keyCache.set(publicKeyB64, k);
  }
  return k;
}
export function verifyEd25519(publicKeyB64: string, message: Buffer | string, sigB64url: string): boolean {
  try {
    const sig = Buffer.from(sigB64url, 'base64url');
    if (sig.length !== 64) return false;
    return edVerify(null, Buffer.isBuffer(message) ? message : Buffer.from(message, 'utf8'), ed25519Key(publicKeyB64), sig);
  } catch {
    return false;
  }
}

function agentErr(reason: 'clock' | 'nonce' | 'signature' | 'unknown-site', message: string) {
  return new ApiError('unauthenticated', message, { reason });
}

export interface SignedParts {
  method: string;
  /** path starting at /v1/ including ?query exactly as sent */
  path: string;
  headers: Record<string, string | undefined>; // lower-case names
  body: Buffer;
}
/**
 * Verify a signed request. Order: site → clock → signature → nonce (a bad signature never burns a nonce).
 * allowDeleted: deleted deployments still authenticate (poll answers deleted:true).
 */
export async function verifyAgent(p: SignedParts, opts: { allowDeleted?: boolean } = {}): Promise<AgentAuth> {
  const site = p.headers['x-opm-site'];
  const ts = p.headers['x-opm-ts'];
  const nonce = p.headers['x-opm-nonce'];
  const sig = p.headers['x-opm-sig'];
  if (!site || !ts || !nonce || !sig) throw agentErr('signature', 'missing X-OPM-Site / X-OPM-Ts / X-OPM-Nonce / X-OPM-Sig');
  const m = /^([A-Za-z0-9]{1,64})\/([a-z][a-z0-9-]{1,39})$/.exec(site);
  if (!m) throw agentErr('unknown-site', 'X-OPM-Site must be <deploymentId>/<siteSlug>');
  const [, deploymentId, siteSlug] = m as unknown as [string, string, string];
  const [depSnap, agentSnap] = await db().getAll(col.deployments().doc(deploymentId), col.agents(deploymentId).doc(siteSlug));
  if (!depSnap!.exists) throw agentErr('unknown-site', 'unknown deployment');
  const dep = decodeDeployment(depSnap!.data()!);
  if (dep.deleted && !opts.allowDeleted) throw agentErr('unknown-site', 'the deployment was deleted');
  if (!agentSnap!.exists) throw agentErr('unknown-site', `site ${siteSlug} is not enrolled`);
  const agent = agentSnap!.data() as AgentDoc;
  if (!/^\d{1,12}$/.test(ts)) throw agentErr('clock', 'X-OPM-Ts must be unix seconds');
  const skew = Math.abs(Math.floor(Date.now() / 1000) - Number(ts));
  if (skew > CLOCK_SKEW_SEC) throw agentErr('clock', `clock skew ${skew} s > ${CLOCK_SKEW_SEC} s (use X-OPM-Server-Time)`);
  if (!/^[A-Za-z0-9_-]{16,64}$/.test(nonce)) throw agentErr('nonce', 'X-OPM-Nonce must be base64url');
  const str = signedRequestString(p.method, p.path, ts, nonce, sha256Hex(p.body));
  if (!verifyEd25519(agent.publicKey, str, sig)) throw agentErr('signature', 'signature does not verify with the enrolled site key');
  const nonceId = sha256Hex(`${deploymentId}/${siteSlug}/${nonce}`);
  try {
    await col.agentNonces().doc(nonceId).create({ expiresAt: Timestamp.fromMillis(Date.now() + NONCE_TTL_MS) });
  } catch (e) {
    if (!isAlreadyExists(e)) throw e;
    // an expired entry that TTL has not removed yet does not count
    const old = await col.agentNonces().doc(nonceId).get();
    const exp = (old.data()?.expiresAt as Timestamp | undefined)?.toMillis() ?? 0;
    if (exp > Date.now()) throw agentErr('nonce', 'nonce already used (replay)');
    await col.agentNonces().doc(nonceId).set({ expiresAt: Timestamp.fromMillis(Date.now() + NONCE_TTL_MS) });
  }
  return { deploymentId, siteSlug, agent, dep, relayedBy: null };
}
