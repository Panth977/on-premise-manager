/**
 * opm-cloud configuration (§21 #108): an optional JSON file (OPM_CONFIG or --config) overlaid by environment
 * variables. The business code reads process.env through core/env.ts, so the server maps its config onto those names.
 */
import { existsSync, readFileSync, mkdirSync } from 'node:fs';
import path from 'node:path';

export interface OidcConfig {
  issuer: string;
  clientId: string;
  clientSecret: string;
  label: string;
  allowedDomain: string | null;
  redirectUri: string;
}
export interface CloudConfig {
  dataDir: string;
  /** external base URL of this server (agents, install commands, signed blob URLs) */
  publicUrl: string;
  /** console origin (reset links, agent "open in console" links); default publicUrl */
  consoleUrl: string;
  host: string;
  port: number;
  /** built frontend (index.html + _app/…); null = API only */
  staticDir: string | null;
  frontendOrigins: string[];
  /** 302 large downloads to short-lived /_blob token URLs */
  blobRedirect: boolean;
  /** run the scheduled jobs + document triggers in this process */
  runJobs: boolean;
  trustProxy: boolean;
  /** max request body (bytes) */
  bodyLimit: number;
  oidc: OidcConfig | null;
}

type Raw = Partial<Record<string, unknown>>;
function fileConfig(p: string | undefined): Raw {
  if (!p) return {};
  if (!existsSync(p)) throw new Error(`config file ${p} does not exist`);
  return JSON.parse(readFileSync(p, 'utf8')) as Raw;
}
const str = (v: unknown) => (typeof v === 'string' && v.trim() ? v.trim() : undefined);
const bool = (v: unknown, def: boolean) => (v === undefined || v === '' ? def : v === true || v === '1' || v === 'true' || v === 'yes');

export function loadConfig(argv: string[] = process.argv): CloudConfig {
  const ci = argv.indexOf('--config');
  const f = fileConfig(ci >= 0 ? argv[ci + 1] : process.env.OPM_CONFIG);
  const e = process.env;
  const pick = (envName: string, key: string) => str(e[envName]) ?? str(f[key]);
  const port = Number(pick('OPM_PORT', 'port') ?? 8790);
  const publicUrl = (pick('OPM_PUBLIC_URL', 'publicUrl') ?? `http://localhost:${port}`).replace(/\/$/, '');
  const consoleUrl = (pick('OPM_CONSOLE_URL', 'consoleUrl') ?? publicUrl).replace(/\/$/, '');
  const dataDir = path.resolve(pick('OPM_DATA_DIR', 'dataDir') ?? '/var/lib/opm-cloud');
  const staticDir = pick('OPM_STATIC_DIR', 'staticDir') ?? null;
  const origins = new Set<string>([new URL(publicUrl).origin, new URL(consoleUrl).origin]);
  for (const o of (pick('OPM_FRONTEND_ORIGINS', 'frontendOrigins') ?? '').split(',')) if (o.trim()) origins.add(o.trim());
  if (Array.isArray(f.frontendOrigins)) for (const o of f.frontendOrigins) if (typeof o === 'string') origins.add(o);
  const issuer = pick('OPM_OIDC_ISSUER', 'oidcIssuer');
  const oidc: OidcConfig | null = issuer
    ? {
        issuer,
        clientId: pick('OPM_OIDC_CLIENT_ID', 'oidcClientId') ?? '',
        clientSecret: pick('OPM_OIDC_CLIENT_SECRET', 'oidcClientSecret') ?? '',
        label: pick('OPM_OIDC_LABEL', 'oidcLabel') ?? (/accounts\.google\.com/.test(issuer) ? 'Google' : 'single sign-on'),
        allowedDomain: pick('OPM_OIDC_ALLOWED_DOMAIN', 'oidcAllowedDomain') ?? null,
        redirectUri: `${publicUrl}/auth/oidc/callback`,
      }
    : null;
  if (oidc && (!oidc.clientId || !oidc.clientSecret)) throw new Error('OPM_OIDC_ISSUER needs OPM_OIDC_CLIENT_ID and OPM_OIDC_CLIENT_SECRET');
  return {
    dataDir,
    publicUrl,
    consoleUrl,
    host: pick('OPM_HOST', 'host') ?? '0.0.0.0',
    port,
    staticDir: staticDir ? path.resolve(staticDir) : null,
    frontendOrigins: [...origins],
    blobRedirect: bool(e.OPM_BLOB_REDIRECT ?? f.blobRedirect, true),
    runJobs: bool(e.OPM_RUN_JOBS ?? f.runJobs, true),
    trustProxy: bool(e.OPM_TRUST_PROXY ?? f.trustProxy, true),
    bodyLimit: Number(pick('OPM_BODY_LIMIT', 'bodyLimit') ?? 64 * 1024 * 1024),
    oidc,
  };
}

/** what core/env.ts reads — set once, before any handler runs (explicit env values win) */
export function applyEnv(c: CloudConfig) {
  const e = process.env;
  e.OPM_API_BASE ||= c.publicUrl;
  e.OPM_CONSOLE_URL ||= c.consoleUrl;
  e.OPM_FRONTEND_ORIGINS = [...new Set([...(e.OPM_FRONTEND_ORIGINS ?? '').split(',').map((s) => s.trim()).filter(Boolean), ...c.frontendOrigins])].join(',');
  e.OPM_BLOB_REDIRECT = c.blobRedirect ? '1' : '0';
  e.OPM_DATA_DIR = c.dataDir;
  e.OPM_PUBLIC_URL ||= c.publicUrl;
  // the release public key the install script embeds: env, else the data dir's own key (opm-cloud release-key)
  if (!e.OPM_RELEASE_PUBLIC_KEY) {
    const k = path.join(c.dataDir, 'secrets', 'release-key.json');
    if (existsSync(k)) e.OPM_RELEASE_PUBLIC_KEY = (JSON.parse(readFileSync(k, 'utf8')) as { publicKeyB64: string }).publicKeyB64;
  }
  mkdirSync(c.dataDir, { recursive: true });
}
