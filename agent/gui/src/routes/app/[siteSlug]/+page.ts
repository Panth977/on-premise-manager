import { redirect } from '@sveltejs/kit';
import { appHref } from '$lib/nav';
import type { PageLoad } from './$types';

/** /_opm/app/{site} → its Status page */
export const load: PageLoad = ({ params }) => redirect(307, appHref(params.siteSlug, 'status'));
