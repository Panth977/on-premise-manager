import { redirectToModule } from '$lib/redirect';
import type { PageLoad } from './$types';

/** nav2: Updates became Diff; §21 #104: Diff became Branch */
export const load: PageLoad = ({ parent }) => redirectToModule(parent, 'branch', '');
