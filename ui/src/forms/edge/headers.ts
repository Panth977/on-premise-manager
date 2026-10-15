/** Header-name suggestions + row helpers of the edge header editors (both GUIs, SPEC §12, §21 #102). */
import type { EdgeResponseHeaders } from '@opm/shared/types';

/** Request headers commonly SENT TO THE CONTAINER (nginx proxy_set_header). No response-only names (round E). */
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
/** SPEC §12.4: the Caddy DNS providers built into the agent */
export const DNS_PROVIDERS = [
  { value: 'cloudflare', label: 'Cloudflare' },
  { value: 'digitalocean', label: 'DigitalOcean' },
  { value: 'duckdns', label: 'Duck DNS' },
  { value: 'godaddy', label: 'GoDaddy' },
  { value: 'route53', label: 'Amazon Route 53' },
  { value: 'googleclouddns', label: 'Google Cloud DNS' },
  { value: 'namecheap', label: 'Namecheap' },
].map((o) => ({ ...o, hint: o.value }));

export type RespRow = { name: string; value: string; mode: 'set' | 'add'; always: boolean };
export function rowsOf(h: EdgeResponseHeaders): RespRow[] {
  const al = (n: string) => h.always.some((x) => x.toLowerCase() === n.toLowerCase());
  return [...Object.entries(h.set).map(([name, value]) => ({ name, value, mode: 'set' as const, always: al(name) })), ...Object.entries(h.add).map(([name, value]) => ({ name, value, mode: 'add' as const, always: al(name) }))];
}
export function headersOf(rows: RespRow[], remove: string[]): EdgeResponseHeaders {
  const used = rows.filter((r) => r.name.trim());
  const set: Record<string, string> = {};
  const add: Record<string, string> = {};
  for (const r of used) (r.mode === 'set' ? set : add)[r.name.trim()] = r.value;
  return { set, add, remove: [...remove], always: used.filter((r) => r.always).map((r) => r.name.trim()) };
}
/** local problems the Record shape cannot hold (duplicate names, bad names, value without a name) */
export function rowErrors(rows: { name: string; value: string }[]): string[] {
  const out: string[] = [];
  const seen = new Set<string>();
  for (const r of rows) {
    const n = r.name.trim();
    if (!n) {
      if (r.value.trim()) out.push('a header row has a value but no name');
      continue;
    }
    if (!/^[A-Za-z0-9!#$%&'*+.^_`|~-]+$/.test(n)) out.push(`"${n}" is not a valid header name`);
    else if (seen.has(n.toLowerCase())) out.push(`header ${n} is listed twice (use one row)`);
    seen.add(n.toLowerCase());
  }
  return out;
}
