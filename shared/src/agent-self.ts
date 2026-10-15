/**
 * Agent self-management (SPEC-final §7.5, §18 Agent page, §21 #90): everything `opm-agent` can do on the command line is
 * also in the agent GUI — version / status, verify-release, upgrade (+ rollback to opm-agent.prev), restart, a masked
 * view + editor of agent.json safe settings, a support bundle, uninstall instructions. keygen / enroll / install stay
 * CLI-only (pre-enrollment); the page shows the site key fingerprint, the enrollment date and re-enroll help instead.
 *
 * Per-site paths: /_opm/api/sites/{siteSlug}/agent/… . {siteSlug} = this site, another site (forwarded over the mesh
 * with the actor identity, like the tools), or "*" for agentSelf (merged: one item per reachable site).
 * Upgrade all: /_opm/api/agent/upgrade-all on the coordinating site (sequential, the coordinator last, stop on the first
 * failure).
 */
import { createClient, ep, type Client } from './http.js';
import type { AgentHealthResponse, AgentReleaseInfo, AgentUpgradeProgress, AgentUpgradeRun, GuiPort, ListResponse, ReleaseVerification, Slug, Ts } from './types.js';
import type { DownloadTicketResponse } from './agent-tools.js';

const S = 'session' as const;
const SA = 'session+actor' as const;

// ───────────────────────────────────────────────────────────── info (version / status parity)

export interface AgentBuildInfo {
  version: string;
  goVersion: string;
  os: string;
  arch: string;
  /** git commit (make dist ldflags); null for dev builds */
  commit: string | null;
  /** RFC 3339 build time (make dist ldflags); null for dev builds */
  builtAt: string | null;
}

export type AgentSupervisorKind = 'systemd' | 'none';

export interface AgentSupervisorInfo {
  kind: AgentSupervisorKind;
  /** "opm-agent.service" under systemd */
  unit: string | null;
  /** systemctl show: ActiveState / SubState (e.g. active / running); null when not under systemd */
  activeState: string | null;
  subState: string | null;
  /** the boot guard (systemd drop-in ExecStartPre that restores opm-agent.prev when a new binary never became healthy) */
  restartGuard: { installed: boolean; dropInPath: string | null; scriptPath: string | null };
}

export type EnrollmentState = 'enrolled' | 'not-enrolled' | 'revoked';

export interface AgentEnrollmentInfo {
  state: EnrollmentState;
  deploymentId: string;
  siteSlug: Slug;
  apiBase: string;
  enrolledAt: Ts | null;
  /** "SHA256:" + base64 (no padding) of sha256(raw Ed25519 site public key) */
  siteKeyFingerprint: string | null;
  publicKey: string | null; // base64
  keyVersion: number;
  /** revoked / not enrolled: what to do (copyable lines); null when enrolled */
  reenrollHelp: string[] | null;
}

export interface AgentCloudInfo {
  route: 'direct' | 'relay' | 'tunnel' | 'none';
  internet: boolean;
  lastFetchAt: Ts | null; //     last cloud view adopted
  lastHeartbeatAt: Ts | null; // last heartbeat the cloud accepted
  lastError: string | null;
  consoleUrl: string | null;
  deploymentLabel: string | null;
}

export interface AgentDockerInfo {
  ok: boolean;
  host: string; //              DOCKER_HOST / agent.json dockerHost / default socket
  version: string | null; //    Engine version
  apiVersion: string | null; // negotiated API version
  error: string | null;
}

export interface AgentPortsInfo {
  agentPort: number;
  guiAliasPort: number | null;
  /** agent.json edgeHttpPort (the default edge before a head); null = none */
  defaultEdgeHttpPort: number | null;
  edge: { running: boolean; defaultEdge: boolean; httpPort: number | null; httpsPort: number | null; lastError: string | null };
  /** ports the GUI answers on (same as the heartbeat) */
  guiPorts: GuiPort[];
}

/** The pinned release as this site knows it + whether it is newer than the installed binary. */
export interface AgentSelfRelease extends AgentReleaseInfo {
  sha256: string | null; //          known after a release check
  source: 'cloud-view' | 'release-check';
  checkedAt: Ts | null;
  upgradeAvailable: boolean;
  /** installed ≥ GUI_UPGRADE_MIN_VERSION is implied (this endpoint exists); false when the pin is OLDER than installed */
  newer: boolean;
  /** the last signature check of the pinned release (release check or upgrade verify phase) */
  verification: ReleaseVerification | null;
}

export interface AgentSelfInfo {
  siteSlug: Slug;
  self: boolean;
  version: string;
  build: AgentBuildInfo;
  pid: number;
  startedAt: Ts;
  uptimeSec: number;
  configPath: string | null;
  dataDir: string;
  binaryPath: string;
  /** opm-agent.prev next to the binary (kept by the last upgrade for rollback) */
  previousBinary: { path: string; exists: boolean; version: string | null; sha256: string | null };
  ports: AgentPortsInfo;
  docker: AgentDockerInfo;
  supervisor: AgentSupervisorInfo;
  enrollment: AgentEnrollmentInfo;
  cloud: AgentCloudInfo;
  release: AgentSelfRelease | null;
  /** the running or last upgrade attempt */
  upgrade: AgentUpgradeProgress | null;
  /** an update / rollback / snapshot / restore is running → upgrade + restart + config save answer 409 locked */
  busy: { kind: 'update' | 'rollback' | 'snapshot' | 'restore' | 'upgrade'; id: string; since: Ts } | null;
  /** copyable uninstall commands for this host (no GUI uninstall) */
  uninstall: string[];
}

// ───────────────────────────────────────────────────────────── upgrade

export interface StartAgentUpgradeRequest {
  /** the pinned version the operator saw (else 409 stale: the pin moved, reload) */
  version: string;
  /** typed siteSlug */
  confirm: Slug;
  /** reinstall the same version (CLI --force) */
  force?: boolean;
}

export interface AgentUpgradeStateResponse {
  current: AgentUpgradeProgress | null;
  /** newest first, ≤ 10 */
  history: AgentUpgradeProgress[];
}

export interface StartUpgradeAllRequest {
  version: string;
  confirm: 'UPGRADE';
}

export interface AgentUpgradeRunState {
  run: AgentUpgradeRun | null;
}

// ───────────────────────────────────────────────────────────── terminate (§21 #110)

/**
 * Stop a running update on every site of its plan (any site may ask; "current" = the update this site holds).
 * Nothing is rolled back unless rollback; restartAgent restarts every agent (with rollback: the roll back runs right
 * after the restart). rollback = each site converges to its head config with the volumes of the update's pre-update
 * snapshot (shown as a running "rollback" lock, stoppable the same way).
 */
export interface TerminateUpdateRequest {
  reason?: string; // ≤ 200
  rollback?: boolean;
  restartAgent?: boolean;
}
export interface TerminateSiteResult {
  siteSlug: Slug;
  ok: boolean;
  message: string;
}
export interface TerminateUpdateResponse {
  updateId: string;
  sites: TerminateSiteResult[];
}

// ───────────────────────────────────────────────────────────── restart

export interface RestartAgentRequest {
  /** typed siteSlug */
  confirm: Slug;
  reason?: string; // ≤ 200
}
export interface RestartAgentResponse {
  restarting: true;
  via: 'systemd' | 'exec';
}

// ───────────────────────────────────────────────────────────── agent.json view + safe settings

/** The agent.json settings the GUI may change (everything else is read-only in the GUI; edit the file + restart). */
export interface AgentEditableSettings {
  /** devTools: §21 #97 (default true; absent in views of agents < 0.11) */
  tools: { hostShell: boolean; containerExec: boolean; devTools?: boolean };
  containerNames: 'plain' | 'prefixed';
  /** the default edge HTTP port (before a head); null = no default edge */
  edgeHttpPort: number | null;
  /** the GUI alias port; null = none */
  guiAliasPort: number | null;
}

export interface AgentConfigView {
  path: string;
  /** agent.json as stored, secrets masked ("••••" + last 4; keys matching /token|secret|password|key$/i except releasePublicKey) */
  masked: Record<string, unknown>;
  settings: AgentEditableSettings;
  /** false when the file is not writable by the agent (the editor is read-only, with the reason) */
  writable: boolean;
  writableReason: string | null;
  /** agent.json changed on disk since the agent started (a restart applies it) */
  restartPending: boolean;
}

export interface PatchAgentConfigRequest {
  settings: Partial<{
    tools: Partial<AgentEditableSettings['tools']>;
    containerNames: AgentEditableSettings['containerNames'];
    edgeHttpPort: number | null;
    guiAliasPort: number | null;
  }>;
  /** typed siteSlug */
  confirm: Slug;
  /** default true: restart the agent so the settings apply */
  restart?: boolean;
}

export interface PatchAgentConfigResponse {
  /** dotted keys that changed, e.g. ["tools.hostShell", "edgeHttpPort"] */
  changed: string[];
  restarting: boolean;
  config: AgentConfigView;
}

export interface AgentSettingsIssue {
  path: string; // "edgeHttpPort" | "guiAliasPort" | "containerNames" | "tools.hostShell" | …
  code: 'V_PORT' | 'V_PORT_CLASH' | 'V_ENUM' | 'V_TYPE' | 'V_CONTAINER_NAMES';
  message: string;
  severity: 'error' | 'warning' | 'info';
}

/** Validation of the safe settings (agent + GUI use the same rules; the Go agent mirrors this). */
export function validateAgentSettings(s: AgentEditableSettings, ctx: { agentPort: number; previous?: AgentEditableSettings | null }): AgentSettingsIssue[] {
  const out: AgentSettingsIssue[] = [];
  const port = (path: string, v: unknown) => {
    if (v === null) return;
    if (typeof v !== 'number' || !Number.isInteger(v) || v < 1 || v > 65535) out.push({ path, code: 'V_PORT', message: 'a TCP port 1–65535, or empty for none', severity: 'error' });
    else if (v === ctx.agentPort) out.push({ path, code: 'V_PORT_CLASH', message: `port ${v} is the agent port (mesh + GUI)`, severity: 'error' });
  };
  port('edgeHttpPort', s.edgeHttpPort);
  port('guiAliasPort', s.guiAliasPort);
  if (s.edgeHttpPort !== null && s.edgeHttpPort === s.guiAliasPort) out.push({ path: 'guiAliasPort', code: 'V_PORT_CLASH', message: `port ${s.guiAliasPort} is also the default edge port`, severity: 'error' });
  if (s.containerNames !== 'plain' && s.containerNames !== 'prefixed') out.push({ path: 'containerNames', code: 'V_ENUM', message: 'plain or prefixed', severity: 'error' });
  for (const k of ['hostShell', 'containerExec'] as const) if (typeof s.tools?.[k] !== 'boolean') out.push({ path: `tools.${k}`, code: 'V_TYPE', message: 'on or off', severity: 'error' });
  if (s.tools?.devTools !== undefined && typeof s.tools.devTools !== 'boolean') out.push({ path: 'tools.devTools', code: 'V_TYPE', message: 'on or off', severity: 'error' });
  if (ctx.previous && ctx.previous.containerNames !== s.containerNames && (s.containerNames === 'plain' || s.containerNames === 'prefixed'))
    out.push({ path: 'containerNames', code: 'V_CONTAINER_NAMES', message: 'the running containers are renamed in place at the restart (no recreate); a clash with another container stops the next update with a staging error', severity: 'info' });
  if (s.edgeHttpPort === null) out.push({ path: 'edgeHttpPort', code: 'V_PORT', message: 'no default edge: before the first update the GUI answers only on the agent / alias port', severity: 'warning' });
  return out;
}

// ───────────────────────────────────────────────────────────── support bundle

/** Sections of the support bundle tar.gz (`opm-support-<site>-<yyyymmdd-hhmmss>.tar.gz`). */
export const SUPPORT_BUNDLE_SECTIONS = [
  'version.json', //        AgentSelfInfo (version, build, uptime, ports, supervisor, enrollment without keys)
  'agent.json', //          masked (AgentConfigView.masked)
  'debug/', //              the last 24 h of the debug log (JSONL, already redacted)
  'docker-info.json', //    docker info + version (no registry auths)
  'containers.json', //     docker ps -a of OPM containers (labels, state, ports; env values removed)
  'activities.jsonl', //    the last 500 activities of this site
  'status.json', //         the site status (as GET /sites/{s}/status)
  'upgrade/', //            upgrade state + history
] as const;

// ───────────────────────────────────────────────────────────── endpoint table

const P = '/_opm/api/sites/{siteSlug}/agent';

export const AGENT_SELF_API = {
  agentHealth: ep<void, AgentHealthResponse>()({ method: 'GET', path: '/_opm/api/health', auth: 'public', perm: null, body: 'none', res: 'json', errors: [], summary: '§21 #91 liveness + readiness after a restart (install script, upgrade engine, watcher): ok, version, schema, cloud connected, docker, socket activation. 503 with the same body while not ok.' }),
  agentSelf: ep<void, AgentSelfInfo>()({ method: 'GET', path: P, auth: S, perm: null, body: 'none', res: 'json', errors: ['not-found', 'unavailable'], summary: 'opm-agent version + status parity: version, build, uptime, paths, ports, Docker, systemd, enrollment, cloud link, pinned release, upgrade state, uninstall commands. "*" = { items: AgentSelfInfo[] }.' }),
  checkRelease: ep<void, AgentSelfRelease>()({ method: 'POST', path: `${P}/release/check`, auth: S, perm: null, body: 'none', res: 'json', errors: ['unavailable', 'invalid'], summary: 'opm-agent verify-release parity: fetch GET /v1/agent/release with the site identity and verify the release signature (agent.json releasePublicKey, else the cloud key); no download.' }),
  startUpgrade: ep<StartAgentUpgradeRequest, AgentUpgradeProgress>()({ method: 'POST', path: `${P}/upgrade`, auth: SA, perm: null, body: 'json', res: 'json', ok: 202, errors: ['invalid', 'stale', 'conflict', 'locked', 'unavailable', 'not-found'], activities: ['agent.upgrade-started', 'agent.upgraded', 'agent.upgrade-failed', 'agent.rolled-back'], summary: 'Upgrade to the pinned release: download (signed site identity) → verify sha256 + release signature + self-test → swap (keep opm-agent.prev) → restart → health ≤ 60 s, else rollback. locked: an update / rollback / snapshot / restore runs; conflict: an upgrade runs; invalid: confirm mismatch / already on it (without force).' }),
  upgradeState: ep<void, AgentUpgradeStateResponse>()({ method: 'GET', path: `${P}/upgrade`, auth: S, perm: null, body: 'none', res: 'json', errors: ['unavailable'], summary: 'The running / last upgrade attempt + the last 10 (survives the restart).' }),
  restartAgent: ep<RestartAgentRequest, RestartAgentResponse>()({ method: 'POST', path: `${P}/restart`, auth: SA, perm: null, body: 'json', res: 'json', ok: 202, errors: ['invalid', 'conflict', 'unavailable'], activities: ['agent.restarted'], summary: 'Restart the agent (systemctl restart, else re-exec). Containers keep running. Never blocked by a running update (§21 #110: a coordinator restarted mid-update rolls it back); conflict while an upgrade runs.' }),
  agentConfig: ep<void, AgentConfigView>()({ method: 'GET', path: `${P}/config`, auth: S, perm: null, body: 'none', res: 'json', errors: ['unavailable'], summary: 'agent.json with secrets masked + the editable safe settings.' }),
  patchAgentConfig: ep<PatchAgentConfigRequest, PatchAgentConfigResponse>()({ method: 'PATCH', path: `${P}/config`, auth: SA, perm: null, body: 'json', res: 'json', errors: ['invalid', 'locked', 'conflict', 'forbidden'], activities: ['agent.config-changed', 'agent.restarted'], summary: 'Validate (validateAgentSettings) + write agent.json atomically (agent.json.bak kept) + restart (default). forbidden: the file is not writable. Only tools.*, containerNames, edgeHttpPort, guiAliasPort.' }),
  supportBundle: ep<void, DownloadTicketResponse>()({ method: 'POST', path: `${P}/support-bundle`, auth: SA, perm: null, body: 'none', res: 'json', errors: ['rate-limited', 'unavailable'], activities: ['agent.support-bundle-downloaded'], summary: 'Single-use link (5 min) to opm-support-<site>-<ts>.tar.gz (SUPPORT_BUNDLE_SECTIONS); the activity is written when the download starts.' }),
  // mesh
  startUpgradeAll: ep<StartUpgradeAllRequest, AgentUpgradeRunState>()({ method: 'POST', path: '/_opm/api/agent/upgrade-all', auth: SA, perm: null, body: 'json', res: 'json', ok: 202, errors: ['invalid', 'stale', 'conflict', 'locked', 'unavailable'], activities: ['agent.upgrade-started'], summary: 'Upgrade every reachable site below the pin, one at a time (siteSlug order, this coordinating site LAST), stop on the first failure. Unreachable / unsupported sites = skipped.' }),
  upgradeAllState: ep<void, AgentUpgradeRunState>()({ method: 'GET', path: '/_opm/api/agent/upgrade-all', auth: S, perm: null, body: 'none', res: 'json', errors: [], summary: 'The current / last mesh run of this coordinator (survives its own restart).' }),
  cancelUpgradeAll: ep<void, AgentUpgradeRunState>()({ method: 'POST', path: '/_opm/api/agent/upgrade-all/cancel', auth: SA, perm: null, body: 'none', res: 'json', errors: ['conflict'], summary: 'Stop the run after the site that is upgrading now; the rest = cancelled.' }),
} as const;

export type AgentSelfClient = Client<typeof AGENT_SELF_API>;

export function createAgentSelfClient(baseUrl: string, getActorToken: () => string | null, init?: { fetch?: typeof fetch }): AgentSelfClient {
  return createClient(
    AGENT_SELF_API,
    baseUrl,
    ({ endpoint }) => {
      if (endpoint.auth !== 'session+actor') return null;
      const t = getActorToken();
      return t ? `Actor ${t}` : null;
    },
    { fetch: init?.fetch, credentials: 'same-origin' },
  );
}

/** Uninstall commands (the agent fills paths from its own config; the GUI shows them copyable). Containers + volumes are
 *  NOT removed by these lines; the last two lines are the optional data wipe. */
export function uninstallCommands(p: { configPath: string; dataDir: string; binaryPath: string; systemd: boolean }): string[] {
  const lines: string[] = [];
  // §21 #91: the socket unit goes first (a held socket would start the service again on the next connection)
  if (p.systemd) lines.push('sudo systemctl disable --now opm-agent.socket opm-agent 2>/dev/null; sudo systemctl disable --now opm-agent', 'sudo rm -f /etc/systemd/system/opm-agent.service /etc/systemd/system/opm-agent.socket', 'sudo rm -rf /etc/systemd/system/opm-agent.service.d', 'sudo systemctl daemon-reload');
  else lines.push('sudo pkill -f "opm-agent run"');
  lines.push(`sudo rm -f ${p.binaryPath} ${p.binaryPath}.prev ${p.binaryPath}.new`, 'sudo rm -f /usr/local/lib/opm/upgrade-guard.sh');
  lines.push(`# optional — removes the site identity, snapshots and volumes (the OPM containers keep running until removed):`);
  lines.push(`sudo rm -rf ${p.dataDir} ${p.configPath} /etc/opm/install-token /etc/opm/installed-deps.json`);
  return lines;
}
