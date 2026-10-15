import { redirectToModule } from '$lib/redirect';
import type { PageLoad } from './$types';

/** old URL (before nav2) → /_opm/app/{site}/backups */
export const load: PageLoad = ({ url, parent }) => redirectToModule(parent, 'backups', url.search);
