/**
 * In-browser mock of the agent local API (AGENT_API from @opm/shared) — enabled with VITE_MOCK=1, used by the
 * Playwright tests and for design work without an agent. It is a fake `fetch`: the real typed client
 * (createAgentClient) runs unchanged on top of it, so every page exercises the real request/response code.
 *
 * Test knobs (localStorage):
 *   opm.mock.session   the simulated opm_session cookie (JSON)
 *   opm.mock.down      comma list of unreachable peer sites, e.g. "db"
 *   opm.mock.waitSec   approve / rollback / volume-reset wait (default 60; §21 #95 hold-to-fast-forward runs it up to ×4)
 *   opm.mock.noHead=1  fresh install (no head, no proposal)
 *   opm.mock.noKey=1   the site has no access-key hash yet (login → unavailable)
 *   opm.mock.fail=apply  the next update fails in APPLY on site app → rollback
 *   opm.mock.otherTab=1  another tab holds the internet tunnel lease
 *   opm.mock.presetProposal=1  the open cloud proposal also adds a dev-tool preset on db (§21 #102 Diff dev tools tab)
 *   opm.mock.tunnels=off|empty  the cloud does not carry port tunnels / none added (§21 #125); a session with `remote` works through the cloud
 */
import { AGENT_API } from '@opm/shared/agent-api';
import { toolsFetch } from './tools'; // round T: operator toolbox
import { devtoolsFetch } from './devtools'; // §21 #97 dev tools
import * as VO from './volops'; // §21 #96 volume manager
import { volsyncFetch, seedAll as seedVolumeSync, type VolSyncDeps } from './volsync'; // §21 #101 persist-to-cloud volumes
import { isDatabaseImage } from '@opm/shared/volume-ops';
import { newDevToolPreset } from '@opm/shared/devtools';
import { dnsFailureHint, dnsFailureMessage } from '@opm/shared/depends'; // §21 #93
import { advanceIntentWait, fastForwardLabel, fastForwardUse, intentReady, newIntentWait, setIntentSpeed, isIntentSpeed, type IntentWaitState } from '@opm/shared/intent-wait'; // §21 #95
import { agentFetch, agentSchemaOf, agentVersionOf, isSelfDown, restartAllAgents, seedAgentActivities } from './agentself'; // round I: agent self-management
import type { EndpointDef } from '@opm/shared';
import { ERROR_HTTP_STATUS, MASK, PROPOSAL_TERMINAL, REVEAL_RATE_PER_MINUTE, REVEAL_SECONDS } from '@opm/shared/types';
import type { TerminateUpdateRequest, TerminateUpdateResponse } from '@opm/shared/agent-self';
import type {
  ActorRow,
  ApproveIntent,
  FastForwardUse,
  IntentProgress,
  Config,
  ConfigInput,
  ConfigView,
  ContainerStatus,
  DebugLine,
  DiffChange,
  ErrorCode,
  GateCheck,
  LocalProposalRequest,
  OverviewResponse,
  Proposal,
  Revision,
  SiteStaging,
  SiteStatus,
  SiteTile,
  Snapshot,
  SnapshotVolume,
  SiteVolumeStatus,
  SiteVolumesResponse,
  StagingDetail,
  Transfer,
  TunnelLease,
  TunnelRequest,
  UpdateRun,
  UpdateSiteState,
  UpdateStepName,
  ValidationIssue,
} from '@opm/shared/types';
import { diffConfigs } from '$shared/diff.js';
import { checkLocalEdit, containerMounts, containerPorts, validateConfig, resolveSiteHosts, volumeUsers, locationPath, type CatalogImage } from '@opm/shared/validate';
import type { CertStatus, SiteConfig } from '@opm/shared/types';
import * as D from './data';
import { createBranchMock } from './branch'; // §21 #104 branches
import { configBlobs, type BlobNeed, type CreateStageRequest } from '@opm/shared/staging'; // §21 #107 commit staging
import { sha256 as sha256Bytes, toHex } from '@opm/shared/hash';
import { hostFsFile, hostFsList } from './hostfs';
import { createFilesMock } from './files'; // §21 #123 shared files
import { createTunnelsMock } from './tunnels'; // §21 #125 port tunnels

type Json = Record<string, unknown>;
class MockError extends Error {
  constructor(
    public code: ErrorCode,
    message: string,
    public details: Json = {},
  ) {
    super(message);
  }
}
const ls = (k: string) => {
  try {
    return localStorage.getItem(k);
  } catch {
    return null;
  }
};
const down = (): string[] => (ls('opm.mock.down') ?? '').split(',').map((s) => s.trim()).filter(Boolean);
const waitSec = () => Number(ls('opm.mock.waitSec') ?? 60) || 60;
const nowMs = () => Date.now();
const revealRate = new Map<string, { minute: number; n: number }>();
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
const clone = <T>(v: T): T => JSON.parse(JSON.stringify(v)) as T;
const SESSION_KEY = 'opm.mock.session';

// ───────────────────────────────────────────── state
interface PropRec {
  p: Proposal;
  rev: Revision;
  createdMs: number;
  stageMs: number;
  /** fixed staging per site (seeded proposals); otherwise simulated from createdMs */
  fixed?: Record<string, SiteStaging>;
  failSite?: string;
}
interface RunSim {
  run: UpdateRun;
  t0: number;
  order: string[];
  sites: string[];
  target: Revision;
  from: Revision | null;
  revKey: string | null; // proposal revId (update) or null (rollback)
  failAt: { step: UpdateStepName; site: string } | null;
  actions: Record<string, UpdateSiteState['containers']>;
  finalized: boolean;
  snapshotId: string;
  targetSnapshotId: string | null;
  /** §21 #110 a recovery after terminate { rollback: true } (updateId recover-<id>) */
  kind?: 'recover';
  recoverOf?: string;
}

const noHead = ls('opm.mock.noHead') === '1';
const revisions: Revision[] = [];
let headRevId: string | null = null;
const props = new Map<string, PropRec>();
const snapshots: { s: Snapshot; createdMs: number; readyMs: number }[] = [];
const activities: Record<string, ReturnType<typeof D.makeActivities>> = { app: D.makeActivities('app', 140), db: D.makeActivities('db', 90), northwind: D.makeActivities('northwind', 40) };
const debug: Record<string, DebugLine[]> = { app: D.makeDebug('app', 900), db: D.makeDebug('db', 500), northwind: D.makeDebug('northwind', 200) };
const transfers: Record<string, Transfer[]> = { app: D.makeTransfers('app'), db: D.makeTransfers('db'), northwind: [] };
type MockIntent = ApproveIntent & { createdMs: number; wait: IntentWaitState; site: string };
const intents = new Map<string, MockIntent>();
/** §21 #95: a new intent (the effective wait is integrated like the agent does — shared/src/intent-wait.ts) */
function newIntent(fields: Omit<ApproveIntent, 'intentId' | 'notBefore' | 'expiresAt' | 'waitSec'>, site: string = D.SELF): MockIntent {
  const w = waitSec();
  const it: MockIntent = { intentId: D.mockUlid(), ...fields, notBefore: D.iso(nowMs() + w * 1000), expiresAt: D.iso(nowMs() + w * 1000 + 600_000), waitSec: w, createdMs: nowMs(), wait: newIntentWait(w, nowMs()), site };
  intents.set(it.intentId, it);
  return it;
}
const publicIntent = (it: MockIntent): ApproveIntent => {
  const { createdMs: _c, wait: _w, site: _s, ...pub } = it;
  return pub;
};
/** remaining effective seconds (0 = ACCEPT allowed) */
function intentLeft(it: MockIntent): number {
  advanceIntentWait(it.wait, nowMs());
  return intentReady(it.wait) ? 0 : Math.max(0, it.wait.waitSec - it.wait.effective);
}
/** mark used: the fast-forward record (null = waited at ×1) */
function useIntent(it: MockIntent, actor: string): FastForwardUse | null {
  advanceIntentWait(it.wait, nowMs());
  intents.delete(it.intentId);
  return fastForwardUse(it.wait, actor);
}
const ffSuffix = (ff: FastForwardUse | null) => (ff ? ` · ${fastForwardLabel(ff)}` : '');
const ffData = (ff: FastForwardUse | null) => (ff ? { fastForward: ff } : {});
function intentProgress(it: MockIntent): IntentProgress {
  const n = nowMs();
  advanceIntentWait(it.wait, n);
  const w = it.wait;
  return {
    intentId: it.intentId,
    kind: it.kind,
    waitSec: w.waitSec,
    effectiveSec: Math.round(w.effective * 1000) / 1000,
    remainingSec: Math.round(Math.max(0, w.waitSec - w.effective) * 1000) / 1000,
    speed: w.speed,
    speedUntil: w.speed > 1 ? D.iso(w.untilMs) : null,
    maxSpeed: w.maxSpeed,
    ready: intentReady(w),
    createdAt: D.iso(w.createdMs),
    serverTime: D.iso(n),
  };
}
function pendingIntent(id: string, s: MockSession | null, site: string): MockIntent {
  const it = intents.get(id);
  if (!it || it.sessionId !== s?.sessionId || it.site !== site || Date.parse(it.expiresAt) <= nowMs()) throw new MockError('not-found', 'no such pending intent');
  return it;
}
function speedIntent(it: MockIntent, body: unknown): IntentProgress {
  const sp = (body as { speed?: unknown } | null)?.speed;
  if (!isIntentSpeed(sp)) throw new MockError('invalid', 'speed must be 1, 2 or 4', { errors: [{ path: 'speed', code: 'V_RANGE', message: '1, 2 or 4 (at most ×4)', severity: 'error' }] });
  setIntentSpeed(it.wait, sp, nowMs());
  return intentProgress(it);
}
const runs = new Map<string, RunSim>();
let currentRunId: string | null = null;
let lease: TunnelLease | null = null;
let leaseSession: string | null = null;
let tunnelSeq = 0;
const handed = new Map<string, { req: TunnelRequest; at: number }>();
const actors: ActorRow[] = [
  { email: 'ravi@acme.example', lastUsedAt: D.ago(3600 * 5), count: 41 },
  { email: 'mira@acme.example', lastUsedAt: D.ago(86400), count: 17 },
  { email: 'ops-oncall@acme.example', lastUsedAt: D.ago(86400 * 6), count: 4 },
];
const certs: Record<string, CertStatus[]> = { app: D.certsFor('app'), db: D.certsFor('db'), northwind: D.certsFor('northwind') };
let loginFails: number[] = [];

(function seed() {
  if (noHead) return;
  const c5 = D.baseConfig();
  (c5.variables.find((v) => v.variableSlug === 'greeting')!.value as string) = 'Hello';
  // seq 7 raises the api memory 96 → 128 MB (the earlier revisions run 96)
  c5.sites.find((s) => s.siteSlug === 'app')!.config.containers[0].resources.memoryMb = 96;
  const r5 = D.makeRevision(5, c5, { revId: '01JAMQCK0000000000000REV05', createdMs: nowMs() - 86400_000 * 9, summary: 'first setup' });
  const c6 = D.baseConfig();
  c6.sites.find((s) => s.siteSlug === 'app')!.config.containers[0].resources.memoryMb = 96;
  const r6 = D.makeRevision(6, c6, { revId: '01JAMQCK0000000000000REV06', createdMs: nowMs() - 86400_000 * 4, parentRevId: r5.revId, summary: 'greeting "Hello" → "Hello from OPM"' });
  const c7 = D.baseConfig();
  c7.sites.find((s) => s.siteSlug === 'app')!.config.containers[0].resources.memoryMb = 128;
  const r7 = D.makeRevision(7, c7, { revId: '01JAMQCK0000000000000REV07', createdMs: nowMs() - 86400_000 * 1.2, parentRevId: r6.revId, summary: 'api memory 96 → 128 MB', origin: 'agent', actor: 'ravi@acme.example' });
  revisions.push(r5, r6, r7);
  headRevId = r7.revId;
  const both = (s: SiteStaging['staging']) => ({ app: D.stagingOf(s, 1, nowMs() - 3600_000), db: D.stagingOf(s, 1, nowMs() - 3600_000), northwind: D.stagingOf(s, 1, nowMs() - 3600_000) });
  props.set(r6.revId, { p: D.makeProposal(r6, 'applied', both('ready'), { approvedBy: 'mira@acme.example', approvedAt: D.iso(nowMs() - 86400_000 * 4 + 600_000), result: { ok: true, message: 'applied', snapshotId: null } }), rev: r6, createdMs: 0, stageMs: 0, fixed: both('ready') });
  props.set(r7.revId, { p: D.makeProposal(r7, 'applied', both('ready'), { approvedBy: 'ravi@acme.example', approvedAt: D.iso(nowMs() - 86400_000 * 1.2 + 300_000), result: { ok: true, message: 'applied', snapshotId: null } }), rev: r7, createdMs: 0, stageMs: 0, fixed: both('ready') });
  // open proposal from the cloud: theme green → yellow (staged, gate open)
  const c8 = clone(c7);
  c8.variables.find((v) => v.variableSlug === 'theme')!.value = 'yellow';
  // §21 #102 knob opm.mock.presetProposal=1: the cloud proposal also adds a dev-tool preset (Diff shows the dev tools tab)
  if (ls('opm.mock.presetProposal') === '1') {
    const pg = newDevToolPreset('postgres', []);
    pg.presetSlug = 'plant-db';
    pg.name = 'Plant database';
    pg.target = { kind: 'host', host: '10.0.0.12', port: 5432 };
    pg.auth.username = 'postgres';
    c8.sites.find((s) => s.siteSlug === 'db')!.config.devTools = [pg];
  }
  const r8 = D.makeRevision(8, c8, { revId: '01JAMQCK0000000000000REV08', createdMs: nowMs() - 1800_000, parentRevId: r7.revId, summary: 'theme green → yellow' });
  props.set(r8.revId, { p: D.makeProposal(r8, 'ready', both('ready')), rev: r8, createdMs: nowMs() - 1800_000, stageMs: 0, fixed: both('ready') });
  // snapshots
  const snap = (reason: Snapshot['reason'], ageSec: number, rev: Revision, note: string, by: string): Snapshot => ({
    snapshotId: D.mockUlid(nowMs() - ageSec * 1000),
    createdAt: D.ago(ageSec),
    reason,
    revId: rev.revId,
    seq: rev.seq,
    createdBy: by,
    note,
    revision: rev,
    sites: Object.fromEntries(
      rev.config.sites.map((s) => [
        s.siteSlug,
        {
          config: rev.config,
          images: s.config.containers.map((c) => ({ serviceSlug: c.serviceSlug, version: c.version, digest: D.catalog.images[`${c.serviceSlug}:${c.version}`].digest, configDigest: D.catalog.images[`${c.serviceSlug}:${c.version}`].configDigest })),
          // the oldest (pre-update) snapshot is written before round D: its volumes carry the legacy `name`
          volumes: Object.entries(backupVolumes(s.config)).map(([volumeSlug, containers]) =>
            reason === 'pre-update'
              ? ({ name: volumeSlug, containers, sizeBytes: s.siteSlug === 'db' ? 1_203_331 : 40_960, sha256: D.fakeSha(volumeSlug + ageSec), path: `sites/${s.siteSlug}/volumes/${volumeSlug}.tar.zst` } as unknown as SnapshotVolume)
              : { volumeSlug, containers, sizeBytes: s.siteSlug === 'db' ? 1_203_331 : 40_960, sha256: D.fakeSha(volumeSlug + ageSec), path: `sites/${s.siteSlug}/volumes/${volumeSlug}.tar.zst` },
          ),
          sizeBytes: s.siteSlug === 'db' ? 1_203_331 : 40_960,
        },
      ]),
    ),
  });
  for (const [s, age] of [
    [snap('pre-update', 86400 * 1.2 + 60, r6, '', 'ravi@acme.example'), 86400 * 1.2 + 60],
    [snap('scheduled', 86400 * 0.6, r7, '', 'system'), 86400 * 0.6],
    [snap('manual', 7200, r7, 'Before changing the theme', 'mira@acme.example'), 7200],
  ] as [Snapshot, number][])
    snapshots.push({ s, createdMs: nowMs() - age * 1000, readyMs: 0 });
  if (ls('opm.mock.otherTab') === '1')
    lease = { leaseId: D.mockUlid(nowMs() - 600_000), actor: 'mira@acme.example', siteSlug: 'db', userAgent: 'Mozilla/5.0 (Windows NT 10.0) Firefox/131.0', startedAt: D.ago(600), lastPollAt: D.ago(2), status: 'active', encrypted: true, reqs: 214, bytes: 3_481_220, lastError: null };
})();

const head = (): Revision | null => revisions.find((r) => r.revId === headRevId) ?? null;
const headSites = (): string[] => head()?.config.sites.map((s) => s.siteSlug) ?? ['app', 'db'];
const ALL_SITES = ['app', 'db', 'northwind'];
const reachable = (s: string) => s === D.SELF || !down().includes(s);
const lastSeen = (s: string) => (reachable(s) ? D.iso(nowMs() - 2000) : D.ago(260));

function addActivity(site: string, type: string, summary: string, actor: string | null, data: Json = {}) {
  const list = activities[site] ?? (activities[site] = []);
  const seq = (list[0]?.seq ?? 0) + 1;
  list.unshift({ activityId: `${site}:${seq}`, deploymentId: D.DEP, siteSlug: site, seq, ts: D.iso(nowMs()), actor, origin: actor ? 'agent' : 'system', type, summary, data: { siteSlug: site, ...data }, prevHash: list[0]?.hash ?? null, hash: D.fakeSha(`${site}:${seq}:${type}:${nowMs()}`), uploadedVia: null, uploadedAt: null });
}

// ───────────────────────────────────────────── config helpers
function maskConfig(c: Config): ConfigView {
  return { ...clone(c), secrets: c.secrets.map((s) => ({ ...s, value: s.value ? MASK : null, isSet: !!s.value })) };
}
const slugOf = (s: string) => s;
/** declared volumes with backup = true → containerSlugs that mount them (SnapshotVolume; round D backup rule) */
function backupVolumes(sc: SiteConfig): Record<string, string[]> {
  const users = volumeUsers(sc);
  const out: Record<string, string[]> = {};
  for (const v of sc.volumes ?? []) if (v.backup) out[v.volumeSlug] = [...new Set((users[v.volumeSlug] ?? []).map((u) => u.containerSlug))];
  return out;
}
/** the SnapshotVolume list of one site part (declared backup = true volumes only) */
function snapVolumes(site: string, cfg: Config, tag: string): SnapshotVolume[] {
  const sc = cfg.sites.find((x) => x.siteSlug === site)?.config;
  return sc ? Object.entries(backupVolumes(sc)).map(([volumeSlug, containers]) => ({ volumeSlug, containers, sizeBytes: volSize(site, volumeSlug), sha256: D.fakeSha(volumeSlug + tag), path: `sites/${site}/volumes/${volumeSlug}.tar.zst` })) : [];
}
/** snapshots written before round D carry `name` instead of volumeSlug */
const volSlugOf = (v: SnapshotVolume) => v.volumeSlug ?? (v as unknown as { name: string }).name;
/** fake size on disk of a volume directory (deterministic) */
const volSize = (site: string, slug: string) => (site === 'db' ? 1_203_331 : 4096 + (parseInt(D.fakeHex(slug).slice(0, 5), 16) % 900_000));
/** round D: GET /_opm/api/sites/{site}/volumes — declared volumes of the head + an orphan directory on app */
function siteVolumes(site: string): SiteVolumesResponse {
  const h = head();
  const sc = h?.config.sites.find((s) => s.siteSlug === site)?.config;
  const users = sc ? volumeUsers(sc) : {};
  const snapsNewest = [...snapshots].sort((a, b) => b.createdMs - a.createdMs);
  const lastSnap = (slug: string) => {
    for (const x of snapsNewest) {
      const v = x.s.sites[site]?.volumes.find((y) => volSlugOf(y) === slug);
      if (v && x.readyMs <= nowMs()) return { snapshotId: x.s.snapshotId, createdAt: x.s.createdAt, sizeBytes: v.sizeBytes };
    }
    return null;
  };
  const imgPath = (c: { serviceSlug: string; version: string }, iv: string | null) => (iv ? (D.catalog.images[`${c.serviceSlug}:${c.version}`]?.volumes.find((x) => x.name === iv)?.mountPath ?? '') : '');
  const home = D.AGENT_HOME[site] ?? '/var/lib/opm';
  const vols: SiteVolumeStatus[] = (sc?.volumes ?? []).map((v) => ({
    volumeSlug: v.volumeSlug,
    path: locationPath(v.location, 'volumes', v.volumeSlug, home),
    location: v.location ?? { kind: 'auto' },
    label: v.label,
    description: v.description,
    backup: v.backup,
    kind: v.kind ?? 'dir',
    seed: v.seed ?? null,
    seeded: seededOf(site, v),
    declared: true,
    usedBy: (users[v.volumeSlug] ?? []).map((u) => {
      const c = sc!.containers.find((x) => x.containerSlug === u.containerSlug)!;
      const p = u.mountPath ?? imgPath(c, u.imageVolume);
      return { containerSlug: u.containerSlug, imageVolume: u.imageVolume, mountPath: p.replace('{{ container.env.NODE_ENV }}', c.env.find((e) => e.name === 'NODE_ENV')?.value ?? 'worker'), readOnly: u.readOnly };
    }),
    sizeBytes: (users[v.volumeSlug] ?? []).length || lastSnap(v.volumeSlug) ? volSize(site, v.volumeSlug) : null,
    lastSnapshot: lastSnap(v.volumeSlug),
  }));
  if (site === 'app') vols.push({ volumeSlug: 'hello-api-old', path: `${home}/volumes/hello-api-old`, location: { kind: 'auto' }, label: '', description: '', backup: false, kind: 'dir', seed: null, seeded: null, declared: false, usedBy: [], sizeBytes: 2_310_144, lastSnapshot: null });
  const hostPaths = (sc?.containers ?? []).flatMap((c) => c.volumes.filter((v) => v.source.kind === 'host').map((v) => ({ containerSlug: c.containerSlug, mountPath: v.mountPath ?? imgPath(c, v.imageVolume), path: (v.source as { path: string }).path, readOnly: v.readOnly })));
  return { siteSlug: site, agentHome: home, measuredAt: D.iso(nowMs() - 20_000), volumes: vols, hostPaths };
}
function validationCtx() {
  const images: Record<string, CatalogImage> = {};
  for (const [k, im] of Object.entries(D.catalog.images)) images[k] = { kind: im.kind, ports: im.ports, env: im.env, volumes: im.volumes, healthcheck: im.healthcheck };
  const services = Object.fromEntries(Object.entries(D.catalog.services).map(([slug, s]) => [slug, { label: s.label }]));
  return { mode: 'agent' as const, deploymentId: D.DEP, services, images, notFoundPageIds: Object.keys(D.catalog.notFoundPages), detectedHosts: D.ADVERTISE };
}
function genSecret(charset: string, length: number) {
  const al = charset === 'hex' ? '0123456789abcdef' : charset === 'base64url' ? 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_-' : 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789';
  let s = '';
  for (let i = 0; i < length; i++) s += al[Math.floor(Math.random() * al.length)];
  return s;
}
function buildConfig(input: ConfigInput, base: Config): { config: Config; secretChanged: Set<string> } {
  const changed = new Set<string>();
  const secrets = input.secrets.map((s) => {
    const o = base.secrets.find((x) => x.secretSlug === s.secretSlug);
    let value = o?.value ?? null;
    let rotatedAt = o?.rotatedAt ?? null;
    if (s.generateNow && s.generate) {
      value = genSecret(s.generate.charset, s.generate.length);
      rotatedAt = D.iso(nowMs());
      changed.add(s.secretSlug);
    } else if (s.plainValue !== undefined) {
      value = s.plainValue === '' ? null : s.plainValue;
      rotatedAt = D.iso(nowMs());
      if (value !== (o?.value ?? null)) changed.add(s.secretSlug);
    }
    return { secretSlug: s.secretSlug, label: s.label, description: s.description, required: s.required, generate: s.generate, value, rotatedAt };
  });
  return { config: { variables: clone(input.variables), files: clone(input.files), sites: clone(input.sites), secrets }, secretChanged: changed };
}
function localCheck(body: LocalProposalRequest): { config: Config; changes: DiffChange[]; issues: ValidationIssue[] } {
  const h = head();
  if (!h) throw new MockError('stale', 'This site has no head yet; local proposals need a head.');
  if (body.baseRevId !== h.revId) throw new MockError('stale', 'The head changed since you started editing. Reload the config and edit again.', { headRevId: h.revId });
  if (!body.config || !Array.isArray(body.config.variables)) throw new MockError('invalid', 'config is required', { errors: [{ path: 'config', code: 'V_REQUIRED', message: 'config is required', severity: 'error' }] });
  const { config, secretChanged } = buildConfig(body.config, h.config);
  const issues = [...checkLocalEdit(h.config, config), ...validateConfig(config, validationCtx()).list];
  const changes = diffConfigs(h.config, config, slugOf, (s) => secretChanged.has(s));
  if (changes.length === 0) issues.push({ path: '', code: 'V_NO_CHANGE', message: 'nothing changed compared to the head', severity: 'error' });
  return { config, changes, issues };
}

// ───────────────────────────────────────────── proposals + staging + gate
function stagingFor(r: PropRec, site: string): SiteStaging {
  if (r.fixed?.[site]) return r.fixed[site];
  const el = nowMs() - r.createdMs;
  if (r.failSite === site && el > r.stageMs * 0.6) return D.stagingOf('failed', 0.6, r.createdMs + r.stageMs * 0.6, 'dry run: host port 16379 is used by another process');
  if (el < 700) return D.stagingOf('pending', 0, r.createdMs);
  if (el < r.stageMs) return D.stagingOf('downloading', Math.min(0.99, el / r.stageMs), nowMs());
  return D.stagingOf('ready', 1, r.createdMs + r.stageMs);
}
function propSites(r: PropRec): string[] {
  const parent = revisions.find((x) => x.revId === r.rev.parentRevId);
  return [...new Set([...(parent?.config.sites.map((s) => s.siteSlug) ?? []), ...r.rev.config.sites.map((s) => s.siteSlug)])].sort();
}
function refreshProposal(r: PropRec): Proposal {
  if (!PROPOSAL_TERMINAL.includes(r.p.status) && r.p.status !== 'approved' && r.p.status !== 'applying') {
    const sites = Object.fromEntries(propSites(r).map((s) => [s, stagingFor(r, s)]));
    r.p.sites = sites;
    const st = Object.values(sites).map((s) => s.staging);
    r.p.status = st.every((s) => s === 'ready') ? 'ready' : st.every((s) => s === 'pending') ? 'published' : 'staging';
    if (r.rev.parentRevId !== headRevId) {
      r.p.status = 'stale';
      r.p.updatedAt = D.iso(nowMs());
    }
  }
  return r.p;
}
function runningRun(): RunSim | null {
  if (!currentRunId) return null;
  const sim = runs.get(currentRunId)!;
  computeRun(sim);
  return sim.run.finishedAt ? null : sim;
}
function gateFor(sites: string[], parentOk: boolean | null, staged: { ok: boolean; detail: string }, statusOk: { ok: boolean; detail: string } | null): GateCheck {
  const unreach = sites.filter((s) => !reachable(s));
  const run = runningRun();
  const conditions: GateCheck['conditions'] = [];
  if (parentOk !== null) conditions.push({ key: 'parent', ok: parentOk, detail: parentOk ? `every site has head seq ${head()?.seq ?? 0}` : 'the head of a site is not the parent of this proposal' });
  conditions.push({ key: 'reachable', ok: unreach.length === 0, detail: unreach.length ? `${unreach.join(', ')} not reachable (no ping answer in the last 30 s)` : `${sites.length} of ${sites.length} sites answered in the last 30 s` });
  conditions.push({ key: 'staged', ...staged });
  // §21 #110 a running update never closes a gate: ok + warn (starting stops it)
  conditions.push(run ? { key: 'idle', ok: true, warn: true, detail: `an update is running on ${run.sites.join(', ')}: starting stops it` } : { key: 'idle', ok: true, detail: 'no update is running' });
  if (statusOk) conditions.push({ key: 'status', ...statusOk });
  return { open: conditions.every((c) => c.ok), conditions, sites };
}
function proposalGate(r: PropRec): GateCheck {
  const p = refreshProposal(r);
  const sites = propSites(r);
  const notReady = sites.filter((s) => p.sites[s]?.staging !== 'ready');
  return gateFor(
    sites,
    r.rev.parentRevId === headRevId,
    { ok: notReady.length === 0, detail: notReady.length ? `staging not ready on ${notReady.join(', ')}` : 'staging ready on every site' },
    { ok: p.status === 'ready', detail: `proposal status is ${p.status}` },
  );
}
function stagingDetail(r: PropRec, site: string): StagingDetail {
  const st = stagingFor(r, site);
  const sc = r.rev.config.sites.find((s) => s.siteSlug === site);
  const imgs = [...new Set((sc?.config.containers ?? []).map((c) => `${c.serviceSlug}:${c.version}`))].map((k) => D.catalog.images[k]).filter(Boolean); // an image used by two containers is staged once
  const blobs: StagingDetail['blobs'] = [];
  imgs.forEach((im, k) => {
    im.layers.forEach((l, i) => {
      const have = st.staging === 'ready' || (st.staging === 'downloading' && (i + k) / 4 < st.progress);
      blobs.push({ sha256: l.digest, kind: 'layer', sizeBytes: l.sizeBytes, source: have ? (site === 'db' && i === 0 ? 'peer:app' : 'cloud') : null, state: have ? 'have' : st.staging === 'failed' ? 'failed' : st.staging === 'downloading' ? (i === 0 ? 'downloading' : 'waiting') : 'waiting' });
    });
  });
  const files = (sc?.config.containers ?? []).flatMap((c) => c.volumes.filter((v) => v.source.kind === 'file'));
  const haveLayers = blobs.filter((b) => b.state === 'have').length;
  return {
    siteSlug: site,
    staging: st,
    layers: { total: blobs.length, have: haveLayers },
    files: { total: files.length, have: st.staging === 'ready' ? files.length : 0 },
    blobs,
    issues: st.staging === 'failed' ? [{ path: `sites[${site}].config.containers[redis].ports[tcp].hostPort`, code: 'V_PORT_CONFLICT', message: st.error ?? 'dry run failed', severity: 'error' }] : [],
  };
}

// ───────────────────────────────────────────── update simulation
const SEG = { lock: 1200, snapshot: 2300, apply: 2500, verify: 1500, commit: 1000, rollback: 3000 };
function containerActions(from: Config | null, to: Config, site: string): UpdateSiteState['containers'] {
  const a = from?.sites.find((s) => s.siteSlug === site)?.config.containers ?? [];
  const b = to.sites.find((s) => s.siteSlug === site)?.config.containers ?? [];
  const changed = new Set(diffConfigs(from, to, slugOf).filter((c) => ['variables', 'secrets', 'files'].includes(c.section)).map((c) => c.path));
  const out: UpdateSiteState['containers'] = [];
  for (const c of b) {
    const o = a.find((x) => x.containerSlug === c.containerSlug);
    const refs = JSON.stringify(c.env) + JSON.stringify(c.volumes);
    const touched = [...changed].some((p) => refs.includes(`var.${p}`) || refs.includes(`secret.${p}`) || refs.includes(`"${p}"`) || refs.includes(`file.${p}`));
    out.push({ containerSlug: c.containerSlug, serviceSlug: c.serviceSlug, action: !o ? 'create' : JSON.stringify(o) !== JSON.stringify(c) || touched ? 'recreate' : 'keep', state: 'pending', error: null });
  }
  for (const o of a) if (!b.some((x) => x.containerSlug === o.containerSlug)) out.push({ containerSlug: o.containerSlug, serviceSlug: o.serviceSlug, action: 'remove', state: 'pending', error: null });
  return out;
}
/** §21 #93: the captured log tail of the failed container: a Deno crash that could not resolve another container of its site */
function failTail(site: string, slug: string): string[] {
  const other = (head()?.config.sites.find((s) => s.siteSlug === site)?.config.containers ?? []).map((c) => c.containerSlug).find((s) => s !== slug && !s.startsWith(slug)) ?? 'redis';
  return [
    '2026-10-08T03:00:01.000Z stdout listening on :80',
    '2026-10-08T03:00:02.000Z stderr db unreachable: connect ECONNREFUSED 127.0.0.1:5432',
    `2026-10-08T03:00:03.000Z stderr \x1b[0m\x1b[1m\x1b[31merror\x1b[0m: Uncaught (in promise) Error: getaddrinfo EAI_AGAIN ${other}`,
    '2026-10-08T03:00:03.000Z stderr     \x1b[0m\x1b[2m\x1b[38;5;245mat \x1b[0mGetAddrInfoReqWrap.onlookup [as oncomplete] (\x1b[0m\x1b[36mnode:dns\x1b[0m:\x1b[0m\x1b[33m107\x1b[0m:\x1b[0m\x1b[33m26\x1b[0m)',
  ];
}
function failHint(site: string, slug: string): string | null {
  const slugs = (head()?.config.sites.find((s) => s.siteSlug === site)?.config.containers ?? []).map((c) => c.containerSlug);
  const h = dnsFailureHint(failTail(site, slug), slugs, slug);
  return h ? dnsFailureMessage(slug, h) : null;
}
/**
 * §21 #110 stop a running update on every site now: nothing is rolled back (the containers stay as they are).
 * Returns false when it was not running.
 */
function terminateRun(sim: RunSim, reason: string, actor: string | null): boolean {
  computeRun(sim);
  if (sim.finalized) return false;
  const at = D.iso(nowMs());
  const msg = `terminated: ${reason}`;
  sim.finalized = true;
  sim.run.finishedAt = at;
  sim.run.phase = 'failed';
  sim.run.result = { ok: false, message: msg, snapshotId: sim.snapshotId };
  for (const [site, st] of Object.entries(sim.run.sites)) if (st.state === 'running' || st.state === 'idle') sim.run.sites[site] = { ...st, state: 'failed', error: msg, finishedAt: at };
  const prop = sim.revKey ? props.get(sim.revKey) : null;
  if (prop) {
    prop.p.status = 'ready';
    prop.p.result = { ok: false, message: msg, snapshotId: sim.snapshotId };
    prop.p.updatedAt = at;
  }
  for (const s of sim.sites) addActivity(s, 'update.terminated', `update terminated on ${s}: ${reason}`, actor, { updateId: sim.run.updateId, siteSlug: s, reason });
  return true;
}
function startRun(kind: 'update' | 'rollback', target: Revision, approver: string, revKey: string | null, targetSnapshotId: string | null, ff: FastForwardUse | null = null, id: string | null = null): RunSim {
  // §21 #110 starting a new update while one runs stops the running one first (no roll back)
  const prev = runningRun();
  if (prev) terminateRun(prev, 'a new update started', approver);
  const from = head();
  const sites = [...new Set([...(from?.config.sites.map((s) => s.siteSlug) ?? []), ...target.config.sites.map((s) => s.siteSlug)])];
  const order = ['db', 'northwind', 'app'].filter((s) => sites.includes(s));
  const updateId = id ?? D.mockUlid();
  const fail = ls('opm.mock.fail') === 'apply' ? { step: 'apply' as const, site: 'app' } : null;
  const sim: RunSim = {
    run: { updateId, kind, revId: target.revId, coordinator: D.SELF, approvedBy: approver, approvedAt: D.iso(nowMs()), phase: 'lock', result: null, sites: {}, startedAt: D.iso(nowMs()), finishedAt: null, fastForward: ff },
    t0: nowMs(),
    order,
    sites,
    target,
    from,
    revKey,
    failAt: fail,
    actions: Object.fromEntries(sites.map((s) => [s, containerActions(from?.config ?? null, target.config, s)])),
    finalized: false,
    snapshotId: D.mockUlid(),
    targetSnapshotId,
  };
  runs.set(updateId, sim);
  currentRunId = updateId;
  addActivity(D.SELF, 'update.started', `${kind === 'rollback' ? 'Rollback' : 'Update'} started (seq ${target.seq})${ffSuffix(ff)}`, approver, { updateId, revId: target.revId, kind, sites, ...ffData(ff) });
  computeRun(sim);
  return sim;
}
function computeRun(sim: RunSim) {
  if (sim.finalized) return;
  const el = nowMs() - sim.t0;
  const tLock = SEG.lock;
  const tSnap = tLock + SEG.snapshot;
  const applyEnd = (i: number) => tSnap + SEG.apply * (i + 1);
  const tApply = applyEnd(sim.order.length - 1);
  const tVerify = tApply + SEG.verify;
  const tCommit = tVerify + SEG.commit;
  const failIdx = sim.failAt ? sim.order.indexOf(sim.failAt.site) : -1;
  const tFail = failIdx >= 0 ? applyEnd(failIdx) : Infinity;
  const tRbEnd = tFail + SEG.rollback;
  const iso = (ms: number) => D.iso(sim.t0 + ms);
  const st = (site: string, step: UpdateStepName | null, state: UpdateSiteState['state'], startedMs: number | null, endMs: number | null, containers: UpdateSiteState['containers'], error: string | null = null): UpdateSiteState => ({ updateId: sim.run.updateId, siteSlug: site, step, state, error, preSnapshotId: sim.snapshotId, startedAt: startedMs === null ? null : iso(startedMs), finishedAt: endMs === null ? null : iso(endMs), containers });
  const cont = (site: string, s: 'pending' | 'running' | 'ok' | 'failed', failOne = false) =>
    sim.actions[site].map((c, i) => ({ ...c, state: c.action === 'keep' ? ('ok' as const) : failOne && i === sim.actions[site].findIndex((x) => x.action !== 'keep') ? ('failed' as const) : s, error: failOne && c.action !== 'keep' && i === sim.actions[site].findIndex((x) => x.action !== 'keep') ? 'health check failed 3 times in a row (http /healthz → 500)' : null,
      failure:
        failOne && c.action !== 'keep' && i === sim.actions[site].findIndex((x) => x.action !== 'keep')
          ? {
              containerSlug: c.containerSlug,
              dockerName: `opm_${D.DEP}_${site}_${c.containerSlug}`,
              keptAs: `opm_${D.DEP}_${site}_${c.containerSlug}-failed-1791400000`,
              error: `${c.containerSlug}: unhealthy: 3 checks failed in a row after the 30 s start period: GET /healthz → 500 (want 200)`,
              state: { status: 'running', exitCode: 0, oomKilled: false, restartCount: 0, error: '', startedAt: D.iso(sim.t0), finishedAt: '0001-01-01T00:00:00Z' },
              ports: '80/tcp=127.0.0.1:32768',
              logTail: failTail(site, c.containerSlug),
              hint: failHint(site, c.containerSlug),
              healthHistory: [
                { at: D.iso(sim.t0), ok: false, counted: false, message: 'GET /healthz → 500 (want 200)' },
                { at: D.iso(sim.t0 + 30_000), ok: false, counted: true, message: 'GET /healthz → 500 (want 200)' },
              ],
              capturedAt: D.iso(sim.t0 + 60_000),
            }
          : null,
    }));
  const sites: Record<string, UpdateSiteState> = {};
  let phase: UpdateRun['phase'];
  if (el < tLock) phase = 'lock';
  else if (el < tSnap) phase = 'snapshot';
  else if (el < Math.min(tApply, tFail)) phase = 'apply';
  else if (tFail !== Infinity && el < tRbEnd) phase = 'rollback';
  else if (tFail !== Infinity) phase = 'failed';
  else if (el < tVerify) phase = 'verify';
  else if (el < tCommit) phase = 'commit';
  else phase = 'done';
  for (const site of sim.sites) {
    const i = sim.order.indexOf(site);
    if (phase === 'lock') sites[site] = st(site, 'lock', 'running', 0, null, cont(site, 'pending'));
    else if (phase === 'snapshot') sites[site] = st(site, 'snapshot', 'running', tLock, null, cont(site, 'pending'));
    else if (phase === 'apply') {
      const s0 = applyEnd(i) - SEG.apply;
      if (el < s0) sites[site] = st(site, 'snapshot', 'ok', tLock, tSnap, cont(site, 'pending'));
      else if (el < applyEnd(i)) sites[site] = st(site, 'apply', 'running', s0, null, cont(site, 'running'));
      else sites[site] = st(site, 'apply', 'ok', s0, applyEnd(i), cont(site, 'ok'));
    } else if (phase === 'rollback' || phase === 'failed') {
      const done = phase === 'failed';
      const failed = site === sim.failAt?.site;
      sites[site] = st(site, done ? 'unlock' : 'rollback', done ? 'ok' : 'running', tFail, done ? tRbEnd : null, cont(site, failed ? 'failed' : 'ok', failed), failed ? `apply failed: hello-web health check failed 3 times in a row` : null);
    } else if (phase === 'verify') sites[site] = st(site, 'verify', 'running', tApply, null, cont(site, 'ok'));
    else if (phase === 'commit') sites[site] = st(site, 'commit', 'running', tVerify, null, cont(site, 'ok'));
    else sites[site] = st(site, 'commit', 'ok', tVerify, tCommit, cont(site, 'ok'));
  }
  sim.run.sites = sites;
  sim.run.phase = phase;
  const prop = sim.revKey ? props.get(sim.revKey) : null;
  if (prop && !sim.finalized) prop.p.status = phase === 'lock' ? 'approved' : 'applying';
  if (phase === 'done' || phase === 'failed') finalizeRun(sim, phase === 'done', iso(phase === 'done' ? tCommit : tRbEnd));
}
function finalizeRun(sim: RunSim, ok: boolean, at: string) {
  sim.finalized = true;
  sim.run.finishedAt = at;
  const from = sim.from;
  if (sim.kind === 'recover') {
    // §21 #110 converged back to the head config with the volumes of the pre-update backup
    const pre = runs.get(sim.recoverOf ?? '')?.snapshotId ?? '';
    sim.run.result = { ok, message: ok ? 'rolled back to the pre-update backup' : 'the roll back had errors', snapshotId: pre };
    for (const s of sim.sites) addActivity(s, 'update.recovered', `rolled back on ${s} to the pre-update backup ${pre}`, sim.run.approvedBy, { updateId: sim.recoverOf ?? '', siteSlug: s, snapshotId: pre, reason: 'stopped by a person', durationMs: Date.parse(at) - sim.t0 });
    return;
  }
  // pre-update snapshot
  if (from)
    snapshots.push({
      s: { snapshotId: sim.snapshotId, createdAt: D.iso(sim.t0 + SEG.lock), reason: 'pre-update', revId: from.revId, seq: from.seq, createdBy: sim.run.approvedBy, note: '', revision: from, sites: Object.fromEntries(from.config.sites.map((s) => [s.siteSlug, { config: from.config, images: [], volumes: snapVolumes(s.siteSlug, from.config, 'pre'), sizeBytes: 81_920 }])) },
      createdMs: sim.t0,
      readyMs: 0,
    });
  const msg = ok ? 'applied' : `apply failed on ${sim.failAt?.site}: hello-web health check failed 3 times in a row`;
  sim.run.result = { ok, message: msg, snapshotId: sim.snapshotId };
  if (ok) {
    if (!revisions.includes(sim.target)) revisions.push(sim.target);
    headRevId = sim.target.revId;
    for (const s of sim.sites) addActivity(s, 'update.applied', `Revision seq ${sim.target.seq} applied`, null, { revId: sim.target.revId, seq: sim.target.seq, durationMs: Date.parse(at) - sim.t0 });
    if (sim.targetSnapshotId) addActivity(D.SELF, 'snapshot.rolled-back', `Rolled back to snapshot ${sim.targetSnapshotId}`, sim.run.approvedBy, { snapshotId: sim.targetSnapshotId, newRevId: sim.target.revId });
  } else {
    const fs = (sim.run.sites[sim.failAt!.site]?.containers ?? []).filter((c) => c.failure).map((c) => ({ containerSlug: c.containerSlug, error: c.failure!.error, ports: c.failure!.ports, keptAs: c.failure!.keptAs, hint: c.failure!.hint ?? undefined, exitCode: 0, oomKilled: false, restartCount: 0, logTail: c.failure!.logTail }));
    addActivity(sim.failAt!.site, 'update.failed', `Update failed in apply on ${sim.failAt!.site}`, null, { step: 'apply', error: msg, failures: fs });
    for (const s of sim.sites) addActivity(s, 'update.rolled-back', 'Update rolled back to the pre-update snapshot', null, { snapshotId: sim.snapshotId });
    try {
      localStorage.removeItem('opm.mock.fail');
    } catch {
      /* */
    }
  }
  const prop = sim.revKey ? props.get(sim.revKey) : null;
  if (prop) {
    prop.p.status = ok ? 'applied' : 'failed-rolled-back';
    prop.p.result = { ok, message: msg, snapshotId: sim.snapshotId };
    prop.p.updatedAt = at;
    prop.fixed = prop.p.sites;
  }
  // other open proposals with the old parent become stale
  if (ok) for (const r of props.values()) refreshProposal(r);
}

// ───────────────────────────────────────────── status
function siteStatus(site: string): SiteStatus {
  const h = head();
  const sc = h?.config.sites.find((s) => s.siteSlug === site);
  const run = runningRun();
  const containers: ContainerStatus[] = (sc?.config.containers ?? []).map((c, i) => {
    const im = D.catalog.images[`${c.serviceSlug}:${c.version}`];
    const restarting = run && run.run.sites[site]?.state === 'running' && run.run.phase === 'apply' && run.actions[site]?.find((x) => x.containerSlug === c.containerSlug)?.action === 'recreate';
    // §21 #96 knob opm.mock.pgMismatch=<site>/<container>: a crash loop with the custom-pg data-directory banner
    const mismatch = ls('opm.mock.pgMismatch') === `${site}/${c.containerSlug}`;
    if (mismatch)
      return { siteSlug: site, containerSlug: c.containerSlug, serviceSlug: c.serviceSlug, version: c.version, image: `${c.serviceSlug}:${c.version}`, kind: im?.kind ?? 'service', enabled: c.enabled, managed: true, state: 'restarting' as const, exitCode: 1, health: 'starting' as const, ready: false, restarts: 13, cpuPct: null, memMb: null, startedAt: D.ago(20), ports: [], specHash: null, revSeq: h?.seq ?? null };
    return {
      siteSlug: site,
      containerSlug: c.containerSlug,
      serviceSlug: c.serviceSlug,
      version: c.version,
      image: `${c.serviceSlug}:${c.version}`,
      kind: im?.kind ?? 'service',
      enabled: c.enabled,
      managed: true,
      state: restarting ? 'restarting' : c.enabled ? 'running' : 'exited',
      exitCode: c.enabled ? null : 0,
      health: restarting ? 'starting' : im?.healthcheck ? 'healthy' : 'none',
      ready: !restarting && c.enabled,
      restarts: i === 1 ? 2 : 0,
      cpuPct: 0.4 + i * 1.7,
      memMb: 18 + i * 31,
      startedAt: D.ago(3600 * 20 + i * 60),
      ports: (im ? containerPorts(c, im) : []).map((p) => ({ portName: p.portName, containerPort: ((x) => (typeof x === 'number' ? x : 0))(im?.ports.find((ip) => ip.name === p.portName)?.port), hostPort: typeof p.hostPort === 'number' ? p.hostPort : p.hostPort ? 16379 : null, protocol: p.protocol })),
      specHash: D.fakeHex(JSON.stringify(c)).slice(0, 16),
      revSeq: h?.seq ?? null,
    };
  });
  if (site === 'app')
    containers.push({ siteSlug: 'app', containerSlug: 'portainer', serviceSlug: '', version: '', image: 'portainer/portainer-ce:2.21', kind: 'service', enabled: true, managed: false, state: 'running', exitCode: null, health: 'none', ready: true, restarts: 0, cpuPct: 0.1, memMb: 22, startedAt: D.ago(86400 * 12), ports: [{ portName: '9443', containerPort: 9443, hostPort: 9443, protocol: 'tcp' }], specHash: null, revSeq: null });
  const subjects = ['site', ...(sc?.config.containers ?? []).map((c) => c.containerSlug)];
  return {
    siteSlug: site,
    reachable: reachable(site),
    lastSeenAt: lastSeen(site),
    agentVersion: agentVersionOf(site),
    host: { cpuPct: site === 'app' ? 12.4 : 4.1, memUsedMb: site === 'app' ? 2_412 : 1_103, memTotalMb: 7_960, diskUsedPct: site === 'app' ? 41.2 : 67.9, diskFreeMb: site === 'app' ? 58_331 : 20_112, loadAvg1: site === 'app' ? 0.42 : 0.18 },
    containers,
    uptime24h: subjects.map((s, i) => ({ subject: s, cells: D.uptimeCells(144, 10, i + (site === 'db' ? 9 : 1)) })),
    uptime30d: subjects.map((s, i) => ({ subject: s, cells: D.uptimeCells(720, 60, i + (site === 'db' ? 19 : 4), site === 'db' ? 96 : 0) })),
    certs: certs[site] ?? [],
    edge: { revSeq: h?.seq ?? null, reloadedAt: D.ago(86400), lastReloadError: null, boundPorts: site === 'app' ? [8081, 8443, 8090, 7080] : site === 'db' ? [8082, 8091, 7081] : [80, 443, 8092, 7082], bindErrors: ls('opm.mock.port80Busy') === '1' && site === 'app' ? [{ port: 80, purpose: 'edge-http', error: 'listen tcp :80: bind: address already in use', owner: 'nginx (pid 812)' }] : [], certs: certs[site] ?? [], defaultEdge: false },
  };
}
function overview(): OverviewResponse {
  const h = head();
  const run = runningRun();
  const lock = run ? { updateId: run.run.updateId, kind: run.run.kind, revId: run.run.revId, coordinator: run.run.coordinator, startedAt: run.run.startedAt, lastContactAt: D.iso(nowMs() - 1000) } : null;
  const hosts = h ? resolveSiteHosts(h.config, D.DEP, { detected: D.ADVERTISE, placeholder: false }) : {};
  const open = [...props.values()].map((r) => ({ r, p: refreshProposal(r) })).filter((x) => !PROPOSAL_TERMINAL.includes(x.p.status)).sort((a, b) => b.p.seq - a.p.seq)[0];
  const tunnelActive = lease?.status === 'active' && leaseSession !== null;
  const tiles: SiteTile[] = ALL_SITES.map((s) => {
    const st = siteStatus(s);
    const managed = st.containers.filter((c) => c.managed);
    return {
      siteSlug: s,
      label: h?.config.sites.find((x) => x.siteSlug === s)?.label ?? null,
      self: s === D.SELF,
      enrolled: true,
      reachable: reachable(s),
      lastSeenAt: lastSeen(s),
      host: hosts[s]?.host ?? null,
      advertiseHost: D.ADVERTISE[s] ?? null,
      agentPort: s === 'app' ? 7080 : s === 'db' ? 7081 : 7082,
      agentVersion: agentVersionOf(s),
      head: h ? { revId: h.revId, seq: h.seq } : null,
      staging: open ? { revId: open.p.revId, staging: open.p.sites[s] ?? D.stagingOf('pending', 0, nowMs()) } : null,
      cloudRoute: s === 'app' ? (tunnelActive ? 'tunnel' : 'direct') : 'relay',
      containers: { total: managed.length, ready: managed.filter((c) => c.ready).length },
      inConfig: !h || headSites().includes(s),
      lock,
    };
  });
  const dbUp = reachable('db');
  return {
    deploymentId: D.DEP,
    self: D.SELF,
    serverTime: D.iso(nowMs()),
    head: h ? { revId: h.revId, seq: h.seq, hash: h.hash, summary: h.summary, createdAt: h.createdAt } : null,
    sites: tiles,
    links: { app: { app: 'self', db: dbUp ? 3 : null, northwind: reachable('northwind') ? 21 : null }, db: { app: dbUp ? 4 : null, db: 'self', northwind: dbUp && reachable('northwind') ? 22 : null }, northwind: { app: reachable('northwind') ? 20 : null, db: dbUp && reachable('northwind') ? 23 : null, northwind: 'self' } },
    cloud: { route: tunnelActive ? 'tunnel' : 'direct', lastOkAt: D.iso(nowMs() - 4000), fetchedAt: D.iso(nowMs() - 9000), source: 'cloud' },
    lock,
    deleted: false,
  };
}

// ───────────────────────────────────────────── session + actor
interface MockSession {
  sessionId: string;
  expiresAt: string;
  keyVersion: number;
  /** §21 #118 the session runs through the cloud (tests set it in opm.mock.session) */
  remote?: { email: string; name: string | null; actorToken: string; expiresAt: string } | null;
}
function getSession(): MockSession | null {
  try {
    return JSON.parse(ls(SESSION_KEY) ?? 'null') as MockSession | null;
  } catch {
    return null;
  }
}
function requireSession(): MockSession {
  const s = getSession();
  if (!s) throw new MockError('unauthenticated', 'Log in first.');
  if (Date.parse(s.expiresAt) <= nowMs()) throw new MockError('session-expired', 'The session ended. Log in again.');
  return s;
}
function requireActor(headers: Headers, s: MockSession): string {
  const a = headers.get('Authorization') ?? '';
  const m = /^Actor mock\.(.+)$/.exec(a);
  if (!m) throw new MockError('actor-required', 'Say who is working right now.');
  try {
    const c = JSON.parse(atob(m[1])) as { email: string; sid: string; exp: number };
    if (c.sid !== s.sessionId || c.exp * 1000 <= nowMs()) throw new Error('x');
    return c.email;
  } catch {
    throw new MockError('actor-required', 'The actor token is not valid for this session.');
  }
}

// ───────────────────────────────────────────── tunnel
function tunnelRequest(): TunnelRequest {
  tunnelSeq++;
  // like the agent: everything is sealed, the tab sees one route and ciphertext (§9.10)
  const big = tunnelSeq % 4 === 0;
  const url = `${D.MOCK_API}/v1/agent/sealed${big ? '?mock=big' : ''}`;
  return { reqId: D.mockUlid(), method: 'POST', url, headers: { 'Content-Type': 'application/json' }, bodyB64: btoa(JSON.stringify({ v: 1, kid: '0123456789abcdef', epk: 'bW9jaw==', ct: 'bW9jaw==' })), createdAt: D.iso(nowMs()), timeoutSec: 60 };
}
function checkLease(leaseId: string) {
  if (lease && lease.status === 'active' && nowMs() - Date.parse(lease.lastPollAt) > 30_000 && lease.leaseId === leaseId) lease.status = 'expired';
  if (!lease || lease.leaseId !== leaseId || lease.status !== 'active') throw new MockError('conflict', 'This tunnel lease is not active any more.', { lease });
}

/** mock of the cloud API answering tunnelled requests */
async function cloudFetch(url: URL, init: RequestInit): Promise<Response> {
  await sleep(120 + Math.random() * 200);
  const hdr = { 'Content-Type': 'application/json', 'X-OPM-Server-Time': String(Math.floor(nowMs() / 1000)) };
  const size = url.searchParams.get('mock') === 'big' ? 5 * 1024 * 1024 + 4321 : 2048;
  if (url.pathname === '/v1/agent/sealed' && init.method === 'POST') {
    const ct = new Uint8Array(size);
    for (let i = 0; i < ct.length; i += 4096) ct[i] = i & 0xff;
    return new Response(ct, { status: 200, headers: { 'Content-Type': 'application/octet-stream' } });
  }
  return new Response(JSON.stringify({ error: { code: 'not-found', message: 'no such cloud endpoint (mock)' } }), { status: 404, headers: hdr });
}

// ───────────────────────────────────────────── handlers
interface Ctx {
  params: Record<string, string>;
  query: URLSearchParams;
  body: unknown;
  raw: Uint8Array | null;
  headers: Headers;
}
type Handler = (c: Ctx, s: MockSession | null, actor: string | null) => unknown | Promise<unknown>;
const needSite = (s: string, allowStar: boolean) => {
  if (s === '*' && allowStar) return;
  if (!ALL_SITES.includes(s)) throw new MockError('not-found', `site ${s} is not part of this deployment`);
};
const needReach = (s: string) => {
  if (!reachable(s)) throw new MockError('unavailable', `site ${s} does not answer (last seen ${lastSeen(s).slice(11, 16)} UTC)`);
};
const fileResponse = (body: BlobPart, name: string, type = 'application/octet-stream', extra: Record<string, string> = {}) => new Response(new Blob([body], { type }), { status: 200, headers: { 'Content-Type': type, 'Content-Disposition': `attachment; filename="${name}"`, ...extra } });

/** round E: when the starter content of a volume was applied (site/volume → state); seeded volumes start seeded 3 days ago */
const seededAt: Record<string, { at: string; source: NonNullable<SiteConfig['volumes'][number]['seed']>['kind']; entries: number }> = {};
const seedEntries = (seed: NonNullable<SiteConfig['volumes'][number]['seed']>) => (seed.kind === 'files' || seed.kind === 'tree' ? seed.entries.length : 1);
function seededOf(site: string, v: SiteConfig['volumes'][number]) {
  if (!v.seed) return null;
  return seededAt[`${site}/${v.volumeSlug}`] ?? { at: D.ago(86400 * 3), source: v.seed.kind, entries: seedEntries(v.seed) };
}
/** §21 #96 dependencies of the mock volume manager */
const volDeps: VO.VolDeps = {
  fail: (code, message, details = {}) => {
    throw new MockError(code, message, details as Json);
  },
  addActivity: (site, type, summary, actor, data) => addActivity(site, type, summary, actor, data),
  snapshot: (site, volumeSlug, actor, note) => {
    const h = head();
    const snap: Snapshot = { snapshotId: D.mockUlid(), createdAt: D.iso(nowMs()), reason: 'manual', revId: h?.revId ?? null, seq: h?.seq ?? 0, createdBy: actor, note, revision: h, sites: { [site]: { config: h!.config, images: [], volumes: snapVolumes(site, h!.config, note).filter((x) => x.volumeSlug === volumeSlug), sizeBytes: 40_960 } } };
    snapshots.push({ s: snap, createdMs: nowMs(), readyMs: 0 });
    addActivity(site, 'snapshot.created', `Snapshot ${snap.snapshotId} created (${note})`, actor, { snapshotId: snap.snapshotId, reason: 'manual', volumes: [volumeSlug] });
    return snap.snapshotId;
  },
  users: (site, volumeSlug) => {
    const sc = head()?.config.sites.find((x) => x.siteSlug === site)?.config;
    if (!sc) return [];
    return sc.containers.filter((c) => c.volumes.some((m) => m.source.kind === 'volume' && m.source.volumeSlug === volumeSlug)).map((c) => ({ slug: c.containerSlug, running: c.enabled, db: isDatabaseImage(c.serviceSlug) }));
  },
  updateRunning: () => !!runningRun(),
};
function volIntentStart(kind: 'volume-empty' | 'volume-restore'): Handler {
  return (c, s, actor) => {
    const site = c.params.siteSlug;
    needSite(site, false);
    needReach(site);
    VO.guard(site, [c.params.volumeSlug], volDeps);
    if (!head()?.config.sites.find((x) => x.siteSlug === site)?.config.volumes.some((v) => v.volumeSlug === c.params.volumeSlug)) throw new MockError('not-found', `site ${site} declares no volume ${c.params.volumeSlug}`);
    const intent = newIntent({ kind, revId: null, snapshotId: null, volume: { siteSlug: site, volumeSlug: c.params.volumeSlug }, actor: actor!, sessionId: s!.sessionId }, site);
    return { intent: publicIntent(intent), gate: { open: true, conditions: [{ key: 'idle', ok: true, detail: 'no update and no other action on this volume is running' }], sites: [site] } };
  };
}
/** §21 #101 dependencies of the mock volume sync (lib/mock/volsync.ts) */
const volSyncDeps: VolSyncDeps = {
  requireSession,
  requireActor: (h, s) => requireActor(h, s as MockSession),
  json,
  fail: (code, message, details = {}) => {
    throw new MockError(code as ErrorCode, message, details as Json);
  },
  reach: (site) => needReach(site),
  head: () => head(),
  addActivity: (site, type, summary, actor, data) => addActivity(site, type, summary, actor, (data ?? {}) as Json),
  snapshot: (site, volumeSlug, actor, note) => volDeps.snapshot(site, volumeSlug, actor, note),
  users: (site, volumeSlug) => volDeps.users(site, volumeSlug).map((u) => ({ slug: u.slug, running: u.running })),
  updateRunning: () => !!runningRun(),
  guard: (site, volumeSlug) => VO.guard(site, [volumeSlug], volDeps),
  startIntent: (site, volumeSlug, actor, sessionId) => {
    const intent = newIntent({ kind: 'volume-content-apply', revId: null, snapshotId: null, volume: { siteSlug: site, volumeSlug }, actor, sessionId }, site);
    return { intent: publicIntent(intent), gate: { open: true, conditions: [{ key: 'idle', ok: true, detail: 'no update and no other action on this volume is running' }], sites: [site] } };
  },
  useIntent: (_site, volumeSlug, intentId, sessionId, actor) => {
    const it = intents.get(intentId);
    if (!it || it.sessionId !== sessionId || it.kind !== 'volume-content-apply' || it.volume?.volumeSlug !== volumeSlug) throw new MockError('invalid', 'There is no pending apply for this session. Start again.');
    if (intentLeft(it) > 0) throw new MockError('invalid', `Wait ${Math.ceil(intentLeft(it))} s more.`);
    return useIntent(it, actor);
  },
  ffSuffix,
};
function volIntentUse(c: Ctx, s: MockSession | null, actor: string | null, kind: 'volume-empty' | 'volume-restore', typed: unknown, intentId: unknown): FastForwardUse | null {
  const it = intents.get(String(intentId ?? ''));
  if (!it || it.sessionId !== s!.sessionId || it.kind !== kind || it.volume?.volumeSlug !== c.params.volumeSlug) throw new MockError('invalid', 'There is no pending action for this session. Start again.');
  if (intentLeft(it) > 0) throw new MockError('invalid', `Wait ${Math.ceil(intentLeft(it))} s more.`);
  if (String(typed ?? '').trim() !== c.params.volumeSlug) throw new MockError('invalid', `Type ${c.params.volumeSlug} exactly.`);
  return useIntent(it, actor!);
}
/** §21 #100 store-and-forward simulation of a new to-cloud transfer: queued (0.8 s) → uploading (to 3.5 s) → stored */
const simStart = new Map<string, number>();
function newOutgoing(site: string, fileName: string, size: number, note: string, actor: string, sourcePath: string | null): Transfer {
  const t: Transfer = { transferId: 'local-' + D.mockUlid().slice(0, 16), deploymentId: D.DEP, siteSlug: site, direction: 'to-cloud', fileName, sizeBytes: size, sha256: D.fakeSha(fileName + nowMs()), storagePath: '', uploadId: null, createdAt: D.iso(nowMs()), createdBy: actor, note, expiresAt: null, status: 'uploading', savedPath: null, error: null, sourcePath, delivery: { phase: 'queued', bytesDone: 0, attempts: 0, lastError: null, lastAttemptAt: null, nextAttemptAt: null } };
  transfers[site].push(t);
  simStart.set(t.transferId, nowMs());
  return t;
}
function simTransfer(t: Transfer) {
  const s = simStart.get(t.transferId);
  if (s === undefined || !t.delivery) return;
  const el = nowMs() - s;
  if (el >= 3500) {
    t.status = 'ready';
    t.delivery = { ...t.delivery, phase: 'stored', bytesDone: t.sizeBytes };
    simStart.delete(t.transferId);
  } else if (el >= 800) t.delivery = { ...t.delivery, phase: 'uploading', bytesDone: Math.round((t.sizeBytes * (el - 800)) / 2700) };
}
// §21 #113 mock: images on this host's Docker (image picker) + the drift policy
const MOCK_HOST_IMAGES = [
  { id: 'sha256:7f1d2c3b4a5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f8', repoTags: ['nginx:1.27-alpine'], sizeBytes: 48_300_000, created: 1760000000 },
  { id: 'sha256:1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f809', repoTags: ['grafana/grafana:11.2.0'], sizeBytes: 412_000_000, created: 1759000000 },
];
let driftPolicyMode: 'ask' | 'auto-commit' = 'ask';

const H: Partial<Record<keyof typeof AGENT_API, Handler>> = {
  // §21 #96 volume manager
  listVolumeOps: (c) => {
    needSite(c.params.siteSlug, false);
    needReach(c.params.siteSlug);
    return { items: VO.volumeOpsList(c.params.siteSlug, c.query.get('volume')) };
  },
  getVolumeOp: (c) => {
    needSite(c.params.siteSlug, false);
    needReach(c.params.siteSlug);
    return clone(VO.volumeOp(c.params.siteSlug, c.params.opId, volDeps));
  },
  emptyVolumeStart: volIntentStart('volume-empty'),
  emptyVolume: (c, s, actor) => {
    needReach(c.params.siteSlug);
    const b = c.body as { intentId?: string; typed?: string; snapshot?: boolean };
    VO.guard(c.params.siteSlug, [c.params.volumeSlug], volDeps);
    const ff = volIntentUse(c, s, actor, 'volume-empty', b.typed, b.intentId);
    return clone(VO.emptyVolume(c.params.siteSlug, c.params.volumeSlug, actor!, !!b.snapshot, ff, volDeps));
  },
  copyVolume: (c, _s, actor) => {
    needReach(c.params.siteSlug);
    return clone(VO.copyVolume(c.params.siteSlug, c.params.volumeSlug, actor!, c.body as Parameters<typeof VO.copyVolume>[3], volDeps));
  },
  uploadVolumeRestore: (c) => {
    needReach(c.params.siteSlug);
    return VO.restoreUpload(c.params.siteSlug, c.params.volumeSlug, c.raw?.length ?? 0, c.query.get('fileName') ?? 'upload.tar.gz', volDeps);
  },
  restoreVolumeStart: volIntentStart('volume-restore'),
  restoreVolume: (c, s, actor) => {
    needReach(c.params.siteSlug);
    const b = c.body as { intentId?: string; typed?: string; source: { kind: 'copy'; copyId: string } | { kind: 'upload'; uploadId: string } | { kind: 'snapshot'; snapshotId: string } };
    VO.guard(c.params.siteSlug, [c.params.volumeSlug], volDeps);
    const ff = volIntentUse(c, s, actor, 'volume-restore', b.typed, b.intentId);
    return clone(VO.restoreVolume(c.params.siteSlug, c.params.volumeSlug, actor!, b.source, ff, volDeps));
  },
  snapshotVolume: (c, _s, actor) => {
    needReach(c.params.siteSlug);
    VO.guard(c.params.siteSlug, [c.params.volumeSlug], volDeps);
    const note = String((c.body as { note?: string })?.note ?? '').trim() || `volume ${c.params.volumeSlug}`;
    const id = volDeps.snapshot(c.params.siteSlug, c.params.volumeSlug, actor!, note);
    return { snapshotId: id, sizeBytes: 40_960 };
  },
  // §21 #112 every mock snapshot of the site that holds the volume (newest first); the volume's policy
  listVolumeBackups: (c) => {
    needReach(c.params.siteSlug);
    const site = c.params.siteSlug;
    const slug = c.params.volumeSlug;
    const sv = (head()?.config.sites.find((s) => s.siteSlug === site)?.config.volumes ?? []).find((v) => v.volumeSlug === slug);
    if (!sv) throw new MockError('not-found', `site ${site} declares no volume ${slug}`, {});
    const items = [...snapshots]
      .sort((a, b) => b.createdMs - a.createdMs)
      .flatMap(({ s }) => {
        const v = s.sites[site]?.volumes.find((x) => x.volumeSlug === slug);
        return v ? [{ snapshotId: s.snapshotId, createdAt: s.createdAt, reason: s.reason, note: s.note ?? '', createdBy: s.createdBy ?? 'system', sizeBytes: v.sizeBytes, auto: s.reason === 'volume-auto' }] : [];
      });
    const explicit = !!sv.backupPolicy;
    return { volumeSlug: slug, policy: sv.backup ? (sv.backupPolicy ?? { schedule: 'daily', keep: 7 }) : { schedule: 'off', keep: 0 }, explicit, nextAt: sv.backup ? D.iso(nowMs() + 3600_000) : null, items } as unknown as Json;
  },
  // §21 #112 the agent process itself
  agentProcess: (c) => {
    needReach(c.params.siteSlug);
    return { siteSlug: c.params.siteSlug, version: '0.17.1-mock', pid: 4242, uptimeSec: 86_400, rssBytes: 92 << 20, cpuPct: 1.4, openFds: 61, threads: 14, heapInUseBytes: 38 << 20, heapSysBytes: 56 << 20, memLimitBytes: 1024 << 20, goroutines: 187, gcCount: 1290, gcPauseLastMs: 0.21, dockerCalls: 48_211, dockerCallsPerMin: 92, dockerCacheLive: true, dockerCacheHits: 310_554, store: { ok: true, lastError: null, failures: 0 }, disk: { opmDbBytes: 18 << 20, metricsDbBytes: 41 << 20, blobsBytes: 2_400 << 20, snapshotsBytes: 6_100 << 20, snapshotBudgetBytes: 24_000 << 20, logsBytes: 33 << 20, freeBytes: 51_000 << 20, totalBytes: 80_000 << 20 }, warnings: [], loops: [{ name: 'heartbeat', periodSec: 30, ticks: 2880, running: false, panics: 0, lastMs: 41 }, { name: 'monitor', periodSec: 5, ticks: 17280, running: false, panics: 0, lastMs: 3 }] } as unknown as Json;
  },
  listVolumeCopies: (c) => {
    needSite(c.params.siteSlug, false);
    needReach(c.params.siteSlug);
    return { items: VO.volumeCopies(c.params.siteSlug) };
  },
  deleteVolumeCopy: (c, _s, actor) => {
    needReach(c.params.siteSlug);
    return VO.deleteCopy(c.params.siteSlug, c.params.copyId, String((c.body as { confirm?: string })?.confirm ?? ''), actor!, volDeps);
  },
  resetVolumeStart: (c, s, actor) => {
    const site = c.params.siteSlug;
    const v = head()?.config.sites.find((x) => x.siteSlug === site)?.config.volumes.find((x) => x.volumeSlug === c.params.volumeSlug);
    if (!v) throw new MockError('not-found', `site ${site} declares no volume ${c.params.volumeSlug}`);
    if (!v.seed) throw new MockError('invalid', 'this volume has no starter content', { errors: [{ path: 'seed', code: 'V_REQUIRED', message: 'no starter content', severity: 'error' }] });
    const gate = gateFor([site], null, { ok: true, detail: 'no staging needed' }, null);
    const intent = newIntent({ kind: 'volume-reset', revId: null, snapshotId: null, volume: { siteSlug: site, volumeSlug: v.volumeSlug }, actor: actor!, sessionId: s!.sessionId }, site);
    return { intent: publicIntent(intent), gate };
  },
  resetVolume: (c, s, actor) => {
    const site = c.params.siteSlug;
    const h = head();
    const v = h?.config.sites.find((x) => x.siteSlug === site)?.config.volumes.find((x) => x.volumeSlug === c.params.volumeSlug);
    if (!v || !v.seed) throw new MockError('not-found', 'no such volume with starter content');
    const b = c.body as { intentId?: string; typed?: string };
    const it = intents.get(b.intentId ?? '');
    if (!it || it.sessionId !== s!.sessionId || it.kind !== 'volume-reset') throw new MockError('invalid', 'There is no pending reset for this session.');
    if (intentLeft(it) > 0) throw new MockError('invalid', `Wait ${Math.ceil(intentLeft(it))} s more.`);
    if (b.typed !== 'ACCEPT') throw new MockError('invalid', 'Type ACCEPT exactly.');
    const ff = useIntent(it, actor!);
    const snap: Snapshot = { snapshotId: D.mockUlid(), createdAt: D.iso(nowMs()), reason: 'manual', revId: h?.revId ?? null, seq: h?.seq ?? 0, createdBy: actor!, note: `before reset of ${v.volumeSlug}`, revision: h, sites: { [site]: { config: h!.config, images: [], volumes: snapVolumes(site, h!.config, 'reset').filter((x) => x.volumeSlug === v.volumeSlug), sizeBytes: 40_960 } } };
    snapshots.push({ s: snap, createdMs: nowMs(), readyMs: 0 });
    const entries = seedEntries(v.seed);
    seededAt[`${site}/${v.volumeSlug}`] = { at: D.iso(nowMs()), source: v.seed.kind, entries };
    addActivity(site, 'snapshot.created', `Snapshot ${snap.snapshotId} created (before reset of ${v.volumeSlug})`, actor, { snapshotId: snap.snapshotId, reason: 'manual' });
    addActivity(site, 'volume.reset-to-seed', `Volume ${v.volumeSlug} reset to its starter content${ffSuffix(ff)}`, actor, { volumeSlug: v.volumeSlug, snapshotId: snap.snapshotId, entries, ...ffData(ff) });
    return { siteSlug: site, volumeSlug: v.volumeSlug, snapshotId: snap.snapshotId, entries };
  },
  healthz: () => new Response('ok', { headers: { 'Content-Type': 'text/plain' } }),
  info: () => ({ deploymentId: D.DEP, siteSlug: D.SELF, agentVersion: agentVersionOf(D.SELF), schema: agentSchemaOf(D.SELF), enrolled: true, hasAccessKey: ls('opm.mock.noKey') !== '1', keyVersion: 1, serverTime: D.iso(nowMs()), surface: 'gui-alias', ...{ apiBase: 'https://opm.example.com', deploymentLabel: 'Hello World' } }),
  login: (c) => {
    if (ls('opm.mock.noKey') === '1') throw new MockError('unavailable', 'This site has no access key yet; it gets it from the cloud or a peer.');
    loginFails = loginFails.filter((t) => nowMs() - t < 300_000);
    if (loginFails.length >= 5) {
      const retry = Math.ceil((loginFails[0] + 300_000 - nowMs()) / 1000);
      throw new MockError('rate-limited', 'Too many failed logins from this address.', { retryAfterSec: retry });
    }
    const key = String((c.body as { accessKey?: string })?.accessKey ?? '').toUpperCase().replace(/[^A-Z2-7]/g, '');
    if (key !== D.ACCESS_KEY) {
      loginFails.push(nowMs());
      addActivity(D.SELF, 'login.failed', 'Login failed (wrong access key)', null, { ip: '127.0.0.1' });
      throw new MockError('invalid-key', 'Wrong access key.', { failures: loginFails.length, max: 5 });
    }
    loginFails = [];
    const s: MockSession = { sessionId: D.mockUlid(), expiresAt: D.iso(nowMs() + 6 * 3600_000), keyVersion: 1 };
    localStorage.setItem(SESSION_KEY, JSON.stringify(s));
    addActivity(D.SELF, 'login.succeeded', 'Login with access key v1', null, { sessionId: s.sessionId, keyVersion: 1 });
    return s;
  },
  logout: (_c, s) => {
    localStorage.removeItem(SESSION_KEY);
    addActivity(D.SELF, 'logout', 'Logout', null, { sessionId: s?.sessionId });
    return { loggedOut: true };
  },
  session: (_c, s) => s,
  declareActor: (c, s) => {
    const email = String((c.body as { email?: string })?.email ?? '').trim().toLowerCase();
    if (!/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(email) || email.length > 254) throw new MockError('invalid', 'Enter a valid email address.', { errors: [{ path: 'email', code: 'V_FORMAT', message: 'not an email address', severity: 'error' }] });
    const exp = Math.floor(Date.parse(s!.expiresAt) / 1000);
    const token = 'mock.' + btoa(JSON.stringify({ email, sid: s!.sessionId, dep: D.DEP, site: D.SELF, iat: Math.floor(nowMs() / 1000), exp }));
    const row = actors.find((a) => a.email === email);
    if (row) {
      row.count++;
      row.lastUsedAt = D.iso(nowMs());
    } else actors.unshift({ email, lastUsedAt: D.iso(nowMs()), count: 1 });
    addActivity(D.SELF, 'actor.declared', `${email} is working now`, email, { email, sessionId: s!.sessionId });
    return { actorToken: token, expiresAt: s!.expiresAt, email };
  },
  recentActors: (c) => ({ actors: [...actors].sort((a, b) => b.lastUsedAt.localeCompare(a.lastUsedAt)).slice(0, Number(c.query.get('limit') ?? 10)) }),
  overview: () => overview(),
  config: () => {
    const h = head();
    return { head: h ? { revId: h.revId, seq: h.seq, hash: h.hash } : null, config: h ? maskConfig(h.config) : { variables: [], secrets: [], files: [], sites: [] }, catalog: h ? D.catalog : null, knownCatalog: h ? D.catalog : undefined };
  },
  // §21 #113 site images (mock: a resolved image joins the catalog at once)
  siteImages: () => ({ catalog: D.catalog, host: MOCK_HOST_IMAGES }),
  resolveSiteImage: (c) => {
    const b = (c.body ?? {}) as { reference?: string; imageId?: string };
    const host = b.imageId ? MOCK_HOST_IMAGES.find((x) => x.id === b.imageId) : null;
    const ref = (b.reference ?? host?.repoTags[0] ?? '').trim();
    if (!ref) throw new MockError('invalid', 'give a docker reference or a host image', { errors: [{ path: 'reference', code: 'V_REQUIRED', message: 'a reference is required', severity: 'error' }] });
    if (/\s/.test(ref) || ref.startsWith('/')) throw new MockError('invalid', 'not a docker reference', { errors: [{ path: 'reference', code: 'V_REFERENCE', message: 'a docker reference is [host/]repository[:tag]', severity: 'error' }] });
    if (ref.startsWith('unreachable.example/')) throw new MockError('unavailable', `docker pull ${ref}: registry not reachable from this site`);
    const repo = ref.split('@')[0]!.replace(/:[^:/]+$/, '').split('/').pop()!.toLowerCase().replace(/[^a-z0-9-]/g, '-').replace(/^[^a-z]+/, '') || 'image';
    let h = 0;
    for (const ch of ref) h = (h * 31 + ch.charCodeAt(0)) >>> 0;
    const hex = (h.toString(16) + '000000000000').slice(0, 12);
    const id = host?.id ?? `sha256:${hex}${'0'.repeat(52)}`;
    const image = { serviceSlug: repo, version: `local-${hex}`, digest: '', configDigest: id, platform: 'linux/amd64', manifestMediaType: '', layers: [], sizeBytes: host?.sizeBytes ?? 40_000_000, kind: 'service' as const, entrypoint: null, command: null, workdir: null, user: null, ports: [{ name: 'p80', port: 80, protocol: 'tcp' as const }], env: [], volumes: [], healthcheck: null, stopTimeoutSec: 10, reference: ref.includes('@') ? ref : `${ref}@sha256:${hex}${'f'.repeat(52)}` };
    const key = `${image.serviceSlug}:${image.version}`;
    const cat = D.catalog as unknown as { services: Record<string, unknown>; images: Record<string, unknown> };
    cat.images[key] = image;
    const service = { serviceSlug: repo, label: repo, icon: null };
    cat.services[repo] ??= service;
    return { key, image, service, pulled: !host };
  },
  getDriftPolicy: () => ({ mode: driftPolicyMode }),
  putDriftPolicy: (c) => {
    const m = (c.body as { mode?: string } | null)?.mode;
    if (m !== 'ask' && m !== 'auto-commit') throw new MockError('invalid', 'mode is ask or auto-commit');
    driftPolicyMode = m;
    return { mode: m };
  },
  siteGetDriftPolicy: () => ({ mode: driftPolicyMode }),
  sitePutDriftPolicy: (c) => {
    const m = (c.body as { mode?: string } | null)?.mode;
    if (m !== 'ask' && m !== 'auto-commit') throw new MockError('invalid', 'mode is ask or auto-commit');
    driftPolicyMode = m;
    return { mode: m };
  },
  listRevisions: () => ({ items: [...revisions].reverse().map(({ revId, seq, parentRevId, origin, actor, createdAt, hash, summary, kind }) => ({ revId, seq, parentRevId, origin, actor, createdAt, hash, summary, kind })) }),
  getRevision: (c) => {
    const r = revisions.find((x) => x.revId === c.params.revId) ?? props.get(c.params.revId)?.rev;
    if (!r) throw new MockError('not-found', 'no such revision');
    return { ...r, config: maskConfig(r.config) };
  },
  revealSecret: (c, s, actor) => {
    // §21 #84 e: 30 reveals per session per fixed minute → 429 rate-limited {retryAfterSec}
    const minute = Math.floor(nowMs() / 60_000);
    const key = s?.sessionId ?? '-';
    const w = revealRate.get(key);
    const n = w && w.minute === minute ? w.n : 0;
    if (n >= REVEAL_RATE_PER_MINUTE) throw new MockError('rate-limited', 'Too many reveals; try again in a minute.', { retryAfterSec: Math.max(1, Math.ceil(((minute + 1) * 60_000 - nowMs()) / 1000)) });
    revealRate.set(key, { minute, n: n + 1 });
    const v = head()?.config.secrets.find((x) => x.secretSlug === c.params.secretSlug);
    if (!v) throw new MockError('not-found', 'no such secret');
    if (!v.value) throw new MockError('not-found', 'this secret has no value');
    addActivity(D.SELF, 'secret.revealed', `Secret ${v.secretSlug} revealed`, actor, { secretSlug: v.secretSlug, actor, where: 'agent', siteSlug: D.SELF });
    return { value: v.value, hideAfterSec: REVEAL_SECONDS };
  },
  getFileContent: (c) => {
    const f = head()?.config.files.find((x) => x.fileSlug === c.params.fileSlug);
    if (!f || !f.content) throw new MockError('not-found', 'this file has no content');
    return fileResponse(D.fileBlobs[f.content.sha256] ?? '', f.fileSlug, f.mime);
  },
  uploadBlob: (c) => {
    const bytes = c.raw ?? new Uint8Array();
    if (bytes.length > 50 * 1024 * 1024) throw new MockError('too-large', 'file content is larger than 50 MiB');
    const text = new TextDecoder().decode(bytes);
    const sha = D.fakeSha(text);
    D.fileBlobs[sha] = text;
    return { sha256: sha, sizeBytes: bytes.length };
  },
  previewLocalProposal: (c) => {
    const { changes, issues } = localCheck(c.body as LocalProposalRequest);
    return { changes, validation: { ok: !issues.some((i) => i.severity === 'error'), issues } };
  },
  createLocalProposal: (c, _s, actor) => {
    const body = c.body as LocalProposalRequest;
    const { config, changes, issues } = localCheck(body);
    if (issues.some((i) => i.severity === 'error')) throw new MockError('invalid', 'The local proposal has errors.', { errors: issues });
    if (!body.summary || body.summary.length > 200) throw new MockError('invalid', 'summary must be 1–200 characters', { errors: [{ path: 'summary', code: 'V_LENGTH', message: 'summary must be 1–200 characters', severity: 'error' }] });
    const h = head()!;
    const rev = D.makeRevision(h.seq + 1, config, { createdMs: nowMs(), parentRevId: h.revId, summary: body.summary, origin: 'agent', actor: actor! });
    for (const r of props.values())
      if (!PROPOSAL_TERMINAL.includes(refreshProposal(r).status)) {
        r.p.status = 'superseded';
        r.p.supersededBy = rev.revId;
        r.fixed = r.p.sites;
      }
    const rec: PropRec = { p: D.makeProposal(rev, 'published', {}), rev, createdMs: nowMs(), stageMs: 4500, failSite: body.summary.includes('FAILSTAGE') ? 'db' : undefined };
    rec.p.createdBy = actor!;
    props.set(rev.revId, rec);
    addActivity(D.SELF, 'proposal.created', `Local proposal seq ${rev.seq}: ${body.summary}`, actor, { revId: rev.revId, seq: rev.seq, summary: body.summary });
    return { proposal: refreshProposal(rec), changes };
  },
  listProposals: () => {
    const all = [...props.values()].map((r) => refreshProposal(r)).sort((a, b) => b.seq - a.seq);
    const open = all.filter((p) => !PROPOSAL_TERMINAL.includes(p.status));
    const term = all.filter((p) => PROPOSAL_TERMINAL.includes(p.status)).slice(0, 20);
    return { items: [...open, ...term].map((p) => ({ proposal: p, isHeadParent: p.parentRevId === headRevId })) };
  },
  getProposal: (c) => {
    const r = props.get(c.params.revId);
    if (!r) throw new MockError('not-found', 'no such proposal');
    const p = refreshProposal(r);
    const parent = revisions.find((x) => x.revId === r.rev.parentRevId) ?? null;
    return { proposal: p, revision: { ...r.rev, config: maskConfig(r.rev.config) }, changes: diffConfigs(head()?.config ?? parent?.config ?? null, r.rev.config, slugOf), staging: propSites(r).map((s) => stagingDetail(r, s)), gate: proposalGate(r) };
  },
  restage: (c) => {
    const r = props.get(c.params.revId);
    if (!r) throw new MockError('not-found', 'no such proposal');
    if (PROPOSAL_TERMINAL.includes(refreshProposal(r).status)) throw new MockError('conflict', `the proposal is ${r.p.status}`);
    r.failSite = undefined;
    r.fixed = undefined;
    r.createdMs = nowMs();
    return { ok: true };
  },
  approveStart: (c, s, actor) => {
    const r = props.get(c.params.revId);
    if (!r) throw new MockError('not-found', 'no such proposal');
    const gate = proposalGate(r);
    if (!gate.open) throw new MockError('gate-closed', 'The gate is closed.', { gate });
    const intent = newIntent({ kind: 'update', revId: r.rev.revId, snapshotId: null, actor: actor!, sessionId: s!.sessionId });
    return { intent: publicIntent(intent), gate };
  },
  approve: (c, s, actor) => {
    const r = props.get(c.params.revId);
    if (!r) throw new MockError('not-found', 'no such proposal');
    const b = c.body as { intentId?: string; typed?: string };
    const it = intents.get(b.intentId ?? '');
    if (!it || it.sessionId !== s!.sessionId || Date.parse(it.expiresAt) < nowMs()) throw new MockError('invalid', 'There is no pending approval for this session. Press Approve again.');
    if (intentLeft(it) > 0) throw new MockError('invalid', `Wait ${Math.ceil(intentLeft(it))} s more.`);
    if (b.typed !== 'ACCEPT') throw new MockError('invalid', 'Type ACCEPT exactly.');
    const gate = proposalGate(r);
    if (!gate.open) throw new MockError('gate-closed', 'The gate closed during the wait.', { gate });
    const ff = useIntent(it, actor!);
    r.p.approvedBy = actor;
    r.p.approvedAt = D.iso(nowMs());
    r.p.approvedFastForward = ff;
    addActivity(D.SELF, 'proposal.approved', `Proposal seq ${r.p.seq} approved${ffSuffix(ff)}`, actor, { revId: r.rev.revId, approver: actor, ...ffData(ff) });
    const sim = startRun('update', r.rev, actor!, r.rev.revId, null, ff);
    r.p.updateId = sim.run.updateId;
    return { updateId: sim.run.updateId, run: sim.run };
  },
  cancelIntent: (c, s) => {
    intents.delete(pendingIntent(c.params.intentId, s, D.SELF).intentId);
    return { cancelled: true };
  },
  // §21 #95 hold-to-fast-forward (the agent integrates on its own clock; the GUI only animates)
  getIntent: (c, s) => intentProgress(pendingIntent(c.params.intentId, s, D.SELF)),
  intentSpeed: (c, s) => speedIntent(pendingIntent(c.params.intentId, s, D.SELF), c.body),
  siteIntent: (c, s) => {
    needSite(c.params.siteSlug, false);
    needReach(c.params.siteSlug);
    return intentProgress(pendingIntent(c.params.intentId, s, c.params.siteSlug));
  },
  siteIntentSpeed: (c, s) => {
    needSite(c.params.siteSlug, false);
    needReach(c.params.siteSlug);
    return speedIntent(pendingIntent(c.params.intentId, s, c.params.siteSlug), c.body);
  },
  siteCancelIntent: (c, s) => {
    needSite(c.params.siteSlug, false);
    needReach(c.params.siteSlug);
    intents.delete(pendingIntent(c.params.intentId, s, c.params.siteSlug).intentId);
    return { cancelled: true };
  },
  currentUpdate: () => {
    if (!currentRunId) return { run: null };
    const sim = runs.get(currentRunId)!;
    computeRun(sim);
    if (sim.run.finishedAt && nowMs() - Date.parse(sim.run.finishedAt) > 600_000) return { run: null };
    return { run: sim.run };
  },
  getUpdate: (c) => {
    const sim = runs.get(c.params.updateId);
    if (!sim) throw new MockError('not-found', 'no such update');
    computeRun(sim);
    return sim.run;
  },
  // §21 #110 stop the update on every site of its plan (any site; "current" = the running one)
  terminateUpdate: (c, _s, actor) => {
    const b = (c.body ?? {}) as TerminateUpdateRequest;
    const id = c.params.updateId === 'current' ? (runningRun()?.run.updateId ?? currentRunId ?? '') : c.params.updateId;
    const sim = runs.get(id);
    if (!sim) throw new MockError('not-found', `no update ${c.params.updateId}`);
    const reason = (b.reason ?? '').slice(0, 200) || 'stopped by a person';
    const ran = terminateRun(sim, reason, actor);
    const sites: TerminateUpdateResponse['sites'] = sim.sites.map((site) => {
      if (!reachable(site)) return { siteSlug: site, ok: false, message: 'not reachable: it stops the update at its next ping' };
      let m = ran ? 'stopped' : 'nothing was running here';
      if (b.rollback && b.restartAgent) m += '; the agent restarts, then rolls back to the pre-update backup';
      else if (b.rollback) m += '; rolling back to the pre-update backup';
      else if (b.restartAgent) m += '; the agent restarts';
      return { siteSlug: site, ok: true, message: m };
    });
    if (b.restartAgent) restartAllAgents(sim.sites.filter((x) => reachable(x)), addActivity, actor, `terminate${b.rollback ? ' + roll back' : ''}: ${reason}`);
    if (b.rollback && sim.kind !== 'recover') {
      const h = head();
      if (h) {
        const rec = startRun('rollback', h, actor ?? 'agent', null, null, null, `recover-${sim.run.updateId}`);
        rec.kind = 'recover';
        rec.recoverOf = sim.run.updateId;
      }
    }
    return { updateId: sim.run.updateId, sites } satisfies TerminateUpdateResponse;
  },
  abandonUpdate: () => {
    throw new MockError('conflict', 'The coordinator answered within the last 10 min; abandon is not allowed.');
  },
  listSnapshots: () => ({
    items: snapshots
      .map(({ s, readyMs }) => {
        const pending = readyMs > nowMs();
        const parts = Object.keys(s.sites);
        const missing = pending ? parts.filter((x, i) => nowMs() < readyMs - 1200 * (parts.length - 1 - i)) : parts.filter((x) => !reachable(x) && s.reason === 'imported');
        return D.snapshotSummary(s, missing);
      })
      .sort((a, b) => b.createdAt.localeCompare(a.createdAt)),
  }),
  createSnapshot: (c, _s, actor) => {
    const sites = headSites();
    const un = sites.filter((s) => !reachable(s));
    if (un.length) throw new MockError('gate-closed', `every site of the head config must be reachable (${un.join(', ')} is not)`, { gate: gateFor(sites, null, { ok: true, detail: '' }, null) });
    const note = String((c.body as { note?: string })?.note ?? '');
    if (note.length > 500) throw new MockError('invalid', 'note ≤ 500 characters');
    const h = head();
    const s: Snapshot = { snapshotId: D.mockUlid(), createdAt: D.iso(nowMs()), reason: 'manual', revId: h?.revId ?? null, seq: h?.seq ?? 0, createdBy: actor!, note, revision: h, sites: Object.fromEntries(sites.map((x) => [x, { config: h!.config, images: [], volumes: snapVolumes(x, h!.config, 'manual'), sizeBytes: x === 'db' ? 1_250_002 : 41_200 }])) };
    snapshots.push({ s, createdMs: nowMs(), readyMs: nowMs() + 3000 });
    addActivity(D.SELF, 'snapshot.created', `Snapshot ${s.snapshotId} created (manual)`, actor, { snapshotId: s.snapshotId, reason: 'manual' });
    return { snapshotId: s.snapshotId, sites };
  },
  getSnapshot: (c) => {
    const x = snapshots.find((y) => y.s.snapshotId === c.params.snapshotId);
    if (!x) throw new MockError('not-found', 'no such snapshot');
    const list = (H.listSnapshots!(c, null, null) as { items: ReturnType<typeof D.snapshotSummary>[] }).items;
    const s = clone(x.s);
    for (const k of Object.keys(s.sites)) s.sites[k].config = maskConfig(s.sites[k].config) as unknown as Config;
    if (s.revision) s.revision = { ...s.revision, config: maskConfig(s.revision.config) as unknown as Config };
    return { summary: list.find((y) => y.snapshotId === x.s.snapshotId)!, snapshot: s };
  },
  patchSnapshot: (c) => {
    const x = snapshots.find((y) => y.s.snapshotId === c.params.snapshotId);
    if (!x) throw new MockError('not-found', 'no such snapshot');
    const note = String((c.body as { note?: string })?.note ?? '');
    if (note.length > 500) throw new MockError('invalid', 'note ≤ 500 characters', { errors: [{ path: 'note', code: 'V_LENGTH', message: 'at most 500 characters', severity: 'error' }] });
    x.s.note = note;
    return D.snapshotSummary(x.s);
  },
  deleteSnapshot: (c, _s, actor) => {
    const i = snapshots.findIndex((y) => y.s.snapshotId === c.params.snapshotId);
    if (i < 0) throw new MockError('not-found', 'no such snapshot');
    const run = runningRun();
    if (run && run.snapshotId === c.params.snapshotId) throw new MockError('conflict', 'this is the pre-update snapshot of the running update');
    snapshots.splice(i, 1);
    addActivity(D.SELF, 'snapshot.deleted', `Snapshot ${c.params.snapshotId} deleted`, actor, { snapshotId: c.params.snapshotId });
    return { deleted: true };
  },
  downloadSnapshot: (c) => {
    const x = snapshots.find((y) => y.s.snapshotId === c.params.snapshotId);
    if (!x) throw new MockError('not-found', 'no such snapshot');
    const un = Object.keys(x.s.sites).filter((s) => !reachable(s));
    if (un.length) throw new MockError('unavailable', `the part of site ${un.join(', ')} is not reachable`);
    return fileResponse(`opmsnap/1 mock archive of ${x.s.snapshotId}${c.query.get('includeImages') === '1' ? ' (with images)' : ''}\n`, `${D.DEP}-${x.s.snapshotId}.opmsnap`, 'application/x-tar', { 'X-OPM-Warning': 'contains-secrets' });
  },
  importSnapshot: (c, _s, actor) => {
    const text = new TextDecoder().decode((c.raw ?? new Uint8Array()).slice(0, 64));
    if (!text.startsWith('opmsnap/1')) throw new MockError('invalid', 'not an .opmsnap file: manifest.json is missing (first entry)', { errors: [{ path: 'manifest.json', code: 'V_FORMAT', message: 'manifest.json is missing', severity: 'error' }] });
    const m = /opmsnap\/1 mock archive of (\w+)/.exec(text);
    if (m && snapshots.some((y) => y.s.snapshotId === m[1])) throw new MockError('conflict', `snapshot ${m[1]} already exists on this deployment`);
    const h = head()!;
    const s: Snapshot = { snapshotId: D.mockUlid(), createdAt: D.ago(86400 * 3), reason: 'imported', revId: h.revId, seq: h.seq, createdBy: actor!, note: 'imported from file', revision: h, sites: Object.fromEntries(headSites().map((x) => [x, { config: h.config, images: [], volumes: [], sizeBytes: 50_000 }])) };
    snapshots.push({ s, createdMs: nowMs(), readyMs: 0 });
    addActivity(D.SELF, 'snapshot.imported', `Snapshot ${s.snapshotId} imported`, actor, { snapshotId: s.snapshotId });
    return D.snapshotSummary(s);
  },
  rollbackPlan: (c) => {
    const x = snapshots.find((y) => y.s.snapshotId === c.params.snapshotId);
    if (!x) throw new MockError('not-found', 'no such snapshot');
    const h = head();
    const sc = x.s.revision?.config ?? Object.values(x.s.sites)[0]?.config;
    const sites = [...new Set([...headSites(), ...Object.keys(x.s.sites)])];
    const imgs = sites.flatMap((site) => {
      const a = h?.config.sites.find((s) => s.siteSlug === site)?.config.containers ?? [];
      const b = sc?.sites.find((s) => s.siteSlug === site)?.config.containers ?? [];
      const ids = [...new Set([...a, ...b].map((cc) => cc.containerSlug))];
      const tag = (cc: { serviceSlug: string; version: string } | undefined) => (cc ? `${cc.serviceSlug}:${cc.version}` : null);
      return ids.map((id) => {
        const ca = a.find((cc) => cc.containerSlug === id);
        const cb = b.find((cc) => cc.containerSlug === id);
        return { siteSlug: site, containerSlug: id, serviceSlug: (cb ?? ca)!.serviceSlug, from: tag(ca), to: tag(cb) };
      });
    });
    const vols = Object.entries(x.s.sites).flatMap(([site, p]) => p.volumes.map((v) => ({ siteSlug: site, volumeSlug: volSlugOf(v), sizeBytes: v.sizeBytes })));
    const pending = x.readyMs > nowMs();
    return {
      snapshot: D.snapshotSummary(x.s),
      images: imgs,
      changes: sc ? diffConfigs(h?.config ?? null, sc, slugOf) : [],
      volumesRestored: vols,
      volumesKept: sites.flatMap((site) => (h?.config.sites.find((s) => s.siteSlug === site)?.config.volumes ?? []).filter((v) => !vols.some((r) => r.siteSlug === site && r.volumeSlug === v.volumeSlug)).map((v) => ({ siteSlug: site, volumeSlug: v.volumeSlug, backup: v.backup }))),
      dataLossSince: x.s.createdAt,
      newSeq: (h?.seq ?? 0) + 1,
      gate: gateFor(sites, null, { ok: !pending, detail: pending ? 'the snapshot is still being taken' : 'the snapshot is complete on every site and every image exists there' }, null),
    };
  },
  rollbackStart: (c, s, actor) => {
    const x = snapshots.find((y) => y.s.snapshotId === c.params.snapshotId);
    if (!x) throw new MockError('not-found', 'no such snapshot');
    const plan = H.rollbackPlan!(c, s, actor) as { gate: GateCheck };
    if (!plan.gate.open) throw new MockError('gate-closed', 'The gate is closed.', { gate: plan.gate });
    const intent = newIntent({ kind: 'rollback', revId: null, snapshotId: x.s.snapshotId, actor: actor!, sessionId: s!.sessionId });
    return { intent: publicIntent(intent), gate: plan.gate };
  },
  rollback: (c, s, actor) => {
    const x = snapshots.find((y) => y.s.snapshotId === c.params.snapshotId);
    if (!x) throw new MockError('not-found', 'no such snapshot');
    const b = c.body as { intentId?: string; typed?: string };
    const it = intents.get(b.intentId ?? '');
    if (!it || it.sessionId !== s!.sessionId) throw new MockError('invalid', 'There is no pending rollback for this session.');
    if (intentLeft(it) > 0) throw new MockError('invalid', `Wait ${Math.ceil(intentLeft(it))} s more.`);
    if (b.typed !== 'ACCEPT') throw new MockError('invalid', 'Type ACCEPT exactly.');
    const ff = useIntent(it, actor!);
    const h = head();
    const cfg = clone(x.s.revision?.config ?? h!.config);
    const rev = D.makeRevision((h?.seq ?? 0) + 1, cfg, { createdMs: nowMs(), parentRevId: h?.revId ?? null, origin: 'agent', actor: actor!, kind: 'rollback', summary: `rollback to ${x.s.snapshotId} (seq ${x.s.seq})`, rollbackOf: { snapshotId: x.s.snapshotId, seq: x.s.seq } });
    const sim = startRun('rollback', rev, actor!, null, x.s.snapshotId, ff);
    return { updateId: sim.run.updateId, run: sim.run };
  },
  siteVolumes: (c) => {
    needSite(c.params.siteSlug, false);
    needReach(c.params.siteSlug);
    return siteVolumes(c.params.siteSlug);
  },
  siteStatus: (c) => {
    needSite(c.params.siteSlug, true);
    return { items: (c.params.siteSlug === '*' ? ALL_SITES : [c.params.siteSlug]).map((s) => siteStatus(s)) };
  },
  // §21 #124 the parts: the mock answers each with the whole status (the page only reads its own part)
  siteStatusOverview: (c) => {
    needSite(c.params.siteSlug, true);
    return { items: (c.params.siteSlug === '*' ? ALL_SITES : [c.params.siteSlug]).map((s) => siteStatus(s)) };
  },
  siteStatusContainers: (c) => {
    needSite(c.params.siteSlug, true);
    return { items: (c.params.siteSlug === '*' ? ALL_SITES : [c.params.siteSlug]).map((s) => siteStatus(s)) };
  },
  siteStatusUptime: (c) => {
    needSite(c.params.siteSlug, true);
    return { items: (c.params.siteSlug === '*' ? ALL_SITES : [c.params.siteSlug]).map((s) => siteStatus(s)) };
  },
  siteStatusEdge: (c) => {
    needSite(c.params.siteSlug, true);
    return { items: (c.params.siteSlug === '*' ? ALL_SITES : [c.params.siteSlug]).map((s) => siteStatus(s)) };
  },
  containerLogs: (c) => {
    needSite(c.params.siteSlug, false);
    needReach(c.params.siteSlug);
    const tail = Math.min(5000, Number(c.query.get('tail') ?? 200));
    const q = (c.query.get('q') ?? '').toLowerCase();
    const slug = c.params.containerSlug;
    const lines = Array.from({ length: 900 }, (_, i) => {
      const t = D.iso(nowMs() - (900 - i) * 4000);
      // §21 #93: the Deno backend logs with ANSI colors (ESC bytes kept, as Docker stores them)
      if (slug === 'northwind-backend-admin') return { ts: t, stream: (i % D.DENO_LINES.length < 3 ? 'stderr' : 'stdout') as 'stdout' | 'stderr', text: D.DENO_LINES[i % D.DENO_LINES.length] };
      if (slug === 'nodered') return { ts: t, stream: 'stdout' as const, text: D.ESC_LOST_LINES[i % D.ESC_LOST_LINES.length] };
      if (ls('opm.mock.pgMismatch') === `${c.params.siteSlug}/${slug}`) {
        const PG = ['\x1b[31m************************************************************\x1b[0m', '\x1b[31m***   DATA DIRECTORY / IMAGE VERSION MISMATCH              ***\x1b[0m', 'The data directory /var/lib/postgresql/data was initialised by PostgreSQL 16; this image runs PostgreSQL 17.', 'Refusing to start. Move the old data aside or run a pg_upgrade.'];
        return { ts: t, stream: 'stderr' as const, text: PG[i % PG.length] };
      }
      const text = i % 17 === 0 ? `WARN slow request GET /api/count took ${200 + i}ms` : i % 53 === 0 ? `ERROR redis: connection reset by peer (retry ${i % 5})` : `${slug} GET ${i % 3 ? '/api/count' : '/healthz'} 200 ${(i % 9) + 1}ms`;
      return { ts: t, stream: (i % 53 === 0 ? 'stderr' : 'stdout') as 'stdout' | 'stderr', text };
    }).filter((l) => !q || l.text.toLowerCase().includes(q));
    const cs = head()?.config.sites.find((x) => x.siteSlug === c.params.siteSlug)?.config.containers.find((x) => x.containerSlug === slug);
    return { siteSlug: c.params.siteSlug, containerSlug: slug, serviceSlug: cs?.serviceSlug ?? '', lines: lines.slice(-tail) };
  },
  siteActivities: (c) => {
    const site = c.params.siteSlug;
    needSite(site, true);
    if (site !== '*') needReach(site);
    let list = site === '*' ? ALL_SITES.filter(reachable).flatMap((s) => activities[s]).sort((a, b) => b.ts.localeCompare(a.ts)) : activities[site];
    const type = c.query.get('type');
    const actor = c.query.get('actor');
    if (type) list = list.filter((a) => (type.endsWith('.') ? a.type.startsWith(type) : a.type === type || a.type.startsWith(type)));
    if (actor) list = list.filter((a) => a.actor === actor.toLowerCase());
    // opaque cursor = activityId of the last item returned (stable while new entries are prepended)
    const cur = c.query.get('cursor');
    const off = cur ? list.findIndex((a) => a.activityId === cur) + 1 : 0;
    const lim = Math.min(500, Number(c.query.get('limit') ?? 100));
    const items = list.slice(off, off + lim);
    return { items, cursor: off + lim < list.length ? items[items.length - 1].activityId : null };
  },
  downloadActivities: (c, _s, actor) => {
    const site = c.params.siteSlug;
    needSite(site, true);
    if (site !== '*') needReach(site);
    const from = c.query.get('from') ?? '1';
    const to = c.query.get('to') ?? String(activities[site === '*' ? 'app' : site][0].seq);
    if (c.query.get('includeDebug') === '1') addActivity(D.SELF, 'debug.downloaded', 'Activity bundle with debug log downloaded', actor, { sites: site === '*' ? ALL_SITES : [site], fromSeq: Number(from), toSeq: Number(to) });
    return fileResponse(`opmlog/1 mock bundle ${site} ${from}-${to}\n`, `${D.DEP}-${site === '*' ? 'all' : site}-${from}-${to}.opmlog`, 'application/x-tar');
  },
  siteDebug: (c) => {
    const site = c.params.siteSlug;
    needSite(site, true);
    if (site !== '*') needReach(site);
    const order = { debug: 0, info: 1, warn: 2, error: 3 };
    const min = order[(c.query.get('level') ?? 'debug') as keyof typeof order] ?? 0;
    const mod = c.query.get('module');
    const since = c.query.get('since');
    const q = (c.query.get('q') ?? '').toLowerCase();
    const lim = Math.min(5000, Number(c.query.get('limit') ?? 500));
    let list = site === '*' ? ALL_SITES.filter(reachable).flatMap((s) => debug[s].map((l) => ({ ...l, site: s }))).sort((a, b) => b.ts.localeCompare(a.ts)) : debug[site];
    list = list.filter((l) => order[l.level] >= min && (!mod || l.module === mod) && (!since || l.ts >= since) && (!q || (l.msg + JSON.stringify(l.fields)).toLowerCase().includes(q)));
    return { items: list.slice(0, lim) };
  },
  listCerts: (c) => {
    const site = c.query.get('site') ?? D.SELF;
    return { items: site === '*' ? Object.values(certs).flat() : (certs[site] ?? []) };
  },
  renewCert: (c, _s, actor) => {
    const site = c.query.get('site') ?? D.SELF;
    needReach(site);
    const cert = (certs[site] ?? []).find((x) => x.hostname === c.params.hostname);
    if (!cert) throw new MockError('not-found', 'no such host certificate');
    if (cert.mode === 'none') throw new MockError('invalid', 'this host has TLS mode none; there is nothing to renew');
    cert.lastRenewalAt = D.iso(nowMs());
    cert.notBefore = D.iso(nowMs());
    cert.notAfter = D.iso(nowMs() + 12 * 3600_000);
    cert.nextRenewalAt = D.iso(nowMs() + 8 * 3600_000);
    addActivity(site, 'cert.renewed', `Certificate renewed for ${cert.hostname}`, actor, { hostname: cert.hostname, mode: cert.mode });
    return { status: 'renewed', cert };
  },
  localCa: (c) => {
    const site = c.query.get('site') ?? D.SELF;
    if (!(certs[site] ?? []).some((x) => x.mode === 'internal')) throw new MockError('not-found', 'this site has no internal TLS host');
    return fileResponse('-----BEGIN CERTIFICATE-----\nMIIBozCCAUmgAwIBAgIQMockOPMLocalCARoot0000000wCgYIKoZIzj0EAwIw\n-----END CERTIFICATE-----\n', `opm-${site}-local-ca.crt`, 'application/x-pem-file');
  },
  listSiteTransfers: (c) => {
    const site = c.params.siteSlug;
    needSite(site, false);
    needReach(site);
    const dir = c.query.get('direction');
    for (const t of transfers[site]) simTransfer(t);
    return { items: transfers[site].filter((t) => t.direction === dir).sort((a, b) => b.createdAt.localeCompare(a.createdAt)) };
  },
  sendFromBrowser: (c, _s, actor) => {
    const site = c.params.siteSlug;
    needSite(site, false);
    needReach(site);
    const name = c.query.get('fileName') ?? '';
    if (!name || /[/\\\0]/.test(name) || name.length > 255) throw new MockError('invalid', 'fileName must be 1–255 characters without / \\');
    const t = newOutgoing(site, name, c.raw?.length ?? 0, c.query.get('note') ?? '', actor!, null);
    addActivity(site, 'transfer.sent', `Sent ${name} to the cloud`, actor, { transferId: t.transferId, fileName: name, source: 'browser' });
    return t;
  },
  hostFs: (c) => {
    const site = c.params.siteSlug;
    needSite(site, false);
    needReach(site);
    const h = c.query.get('hidden');
    return hostFsList(c.query.get('path') ?? '/', h === '1' || h === 'true', (code, message, details = {}) => {
      throw new MockError(code, message, details as Json);
    });
  },
  sendFromPath: (c, _s, actor) => {
    const site = c.params.siteSlug;
    needSite(site, false);
    needReach(site);
    const b = c.body as { path?: string; note?: string };
    const path = String(b?.path ?? '');
    if (!path.startsWith('/')) throw new MockError('invalid', 'path must be absolute', { errors: [{ path: 'path', code: 'V_FORMAT', message: 'path must be absolute (start with /)', severity: 'error' }] });
    const f = hostFsFile(path);
    if ('code' in f) throw new MockError(f.code, f.message);
    const name = path.split('/').pop() || 'file';
    const t = newOutgoing(site, name, f.size, b.note ?? '', actor!, path);
    addActivity(site, 'transfer.sent', `Sent ${name} to the cloud (from ${path})`, actor, { transferId: t.transferId, fileName: name, source: 'path', sourcePath: path });
    return t;
  },
  saveTransfer: (c, _s, actor) => {
    const site = c.params.siteSlug;
    needSite(site, false);
    needReach(site);
    const t = transfers[site].find((x) => x.transferId === c.params.transferId);
    if (!t) throw new MockError('not-found', 'no such transfer');
    const path = String((c.body as { path?: string })?.path ?? '');
    if (!path.startsWith('/') || path.endsWith('/')) throw new MockError('invalid', 'path must be an absolute file path', { errors: [{ path: 'path', code: 'V_FORMAT', message: 'path must be an absolute file path', severity: 'error' }] });
    if (path.startsWith('/nope/')) throw new MockError('invalid', `parent directory ${path.split('/').slice(0, -1).join('/')} does not exist`);
    if (transfers[site].some((x) => x.savedPath === path) || path.endsWith('/exists')) throw new MockError('conflict', `${path} already exists`);
    t.status = 'saved-on-server';
    t.savedPath = path;
    addActivity(site, 'transfer.saved', `Saved ${t.fileName} to ${path}`, actor, { transferId: t.transferId, savedPath: path });
    return t;
  },
  downloadSiteTransfer: (c) => {
    const site = c.params.siteSlug;
    needSite(site, false);
    needReach(site);
    const t = transfers[site].find((x) => x.transferId === c.params.transferId);
    if (!t) throw new MockError('not-found', 'no such transfer');
    if (t.direction === 'to-site' && t.status === 'ready') t.status = 'downloaded';
    return fileResponse(`mock content of ${t.fileName}\n`, t.fileName);
  },
  tunnelState: () => {
    if (lease && lease.status === 'active' && nowMs() - Date.parse(lease.lastPollAt) > 30_000 && leaseSession) lease.status = 'expired';
    const active = lease?.status === 'active' ? lease : null;
    return { lease: active, holderSite: active?.siteSlug ?? null, pending: active ? 1 + (tunnelSeq % 3) : 0, encrypted: active?.encrypted ?? true };
  },
  tunnelStart: (c, s, actor) => {
    if (lease?.status === 'active' && !(leaseSession === null && nowMs() - Date.parse(lease.lastPollAt) > 30_000)) throw new MockError('conflict', 'Another tab gives internet already.', { active: lease });
    lease = { leaseId: D.mockUlid(), actor: actor!, siteSlug: D.SELF, userAgent: String((c.body as { userAgent?: string })?.userAgent ?? ''), startedAt: D.iso(nowMs()), lastPollAt: D.iso(nowMs()), status: 'active', encrypted: true, reqs: 0, bytes: 0, lastError: null };
    leaseSession = s!.sessionId;
    addActivity(D.SELF, 'internet.tunnel.started', 'Browser internet tunnel started', actor, { leaseId: lease.leaseId });
    return { lease };
  },
  tunnelStop: (c, _s, actor) => {
    const id = String((c.body as { leaseId?: string })?.leaseId ?? '');
    if (!lease || lease.leaseId !== id) throw new MockError('not-found', 'no such lease');
    if (lease.status !== 'active') throw new MockError('conflict', `the lease is ${lease.status}`);
    lease.status = 'stopped';
    leaseSession = null;
    addActivity(D.SELF, 'internet.tunnel.stopped', 'Browser internet tunnel stopped', actor, { leaseId: id, reqs: lease.reqs, bytes: lease.bytes });
    return { lease };
  },
  tunnelRevoke: (c, _s, actor) => {
    const id = String((c.body as { leaseId?: string })?.leaseId ?? '');
    if (!lease || lease.leaseId !== id) throw new MockError('not-found', 'no such lease');
    if (lease.status !== 'active') throw new MockError('conflict', `the lease is ${lease.status}`);
    lease.status = 'revoked';
    addActivity(D.SELF, 'internet.tunnel.revoked', `Tunnel of ${lease.actor} revoked`, actor, { leaseId: id, previousActor: lease.actor });
    const out = { lease: { ...lease } };
    lease = null;
    return out;
  },
  tunnelNext: async (c) => {
    const id = c.query.get('leaseId') ?? '';
    checkLease(id);
    lease!.lastPollAt = D.iso(nowMs());
    await sleep(900 + Math.random() * 700);
    checkLease(id);
    const max = Math.min(4, Number(c.query.get('max') ?? 4));
    const n = Math.min(max, 1 + (tunnelSeq % 2));
    const requests = Array.from({ length: n }, () => tunnelRequest());
    for (const r of requests) handed.set(r.reqId, { req: r, at: nowMs() });
    return { requests };
  },
  tunnelResult: (c) => {
    const b = c.body as { leaseId: string; reqId: string; status: number; bodyB64: string | null; chunked: boolean; error: string | null };
    checkLease(b.leaseId);
    if (!handed.has(b.reqId)) throw new MockError('not-found', 'unknown reqId');
    if (b.bodyB64 && b.bodyB64.length > 4 * 1024 * 1024 * 1.37) throw new MockError('too-large', 'inline body > 4 MiB; use chunks');
    lease!.reqs++;
    lease!.bytes += b.bodyB64 ? Math.floor((b.bodyB64.length * 3) / 4) : 0;
    lease!.lastError = b.status === 0 ? b.error : lease!.lastError;
    if (!b.chunked) handed.delete(b.reqId);
    return { accepted: true };
  },
  tunnelChunk: (c) => {
    checkLease(c.query.get('leaseId') ?? '');
    if (!handed.has(c.params.reqId)) throw new MockError('not-found', 'unknown reqId');
    lease!.bytes += c.raw?.length ?? 0;
    return { received: Number(c.query.get('index') ?? 0) + 1 };
  },
  tunnelComplete: (c) => {
    const b = c.body as { leaseId: string; parts: number; sha256: string };
    checkLease(b.leaseId);
    if (!handed.delete(c.params.reqId)) throw new MockError('not-found', 'unknown reqId');
    if (!/^sha256:[0-9a-f]{64}$/.test(b.sha256)) throw new MockError('invalid', 'sha256 must be sha256:<64 hex>');
    return { accepted: true };
  },
};

// §21 #123 shared files: one listing for the deployment; here / job / sources per site (lib/mock/files.ts)
Object.assign(
  H,
  createFilesMock({
    fail: (code, message, details = {}) => {
      throw new MockError(code, message, details);
    },
    needSite,
    needReach,
    reachable,
    sites: ALL_SITES,
    addActivity,
    fileResponse: (body, name) => fileResponse(body, name),
  }),
);

// §21 #125 port tunnels of a site (lib/mock/tunnels.ts)
Object.assign(
  H,
  createTunnelsMock({
    fail: (code, message, details = {}) => {
      throw new MockError(code, message, details);
    },
    needSite,
    needReach,
    addActivity,
    isRemote: (s) => !!(s as MockSession | null)?.remote,
    mode: () => ls('opm.mock.tunnels'),
  }),
);

// §21 #104 branches: this site's branch, the cloud branch, uncommitted (edits + drift); the old local-proposal /
// approve endpoints are gone (410) — merges replace them
const branchMock = createBranchMock({
  head: () => head(),
  revisions: () => revisions,
  adopt: (rev) => {
    revisions.push(rev);
    headRevId = rev.revId;
  },
  startRun: (rev, actor) => startRun('update', rev, actor, null, null).run,
  getRun: (id) => {
    const sim = runs.get(id);
    if (!sim) return null;
    computeRun(sim);
    return sim.run;
  },
  runningRun: () => runningRun()?.run ?? null,
  addActivity: (site, type, summary, actor, data) => addActivity(site, type, summary, actor, (data ?? {}) as Json),
  maskConfig,
  buildConfig,
  validate: (c, before, localEdit) => [...(localEdit ? checkLocalEdit(before, c) : []), ...validateConfig(c, validationCtx()).list],
  fail: (code, message, details = {}) => {
    throw new MockError(code, message, details as Json);
  },
  reachable,
});
Object.assign(H, branchMock.handlers);
for (const op of ['previewLocalProposal', 'createLocalProposal', 'restage', 'approveStart', 'approve'] as const)
  H[op] = () => {
    throw new MockError('gone', 'Proposals are gone (§21 #104): use the Branch page — commit, then merge from the cloud.', { hint: 'Branch page' });
  };
{
  const rb = H.rollback!;
  H.rollback = async (c, s, actor) => {
    const msg = String((c.body as { message?: unknown } | null)?.message ?? '').trim();
    if (!msg) throw new MockError('invalid', 'a commit message is required', { errors: [{ path: 'message', code: 'V_COMMIT_MESSAGE', message: 'a commit message is required', severity: 'error' }] });
    const out = (await rb(c, s, actor)) as { run: UpdateRun };
    branchMock.onRollback(out.run, msg, c.params.snapshotId, actor!);
    return out;
  };
}

// ───────────────────────────────────────────── §21 #107 commit staging (one Commit = one transaction incl. the bytes)
// Knob opm.mock.stage.fail=commit → the next GUI-edit commit with a stage fails AFTER the staged blobs were verified
// (500 internal): the stage is dropped, nothing is committed or promoted (the browser keeps every change).
interface MockStage {
  stageId: string;
  session: string | null;
  blobs: BlobNeed[];
  staged: Record<string, string>;
}
const stages = new Map<string, MockStage>();
const realSha = (b: Uint8Array) => 'sha256:' + toHex(sha256Bytes(b));
H.createLocalStage = (c, s) => {
  const body = (c.body ?? {}) as CreateStageRequest;
  const blobs = Array.isArray(body.blobs) ? body.blobs : [];
  for (const [i, b] of blobs.entries()) if (!/^sha256:[0-9a-f]{64}$/.test(b?.sha256 ?? '')) throw new MockError('invalid', 'bad blob list', { errors: [{ path: `blobs[${i}].sha256`, code: 'V_FORMAT', message: 'sha256 must be "sha256:" + 64 hex', severity: 'error' }] });
  const st: MockStage = { stageId: D.mockUlid(), session: s?.sessionId ?? null, blobs, staged: {} };
  stages.set(st.stageId, st);
  const missing = blobs.filter((b) => D.fileBlobs[b.sha256] === undefined);
  return { stageId: st.stageId, expiresAt: D.iso(nowMs() + 24 * 3600_000), missing, present: blobs.length - missing.length };
};
H.putLocalStageBlob = (c) => {
  const st = stages.get(c.params.stageId);
  if (!st) throw new MockError('not-found', 'stage not found');
  const bytes = c.raw ?? new Uint8Array();
  const sha = realSha(bytes);
  if (sha !== c.params.sha256) throw new MockError('invalid', `sha256 mismatch: body is ${sha}`, { errors: [{ path: 'sha256', code: 'V_FORMAT', message: 'sha256 mismatch', severity: 'error' }] });
  if (D.fileBlobs[sha] !== undefined) return { sha256: sha, sizeBytes: bytes.length, staged: false };
  st.staged[sha] = new TextDecoder().decode(bytes);
  return { sha256: sha, sizeBytes: bytes.length, staged: true };
};
H.deleteLocalStage = (c) => {
  if (!stages.delete(c.params.stageId)) throw new MockError('not-found', 'stage not found');
  return { deleted: true };
};
{
  const commit = H.commitLocal!;
  H.commitLocal = async (c, s, actor) => {
    const b = (c.body ?? {}) as { config?: ConfigInput; stageId?: string };
    const st = b.stageId ? stages.get(b.stageId) : undefined;
    try {
      if (b.config) {
        const h = head();
        const have = new Set(configBlobs(h?.config ?? null).map((x) => x.sha256));
        const needs = configBlobs(b.config).filter((x) => !have.has(x.sha256) && D.fileBlobs[x.sha256] === undefined);
        const missing = needs.filter((x) => st?.staged[x.sha256] === undefined);
        if (missing.length) throw new MockError('invalid', `the content of ${missing.length} file(s) was not uploaded — commit again`, { errors: missing.map((m) => ({ path: 'config', code: 'V_BLOB_MISSING', message: `content ${m.sha256.slice(7, 19)} was not uploaded`, severity: 'error' })), missing });
        if (st && ls('opm.mock.stage.fail') === 'commit') {
          try {
            localStorage.removeItem('opm.mock.stage.fail');
          } catch {
            /* */
          }
          throw new MockError('internal', 'the agent failed while committing (mock knob opm.mock.stage.fail) — nothing was committed');
        }
        // promote (before the apply, like the agent) — a later refusal leaves unreferenced content-addressed blobs
        for (const x of needs) D.fileBlobs[x.sha256] = st!.staged[x.sha256]!;
      }
      return await commit(c, s, actor);
    } finally {
      if (b.stageId) stages.delete(b.stageId);
    }
  };
}

// §21 #126 every site's branch edits from any site's GUI: this site → its own handlers; another site → a small own
// head per site (starts as a copy of this site's head; a commit of browser edits becomes its head at once, no run)
{
  const remoteHeads = new Map<string, { commitId: string; seq: number; config: Config }>();
  const remoteHead = (site: string) => {
    let h = remoteHeads.get(site);
    if (!h) {
      const b = head();
      h = { commitId: D.mockUlid(), seq: b?.seq ?? 0, config: JSON.parse(JSON.stringify(b?.config ?? { variables: [], secrets: [], files: [], sites: [] })) as Config };
      remoteHeads.set(site, h);
    }
    return h;
  };
  const self = (c: Ctx) => c.params.siteSlug === D.SELF;
  const peer = (c: Ctx) => {
    if (!reachable(c.params.siteSlug!)) throw new MockError('unavailable', `site ${c.params.siteSlug} is not reachable`);
    return remoteHead(c.params.siteSlug!);
  };
  H.siteConfig = (c, s, actor) => {
    if (self(c)) return H.config!(c, s, actor);
    const h = peer(c);
    return { head: { revId: h.commitId, seq: h.seq, hash: D.fakeSha(h.commitId) }, config: maskConfig(h.config), catalog: D.catalog, knownCatalog: D.catalog };
  };
  H.siteCreateStage = (c, s, actor) => H.createLocalStage!(c, s, actor);
  H.sitePutStageBlob = (c, s, actor) => H.putLocalStageBlob!(c, s, actor);
  H.siteDeleteStage = (c, s, actor) => H.deleteLocalStage!(c, s, actor);
  H.siteGetFileContent = (c, s, actor) => {
    if (self(c)) return H.getFileContent!(c, s, actor);
    const f = peer(c).config.files.find((x) => x.fileSlug === c.params.fileSlug);
    if (!f || !f.content) throw new MockError('not-found', 'this file has no content');
    return fileResponse(D.fileBlobs[f.content.sha256] ?? '', f.fileSlug, f.mime);
  };
  H.siteRevealSecret = (c, s, actor) => {
    if (self(c)) return H.revealSecret!(c, s, actor);
    const v = peer(c).config.secrets.find((x) => x.secretSlug === c.params.secretSlug);
    if (!v?.value) throw new MockError('not-found', 'this secret has no value');
    return { value: v.value, hideAfterSec: REVEAL_SECONDS };
  };
  H.siteCommitLocal = (c, s, actor) => {
    if (self(c)) return H.commitLocal!(c, s, actor);
    const h = peer(c);
    const b = (c.body ?? {}) as { message?: string; baseCommitId?: string | null; config?: ConfigInput; stageId?: string };
    if (!b.config) throw new MockError('invalid', 'mock: only browser edits are committed on another site here');
    if ((b.baseCommitId ?? null) !== h.commitId) throw new MockError('stale', `site ${c.params.siteSlug} moved on — rebase your changes`, { reason: 'head-moved' });
    const st = b.stageId ? stages.get(b.stageId) : undefined;
    for (const x of configBlobs(b.config)) if (D.fileBlobs[x.sha256] === undefined && st?.staged[x.sha256] !== undefined) D.fileBlobs[x.sha256] = st.staged[x.sha256]!;
    if (b.stageId) stages.delete(b.stageId);
    const next = { commitId: D.mockUlid(), seq: h.seq + 1, config: buildConfig(b.config, h.config).config };
    remoteHeads.set(c.params.siteSlug!, next);
    addActivity(c.params.siteSlug!, 'branch.committed', `Committed on ${c.params.siteSlug}: ${String(b.message ?? '').split('\n')[0]}`, actor, { siteSlug: c.params.siteSlug, seq: next.seq });
    return { commit: { commitId: next.commitId, seq: next.seq }, run: null };
  };
}

// ───────────────────────────────────────────── router
const ROUTES = Object.entries(AGENT_API)
  .map(([op, def]) => ({ op: op as keyof typeof AGENT_API, def, n: (def.path.match(/\{/g) ?? []).length, re: new RegExp('^' + def.path.replace(/\./g, '\\.').replace(/\{(\w+)\}/g, '(?<$1>[^/]+)') + '$') }))
  .sort((a, b) => a.n - b.n);

function json(status: number, body: unknown, extra: Record<string, string> = {}) {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json; charset=utf-8', 'X-Request-Id': D.mockUlid(), ...extra } });
}

export const mockFetch: typeof fetch = async (input, init = {}) => {
  const url = new URL(typeof input === 'string' ? input : input instanceof URL ? input.href : input.url, location.origin);
  if (url.origin === D.MOCK_API) return cloudFetch(url, init);
  if (url.origin !== location.origin) return fetch(input, init);
  // round I: while this site's agent restarts (upgrade / restart / agent.json save) nothing answers, like a real fetch
  if (url.pathname.startsWith('/_opm/') && isSelfDown()) {
    await sleep(250);
    throw new TypeError('Failed to fetch');
  }
  // round I: agent self-management (/_opm/api/sites/{site}/agent/…, /_opm/api/agent/upgrade-all) lives in lib/mock/agentself.ts
  if (/^\/_opm\/api\/(sites\/[^/]+\/agent(\/|$)|agent\/upgrade-all|health$)/.test(url.pathname))
    return agentFetch(url, init, {
      requireSession,
      requireActor,
      json,
      fail: (code, message, details = {}) => {
        throw new MockError(code, message, details as Json);
      },
      addActivity,
      reachable,
      updateLock: () => {
        const r = runningRun();
        return r ? { kind: r.run.kind, id: r.run.updateId, since: r.run.startedAt } : null;
      },
    });
  // §21 #97: the dev tools (/_opm/api/sites/{site}/devtools/…) live in lib/mock/devtools.ts
  if (/^\/_opm\/api\/sites\/[^/]+\/devtools\//.test(url.pathname))
    return devtoolsFetch(url, init, { requireSession, requireActor, json, addActivity, headSeq: () => head()?.seq ?? null, fail: (code, message, details = {}) => { throw new MockError(code, message, details as Json); } });
  // §21 #101: persist-to-cloud volume sync (/_opm/api/sites/{site}/volume-sync…) lives in lib/mock/volsync.ts
  if (/^\/_opm\/api\/sites\/[^/]+\/volume-sync(\/|$)/.test(url.pathname)) {
    try {
      return await volsyncFetch(url, init, volSyncDeps);
    } catch (e) {
      if (e instanceof MockError) return json(ERROR_HTTP_STATUS[e.code] ?? 500, { error: { code: e.code, message: e.message, details: e.details } });
      throw e;
    }
  }
  // round T: the operator toolbox (/_opm/api/sites/{site}/tools/…) lives in lib/mock/tools.ts
  if (url.pathname.includes('/tools/'))
    return toolsFetch(url, init, { updateRunning: () => !!runningRun(), requireSession, requireActor, json, fail: (code, message, details = {}) => { throw new MockError(code, message, details as Json); } });
  const method = (init.method ?? 'GET').toUpperCase();
  const r = ROUTES.find((x) => x.def.method === method && x.re.test(url.pathname));
  // §21 #107 tests: every agent API call the GUI made (e.g. "no upload before Commit")
  try {
    const w = window as unknown as { __opmMockCalls?: string[] };
    (w.__opmMockCalls ??= []).push(`${method} ${url.pathname}`);
  } catch {
    /* */
  }
  await sleep(60 + Math.random() * 120);
  if (!r) return json(404, { error: { code: 'not-found', message: `mock: no endpoint ${method} ${url.pathname}` } });
  const params = Object.fromEntries(Object.entries(r.re.exec(url.pathname)!.groups ?? {}).map(([k, v]) => [k, decodeURIComponent(v)]));
  const headers = new Headers(init.headers);
  let raw: Uint8Array | null = null;
  let body: unknown = null;
  if (init.body !== undefined && init.body !== null) {
    if (typeof init.body === 'string') raw = new TextEncoder().encode(init.body);
    else if (init.body instanceof Uint8Array) raw = init.body;
    else if (init.body instanceof ArrayBuffer) raw = new Uint8Array(init.body);
    else if (init.body instanceof Blob) raw = new Uint8Array(await init.body.arrayBuffer());
    if (r.def.body === 'json' && raw) {
      try {
        body = JSON.parse(new TextDecoder().decode(raw));
      } catch {
        return json(422, { error: { code: 'invalid', message: 'body is not JSON' } });
      }
    }
  }
  try {
    let s: MockSession | null = null;
    let actor: string | null = null;
    if (r.def.auth === 'session' || r.def.auth === 'session+actor') s = requireSession();
    if (r.def.auth === 'session+actor') actor = requireActor(headers, s!);
    const h = H[r.op];
    if (!h) throw new MockError('internal', `mock: ${r.op} not implemented`);
    const out = await h({ params, query: url.searchParams, body, raw, headers }, s, actor);
    if (out instanceof Response) return out;
    return json((r.def as EndpointDef).ok ?? 200, out);
  } catch (e) {
    if (e instanceof MockError) {
      const extra: Record<string, string> = e.code === 'rate-limited' ? { 'Retry-After': String(e.details.retryAfterSec ?? 60) } : {};
      return json(ERROR_HTTP_STATUS[e.code] ?? 500, { error: { code: e.code, message: e.message, details: e.details } }, extra);
    }
    console.error('mock agent error', e);
    return json(500, { error: { code: 'internal', message: e instanceof Error ? e.message : String(e), details: { requestId: 'mock' } } });
  }
};

// round I: a few agent self-management entries in this site's log
seedAgentActivities(addActivity);
// §21 #101: persist-to-cloud volume states + their live trees (one volume per sync state)
seedVolumeSync(volSyncDeps);
// §21 #93: a past failed update on the client site — northwind-backend-admin started before mqtt (the user's server)
{
  const tail = ['2026-10-08T03:00:01.000Z stdout \x1b[32mINFO\x1b[39m loading config', ...D.DENO_LINES.slice(0, 3).map((l, i) => `2026-10-08T03:00:0${2 + i}.000Z stderr ${l}`)];
  const slugs = head()?.config.sites.find((s) => s.siteSlug === 'northwind')?.config.containers.map((c) => c.containerSlug) ?? [];
  const h = dnsFailureHint(tail, slugs, 'northwind-backend-admin');
  addActivity('northwind', 'update.failed', 'Update failed in apply on northwind', null, {
    step: 'apply',
    error: 'apply failed on northwind: northwind-backend-admin exited (code 1) 3 times in a row',
    failures: [{ containerSlug: 'northwind-backend-admin', error: 'northwind-backend-admin: exited with code 1 (3 restarts) before it became ready', ports: 'none', keptAs: `opm_${D.DEP}_northwind_northwind-backend-admin-failed-1791300000`, hint: h ? dnsFailureMessage('northwind-backend-admin', h) : null, exitCode: 1, oomKilled: false, restartCount: 3, logTail: tail }],
  });
}
