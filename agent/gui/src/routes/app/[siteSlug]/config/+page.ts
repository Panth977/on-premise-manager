import { redirect } from '@sveltejs/kit';
import { base } from '$app/paths';
import type { PageLoad } from './$types';

/** app/{site}/config (and old ?tab= links) → the site's section page; "*" keeps the all-sites summary. */
const SECTION: Record<string, string> = { containers: 'containers', edge: 'edge', site: 'settings', settings: 'settings', volumes: 'volumes' };
export const load: PageLoad = ({ params, url }) => {
  if (params.siteSlug === '*') return;
  redirect(307, `${base}/app/${params.siteSlug}/config/${SECTION[url.searchParams.get('tab') ?? ''] ?? 'containers'}`);
};
