import { redirect } from '@sveltejs/kit';
import { appHref } from '$lib/nav';
import type { PageLoad } from './$types';

/** §21 #97: one SQL workbench for PostgreSQL / SQLite / Cassandra — devtools/postgres = devtools/sql?kind=postgres (keeps ?c=) */
export const load: PageLoad = ({ params, url }) => {
  const c = url.searchParams.get('c');
  redirect(307, appHref(params.siteSlug, `devtools/sql?kind=postgres${c ? `&c=${encodeURIComponent(c)}` : ''}`));
};
