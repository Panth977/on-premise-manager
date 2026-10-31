/**
 * Runtime switches (Vite env, fixed at build time).
 * - VITE_MOCK=1            in-memory fake API + fake auth (no backend) — dev + Playwright.
 * - VITE_OPM_API_BASE      API origin (default: same origin).
 * §21 #108: the auth options + an optional API origin come at RUNTIME from <base>/opm-config.json (lib/auth/runtime-config.ts);
 * VITE_OPM_API_BASE / mock still win over the runtime apiBase.
 */
const e = import.meta.env;
export const MOCK = e.VITE_MOCK === '1' || e.VITE_MOCK === 'true';

let runtimeApiBase: string | null = null;
/** §21 #108 apiBase of opm-config.json (set once by session.init before the first API call). */
export function setRuntimeApiBase(b: string | null) {
  runtimeApiBase = b ? b.replace(/\/$/, '') : null;
}

/** Base URL the client prepends to "/v1/…". */
export function apiBase(): string {
  if (MOCK) return 'http://mock.opm.local';
  if (e.VITE_OPM_API_BASE) return e.VITE_OPM_API_BASE.replace(/\/$/, '');
  if (runtimeApiBase) return runtimeApiBase;
  return typeof location !== 'undefined' ? location.origin : '';
}
