import { redirectToModule } from '$lib/redirect';
import type { PageLoad } from './$types';

/** old URL (before nav2) → /_opm/app/{site}/files */
export const load: PageLoad = ({ url, parent }) => redirectToModule(parent, 'files', url.search);
