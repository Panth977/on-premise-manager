/**
 * Runtime switches (Vite env, fixed at build time).
 * - VITE_MOCK=1            in-memory fake API + fake auth (no Firebase, no backend) — dev + Playwright.
 * - VITE_USE_EMULATORS=1   Firebase Auth emulator (127.0.0.1:9099) + the Functions emulator API base.
 * - VITE_OPM_API_BASE      API origin (default: same origin; Hosting rewrites /v1/** → function api).
 * - VITE_OPM_GOOGLE_DOMAIN shows "Continue with Google" restricted to that hosted domain (Firebase provider only).
 * §21 #108: the auth provider + an optional API origin come at RUNTIME from <base>/opm-config.json (lib/auth/runtime-config.ts);
 * VITE_OPM_API_BASE / emulators / mock still win over the runtime apiBase.
 */
const e = import.meta.env;
export const MOCK = e.VITE_MOCK === '1' || e.VITE_MOCK === 'true';
export const USE_EMULATORS = !MOCK && (e.VITE_USE_EMULATORS === '1' || e.VITE_USE_EMULATORS === 'true');
/** Firebase project id the emulators run as (backend/scripts/emu.sh default "demo-opm"; ID tokens must carry it). */
export const EMULATOR_PROJECT = e.VITE_EMULATOR_PROJECT || 'demo-opm';
export const EMULATOR_API_BASE = e.VITE_EMULATOR_API_BASE || `http://127.0.0.1:5001/${EMULATOR_PROJECT}/asia-south1/api`;
export const AUTH_EMULATOR_URL = e.VITE_AUTH_EMULATOR_URL || 'http://127.0.0.1:9099';
export const GOOGLE_DOMAIN = e.VITE_OPM_GOOGLE_DOMAIN || '';

let runtimeApiBase: string | null = null;
/** §21 #108 apiBase of opm-config.json (set once by session.init before the first API call). */
export function setRuntimeApiBase(b: string | null) {
  runtimeApiBase = b ? b.replace(/\/$/, '') : null;
}

/** Base URL the client prepends to "/v1/…". */
export function apiBase(): string {
  if (MOCK) return 'http://mock.opm.local';
  if (e.VITE_OPM_API_BASE) return e.VITE_OPM_API_BASE.replace(/\/$/, '');
  if (USE_EMULATORS) return EMULATOR_API_BASE;
  if (runtimeApiBase) return runtimeApiBase;
  return typeof location !== 'undefined' ? location.origin : '';
}
