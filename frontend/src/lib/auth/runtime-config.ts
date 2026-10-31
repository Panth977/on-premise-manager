/**
 * §21 #108 runtime config: GET <base>/opm-config.json (no-store) at startup; opm-cloud serves it dynamically.
 * Missing / invalid / HTML (SPA fallback) → the defaults.
 * Pure (no Svelte, no import.meta.env) so tests can import it.
 */
export interface OpmAuthConfig {
  /** offer "Forgot password" (the VM can send mail) */
  passwordReset: boolean;
  /** "Continue with <label>" → /auth/oidc/start */
  oidc: { label: string } | null;
}

export interface OpmRuntimeConfig {
  auth: OpmAuthConfig;
  /** API origin override ("" / missing = same origin). VITE_OPM_API_BASE wins over it. */
  apiBase: string | null;
}

export const DEFAULT_RUNTIME_CONFIG: OpmRuntimeConfig = { auth: { passwordReset: false, oidc: null }, apiBase: null };

function defaults(): OpmRuntimeConfig {
  return { auth: { ...DEFAULT_RUNTIME_CONFIG.auth }, apiBase: null };
}

/** Any JSON value → a valid config (unknown / wrong-typed fields fall back to the defaults). */
export function parseRuntimeConfig(raw: unknown): OpmRuntimeConfig {
  const out = defaults();
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return out;
  const r = raw as { auth?: unknown; apiBase?: unknown };
  if (typeof r.apiBase === 'string' && /^https?:\/\/[^\s]+$/i.test(r.apiBase.trim())) out.apiBase = r.apiBase.trim().replace(/\/$/, '');
  const a = r.auth;
  if (!a || typeof a !== 'object' || Array.isArray(a)) return out;
  const auth = a as { passwordReset?: unknown; oidc?: unknown };
  out.auth.passwordReset = auth.passwordReset === true;
  const o = auth.oidc as { label?: unknown } | null | undefined;
  if (o && typeof o === 'object' && typeof o.label === 'string' && o.label.trim()) out.auth.oidc = { label: o.label.trim().slice(0, 60) };
  return out;
}

/** Fetch + parse; never throws (network error, 404, HTML fallback, bad JSON, > timeoutMs → defaults). */
export async function loadRuntimeConfig(url: string, fetchFn: typeof fetch = fetch, timeoutMs = 5000): Promise<OpmRuntimeConfig> {
  const ctl = typeof AbortController !== 'undefined' ? new AbortController() : null;
  const timer = ctl ? setTimeout(() => ctl.abort(), timeoutMs) : null;
  try {
    const res = await fetchFn(url, { cache: 'no-store', headers: { Accept: 'application/json' }, signal: ctl?.signal });
    if (!res.ok) return defaults();
    const text = await res.text();
    if (!/^\s*\{/.test(text)) return defaults(); // SPA fallback index.html, empty body …
    return parseRuntimeConfig(JSON.parse(text));
  } catch {
    return defaults();
  } finally {
    if (timer) clearTimeout(timer);
  }
}
