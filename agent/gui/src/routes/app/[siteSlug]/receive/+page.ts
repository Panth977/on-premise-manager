import { redirect } from '@sveltejs/kit';
import { appHref } from '$lib/nav';
import type { PageLoad } from './$types';

/** §21 #100: Send and Receive are one module — {site}/receive → 307 {site}/transfers */
export const load: PageLoad = ({ params }) => {
  redirect(307, appHref(params.siteSlug, 'transfers'));
};
