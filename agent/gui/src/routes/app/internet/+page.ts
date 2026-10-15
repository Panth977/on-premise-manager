import { redirectToModule } from '$lib/redirect';
import type { PageLoad } from './$types';

/** old URL (before nav2) → /_opm/app/{site}/internet */
export const load: PageLoad = ({ url, parent }) => redirectToModule(parent, 'internet', url.search);
