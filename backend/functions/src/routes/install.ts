/** Install path: script, token-gated agent binary, enroll (SPEC-final §4.2, §19.1, §19.2). */
import { createHash } from 'node:crypto';
import { supportsGuiUpgrade, type AgentDoc, type AgentReleaseResponse, type DepsManifest, type EnrollRequest, type EnrollResponse, type InstallToken, type PeerInfo } from '@opm/shared';
import { col, db, nowIso } from '../core/fire.js';
import { ApiError, E } from '../core/errors.js';
import { bearerInstallToken, checkTokenState, verifyInstallToken } from '../core/auth.js';
import { asObject, keyFingerprint, sha256Hex, V } from '../core/util.js';
import { decodeDeployment } from '../core/codec.js';
import { CloudLog } from '../core/activities.js';
import { env } from '../core/env.js';
import { noRedirectFor, paths, readText, serveObject, sizeOf } from '../core/storage.js';
import { agentReleaseInfo } from './agent-upgrade.js';
import { Reply } from '../core/reply.js';
import { apiBaseOf } from './setup.js';
import type { Handlers } from '../core/router.js';
import { renderInstallScript } from './install-script.js';
import { loadRevision } from './deployments.js';

export const SYSTEMD_UNIT = `[Unit]
Description=On-Prem Manager agent
After=network-online.target docker.service
Wants=network-online.target
Requires=docker.service
[Service]
Type=simple
ExecStart=/usr/local/bin/opm-agent run --config /etc/opm/agent.json
Restart=always
RestartSec=5
LimitNOFILE=65536
KillMode=process
[Install]
WantedBy=multi-user.target
`;

const shq = (s: string) => s.replace(/[^A-Za-z0-9._~:/?#@!$&'()*+,;=%-]/g, '');
/** an IPv4 / IPv6 literal or '' (shell-safe) */
const shIp = (s: string | null | undefined) => {
  const v = (s ?? '').trim().replace(/^::ffff:/, '');
  return /^[0-9.]{7,15}$/.test(v) || /^[0-9A-Fa-f:]{2,39}$/.test(v) ? v : '';
};
const shNum = (n: number | null | undefined) => (typeof n === 'number' && Number.isInteger(n) && n > 0 ? String(n) : '');

export type InstallArch = 'amd64' | 'arm64';
export interface InstallScriptOpts {
  apiBase: string;
  deploymentId: string;
  siteSlug: string;
  token: string;
  releasePublicKey: string;
  /** pinned agent release (exact version; sha256 hex + release signature per linux arch; a missing arch → the script fails clearly) */
  agent: { version: string; files: Partial<Record<InstallArch, { sha256: string; sig: string }>>; schema?: number | null; minSchema?: number | null } | null;
  /** pinned Docker static bundle (deps/manifest.json); null = none uploaded → manual Docker install only */
  docker: { version: string; minApi: string; sha256: Partial<Record<InstallArch, string>> } | null;
  ports?: { agent?: number | null; alias?: number | null; edge?: number | null };
  /** §21 #86: true = the token already enrolled this site → upgrade-only script (never writes the token) */
  upgradeOnly?: boolean;
  /** §21 #86: the client IP the cloud saw when the script was requested (self-check of the public address) */
  seenIp?: string | null;
  /** §21 #91: the script was requested with ?version=<v> (repeated in the printed rerun command) */
  requestedVersion?: string | null;
  /** §21 #92 the token kind (maintenance = the site's regenerated upgrade command; always upgrade-only) */
  kind?: 'install' | 'maintenance';
}

/** §19.1 install script: pinned versions + sha256 embedded at generation (never "latest" at run time). */
export function installScript(o: InstallScriptOpts): string {
  const p = o.ports ?? {};
  return renderInstallScript({
    API: shq(o.apiBase),
    DEP: shq(o.deploymentId),
    SITE: shq(o.siteSlug),
    TOKEN: shq(o.token),
    RELEASE_PUBKEY: shq(o.releasePublicKey),
    AGENT_VERSION: shq(o.agent?.version ?? ''),
    AGENT_SCHEMA: shNum(o.agent?.schema),
    AGENT_MIN_SCHEMA: Number.isInteger(o.agent?.minSchema) && (o.agent!.minSchema as number) >= 0 ? String(o.agent!.minSchema) : '',
    SCRIPT_QS: o.requestedVersion && VERSION_RE.test(o.requestedVersion) ? `?version=${o.requestedVersion}` : '',
    AGENT_SHA_AMD64: shq(o.agent?.files.amd64?.sha256 ?? ''),
    AGENT_SIG_AMD64: shq(o.agent?.files.amd64?.sig ?? ''),
    AGENT_SHA_ARM64: shq(o.agent?.files.arm64?.sha256 ?? ''),
    AGENT_SIG_ARM64: shq(o.agent?.files.arm64?.sig ?? ''),
    DOCKER_VERSION: shq(o.docker?.version ?? ''),
    MIN_DOCKER_API: shq(o.docker?.minApi ?? DEFAULT_MIN_DOCKER_API),
    MAX_DOCKER_API: AGENT_MAX_DOCKER_API,
    DOCKER_SHA_AMD64: shq(o.docker?.sha256.amd64 ?? ''),
    DOCKER_SHA_ARM64: shq(o.docker?.sha256.arm64 ?? ''),
    AGENT_PORT: shNum(p.agent) || '7080',
    ALIAS_PORT: p.alias === null ? '' : shNum(p.alias) || '8080',
    EDGE_PORT: shNum(p.edge),
    SYSTEMD_UNIT,
    UPGRADE_ONLY: o.upgradeOnly || o.kind === 'maintenance' ? '1' : '0',
    CMD_DESC: o.kind === 'maintenance' ? 'This upgrade command belongs to the enrolled site $SITE' : 'This install command already enrolled site $SITE',
    SEEN_IP: shIp(o.seenIp),
    HTTP_PORT: '80',
    // §21 #90: the pinned agent upgrades itself from the GUIs (final message + boot guard expectation)
    GUI_UPGRADE: supportsGuiUpgrade(o.agent?.version) ? '1' : '0',
  });
}
/** The agent negotiates the Docker Engine API between 1.41 (floor = Docker 20.10, user decision; deps/manifest.json
 *  minDockerApi overrides) and 1.47 (cap): a daemon fits when its max API ≥ the floor and its MinAPIVersion ≤ the cap
 *  (Docker 20.10 … 29+). */
export const DEFAULT_MIN_DOCKER_API = '1.41';
export const VERSION_RE = /^[0-9A-Za-z._+-]{1,64}$/;
export const AGENT_MAX_DOCKER_API = '1.47';

/** Agent release for the script: `requested` (§21 #91 ?version=), else the deps manifest agent version, else
 *  OPM_AGENT_VERSION / agent/latest; schema / minSchema from agent/<v>/release.json. */
async function pinnedAgent(manifestVersion: string | null): Promise<InstallScriptOpts['agent']> {
  let version: string;
  try {
    version = await resolveAgentVersion(manifestVersion);
  } catch {
    return null;
  }
  const files: Partial<Record<InstallArch, { sha256: string; sig: string }>> = {};
  for (const arch of ['amd64', 'arm64'] as const) {
    const bin = paths.agentBinary(version, 'linux', arch);
    const [sha, sig] = await Promise.all([readText(`${bin}.sha256`).catch(() => null), readText(`${bin}.sig`).catch(() => null)]);
    if (sha && sig) files[arch] = { sha256: sha.trim().split(/\s+/)[0]!, sig: sig.trim() };
  }
  const meta = await buildMeta(version);
  return { version, files, schema: meta?.schema ?? null, minSchema: meta?.minSchema ?? null };
}

/** §21 #91 agent/<v>/release.json (upload-agent.ts from make dist's dist/release.json); null = none (builds before
 *  0.10.0 → AGENT_LEGACY_SCHEMA). */
export const releaseMetaPath = (version: string) => `agent/${version}/release.json`;
export async function buildMeta(version: string): Promise<{ schema: number; minSchema: number } | null> {
  const t = await readText(releaseMetaPath(version)).catch(() => null);
  if (!t) return null;
  try {
    const j = JSON.parse(t) as { schema?: unknown; minSchema?: unknown };
    const ok = (n: unknown) => Number.isInteger(n) && (n as number) >= 0;
    return ok(j.schema) && ok(j.minSchema) ? { schema: j.schema as number, minSchema: j.minSchema as number } : null;
  } catch {
    return null;
  }
}
const schemaHeaders = (m: { schema: number; minSchema: number } | null): Record<string, string> => (m ? { 'X-OPM-Agent-Schema': String(m.schema), 'X-OPM-Agent-Min-Schema': String(m.minSchema) } : {});

/** §21 #91: curl -f hides error bodies, so an unusable token gets a tiny script that says why (exit 1). */
const shSingle = (s: string) => `'${s.replace(/'/g, `'\\''`)}'`;
export function errorScript(lines: string[]): string {
  return ['#!/bin/sh', '# On-Prem Manager agent installer: this install command cannot be used', ...lines.map((l) => `printf '%s\\n' ${shSingle(l)} >&2`), 'exit 1', ''].join('\n');
}
const REGEN = 'open the cloud Setup page → Install command → Regenerate (or View) and run the new command';
export function tokenProblemText(reason: string, siteSlug: string | null): string[] {
  const site = siteSlug ? ` (site ${siteSlug})` : '';
  switch (reason) {
    case 'expired':
      return [`OPM install: token expired${site} — ${REGEN}.`, 'Nothing was changed on this server.'];
    case 'revoked':
      return [`OPM install: token revoked${site} — ${REGEN}.`, 'Nothing was changed on this server.'];
    case 'used':
      return [`OPM install: this install command was used for an earlier enrollment${site}; that site was revoked or enrolled again with a newer command since — ${REGEN}.`, 'Nothing was changed on this server.'];
    case 'enrollment-ended':
      return [`OPM install: this upgrade command belongs to an enrollment${site} that was revoked — create a new install command for the site in the cloud Setup page.`, 'Nothing was changed on this server.'];
    case 'deleted':
      return ['OPM install: the deployment of this install command was deleted.', 'Nothing was changed on this server.'];
    default:
      return ['OPM install: unknown install token — copy the install command again from the cloud Setup page (View / Regenerate).', 'Nothing was changed on this server.'];
  }
}
const scriptReply = (body: string) => new Reply(200, { 'Content-Type': 'text/x-shellscript; charset=utf-8', 'Cache-Control': 'no-store' }, body);
/** release metadata of one uploaded version (sha256, signature, size, schema); not-found when that os/arch is missing */
async function releaseOf(version: string, os: string, arch: string) {
  const bin = paths.agentBinary(version, os, arch);
  const [sha, sig, size, meta] = await Promise.all([readText(`${bin}.sha256`).catch(() => null), readText(`${bin}.sig`).catch(() => null), sizeOf(bin).catch(() => null), buildMeta(version)]);
  if (!sha || !sig) throw E.notFound(`agent ${version} for ${os}/${arch} is not uploaded`);
  return { version, sha256: sha.trim().split(/\s+/)[0]!, signature: sig.trim(), sizeBytes: size, meta };
}

/** Storage deps/manifest.json (scripts/upload-deps.ts) — pinned install dependencies; null when not uploaded / unreadable. */
export const DEPS_MANIFEST = 'deps/manifest.json';
export async function loadDepsManifest(): Promise<DepsManifest | null> {
  const t = await readText(DEPS_MANIFEST).catch(() => null);
  if (!t) return null;
  try {
    const m = JSON.parse(t) as DepsManifest;
    return m && m.docker && m.docker.files ? m : null;
  } catch {
    return null;
  }
}

export async function resolveAgentVersion(requested: string | null): Promise<string> {
  if (requested) return requested;
  if (env.agentVersion) return env.agentVersion;
  const latest = (await readText(paths.agentLatest))?.trim();
  if (!latest) throw E.notFound('no agent release is uploaded (agent/latest missing)');
  return latest;
}

/** Round C: EnrollRequest / HeartbeatRequest `detectedAddresses` (optional; old agents omit it → []). ≤ 32 strings, each 1–255. */
export const DETECTED_ADDRESSES_MAX = 32;
export function detectedAddressesError(v: unknown): string | null {
  if (v === undefined || v === null) return null;
  if (!Array.isArray(v)) return 'detectedAddresses must be a list of strings';
  if (v.length > DETECTED_ADDRESSES_MAX) return `detectedAddresses has at most ${DETECTED_ADDRESSES_MAX} entries`;
  for (const x of v) if (typeof x !== 'string' || !x || x.length > 255) return 'detectedAddresses entries must be strings of 1–255 characters';
  return null;
}
export const detectedAddressesOf = (v: unknown): string[] => (Array.isArray(v) ? (v as string[]).slice() : []);

function validateEnroll(b: Record<string, unknown>): EnrollRequest {
  const v = new V();
  let raw: Buffer | null = null;
  if (typeof b.publicKey === 'string') raw = Buffer.from(b.publicKey, 'base64');
  if (!raw || raw.length !== 32 || raw.toString('base64') !== b.publicKey) v.err('publicKey', 'V_FORMAT', 'publicKey must be the raw 32-byte Ed25519 key, base64 (std, padded)');
  for (const k of ['agentVersion', 'hostname', 'ip', 'advertiseHost', 'os', 'arch'] as const) if (typeof b[k] !== 'string' || (b[k] as string).length > 255) v.err(k, 'V_TYPE', `${k} must be a string`);
  if (typeof b.advertiseHost === 'string' && !b.advertiseHost) v.err('advertiseHost', 'V_REQUIRED', 'advertiseHost is required');
  if (!Number.isInteger(b.agentPort) || (b.agentPort as number) < 1 || (b.agentPort as number) > 65535) v.err('agentPort', 'V_RANGE', 'agentPort must be 1–65535');
  const da = detectedAddressesError(b.detectedAddresses);
  if (da) v.err('detectedAddresses', 'V_TYPE', da);
  v.throwIf();
  return b as unknown as EnrollRequest;
}
export function peerInfo(a: AgentDoc): PeerInfo {
  return { siteSlug: a.siteSlug, publicKey: a.publicKey, advertiseHost: a.advertiseHost, agentPort: a.agentPort, enrolledAt: a.enrolledAt };
}

/** §21 #86: the pinned agent release of one os/arch (deps manifest agent version, else OPM_AGENT_VERSION / agent/latest). */
export async function pinnedRelease(os: string, arch: string, requested: string | null = null): Promise<{ version: string; sha256: string; signature: string; notes: string | null; sizeBytes: number | null; schema: number | null; minSchema: number | null }> {
  const rel = await agentReleaseInfo();
  if (!requested && !rel) throw E.notFound('no agent release is uploaded (agent/latest missing)');
  const version = requested ?? rel!.version;
  const r = await releaseOf(version, os, arch);
  return { version, sha256: r.sha256, signature: r.signature, notes: rel && rel.version === version ? rel.notes : null, sizeBytes: r.sizeBytes, schema: r.meta?.schema ?? null, minSchema: r.meta?.minSchema ?? null };
}
function releaseResponse(r: Awaited<ReturnType<typeof pinnedRelease>>, os: 'linux' | 'darwin', arch: 'amd64' | 'arm64', binaryBase: string): AgentReleaseResponse {
  return {
    version: r.version,
    os,
    arch,
    sha256: r.sha256,
    signature: r.signature,
    releasePublicKey: env.releasePublicKey,
    binaryPath: `${binaryBase}?os=${os}&arch=${arch}&version=${encodeURIComponent(r.version)}`,
    notes: r.notes,
    sizeBytes: r.sizeBytes,
    schema: r.schema,
    minSchema: r.minSchema,
  };
}
function osArch(c: { query: URLSearchParams }): { os: 'linux' | 'darwin'; arch: 'amd64' | 'arm64' } {
  const os = c.query.get('os');
  const arch = c.query.get('arch');
  if (os !== 'linux' && os !== 'darwin') throw E.bad('os', 'V_ENUM', 'os must be linux | darwin');
  if (arch !== 'amd64' && arch !== 'arm64') throw E.bad('arch', 'V_ENUM', 'arch must be amd64 | arm64');
  return { os, arch };
}

export const installHandlers: Pick<Handlers, 'installScript' | 'installBinary' | 'installDeps' | 'installRelease' | 'enroll' | 'agentRelease' | 'agentBinary'> = {
  /** §21 #86 `opm-agent upgrade` (signed by the site key). */
  async agentRelease(c) {
    const { os, arch } = osArch(c);
    return releaseResponse(await pinnedRelease(os, arch), os, arch, '/v1/agent/binary');
  },
  /** §21 #91 release metadata for the install script (install token; version = ?version= or the pin; uploaded only). */
  async installRelease(c) {
    await verifyInstallToken(c.headers['authorization']);
    const { os, arch } = osArch(c);
    const vq = c.query.get('version');
    if (vq !== null && vq !== '' && !VERSION_RE.test(vq)) throw E.bad('version', 'V_FORMAT', 'version must be 1–64 of [0-9A-Za-z._+-]');
    return releaseResponse(await pinnedRelease(os, arch, vq || null), os, arch, '/v1/install/binary');
  },
  async agentBinary(c) {
    const { os, arch } = osArch(c);
    const version = c.query.get('version');
    if (version === null || !/^[0-9A-Za-z._+-]{1,64}$/.test(version)) throw E.bad('version', 'V_FORMAT', 'version is required (1–64 of [0-9A-Za-z._+-])');
    const bin = paths.agentBinary(version, os, arch);
    const [sha, sig, meta] = await Promise.all([readText(`${bin}.sha256`).catch(() => null), readText(`${bin}.sig`).catch(() => null), buildMeta(version)]);
    if (!sha || !sig) throw E.notFound(`agent ${version} for ${os}/${arch} is not uploaded`);
    return serveObject(bin, {
      range: c.headers['range'],
      fileName: 'opm-agent',
      noRedirect: noRedirectFor(c.headers),
      headers: { 'X-OPM-Sha256': sha.trim().split(/\s+/)[0]!, 'X-OPM-Signature': sig.trim(), 'X-OPM-Agent-Version': version, ...schemaHeaders(meta) },
    });
  },
  async installScript(c) {
    let t: Awaited<ReturnType<typeof verifyInstallToken>>;
    try {
      t = await verifyInstallToken(c.headers['authorization']);
    } catch (e) {
      // §21 #91: a token that cannot be used → 200 + a script that prints why (curl -f would hide a 410 body)
      if (e instanceof ApiError && (e.code === 'gone' || (e.code === 'unauthenticated' && /unknown install token/.test(e.message)))) {
        const d = e.details as { reason?: string; siteSlug?: string };
        return scriptReply(errorScript(tokenProblemText(e.code === 'gone' ? (d.reason ?? 'expired') : 'unknown', d.siteSlug ?? null)));
      }
      throw e;
    }
    const vq = c.query.get('version');
    if (vq !== null && vq !== '' && !VERSION_RE.test(vq)) return scriptReply(errorScript([`OPM install: ?version=${vq.replace(/[^0-9A-Za-z._+-]/g, '?')} is not a version (use e.g. ?version=0.10.0-10090000).`]));
    const [m, depSnap] = await Promise.all([loadDepsManifest(), col.deployments().doc(t.deploymentId).get()]);
    const dep = decodeDeployment(depSnap.data()!);
    const requested = await pinnedAgent(vq || (m?.agent?.version ?? null));
    if (vq && requested && !requested.files.amd64 && !requested.files.arm64) return scriptReply(errorScript([`OPM install: agent ${vq} is not uploaded to the OPM cloud (only uploaded versions can be installed).`, 'Rerun the command without ?version= to install the pinned version.']));
    const head = dep.head ? await loadRevision(dep.deploymentId, dep.head.revId).catch(() => null) : null;
    // ports the agent listens on now (head config; before the first head the defaults 7080 / 8080) + the edge port
    const headSite = head?.config.sites.find((s) => s.siteSlug === t.siteSlug)?.config;
    const draftSite = dep.draft.sites.find((s) => s.siteSlug === t.siteSlug)?.config;
    const body = installScript({
      apiBase: apiBaseOf(c),
      deploymentId: t.deploymentId,
      siteSlug: t.siteSlug,
      token: t.token,
      releasePublicKey: env.releasePublicKey,
      agent: requested,
      docker: m ? { version: m.docker.version, minApi: m.minDockerApi, sha256: { amd64: m.docker.files.amd64?.sha256, arm64: m.docker.files.arm64?.sha256 } } : null,
      ports: { agent: headSite?.agentPort ?? null, alias: headSite ? headSite.agentGuiAliasPort : undefined, edge: (headSite ?? draftSite)?.edge?.httpPort ?? null },
      upgradeOnly: t.mode === 'upgrade',
      seenIp: c.ip,
      requestedVersion: vq || null,
      kind: t.kind,
    });
    return scriptReply(body);
  },
  async installBinary(c) {
    await verifyInstallToken(c.headers['authorization']);
    const os = c.query.get('os');
    const arch = c.query.get('arch');
    if (os !== 'linux' && os !== 'darwin') throw E.bad('os', 'V_ENUM', 'os must be linux | darwin');
    if (arch !== 'amd64' && arch !== 'arm64') throw E.bad('arch', 'V_ENUM', 'arch must be amd64 | arm64');
    const vq = c.query.get('version');
    if (vq !== null && !/^[0-9A-Za-z._+-]{1,64}$/.test(vq)) throw E.bad('version', 'V_FORMAT', 'bad version');
    const version = await resolveAgentVersion(vq);
    const bin = paths.agentBinary(version, os, arch);
    const [sha, sig, meta] = await Promise.all([readText(`${bin}.sha256`), readText(`${bin}.sig`), buildMeta(version)]);
    if (!sha || !sig) throw E.notFound(`agent ${version} for ${os}/${arch} is not uploaded`);
    return serveObject(bin, {
      range: c.headers['range'],
      fileName: 'opm-agent',
      headers: { 'X-OPM-Sha256': sha.trim().split(/\s+/)[0]!, 'X-OPM-Signature': sig.trim(), 'X-OPM-Agent-Version': version, ...schemaHeaders(meta) },
    });
  },
  /** A pinned dependency of the install script (deps/manifest.json): name "docker" = the official static bundle. */
  async installDeps(c) {
    await verifyInstallToken(c.headers['authorization']);
    const arch = c.query.get('arch');
    if (arch !== 'amd64' && arch !== 'arm64') throw E.bad('arch', 'V_ENUM', 'arch must be amd64 | arm64');
    const name = c.params.name!;
    if (name !== 'docker') throw E.notFound(`unknown install dependency ${name} (known: docker)`);
    const m = await loadDepsManifest();
    const f = m?.docker.files[arch];
    if (!m || !f) throw E.notFound('no install dependencies are uploaded (deps/manifest.json missing)');
    return serveObject(f.path, {
      range: c.headers['range'],
      fileName: f.path.split('/').pop() ?? 'docker.tgz',
      headers: { 'X-OPM-Sha256': f.sha256, 'X-OPM-Version': m.docker.version },
    });
  },
  async enroll(c) {
    const token = bearerInstallToken(c.headers['authorization']);
    const tokenHash = sha256Hex(token);
    const b = validateEnroll(asObject(c.json()));
    const q = await db().collectionGroup('installTokens').where('tokenHash', '==', tokenHash).limit(1).get();
    if (q.empty) throw E.unauth('unknown install token');
    const tRef = q.docs[0]!.ref;
    const deploymentId = tRef.parent.parent!.id;
    const depRef = col.deployments().doc(deploymentId);
    return db().runTransaction(async (tx) => {
      const [ts, ds] = await Promise.all([tx.get(tRef), tx.get(depRef)]);
      const t = ts.data() as InstallToken;
      checkTokenState(t);
      if (!ds.exists) throw E.gone('the deployment was deleted');
      const dep = decodeDeployment(ds.data()!);
      if (dep.deleted) throw E.gone('the deployment was deleted');
      const aRef = col.agents(deploymentId).doc(t.siteSlug);
      const existing = await tx.get(aRef);
      if (existing.exists) throw E.conflict(`site ${t.siteSlug} is already enrolled (revoke the enrollment first)`);
      const others = await tx.get(col.agents(deploymentId));
      const now = nowIso();
      const agent: AgentDoc = {
        siteSlug: t.siteSlug,
        publicKey: b.publicKey,
        enrolledAt: now,
        agentVersion: b.agentVersion,
        lastSeenAt: now,
        online: true,
        ip: b.ip || c.ip,
        hostname: b.hostname,
        advertiseHost: b.advertiseHost,
        agentPort: b.agentPort,
        os: b.os,
        arch: b.arch,
        relayVia: null,
        headRevId: null,
        staging: null,
        keyVersion: dep.accessKeyVersion,
        ackSeq: 0,
        metrics: null,
        edge: null,
        peersSeen: [],
        detectedAddresses: detectedAddressesOf(b.detectedAddresses),
      };
      const { online, ...stored } = agent;
      void online;
      // §21 #92: the plaintext stays stored — the used token is now this enrollment's "Upgrade / reinstall command"
      // (revealUpgradeCommand); it is nulled when the enrollment is revoked or the command is regenerated
      tx.update(tRef, { usedAt: now });
      tx.create(aRef, stored);
      const log = new CloudLog(deploymentId, dep);
      const fp = keyFingerprint(b.publicKey);
      log.add(tx, { type: 'install.enrolled', summary: `Site ${t.siteSlug} enrolled (${b.hostname || b.ip})`, data: { siteSlug: t.siteSlug, agentVersion: b.agentVersion, publicKeyFingerprint: fp, ip: agent.ip, hostname: b.hostname }, actor: null });
      tx.update(depRef, { ...log.depUpdate(), agentEpoch: dep.agentEpoch + 1 });
      const peers = [...others.docs.map((d) => d.data() as AgentDoc), agent].map(peerInfo).sort((a, b2) => (a.siteSlug < b2.siteSlug ? -1 : 1));
      const res: EnrollResponse = { deploymentId, siteSlug: t.siteSlug, peers, accessKey: { hash: dep.accessKeyHash, version: dep.accessKeyVersion }, serverTime: now, consoleUrl: env.consoleUrl, deploymentLabel: dep.label ?? null };
      return res;
    });
  },
};
void createHash;
