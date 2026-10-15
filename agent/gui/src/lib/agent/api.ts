/**
 * Agent self-management client (round I, SPEC §21 #90; contract @opm/shared/agent-self AGENT_SELF_API). Same rules as
 * lib/api.ts: same origin, cookie session, Authorization: Actor <token> on session+actor endpoints, the same 401 handling
 * (actor popup / login redirect).
 */
import { createAgentSelfClient, type AgentSelfClient, type AgentSelfInfo } from '@opm/shared/agent-self';
import type { AgentHealthResponse } from '@opm/shared/types';
import { isApiError, onApiError, opmFetch } from '$lib/api';
import { session } from '$lib/state/session.svelte';

const client = createAgentSelfClient('', () => session.actorToken, { fetch: opmFetch });

type AnyCall = (a: unknown) => Promise<unknown>;
const raw = client as unknown as Record<string, AnyCall>;
export const agentApi: AgentSelfClient = new Proxy({} as AgentSelfClient, {
  get(_t, name: string) {
    return async (args: unknown = {}) => {
      try {
        return await raw[name](args ?? {});
      } catch (e) {
        onApiError(name, e);
        throw e;
      }
    };
  },
});

/** agentSelf for one site, or every reachable site ("*" answers { items }) */
export async function agentSelfList(site: string): Promise<AgentSelfInfo[]> {
  const r = (await agentApi.agentSelf({ siteSlug: site })) as AgentSelfInfo | { items: AgentSelfInfo[] };
  return 'items' in r && !('siteSlug' in r) ? r.items : [r as AgentSelfInfo];
}

/** the agent did not answer at all (restarting, network down) — not an API error */
export function isNetworkError(e: unknown): boolean {
  return !isApiError(e) && (e instanceof TypeError || (e instanceof Error && /network|fetch/i.test(e.message)));
}

/** §21 #91 GET /_opm/api/health of THIS agent (public; the body comes with 503 too). null = no such endpoint / no answer. */
export async function fetchHealth(): Promise<AgentHealthResponse | null> {
  try {
    const res = await opmFetch('/_opm/api/health', { method: 'GET', credentials: 'same-origin', cache: 'no-store' });
    if (res.status === 404) return null;
    const b = (await res.json()) as AgentHealthResponse;
    return b && typeof b.ok === 'boolean' ? b : null;
  } catch {
    return null;
  }
}
