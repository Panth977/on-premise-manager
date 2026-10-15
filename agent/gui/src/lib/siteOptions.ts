/** Options of the site selector (top of the sidebar) and of the inline "Choose a site" state (nav2). */
import type { OverviewResponse } from '@opm/shared/types';
import { siteSlugs } from './state/app.svelte';
import type { SelectOption } from './ui/listbox';

export function siteOptions(o: OverviewResponse | undefined, withAll = false): SelectOption[] {
  const out: SelectOption[] = withAll ? [{ value: '*', label: 'All sites', icon: 'grid', hint: '*' }] : [];
  for (const s of siteSlugs(o)) {
    const t = o?.sites.find((x) => x.siteSlug === s);
    const dot = !t ? 'gray' : t.self || t.reachable ? 'green' : t.enrolled ? 'red' : 'gray';
    out.push({
      value: s,
      label: s,
      dot,
      hint: t?.label && t.label !== s ? t.label : undefined,
      badge: o?.self === s ? { text: 'this site', tone: 'blue' } : t && !t.reachable && !t.self ? { text: t.enrolled ? 'unreachable' : 'not enrolled', tone: t.enrolled ? 'red' : 'gray' } : undefined,
    });
  }
  return out;
}
