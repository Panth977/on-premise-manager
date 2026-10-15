/** Old-URL redirects (nav2, SPEC §21 #97): the target site = this tab's selection, else this site, else "*". */
import { redirect } from '@sveltejs/kit';
import { api } from './api';
import { appHref, storedSite } from './nav';

export async function targetSite(): Promise<string> {
  const s = storedSite();
  if (s) return s;
  try {
    return (await api.info({})).siteSlug || '*';
  } catch {
    return '*';
  }
}
/** redirect(307) to /_opm/app/{site}/{path}{search}; `parent` = the load's parent() (the root layout installs the mock fetch) */
export async function redirectToModule(parent: () => Promise<unknown>, path: string, search = '', site?: string): Promise<never> {
  await parent();
  redirect(307, appHref(site ?? (await targetSite()), path) + search);
}
