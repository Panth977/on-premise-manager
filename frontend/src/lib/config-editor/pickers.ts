/** Option lists for the config-editor pickers (Combobox / MultiSelect) — values come from the scope or the catalog. */
import type { Config, ImageEnv } from '@opm/shared';
import { normalizeImageEnv } from '@opm/shared';
import type { ComboOption } from '../ui/Combobox.svelte';
import type { MsOption } from '../ui/MultiSelect.svelte';
import { catalog } from '../stores/catalog.svelte';

/** a service as an option: icon + label + slug */
export function serviceOption(serviceSlug: string, extra: Partial<ComboOption> = {}): ComboOption {
  const s = catalog.service(serviceSlug);
  return { value: serviceSlug, label: s?.label || serviceSlug, sub: serviceSlug, icon: s?.icon ?? null, ...extra };
}
export function serviceMsOption(serviceSlug: string, hint?: string): MsOption {
  const s = catalog.service(serviceSlug);
  return { value: serviceSlug, label: serviceSlug, hint: hint ?? s?.label, icon: s?.icon ?? null };
}
export function secretOptions(config: Pick<Config, 'secrets'>, secretsSet?: readonly string[]): ComboOption[] {
  return config.secrets.map((s) => ({ value: s.secretSlug, label: s.secretSlug, sub: s.label, badge: secretsSet ? (secretsSet.includes(s.secretSlug) ? 'set' : 'not set') : undefined }));
}
export function fileOptions(config: Pick<Config, 'files'>): ComboOption[] {
  return config.files.map((f) => ({ value: f.fileSlug, label: f.fileSlug, sub: f.label, detail: f.mime }));
}
/** one line describing an image env: required · secret · choice · default */
export function envSummary(raw: ImageEnv | undefined | null): string {
  if (!raw) return 'not an image env';
  const ie = normalizeImageEnv(raw);
  const parts: string[] = [];
  if (ie.required) parts.push('required');
  if (ie.secret) parts.push('secret');
  if (ie.options) parts.push(`one of ${ie.options.length}`);
  if (ie.value !== undefined) parts.push(`default ${ie.value === '' ? '""' : ie.value}`);
  return parts.join(' · ') || 'optional';
}

/** SPEC §12.4: the Caddy DNS providers built into the agent */
export const DNS_PROVIDERS: ComboOption[] = [
  { value: 'cloudflare', label: 'Cloudflare' },
  { value: 'digitalocean', label: 'DigitalOcean' },
  { value: 'duckdns', label: 'Duck DNS' },
  { value: 'godaddy', label: 'GoDaddy' },
  { value: 'route53', label: 'Amazon Route 53' },
  { value: 'googleclouddns', label: 'Google Cloud DNS' },
  { value: 'namecheap', label: 'Namecheap' },
].map((o) => ({ ...o, sub: o.value }));

/** header names offered in the route drawer (free text allowed) */
/** Request headers commonly SENT TO THE CONTAINER (nginx proxy_set_header). No response-only names (Cache-Control,
 *  CSP, …): offering them here is how the round-E user mistake happened. */
export const REQUEST_HEADERS = ['X-Forwarded-Prefix', 'X-Script-Name', 'X-Request-Id', 'X-Opm-Site', 'X-Opm-Deployment', 'Host', 'Authorization', 'X-Real-IP', 'X-Forwarded-Host', 'X-Forwarded-Proto'];
/** Response headers commonly set FOR THE BROWSER (nginx add_header / proxy_hide_header). */
export const RESPONSE_HEADERS = [
  'Cache-Control',
  'Pragma',
  'Expires',
  'Content-Security-Policy',
  'Strict-Transport-Security',
  'X-Frame-Options',
  'X-Content-Type-Options',
  'Referrer-Policy',
  'Permissions-Policy',
  'Access-Control-Allow-Origin',
  'Access-Control-Allow-Methods',
  'Access-Control-Allow-Headers',
  'Access-Control-Allow-Credentials',
  'Access-Control-Expose-Headers',
  'Access-Control-Max-Age',
  'Set-Cookie',
  'Server',
  'X-Powered-By',
];
/** @deprecated round E: use REQUEST_HEADERS / RESPONSE_HEADERS */
export const COMMON_HEADERS = REQUEST_HEADERS;
