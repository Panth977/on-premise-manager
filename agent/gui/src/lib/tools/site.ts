/** Toolbox helpers (round T; nav2: the page links live in lib/nav.ts). */
import type { ToolsInfo } from '@opm/shared/agent-tools';
import { tools } from './api';

/** toolsInfo per site, cached for 30 s (capabilities change only when agent.json changes) */
const cache = new Map<string, { at: number; p: Promise<ToolsInfo> }>();
export function toolsInfo(site: string, fresh = false): Promise<ToolsInfo> {
  const c = cache.get(site);
  if (!fresh && c && Date.now() - c.at < 30_000) return c.p;
  const p = tools.toolsInfo({ siteSlug: site });
  cache.set(site, { at: Date.now(), p });
  p.catch(() => cache.delete(site));
  return p;
}
