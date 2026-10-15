import { redirectToModule } from '$lib/redirect';
import type { PageLoad } from './$types';

/** old URL (before nav2) → /_opm/app/{site}/secrets */
export const load: PageLoad = ({ url, parent }) => redirectToModule(parent, 'secrets', url.search);
