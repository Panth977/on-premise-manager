import { redirect } from '@sveltejs/kit';
import { appHref } from '$lib/nav';
import type { PageLoad } from './$types';

/**
 * nav2 (SPEC §21 #97): the Tools group is gone. Logs / Terminal / Monitoring are tabs of the container page; host
 * monitoring is on Status; the agent log is in Debug, the edge access log in Network; the host shell in Agent.
 */
export const load: PageLoad = ({ params, url }) => {
  const s = params.siteSlug;
  const [tool] = (params.rest ?? '').split('/');
  const q = url.searchParams;
  const c = (q.get('c') ?? '').split(',').filter(Boolean)[0];
  const ctr = (tab: string) => appHref(s, `containers/${encodeURIComponent(c!)}${tab ? `?tab=${tab}` : ''}`);
  let to = appHref(s, 'containers');
  if (tool === 'logs') to = c ? ctr('logs') : q.get('src') === 'agent' ? appHref(s, 'debug?tab=agent') : q.get('src') === 'access' ? appHref(s, 'network?tab=access') : to;
  else if (tool === 'terminal') to = c ? ctr('terminal') : to;
  else if (tool === 'monitoring') to = c ? ctr('monitoring') : appHref(s, 'status');
  else if (tool === 'containers') to = c ? ctr('') : to;
  else if (tool === 'volumes' || tool === 'docker' || tool === 'network') to = appHref(s, tool) + url.search;
  redirect(307, to);
};
