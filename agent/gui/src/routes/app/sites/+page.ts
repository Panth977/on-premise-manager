import { redirectToModule } from '$lib/redirect';
import type { PageLoad } from './$types';

/** nav2: the "Sites config" module is gone — its site cards are the all-sites Config page */
export const load: PageLoad = ({ parent }) => redirectToModule(parent, 'config', '', '*');
