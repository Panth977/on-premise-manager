import { redirect } from '@sveltejs/kit';
import { appHref } from '$lib/nav';
import type { PageLoad } from './$types';

/** §21 #104: Diff (proposals) became Branch; ?rev= has no meaning any more (proposals are gone) */
export const load: PageLoad = async ({ params, parent }) => {
  await parent();
  redirect(307, appHref(params.siteSlug, 'branch'));
};
