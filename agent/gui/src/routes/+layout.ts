// SPA: the Go agent serves index.html for /_opm/login, /_opm/app and /_opm/app/** (SPEC §7.1)
import { setFetch } from '$lib/api';

export const ssr = false;
export const prerender = false;
export const trailingSlash = 'never';

let ready = false;
export const load = async () => {
  if (__OPM_MOCK__ && !ready) {
    const m = await import('$lib/mock');
    setFetch(m.mockFetch);
  }
  ready = true;
  return {};
};
