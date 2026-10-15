import { redirectToModule } from '$lib/redirect';
import type { PageLoad } from './$types';

/** old Transfers URL → /_opm/app/{site}/transfers (§21 #100; the global site selection) */
export const load: PageLoad = ({ params, parent }) => redirectToModule(parent, 'transfers', '', params.siteSlug);
