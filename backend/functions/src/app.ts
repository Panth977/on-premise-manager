/**
 * The OPM cloud application, independent of where it runs (§21 #108): the /v1 router with every handler, the CORS
 * rules, the document triggers and the scheduled jobs. Entry points:
 *   index.ts   Cloud Functions (api / importImage / daily / mirror*) — Firebase adapter
 *   server.ts  opm-cloud (one Node process: API + jobs + outbox dispatcher + static frontend) — local or Firebase adapter
 */
import type { Readable } from 'node:stream';
import { dispatch, setHandlers, type Handlers, type ReqLike } from './core/router.js';
import { env } from './core/env.js';
import { catalogHandlers } from './routes/catalog.js';
import { accountHandlers } from './routes/accounts.js';
import { templateHandlers } from './routes/templates.js';
import { blobHandlers } from './routes/blobs.js';
import { deploymentHandlers } from './routes/deployments.js';
import { setupHandlers } from './routes/setup.js';
import { statusHandlers } from './routes/status.js';
import { transferHandlers } from './routes/transfers.js';
import { installHandlers } from './routes/install.js';
import { agentHandlers } from './routes/agent.js';
import { iconHandlers } from './routes/icons.js';
import { agentUpgradeHandlers } from './routes/agent-upgrade.js';
import { templateSyncHandlers } from './routes/template-sync.js';
import { volumeContentHandlers } from './routes/volume-content.js';
import { branchHandlers } from './routes/branches.js';
import { agentBranchHandlers } from './routes/agent-branches.js';
import { remoteGuiHandlers } from './routes/remote-gui.js';
import { githubMirrorHandlers } from './routes/github-mirror.js';
import { runImport } from './core/importer.js';
import { runDaily } from './jobs/daily.js';
import { auditSystem } from './core/audit.js';
import { onAgentCommit, onDeploymentCommit, onTemplateCommit, onVolumeVersion } from './core/mirror-hooks.js';
import { drainBranch, drainDue, recoverStuck } from './core/mirror-worker.js';
import { logger, type DocTrigger, type ScheduledJob } from './ports/index.js';
import { TRIGGER_SPECS } from './core/trigger-specs.js';

const handlers: Handlers = {
  ...catalogHandlers,
  ...accountHandlers,
  ...templateHandlers,
  ...blobHandlers,
  ...deploymentHandlers,
  ...setupHandlers,
  ...statusHandlers,
  ...transferHandlers,
  ...installHandlers,
  ...agentHandlers,
  ...iconHandlers,
  ...agentUpgradeHandlers,
  ...templateSyncHandlers,
  ...volumeContentHandlers,
  ...branchHandlers,
  ...agentBranchHandlers,
  ...githubMirrorHandlers,
  ...remoteGuiHandlers,
};
setHandlers(handlers);

// ───────────────────────────────────────── HTTP (/v1/*) — shared by the Cloud Function and opm-cloud
// open CORS: agent + install routes and the public icon bytes (GET /v1/icons/{id}/content)
const OPEN_CORS = /^\/v1\/((agent|install)\/|icons\/[^/]+\/content$)/;
const ALLOW_HEADERS_OPEN = 'Content-Type, X-OPM-Site, X-OPM-Ts, X-OPM-Nonce, X-OPM-Sig, Authorization, Range';
const EXPOSE_OPEN = 'X-OPM-Server-Time, X-OPM-Sha256, Content-Range, X-OPM-Signature, X-Request-Id';
const ALLOW_HEADERS_HUMAN = 'Content-Type, Authorization, Range, X-OPM-Sha256, If-None-Match';
const EXPOSE_HUMAN = 'X-Request-Id, X-OPM-Sha256, Content-Range, Content-Disposition, Retry-After, X-OPM-Server-Time, ETag, Content-Length';
const METHODS = 'GET, POST, PUT, PATCH, DELETE, OPTIONS';

export function corsHeaders(path: string, origin: string | undefined): Record<string, string> {
  if (OPEN_CORS.test(path)) return { 'Access-Control-Allow-Origin': '*', 'Access-Control-Allow-Methods': METHODS, 'Access-Control-Allow-Headers': ALLOW_HEADERS_OPEN, 'Access-Control-Expose-Headers': EXPOSE_OPEN, 'Access-Control-Max-Age': '3600' };
  if (!origin) return {};
  const allowed = env.frontendOrigins;
  if (allowed.includes('*') || allowed.includes(origin))
    return { 'Access-Control-Allow-Origin': origin, Vary: 'Origin', 'Access-Control-Allow-Methods': METHODS, 'Access-Control-Allow-Headers': ALLOW_HEADERS_HUMAN, 'Access-Control-Expose-Headers': EXPOSE_HUMAN, 'Access-Control-Max-Age': '3600' };
  return { Vary: 'Origin' };
}

export interface HttpIn {
  method: string;
  /** the URL as received (may carry a mount prefix before /v1/) */
  rawUrl: string;
  headers: Record<string, string | undefined>;
  body: Buffer;
  ip: string;
  signal: AbortSignal;
}
export interface HttpOut {
  status: number;
  headers: Record<string, string>;
  body: Buffer | string | Readable | null;
  /** the path used for matching (logging) */
  path: string;
}
/** one /v1 request → reply (CORS + mount handling exactly as the Cloud Function did before §21 #108) */
export async function handleApi(r: HttpIn): Promise<HttpOut> {
  const raw = r.rawUrl || '/';
  const i = raw.indexOf('/v1/');
  const url = i >= 0 ? raw.slice(i) : raw;
  const mount = i > 0 ? raw.slice(0, i) : '';
  const qi = url.indexOf('?');
  const path = qi >= 0 ? url.slice(0, qi) : url;
  const cors = corsHeaders(path, r.headers.origin);
  if (r.method === 'OPTIONS') return { status: 204, headers: cors, body: null, path };
  const req: ReqLike = { method: r.method, url, headers: r.headers, body: r.body, ip: r.ip, signal: r.signal, mount };
  const reply = await dispatch(req);
  return { status: reply.status, headers: { ...cors, ...reply.headers }, body: reply.body as HttpOut['body'], path };
}

// ───────────────────────────────────────── document triggers (Events port)
const HANDLERS: Record<string, DocTrigger['handler']> = {
  async importImage(e) {
    const after = e.after;
    const before = e.before;
    if (!after || after.import?.status !== 'pending') return;
    if (before && before.import?.status === 'pending') return;
    await runImport(e.params.serviceSlug!, e.params.version!);
  },
  // §21 #106 GitHub mirror: commits / volume versions → a queue item; a queue item → drain that branch
  mirrorTemplateCommit: async (e) => void (e.after && (await onTemplateCommit(e.params.templateId!, e.after))),
  mirrorDeploymentCommit: async (e) => void (e.after && (await onDeploymentCommit(e.params.deploymentId!, e.after))),
  mirrorAgentCommit: async (e) => void (e.after && (await onAgentCommit(e.params.deploymentId!, e.params.siteSlug!, e.after))),
  mirrorVolumeVersion: async (e) => void (e.after && (await onVolumeVersion(e.params.deploymentId!, e.after))),
  async mirrorQueue(e) {
    const b = e.after?.gitBranch;
    if (typeof b === 'string') await drainBranch(b);
  },
};
export const TRIGGERS: DocTrigger[] = TRIGGER_SPECS.map((s) => ({ ...s, handler: HANDLERS[s.name]! }));

// ───────────────────────────────────────── scheduled jobs (Scheduler port)
export const JOBS: ScheduledJob[] = [
  {
    name: 'daily',
    schedule: 'every day 03:00',
    timeoutSeconds: 540,
    memory: '512MiB',
    async run() {
      try {
        const r = await runDaily();
        logger.info('daily job', r as unknown as Record<string, unknown>);
      } catch (e) {
        await auditSystem({ job: 'daily', action: 'system.daily-cleanup', targetType: 'system', target: 'system/daily', ok: false, errorMessage: (e as Error)?.message ?? String(e) });
        throw e;
      }
    },
  },
  {
    name: 'mirrorRetry',
    schedule: 'every 5 minutes',
    timeoutSeconds: 540,
    memory: '1GiB',
    async run() {
      await recoverStuck();
      const n = await drainDue(480_000);
      if (n) logger.info('github mirror retry', { processed: n });
    },
  },
];
