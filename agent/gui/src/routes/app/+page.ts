import { redirectToModule } from '$lib/redirect';
import type { PageLoad } from './$types';

/** /_opm/app → the Sites page of this tab's site selection (nav2: every module lives under /_opm/app/{site}/…) */
export const load: PageLoad = ({ parent }) => redirectToModule(parent, 'sites');
