/**
 * Mock of the agent self-management API (round I, SPEC §21 #90, @opm/shared/agent-self AGENT_SELF_API).
 * mock/index.ts routes /_opm/api/sites/{site}/agent/… and /_opm/api/agent/upgrade-all… here, and asks isSelfDown()
 * before every request: while THIS site's agent "restarts" every /_opm/ request fails like a real fetch (TypeError).
 * Sites: app (this site, 0.10.0-10080900), db (0.10.0-10080900), northwind (0.9.0: older than GUI_UPGRADE_MIN_VERSION,
 * no self API → skipped by upgrade-all). Pinned release: PIN.
 *
 * Upgrade timeline (ms after start): download 0–3000 (progress) · verify –4200 · swap –5000 · restart (agent down for
 * restartSec) · health 1500 → done. Knobs (localStorage):
 *   opm.mock.restartSec=4                 how long the agent is down during a restart (default 4)
 *   opm.mock.agentFail=<phase>[:<site>]   the next upgrade fails: download | verify (signature) | health (rolled back)
 *   opm.mock.agentBusy=snapshot           an update / snapshot … is running (info.busy; a warning only, §21 #110)
 *   opm.mock.agentReadOnly=1              agent.json is not writable (editor read-only)
 *   opm.mock.agentRevoked=1               this site's enrollment is revoked (re-enroll help)
 *   opm.mock.restartDropsSession=1        the session is gone after the next restart (GUI → login)
 *   opm.mock.agentPinSame=1               the pin equals the installed version (no upgrade available)
 *   opm.mock.noSocket=1                   health.socketActivated = false (no systemd socket unit)
 */
import type { AgentConfigView, AgentEditableSettings, AgentSelfInfo, AgentSelfRelease, PatchAgentConfigRequest, StartAgentUpgradeRequest } from '@opm/shared/agent-self';
import { SUPPORT_BUNDLE_SECTIONS, uninstallCommands, validateAgentSettings } from '@opm/shared/agent-self';
import type { AgentHealthResponse, AgentUpgradeProgress, AgentUpgradeRun, AgentUpgradeVia, ErrorCode, ReleaseVerification } from '@opm/shared/types';
import { AGENT_UPGRADE_HEALTH_SEC, ERROR_HTTP_STATUS, agentUpgradeAvailable, compareAgentVersions, supportsGuiUpgrade } from '@opm/shared/types';

// eslint-disable-next-line @typescript-eslint/no-explicit-any
type AnySession = any;
export interface AgentMockCtx {
  requireSession: () => AnySession;
  requireActor: (h: Headers, s: AnySession) => string;
  fail: (code: ErrorCode, message: string, details?: Record<string, unknown>) => never;
  json: (status: number, body: unknown, extra?: Record<string, string>) => Response;
  addActivity: (site: string, type: string, summary: string, actor: string | null, data?: Record<string, unknown>) => void;
  reachable: (site: string) => boolean;
  /** the lock of a running update / rollback (overview) */
  updateLock: () => { kind: 'update' | 'rollback' | 'merge'; id: string; since: string } | null;
}

const SELF = 'app';
const SITES = ['app', 'db', 'northwind'];
const DEP = 'DepHelloWorld0000001';
const SESSION_KEY = 'opm.mock.session';
const knob = (k: string) => {
  try {
    return localStorage.getItem(k);
  } catch {
    return null;
  }
};
const now = () => Date.now();
const iso = (ms: number) => new Date(ms).toISOString();
const ulid = () => '01K' + Math.random().toString(36).slice(2, 12).toUpperCase().padEnd(10, '0') + Math.random().toString(36).slice(2, 12).toUpperCase().padEnd(10, 'X');
const hex = (s: string) => {
  let h = 2166136261;
  let out = '';
  for (let i = 0; i < 8; i++) {
    for (const c of s + i) h = Math.imul(h ^ c.charCodeAt(0), 16777619) >>> 0;
    out += h.toString(16).padStart(8, '0');
  }
  return out;
};

const INSTALLED = '0.10.0-10080900';
export const PIN = '0.10.1-10081500';
const SIZE = 21_734_400;
const NOTES = `0.10.1 — agent self-management in the GUI

- Upgrade the agent from the agent GUI (Agent page) and from the cloud console, with automatic rollback when the new agent is not healthy within 60 s.
- Restart the agent, edit the safe agent.json settings, download a support bundle.
- Fix: the default edge answers on port 80 again after a reboot.`;
const pinOf = (site: string) => (knob('opm.mock.agentPinSame') === '1' ? state(site).version : PIN);

const AGENT_PORT: Record<string, number> = { app: 7080, db: 7081, northwind: 7082 };
const ALIAS: Record<string, number> = { app: 8090, db: 8091, northwind: 8092 };
const EDGE: Record<string, [number | null, number | null]> = { app: [8081, 8443], db: [8082, null], northwind: [80, 443] };
const HOME: Record<string, string> = { app: '/var/lib/opm', db: '/opt/opm-db', northwind: '/srv/opm' };

interface Sim {
  p: AgentUpgradeProgress;
  t0: number;
  fail: 'download' | 'verify' | 'health' | null;
  restartMs: number;
  logged: Set<string>;
}
interface SiteAgent {
  version: string;
  startedMs: number;
  pid: number;
  prevVersion: string | null;
  sim: Sim | null;
  history: AgentUpgradeProgress[];
  settings: AgentEditableSettings;
  /** agent.json on disk (≠ settings until the restart) */
  disk: AgentEditableSettings;
  restartPending: boolean;
  /** this agent does not answer until (ms) */
  downUntil: number;
  downFrom: number;
  verification: ReleaseVerification | null;
  checkedAt: string | null;
}
const st: Record<string, SiteAgent> = {};
function state(site: string): SiteAgent {
  if (!st[site]) {
    const settings: AgentEditableSettings = { tools: { hostShell: site !== 'db', containerExec: true, devTools: knob('opm.mock.devtoolsOff') !== '1' }, containerNames: site === 'northwind' ? 'prefixed' : 'plain', edgeHttpPort: EDGE[site]?.[0] ?? null, guiAliasPort: ALIAS[site] ?? null };
    st[site] = {
      version: site === 'northwind' ? '0.9.0' : INSTALLED,
      startedMs: now() - (site === 'app' ? 3 * 86400_000 + 4 * 3600_000 : 11 * 86400_000),
      pid: site === 'app' ? 812 : 1204,
      prevVersion: site === 'northwind' ? null : '0.9.0',
      sim: null,
      history:
        site === 'northwind'
          ? []
          : [
              {
                upgradeId: ulid(),
                requestId: null,
                runId: null,
                via: 'cli',
                siteSlug: site,
                from: '0.9.0',
                to: INSTALLED,
                phase: 'done',
                progress: null,
                bytesDone: SIZE,
                bytesTotal: SIZE,
                verification: null,
                error: null,
                actor: 'root@host',
                startedAt: iso(now() - 3 * 86400_000 - 4 * 3600_000 - 40_000),
                updatedAt: iso(now() - 3 * 86400_000 - 4 * 3600_000),
                finishedAt: iso(now() - 3 * 86400_000 - 4 * 3600_000),
              },
            ],
      settings,
      disk: structuredClone(settings),
      restartPending: false,
      downUntil: 0,
      downFrom: 0,
      verification: null,
      checkedAt: null,
    };
  }
  return st[site];
}

let pendingSessionDrop = false;
let ctxRef: AgentMockCtx | null = null;

function restartMs() {
  const n = Number(knob('opm.mock.restartSec') ?? 4);
  return (Number.isFinite(n) && n >= 0 ? n : 4) * 1000;
}
function restartNow(site: string, delayMs: number, downMs: number) {
  const a = state(site);
  a.downFrom = now() + delayMs;
  a.downUntil = a.downFrom + downMs;
  if (site === SELF && knob('opm.mock.restartDropsSession') === '1') pendingSessionDrop = true;
}
/** the agent of `site` does not answer right now (restart window, upgrade restart / rollback) */
function isDown(site: string): boolean {
  const a = state(site);
  advance(site);
  const t = now();
  if (t >= a.downFrom && t < a.downUntil) return true;
  if (t >= a.downUntil && a.downUntil && a.startedMs < a.downUntil) {
    // came back: a new process
    a.startedMs = a.downUntil;
    a.pid += 17;
    a.restartPending = false;
    a.settings = structuredClone(a.disk);
  }
  return false;
}
/** §21 #110 terminate with restartAgent: every agent restarts (mock/index.ts) */
export function restartAllAgents(sites: string[], addActivity: AgentMockCtx['addActivity'], actor: string | null, reason: string) {
  for (const s of sites) {
    if (!SITES.includes(s)) continue;
    addActivity(s, 'agent.restarted', `Agent restarted (systemd) — ${reason}`, actor, { reason, via: 'systemd' });
    restartNow(s, 700, restartMs());
  }
}
/** mock/index.ts: every /_opm/ request fails while THIS site's agent restarts */
export function isSelfDown(): boolean {
  const d = isDown(SELF);
  if (!d && pendingSessionDrop) {
    pendingSessionDrop = false;
    try {
      localStorage.removeItem(SESSION_KEY);
    } catch {
      /* storage blocked */
    }
  }
  return d;
}
/** DB schema of the installed build (mock: 3 from 0.10.1, 2 for 0.10.0, legacy 1 before) */
export function agentSchemaOf(site: string): number {
  const v = agentVersionOf(site);
  return compareAgentVersions(v, PIN) >= 0 ? 3 : supportsGuiUpgrade(v) ? 2 : 1;
}
/** §21 #91 GET /_opm/api/health of THIS agent (public): not ok while a new agent is still in its health phase */
function healthOf(): { status: number; body: AgentHealthResponse } {
  const a = state(SELF);
  advance(SELF);
  const starting = !!a.sim && a.sim.p.phase === 'health';
  const checks = [
    { name: 'store', ok: !(starting && a.sim?.fail === 'health'), message: starting && a.sim?.fail === 'health' ? 'open opm.db: database is locked' : null },
    { name: 'listeners', ok: true, message: null },
    { name: 'startup', ok: !starting, message: starting ? 'finishing startup (upgrade health phase)' : null },
    { name: 'docker', ok: true, message: null },
    { name: 'cloud', ok: true, message: null },
  ];
  const ok = checks.every((c) => c.ok);
  return {
    status: ok ? 200 : 503,
    body: { ok, version: a.version, schema: agentSchemaOf(SELF), pid: a.pid, startedAt: iso(a.startedMs), cloud: { connected: true, route: 'direct', lastContactAt: iso(now() - 9_000) }, docker: { ok: true }, socketActivated: knob('opm.mock.noSocket') !== '1', checks },
  };
}
/** installed agent version of a site (overview tiles, status, info) */
export function agentVersionOf(site: string): string {
  if (!SITES.includes(site)) return INSTALLED;
  advance(site);
  return state(site).version;
}

// ───────────────────────────────────────────── upgrade simulation

const T = { download: 3000, verify: 1200, swap: 800, health: 1500, healthFail: 3500, rollbackDown: 1500 };
const TERMINAL = new Set(['done', 'failed', 'rolled-back']);

function failKnob(site: string): Sim['fail'] {
  const k = knob('opm.mock.agentFail');
  if (!k) return null;
  const [phase, s] = k.split(':');
  if (s && s !== site) return null;
  return phase === 'download' || phase === 'verify' || phase === 'health' ? phase : null;
}
function verificationFor(site: string, ok: boolean, downloaded: boolean, selfTest: boolean | null): ReleaseVerification {
  return {
    version: pinOf(site),
    sha256: hex(`opm-agent-${PIN}-linux-amd64`),
    signatureOk: ok,
    publicKeySource: 'agent.json',
    publicKeyFingerprint: 'SHA256:3q8xJm0F2Yh5vQ7cZt1nRk9LwP4bGd6sEaUo8iVyXfM',
    sha256Ok: downloaded ? ok : null,
    selfTestOk: selfTest,
    error: ok ? null : 'the release signature does not verify with the agent.json releasePublicKey (SHA256:3q8xJm0F…) — refusing the binary',
    checkedAt: iso(now()),
  };
}
function startSim(site: string, via: AgentUpgradeVia, actor: string, runId: string | null): AgentUpgradeProgress {
  const a = state(site);
  const fail = failKnob(site);
  if (fail) {
    try {
      localStorage.removeItem('opm.mock.agentFail'); // once
    } catch {
      /* storage blocked */
    }
  }
  const p: AgentUpgradeProgress = { upgradeId: ulid(), requestId: null, runId, via, siteSlug: site, from: a.version, to: pinOf(site), phase: 'queued', progress: null, bytesDone: 0, bytesTotal: SIZE, verification: null, error: null, failedPhase: null, actor, startedAt: iso(now()), updatedAt: iso(now()), finishedAt: null };
  a.sim = { p, t0: now(), fail, restartMs: restartMs(), logged: new Set() };
  ctxRef?.addActivity(site, 'agent.upgrade-started', `Agent upgrade ${p.from} → ${p.to} started (${via})`, actor, { upgradeId: p.upgradeId, from: p.from, to: p.to, via, ...(runId ? { runId } : {}) });
  advance(site);
  return structuredClone(a.sim.p);
}
function once(sim: Sim, key: string, fn: () => void) {
  if (sim.logged.has(key)) return;
  sim.logged.add(key);
  fn();
}
function finish(site: string, sim: Sim, phase: 'done' | 'failed' | 'rolled-back', at: number, error: string | null) {
  const a = state(site);
  const p = sim.p;
  p.phase = phase;
  p.progress = null;
  p.error = error;
  if (phase !== 'done') p.failedPhase = phase === 'rolled-back' ? 'health' : sim.fail === 'verify' ? 'verify' : 'download';
  p.updatedAt = iso(at);
  p.finishedAt = iso(at);
  once(sim, 'final', () => {
    if (phase === 'done') {
      a.prevVersion = p.from;
      a.version = p.to;
      ctxRef?.addActivity(site, 'agent.upgraded', `Agent upgraded ${p.from} → ${p.to}`, p.actor, { upgradeId: p.upgradeId, from: p.from, to: p.to, durationSec: Math.round((at - sim.t0) / 1000), via: p.via });
    } else {
      const failedPhase = phase === 'rolled-back' ? 'health' : sim.fail === 'verify' ? 'verify' : 'download';
      ctxRef?.addActivity(site, 'agent.upgrade-failed', `Agent upgrade ${p.from} → ${p.to} failed in ${failedPhase}`, p.actor, { upgradeId: p.upgradeId, from: p.from, to: p.to, phase: failedPhase, error, via: p.via });
      if (phase === 'rolled-back') ctxRef?.addActivity(site, 'agent.rolled-back', `Agent rolled back to ${p.from} (the new agent was not healthy)`, p.actor, { upgradeId: p.upgradeId, from: p.to, to: p.from, reason: error, by: 'watcher' });
    }
    a.history = [structuredClone(p), ...a.history].slice(0, 10);
  });
}
/** move the simulated upgrade of a site forward to "now" */
function advance(site: string) {
  const a = st[site];
  const sim = a?.sim;
  if (!sim || TERMINAL.has(sim.p.phase)) return;
  const t = now() - sim.t0;
  const p = sim.p;
  const at = (ms: number) => iso(sim.t0 + ms);
  const tDl = T.download;
  const tVe = tDl + T.verify;
  const tSw = tVe + T.swap;
  const tRe = tSw + sim.restartMs;
  if (sim.fail === 'download' && t >= tDl / 2) {
    p.bytesDone = Math.round(SIZE * 0.47);
    return finish(site, sim, 'failed', sim.t0 + tDl / 2, 'download interrupted: connection reset by the relay (the download resumes when you try again)');
  }
  if (t < tDl) {
    p.phase = 'download';
    p.progress = Math.min(1, t / tDl);
    p.bytesDone = Math.round(SIZE * p.progress);
    p.updatedAt = iso(now());
    return;
  }
  p.bytesDone = SIZE;
  p.progress = null;
  if (t < tVe) {
    p.phase = 'verify';
    p.verification = verificationFor(site, true, true, null);
    p.updatedAt = at(tDl);
    return;
  }
  if (sim.fail === 'verify') {
    p.verification = verificationFor(site, false, true, null);
    a.verification = p.verification;
    return finish(site, sim, 'failed', sim.t0 + tVe, p.verification.error);
  }
  p.verification = verificationFor(site, true, true, true);
  a.verification = p.verification;
  if (t < tSw) {
    p.phase = 'swap';
    p.updatedAt = at(tVe);
    return;
  }
  if (t < tRe) {
    p.phase = 'restart';
    p.updatedAt = at(tSw);
    once(sim, 'restart', () => {
      a.downFrom = sim.t0 + tSw;
      a.downUntil = sim.t0 + tRe;
      if (site === SELF && knob('opm.mock.restartDropsSession') === '1') pendingSessionDrop = true;
    });
    return;
  }
  // the new agent is up
  if (sim.fail === 'health') {
    const tFail = tRe + T.healthFail;
    if (t < tFail) {
      p.phase = 'health';
      p.updatedAt = at(tRe);
      return;
    }
    // the watcher stops the new agent and starts opm-agent.prev: down again
    once(sim, 'rollback-down', () => {
      a.downFrom = sim.t0 + tFail;
      a.downUntil = sim.t0 + tFail + T.rollbackDown;
    });
    if (t < tFail + T.rollbackDown) {
      p.phase = 'health';
      return;
    }
    return finish(site, sim, 'rolled-back', sim.t0 + tFail + T.rollbackDown, `the new agent ${p.to} did not report healthy within ${AGENT_UPGRADE_HEALTH_SEC} s (store open failed: database is locked) — the watcher restored opm-agent.prev (${p.from})`);
  }
  if (t < tRe + T.health) {
    p.phase = 'health';
    p.updatedAt = at(tRe);
    return;
  }
  finish(site, sim, 'done', sim.t0 + tRe + T.health, null);
}

// ───────────────────────────────────────────── upgrade-all (mesh run, coordinator = this site, LAST)

let run: AgentUpgradeRun | null = null;
let runCancel = false;
function advanceRun() {
  if (!run || run.status !== 'running') return;
  for (;;) {
    const cur = run.sites.find((s) => s.status === 'running');
    if (cur) {
      advance(cur.siteSlug);
      const p = state(cur.siteSlug).sim?.p;
      if (!p || !TERMINAL.has(p.phase)) return;
      cur.at = p.finishedAt;
      if (p.phase === 'done') cur.status = 'done';
      else {
        cur.status = 'failed';
        cur.error = p.error;
        for (const s of run.sites) if (s.status === 'pending') ((s.status = 'cancelled'), (s.error = `not started: ${cur.siteSlug} failed`));
        run.status = 'failed';
        run.finishedAt = p.finishedAt;
        return;
      }
      continue;
    }
    const next = run.sites.find((s) => s.status === 'pending');
    if (runCancel) {
      for (const s of run.sites) if (s.status === 'pending') ((s.status = 'cancelled'), (s.error = 'cancelled by the operator'));
      run.status = 'cancelled';
      run.finishedAt = iso(now());
      return;
    }
    if (!next) {
      run.status = run.sites.some((s) => s.status === 'failed') ? 'failed' : 'done';
      run.finishedAt = iso(now());
      return;
    }
    next.status = 'running';
    next.at = iso(now());
    startSim(next.siteSlug, 'mesh', run.requestedBy, run.runId);
    return;
  }
}

// ───────────────────────────────────────────── views

function release(site: string): AgentSelfRelease {
  const a = state(site);
  const pin = pinOf(site);
  return {
    version: pin,
    notes: NOTES,
    sha256: a.checkedAt ? hex(`opm-agent-${PIN}-linux-amd64`) : null,
    source: a.checkedAt ? 'release-check' : 'cloud-view',
    checkedAt: a.checkedAt,
    upgradeAvailable: agentUpgradeAvailable(a.version, pin),
    newer: compareAgentVersions(pin, a.version) >= 0,
    verification: a.verification,
  };
}
function busyOf(site: string): AgentSelfInfo['busy'] {
  const a = state(site);
  if (a.sim && !TERMINAL.has(a.sim.p.phase)) return { kind: 'upgrade', id: a.sim.p.upgradeId, since: a.sim.p.startedAt };
  const k = knob('opm.mock.agentBusy');
  if (k === 'update' || k === 'rollback' || k === 'snapshot' || k === 'restore') return { kind: k, id: ulid(), since: iso(now() - 95_000) };
  const l = ctxRef?.updateLock();
  return l ? { kind: l.kind === 'merge' ? 'update' : l.kind, id: l.id, since: l.since } : null;
}
function infoOf(site: string): AgentSelfInfo {
  const a = state(site);
  advance(site);
  const revoked = site === SELF && knob('opm.mock.agentRevoked') === '1';
  const [http, https] = EDGE[site] ?? [null, null];
  const binaryPath = '/usr/local/bin/opm-agent';
  const configPath = '/etc/opm/agent.json';
  const guiPorts = [
    ...(http ? [{ port: http, scheme: 'http' as const, kind: 'edge' as const }] : []),
    ...(https ? [{ port: https, scheme: 'https' as const, kind: 'edge' as const }] : []),
    ...(a.settings.guiAliasPort ? [{ port: a.settings.guiAliasPort, scheme: 'http' as const, kind: 'gui-alias' as const }] : []),
    { port: AGENT_PORT[site], scheme: 'http' as const, kind: 'agent-port' as const },
  ];
  return {
    siteSlug: site,
    self: site === SELF,
    version: a.version,
    build: { version: a.version, goVersion: 'go1.23.4', os: 'linux', arch: site === 'db' ? 'arm64' : 'amd64', commit: site === 'northwind' ? null : 'f39184b5c0e1', builtAt: site === 'northwind' ? null : '2026-10-08T09:00:00Z' },
    pid: a.pid,
    startedAt: iso(a.startedMs),
    uptimeSec: Math.max(0, Math.round((now() - a.startedMs) / 1000)),
    configPath,
    dataDir: HOME[site],
    binaryPath,
    previousBinary: { path: `${binaryPath}.prev`, exists: !!a.prevVersion, version: a.prevVersion, sha256: a.prevVersion ? hex(`prev-${a.prevVersion}`) : null },
    ports: { agentPort: AGENT_PORT[site], guiAliasPort: a.settings.guiAliasPort, defaultEdgeHttpPort: a.settings.edgeHttpPort, edge: { running: true, defaultEdge: false, httpPort: http, httpsPort: https, lastError: null }, guiPorts },
    docker: site === 'db' ? { ok: true, host: 'unix:///var/run/docker.sock', version: '27.3.1', apiVersion: '1.47', error: null } : { ok: true, host: 'unix:///var/run/docker.sock', version: '28.5.2', apiVersion: '1.47', error: null },
    supervisor: { kind: 'systemd', unit: 'opm-agent.service', activeState: 'active', subState: 'running', restartGuard: { installed: site !== 'db', dropInPath: site !== 'db' ? '/etc/systemd/system/opm-agent.service.d/upgrade-guard.conf' : null, scriptPath: site !== 'db' ? '/usr/local/lib/opm/upgrade-guard.sh' : null } },
    enrollment: {
      state: revoked ? 'revoked' : 'enrolled',
      deploymentId: DEP,
      siteSlug: site,
      apiBase: 'https://opm.example.com',
      enrolledAt: iso(now() - 21 * 86400_000),
      siteKeyFingerprint: `SHA256:${btoa(hex(site)).slice(0, 43)}`,
      publicKey: btoa(hex(`pk-${site}`)).slice(0, 44),
      keyVersion: 1,
      reenrollHelp: revoked
        ? [
            '# 1. In the cloud console: Setup → this site → Revoke (done) → "New install command"',
            `sudo systemctl stop opm-agent`,
            `sudo mv ${HOME[site]} ${HOME[site]}.revoked-${new Date().toISOString().slice(0, 10)}`,
            '# 2. run the new install command from the Setup page (curl … | sudo sh)',
          ]
        : null,
    },
    cloud: { route: site === SELF ? 'direct' : 'relay', internet: site === SELF, lastFetchAt: iso(now() - 9_000), lastHeartbeatAt: iso(now() - 14_000), lastError: site === 'db' ? 'dial tcp: lookup opm.example.com: no such host (using the relay via app)' : null, consoleUrl: `https://opm.example.com/app/deployments/${DEP}`, deploymentLabel: 'Hello World' },
    release: release(site),
    upgrade: a.sim ? structuredClone(a.sim.p) : (a.history[0] ?? null),
    busy: busyOf(site),
    uninstall: uninstallCommands({ configPath, dataDir: HOME[site], binaryPath, systemd: true }),
  };
}
const MASK = (v: string) => `••••${v.slice(-4)}`;
function configOf(site: string): AgentConfigView {
  const a = state(site);
  const readOnly = knob('opm.mock.agentReadOnly') === '1';
  const d = a.disk;
  return {
    path: '/etc/opm/agent.json',
    masked: {
      apiBase: 'https://opm.example.com',
      deploymentId: DEP,
      siteSlug: site,
      agentPort: AGENT_PORT[site],
      guiAliasPort: d.guiAliasPort,
      edgeHttpPort: d.edgeHttpPort,
      dataDir: HOME[site],
      dockerHost: null,
      containerNames: d.containerNames,
      tools: { hostShell: d.tools.hostShell, containerExec: d.tools.containerExec, devTools: d.tools.devTools },
      releasePublicKey: 'MCowBQYDK2VwAyEAq2bN0rQ8m4jW1yX5zT7vH3sK9pL6dE0fG2hJ4kM8nOQ=',
      installToken: MASK('opi_7hQ2mX9pL4dW6zT3f9a'),
      proxy: site === 'db' ? { url: 'http://proxy.lan:3128', password: MASK('hunter2-proxy-pw') } : null,
    },
    settings: structuredClone(d),
    writable: !readOnly,
    writableReason: readOnly ? '/etc/opm/agent.json is owned by root (mode 0644) and the agent runs as user opm — edit the file on the server and restart the agent' : null,
    restartPending: a.restartPending,
  };
}

// ───────────────────────────────────────────── routing

function parseBody(init: RequestInit): Record<string, unknown> {
  const b = init.body;
  try {
    if (typeof b === 'string') return JSON.parse(b);
    if (b instanceof Uint8Array) return JSON.parse(new TextDecoder().decode(b));
  } catch {
    /* none */
  }
  return {};
}
const tickets = new Map<string, string>();

export function seedAgentActivities(add: AgentMockCtx['addActivity']) {
  add('app', 'agent.upgraded', `Agent upgraded 0.9.0 → ${INSTALLED}`, 'root@host', { upgradeId: ulid(), from: '0.9.0', to: INSTALLED, durationSec: 38, via: 'cli' });
  add('app', 'agent.config-changed', 'agent.json changed: tools.hostShell', 'ravi@acme.example', { changed: ['tools.hostShell'], before: { tools: { hostShell: false } }, after: { tools: { hostShell: true } } });
  add('app', 'agent.restarted', 'Agent restarted (systemd)', 'ravi@acme.example', { reason: 'apply agent.json', via: 'systemd' });
}

export async function agentFetch(url: URL, init: RequestInit, ctx: AgentMockCtx): Promise<Response> {
  ctxRef = ctx;
  const method = (init.method ?? 'GET').toUpperCase();
  const headers = new Headers(init.headers);
  const body = parseBody(init);
  await new Promise((r) => setTimeout(r, 60 + Math.random() * 90));
  const ok = (v: unknown, s = 200) => ctx.json(s, v);
  try {
    // ticket download (support bundle): no session needed, single use
    const dl = /^\/_opm\/api\/sites\/([^/]+)\/agent\/support-bundle\/download$/.exec(url.pathname);
    if (dl && method === 'GET') {
      const site = tickets.get(url.searchParams.get('ticket') ?? '');
      if (!site) ctx.fail('gone', 'this link was used or expired; create a new one');
      tickets.delete(url.searchParams.get('ticket') ?? '');
      const name = `opm-support-${site}-${new Date().toISOString().replace(/[-:]/g, '').slice(0, 15).replace('T', '-')}.tar.gz`;
      ctx.addActivity(site!, 'agent.support-bundle-downloaded', 'Support bundle downloaded', null, { sizeBytes: 482_113, sections: [...SUPPORT_BUNDLE_SECTIONS] });
      return new Response(new Blob([`mock support bundle of ${site}\n${SUPPORT_BUNDLE_SECTIONS.join('\n')}\n`], { type: 'application/gzip' }), { status: 200, headers: { 'Content-Type': 'application/gzip', 'Content-Disposition': `attachment; filename="${name}"` } });
    }
    if (url.pathname === '/_opm/api/health' && method === 'GET') {
      const h = healthOf();
      return ctx.json(h.status, h.body);
    }
    const s = ctx.requireSession();
    const actor = () => ctx.requireActor(headers, s);

    // mesh upgrade-all
    if (url.pathname === '/_opm/api/agent/upgrade-all') {
      advanceRun();
      if (method === 'GET') return ok({ run: run ? structuredClone(run) : null });
      const a = actor();
      const b = body as { version?: string; confirm?: string };
      if (b.confirm !== 'UPGRADE') ctx.fail('invalid', 'Type UPGRADE exactly.', { errors: [{ path: 'confirm', code: 'V_CONFIRM', message: 'type UPGRADE', severity: 'error' }] });
      if (b.version !== PIN) ctx.fail('stale', `the pinned release moved to ${PIN}; reload and try again`, { version: PIN });
      if (run?.status === 'running') ctx.fail('conflict', `an upgrade-all run is running (${run.runId})`);
      const busy = busyOf(SELF);
      if (SITES.some((x) => state(x).sim && !TERMINAL.has(state(x).sim!.p.phase))) ctx.fail('conflict', 'an upgrade is running on a site');
      runCancel = false;
      const order = [...SITES.filter((x) => x !== SELF).sort(), SELF];
      run = {
        runId: ulid(),
        origin: 'agent',
        coordinator: SELF,
        version: PIN,
        requestedBy: a,
        requestedAt: iso(now()),
        status: 'running',
        sites: order.map((x) => {
          const v = state(x).version;
          const skip = !ctx.reachable(x) ? 'unreachable' : !supportsGuiUpgrade(v) ? `agent ${v} is older than 0.10.0 — rerun the install command of this site once` : !agentUpgradeAvailable(v, PIN) ? `already on ${v}` : null;
          return { siteSlug: x, status: skip ? 'skipped' : 'pending', from: v, requestId: null, error: skip, at: null };
        }),
        finishedAt: null,
      };
      advanceRun();
      return ok({ run: structuredClone(run) }, 202);
    }
    if (url.pathname === '/_opm/api/agent/upgrade-all/cancel' && method === 'POST') {
      actor();
      advanceRun();
      if (!run || run.status !== 'running') ctx.fail('conflict', 'no upgrade-all run is running');
      runCancel = true;
      advanceRun();
      return ok({ run: structuredClone(run) });
    }

    const m = /^\/_opm\/api\/sites\/([^/]+)\/agent(?:\/(.*))?$/.exec(url.pathname);
    if (!m) return ctx.json(404, { error: { code: 'not-found', message: `mock: no agent endpoint ${url.pathname}` } });
    const site = decodeURIComponent(m[1]);
    const rest = m[2] ?? '';
    advanceRun();
    if (site === '*' && method === 'GET' && rest === '') {
      return ok({ items: SITES.filter((x) => ctx.reachable(x) && supportsGuiUpgrade(state(x).version) && !isDown(x)).map(infoOf) });
    }
    if (!SITES.includes(site)) ctx.fail('not-found', `site ${site} is not part of this deployment`);
    if (!ctx.reachable(site)) ctx.fail('unavailable', `site ${site} does not answer`);
    if (site !== SELF && isDown(site)) ctx.fail('unavailable', `site ${site} does not answer (its agent is restarting)`);
    if (!supportsGuiUpgrade(state(site).version)) ctx.fail('not-found', `site ${site} runs agent ${state(site).version}: no self-management API (older than 0.10.0). Rerun the install command of this site once — it stays valid for upgrades.`);
    const a = state(site);
    const lockedIf = () => {
      const b = busyOf(site);
      if (b?.kind === 'upgrade') ctx.fail('conflict', `an agent upgrade is running on ${site} (since ${b.since.slice(11, 19)})`, { busy: b });
      // §21 #110 an update / snapshot never blocks restart, upgrade or agent.json saves
    };

    if (method === 'GET' && rest === '') return ok(infoOf(site));
    if (method === 'POST' && rest === 'release/check') {
      const bad = knob('opm.mock.agentFail') === 'verify';
      a.checkedAt = iso(now());
      a.verification = verificationFor(site, !bad, false, null);
      await new Promise((r) => setTimeout(r, 400));
      return ok(release(site));
    }
    if (rest === 'upgrade' && method === 'GET') return ok({ current: a.sim ? structuredClone(a.sim.p) : null, history: structuredClone(a.history) });
    if (rest === 'upgrade' && method === 'POST') {
      const who = actor();
      const b = body as Partial<StartAgentUpgradeRequest>;
      if (b.confirm !== site) ctx.fail('invalid', `Type the site slug ${site} exactly.`, { errors: [{ path: 'confirm', code: 'V_CONFIRM', message: `type ${site}`, severity: 'error' }] });
      if (a.sim && !TERMINAL.has(a.sim.p.phase)) ctx.fail('conflict', `an agent upgrade is running on ${site}`);
      lockedIf();
      if (b.version !== pinOf(site)) ctx.fail('stale', `the pinned release moved to ${pinOf(site)}; reload and try again`, { version: pinOf(site) });
      if (!b.force && compareAgentVersions(a.version, b.version) >= 0) ctx.fail('invalid', `already on ${a.version} (reinstall = force)`);
      return ok(startSim(site, 'gui', who, null), 202);
    }
    if (method === 'POST' && rest === 'restart') {
      const who = actor();
      const b = body as { confirm?: string; reason?: string };
      if (b.confirm !== site) ctx.fail('invalid', `Type the site slug ${site} exactly.`, { errors: [{ path: 'confirm', code: 'V_CONFIRM', message: `type ${site}`, severity: 'error' }] });
      if ((b.reason ?? '').length > 200) ctx.fail('invalid', 'reason: at most 200 characters');
      lockedIf();
      ctx.addActivity(site, 'agent.restarted', `Agent restarted (systemd)${b.reason ? ` — ${b.reason}` : ''}`, who, { reason: b.reason ?? '', via: 'systemd' });
      restartNow(site, 700, restartMs());
      return ok({ restarting: true, via: 'systemd' }, 202);
    }
    if (method === 'GET' && rest === 'config') return ok(configOf(site));
    if (method === 'PATCH' && rest === 'config') {
      const who = actor();
      const b = body as Partial<PatchAgentConfigRequest>;
      if (knob('opm.mock.agentReadOnly') === '1') ctx.fail('forbidden', configOf(site).writableReason ?? 'agent.json is not writable');
      if (b.confirm !== site) ctx.fail('invalid', `Type the site slug ${site} exactly.`, { errors: [{ path: 'confirm', code: 'V_CONFIRM', message: `type ${site}`, severity: 'error' }] });
      lockedIf();
      const p = b.settings ?? {};
      const next: AgentEditableSettings = { tools: { ...a.disk.tools, ...(p.tools ?? {}) }, containerNames: p.containerNames ?? a.disk.containerNames, edgeHttpPort: p.edgeHttpPort !== undefined ? p.edgeHttpPort : a.disk.edgeHttpPort, guiAliasPort: p.guiAliasPort !== undefined ? p.guiAliasPort : a.disk.guiAliasPort };
      const issues = validateAgentSettings(next, { agentPort: AGENT_PORT[site], previous: a.disk });
      const errs = issues.filter((i) => i.severity === 'error');
      if (errs.length) ctx.fail('invalid', errs.map((i) => `${i.path}: ${i.message}`).join('; '), { errors: issues });
      const flat = (x: AgentEditableSettings): Record<string, unknown> => ({ 'tools.hostShell': x.tools.hostShell, 'tools.containerExec': x.tools.containerExec, 'tools.devTools': x.tools.devTools ?? true, containerNames: x.containerNames, edgeHttpPort: x.edgeHttpPort, guiAliasPort: x.guiAliasPort });
      const fb = flat(a.disk);
      const fa = flat(next);
      const changed = Object.keys(fa).filter((k) => fa[k] !== fb[k]);
      const restart = b.restart !== false;
      if (changed.length) {
        a.disk = next;
        a.restartPending = true;
        ctx.addActivity(site, 'agent.config-changed', `agent.json changed: ${changed.join(', ')}`, who, { changed, before: Object.fromEntries(changed.map((k) => [k, fb[k]])), after: Object.fromEntries(changed.map((k) => [k, fa[k]])) });
      }
      if (restart && changed.length) {
        ctx.addActivity(site, 'agent.restarted', 'Agent restarted (systemd) — apply agent.json', who, { reason: 'apply agent.json', via: 'systemd' });
        restartNow(site, 700, restartMs());
      }
      return ok({ changed, restarting: restart && changed.length > 0, config: configOf(site) });
    }
    if (method === 'POST' && rest === 'support-bundle') {
      actor();
      const t = `sb_${ulid()}`;
      tickets.set(t, site);
      return ok({ url: `/_opm/api/sites/${site}/agent/support-bundle/download?ticket=${t}`, expiresAt: iso(now() + 300_000) });
    }
    return ctx.json(404, { error: { code: 'not-found', message: `mock: no agent endpoint ${method} ${url.pathname}` } });
  } catch (e) {
    const err = e as { code?: string; message?: string; details?: Record<string, unknown> };
    if (err && typeof err.code === 'string') return ctx.json(ERROR_HTTP_STATUS[err.code as ErrorCode] ?? 500, { error: { code: err.code, message: err.message, details: err.details ?? {} } });
    throw e;
  }
}
