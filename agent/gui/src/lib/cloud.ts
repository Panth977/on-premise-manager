/**
 * Where the cloud console lives, for helpful copy ("open the deployment in the cloud console → Diff → Publish proposal").
 * AgentInfoResponse has no console URL today; the GUI reads OPTIONAL fields when the agent sends them (requested in
 * ORCH.md): `consoleUrl` (the console origin), `apiBase` (/etc/opm/agent.json; the console origin when the API is
 * served through Hosting on the same origin) and `deploymentLabel`. Without them the copy stays generic.
 */
import type { AgentInfoResponse } from '@opm/shared/types';

export interface InfoExtras {
  consoleUrl?: string | null;
  apiBase?: string | null;
  deploymentLabel?: string | null;
}

function extras(info: AgentInfoResponse | null | undefined): InfoExtras {
  return (info ?? {}) as InfoExtras;
}

function httpOrigin(u: string | null | undefined): string | null {
  if (!u) return null;
  try {
    const x = new URL(u);
    return x.protocol === 'https:' || x.protocol === 'http:' ? x.origin : null;
  } catch {
    return null;
  }
}

/** the cloud console origin, or null when unknown (an API on Cloud Functions / Cloud Run is not the console) */
export function consoleOrigin(info: AgentInfoResponse | null | undefined): string | null {
  const e = extras(info);
  const direct = httpOrigin(e.consoleUrl);
  if (direct) return direct;
  const api = httpOrigin(e.apiBase);
  if (!api) return null;
  return /(\.cloudfunctions\.net|\.run\.app|\.a\.run\.app)$/.test(new URL(api).hostname) ? null : api;
}

/** a page of this deployment in the cloud console (path e.g. "/diff"), or null when the console is unknown */
export function consoleDeploymentUrl(info: AgentInfoResponse | null | undefined, path = ''): string | null {
  const o = consoleOrigin(info);
  return o && info ? `${o}/app/deployments/${encodeURIComponent(info.deploymentId)}${path}` : null;
}

export function deploymentLabel(info: AgentInfoResponse | null | undefined): string | null {
  const l = extras(info).deploymentLabel;
  return typeof l === 'string' && l.trim() ? l.trim() : null;
}
