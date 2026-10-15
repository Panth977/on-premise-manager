import { redirect } from '@sveltejs/kit';
import { appHref } from '$lib/nav';
import type { PageLoad } from './$types';

/** §21 #97: one SQL workbench for PostgreSQL / SQLite / Cassandra — devtools/sqlite = devtools/sql?kind=sqlite (keeps ?c=) */
export const load: PageLoad = ({ params, url }) => {
  const c = url.searchParams.get('c');
  redirect(307, appHref(params.siteSlug, `devtools/sql?kind=sqlite${c ? `&c=${encodeURIComponent(c)}` : ''}`));
};
