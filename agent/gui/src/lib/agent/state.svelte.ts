/**
 * Agent versions of every reachable site (round I). Polled by the app layout (60 s) for the sidebar "upgrade available"
 * badge and the Status / Sites hints; the Agent page reloads it after an upgrade / restart.
 */
import type { AgentSelfInfo } from '@opm/shared/agent-self';
import { Resource } from '$lib/state/resource.svelte';
import { agentSelfList } from './api';

export const agentAll = new Resource<AgentSelfInfo[]>(() => agentSelfList('*'));

export function agentOf(site: string): AgentSelfInfo | undefined {
  return agentAll.data?.find((a) => a.siteSlug === site);
}
/** sites whose installed agent is below the pinned release */
export function upgradableSites(): AgentSelfInfo[] {
  return (agentAll.data ?? []).filter((a) => a.release?.upgradeAvailable);
}
