/**
 * Minimal OCI / Docker distribution client: token auth (Bearer realm flow with basic credentials, or plain Basic),
 * manifests (index/list → platform pick) and blobs (redirects followed WITHOUT forwarding credentials).
 * Works with Docker Hub, Google Artifact Registry (username _json_key) and generic v2 registries.
 */
import { createHash } from 'node:crypto';
import { Readable } from 'node:stream';
import type { Platform } from '@opm/shared';

export class RegistryError extends Error {
  constructor(
    message: string,
    public readonly status: number | null = null,
  ) {
    super(message);
  }
}
/** The registry gave a token but no pull access to the repository (or answered 401 for the manifest). */
export class RegistryAccessError extends RegistryError {
  readonly kind = 'registry-access';
  constructor(
    message: string,
    public readonly repository: string,
  ) {
    super(message, 401);
  }
}
export interface RegistryCreds {
  url: string;
  username: string;
  password: string;
}
export const ACCEPT_MANIFEST = [
  'application/vnd.oci.image.index.v1+json',
  'application/vnd.docker.distribution.manifest.list.v2+json',
  'application/vnd.oci.image.manifest.v1+json',
  'application/vnd.docker.distribution.manifest.v2+json',
].join(', ');
const INDEX_TYPES = ['application/vnd.oci.image.index.v1+json', 'application/vnd.docker.distribution.manifest.list.v2+json'];

const excerpt = (s: string) => s.replace(/\s+/g, ' ').trim().slice(0, 200);
const TIMEOUT_MS = 30_000;

/** instanceof, or the marker (module instances can differ under tsx in tests). */
export const isRegistryAccessError = (e: unknown): e is RegistryAccessError => e instanceof RegistryAccessError || (e as { kind?: unknown } | null)?.kind === 'registry-access';
/** Docker Hub registry URL (registry-1.docker.io / index.docker.io / docker.io). */
export const isDockerHub = (registryUrl: string) => /^https:\/\/(registry-1\.docker\.io|index\.docker\.io|docker\.io)\/?$/.test(registryUrl);
/** Docker Hub official images live under "library/". */
export function normalizeRepository(registryUrl: string, repository: string): string {
  if (isDockerHub(registryUrl) && !repository.includes('/')) return `library/${repository}`;
  return repository;
}
/** Anonymous registry = username "" and password "" (public images). */
export const isAnonymous = (c: Pick<RegistryCreds, 'username' | 'password'>) => !c.username && !c.password;

/** Payload of a JWT (no signature check), or null when the token is not a JWT. */
export function jwtPayload(token: string): Record<string, unknown> | null {
  const parts = token.split('.');
  if (parts.length !== 3) return null;
  try {
    const j = JSON.parse(Buffer.from(parts[1]!, 'base64url').toString('utf8'));
    return j && typeof j === 'object' && !Array.isArray(j) ? (j as Record<string, unknown>) : null;
  } catch {
    return null;
  }
}
/**
 * Docker distribution tokens (Docker Hub and most token servers) are JWTs with an `access` claim listing what was
 * granted: [{ type: "repository", name, actions: ["pull"] }]. Docker Hub issues a token even when the account cannot
 * pull the repository (unknown repository or no access) — with an empty `access` list. Returns false only when the
 * token is a JWT with an `access` claim that does not grant pull on the repository; null = cannot tell (opaque token).
 */
export function tokenGrantsPull(token: string, repository: string): boolean | null {
  const p = jwtPayload(token);
  if (!p || !('access' in p)) return null;
  const access = Array.isArray(p.access) ? (p.access as { type?: string; name?: string; actions?: unknown }[]) : [];
  return access.some((a) => a && a.type === 'repository' && a.name === repository && Array.isArray(a.actions) && (a.actions.includes('pull') || a.actions.includes('*')));
}
/** The clear message for "token without pull access" / "manifest 401". */
export function noAccessMessage(creds: Pick<RegistryCreds, 'url' | 'username' | 'password'>, repository: string): string {
  const who = isAnonymous(creds) ? 'anonymous access' : `account ${creds.username}`;
  const why = isDockerHub(creds.url) ? 'Docker Hub answers 401 for both' : 'the registry answers 401 for both';
  return `repository ${repository} not found, or ${who} has no pull access to it (${why})`;
}

function parseChallenge(h: string): { scheme: string; params: Record<string, string> } {
  const sp = h.indexOf(' ');
  const scheme = (sp < 0 ? h : h.slice(0, sp)).toLowerCase();
  const params: Record<string, string> = {};
  const re = /([a-zA-Z_]+)="([^"]*)"/g;
  let m: RegExpExecArray | null;
  while ((m = re.exec(h))) params[m[1]!.toLowerCase()] = m[2]!;
  return { scheme, params };
}

export class RegistryClient {
  private tokens = new Map<string, string | null>();
  readonly base: string;
  private readonly timeoutMs: number;
  constructor(
    private readonly creds: RegistryCreds,
    opts: { timeoutMs?: number } = {},
  ) {
    this.base = creds.url.replace(/\/$/, '');
    this.timeoutMs = opts.timeoutMs ?? TIMEOUT_MS;
  }
  /** Basic credentials, or null for an anonymous registry. */
  private basic(): string | null {
    if (isAnonymous(this.creds)) return null;
    return 'Basic ' + Buffer.from(`${this.creds.username}:${this.creds.password}`).toString('base64');
  }
  /**
   * Authorization header for a scope ("repository:<repo>:pull", "registry:catalog:*" or null), or null when the
   * registry is open. For a repository pull scope the JWT `access` claim is checked (checkAccess, default true):
   * a token that does not grant pull → RegistryAccessError.
   */
  async authFor(scope: string | null, checkAccess = true): Promise<string | null> {
    const key = scope ?? '';
    const header = this.tokens.has(key) ? this.tokens.get(key)! : await this.fetchAuth(scope);
    this.tokens.set(key, header);
    const m = scope ? /^repository:(.+):pull$/.exec(scope) : null;
    if (checkAccess && m && header?.startsWith('Bearer ') && tokenGrantsPull(header.slice(7), m[1]!) === false) throw new RegistryAccessError(noAccessMessage(this.creds, m[1]!), m[1]!);
    return header;
  }
  private async fetchAuth(scope: string | null): Promise<string | null> {
    let res: Response;
    try {
      res = await fetch(`${this.base}/v2/`, { signal: AbortSignal.timeout(this.timeoutMs) });
    } catch (e) {
      throw new RegistryError(`registry not reachable: ${(e as Error).message}`);
    }
    await res.arrayBuffer().catch(() => undefined);
    let header: string | null = null;
    if (res.status === 401) {
      const ch = parseChallenge(res.headers.get('www-authenticate') ?? '');
      if (ch.scheme === 'bearer' && ch.params.realm) {
        const u = new URL(ch.params.realm);
        if (ch.params.service) u.searchParams.set('service', ch.params.service);
        if (scope) u.searchParams.set('scope', scope);
        let t: Response;
        const b = this.basic();
        try {
          t = await fetch(u, { headers: b ? { Authorization: b } : {}, signal: AbortSignal.timeout(this.timeoutMs) });
        } catch (e) {
          throw new RegistryError(`token endpoint not reachable: ${(e as Error).message}`);
        }
        const body = await t.text();
        if (!t.ok) throw new RegistryError(`token request failed: HTTP ${t.status} ${excerpt(body)}`, t.status);
        let j: { token?: string; access_token?: string };
        try {
          j = JSON.parse(body);
        } catch {
          throw new RegistryError(`token response is not JSON: ${excerpt(body)}`, t.status);
        }
        const tok = j.token ?? j.access_token;
        if (!tok) throw new RegistryError('token response has no token', t.status);
        header = `Bearer ${tok}`;
      } else header = this.basic();
    } else if (!res.ok) throw new RegistryError(`GET /v2/ answered HTTP ${res.status}`, res.status);
    return header;
  }
  /**
   * Login test (§6.1): token login, then GET /v2/ with it must answer 200. checkAccess: the scope is a repository the
   * caller named → a token without pull access fails the test (accessDenied).
   */
  async test(scope: string | null, checkAccess = false): Promise<{ ok: boolean; message: string; accessDenied?: string }> {
    try {
      const a = await this.authFor(scope, checkAccess);
      const res = await fetch(`${this.base}/v2/`, { headers: a ? { Authorization: a } : {}, signal: AbortSignal.timeout(this.timeoutMs) });
      const body = await res.text();
      if (res.ok) return { ok: true, message: 'login ok' };
      // registries that answer /v2/ with 401 even for valid repository-scoped tokens: the token itself proves the login
      if (res.status === 401 && a?.startsWith('Bearer ') && scope) return { ok: true, message: 'login ok' };
      return { ok: false, message: `HTTP ${res.status} ${excerpt(body)}`.trim() };
    } catch (e) {
      if (isRegistryAccessError(e)) return { ok: false, message: e.message, accessDenied: e.repository };
      return { ok: false, message: (e as Error).message.slice(0, 300) };
    }
  }
  /** GET <base>/v2/<path> with the auth of a scope (no redirects followed; browsing only). */
  async getV2(path: string, scope: string | null): Promise<Response> {
    const a = await this.authFor(scope, false);
    try {
      return await fetch(`${this.base}/v2/${path}`, { headers: a ? { Authorization: a } : {}, signal: AbortSignal.timeout(this.timeoutMs) });
    } catch (e) {
      throw new RegistryError(`GET /v2/${path} failed: ${(e as Error).message}`);
    }
  }
  async get(repo: string, path: string, accept?: string): Promise<Response> {
    const a = await this.authFor(`repository:${repo}:pull`);
    let url = `${this.base}/v2/${repo}/${path}`;
    let headers: Record<string, string> = {};
    if (a) headers.Authorization = a;
    if (accept) headers.Accept = accept;
    for (let hop = 0; hop < 6; hop++) {
      let res: Response;
      try {
        res = await fetch(url, { headers, redirect: 'manual', signal: AbortSignal.timeout(10 * 60_000) });
      } catch (e) {
        throw new RegistryError(`GET ${url} failed: ${(e as Error).message}`);
      }
      if (res.status >= 300 && res.status < 400 && res.headers.get('location')) {
        const next = new URL(res.headers.get('location')!, url);
        await res.arrayBuffer().catch(() => undefined);
        // credentials go only to the registry host itself
        if (next.host !== new URL(this.base).host) headers = accept ? { Accept: accept } : {};
        url = next.toString();
        continue;
      }
      return res;
    }
    throw new RegistryError(`too many redirects for ${path}`);
  }
  async manifest(repo: string, ref: string): Promise<{ bytes: Buffer; mediaType: string; digest: string }> {
    const res = await this.get(repo, `manifests/${ref}`, ACCEPT_MANIFEST);
    const bytes = Buffer.from(await res.arrayBuffer());
    if (res.status === 404) throw new RegistryError(`manifest ${repo}:${ref} not found`, 404);
    if (res.status === 401) throw new RegistryAccessError(`GET manifest ${repo}:${ref}: ${noAccessMessage(this.creds, repo)}`, repo);
    if (!res.ok) throw new RegistryError(`GET manifest ${repo}:${ref}: HTTP ${res.status} ${excerpt(bytes.toString('utf8'))}`, res.status);
    let mediaType = (res.headers.get('content-type') ?? '').split(';')[0]!.trim();
    try {
      const j = JSON.parse(bytes.toString('utf8')) as { mediaType?: string; manifests?: unknown; schemaVersion?: number };
      if (j.mediaType) mediaType = j.mediaType;
      else if (!mediaType || mediaType === 'application/json') mediaType = j.manifests ? 'application/vnd.oci.image.index.v1+json' : 'application/vnd.oci.image.manifest.v1+json';
      if (j.schemaVersion === 1) throw new RegistryError('schema v1 manifests are not supported');
    } catch (e) {
      if (e instanceof RegistryError) throw e;
      throw new RegistryError('manifest is not JSON');
    }
    return { bytes, mediaType, digest: 'sha256:' + createHash('sha256').update(bytes).digest('hex') };
  }
  /** Platform manifest for tag + platform (index/list → pick linux/<arch>). */
  async platformManifest(repo: string, tag: string, platform: Platform): Promise<{ bytes: Buffer; mediaType: string; digest: string; manifest: OciManifest }> {
    const arch = platform.split('/')[1]!;
    let m = await this.manifest(repo, tag);
    if (INDEX_TYPES.includes(m.mediaType)) {
      const idx = JSON.parse(m.bytes.toString('utf8')) as { manifests: { digest: string; mediaType?: string; platform?: { os?: string; architecture?: string; variant?: string } }[] };
      const cands = (idx.manifests ?? []).filter((e) => e.platform?.os === 'linux' && e.platform?.architecture === arch);
      const pick = cands.find((e) => !e.platform?.variant || e.platform.variant === 'v8') ?? cands[0];
      if (!pick) throw new RegistryError(`platform ${platform} not in the image`);
      m = await this.manifest(repo, pick.digest);
      if (m.digest !== pick.digest) throw new RegistryError(`manifest digest mismatch: ${m.digest} ≠ ${pick.digest}`);
      if (INDEX_TYPES.includes(m.mediaType)) throw new RegistryError('nested image index is not supported');
    }
    const manifest = JSON.parse(m.bytes.toString('utf8')) as OciManifest;
    if (!manifest.config?.digest || !Array.isArray(manifest.layers)) throw new RegistryError('manifest has no config / layers');
    return { ...m, manifest };
  }
  async blobBytes(repo: string, digest: string): Promise<Buffer> {
    const res = await this.get(repo, `blobs/${digest}`);
    if (!res.ok) throw new RegistryError(`GET blob ${digest}: HTTP ${res.status}`, res.status);
    return Buffer.from(await res.arrayBuffer());
  }
  async blobStream(repo: string, digest: string): Promise<{ stream: Readable; size: number | null }> {
    const res = await this.get(repo, `blobs/${digest}`);
    if (!res.ok || !res.body) {
      const t = await res.text().catch(() => '');
      throw new RegistryError(`GET blob ${digest}: HTTP ${res.status} ${excerpt(t)}`, res.status);
    }
    const len = res.headers.get('content-length');
    return { stream: Readable.fromWeb(res.body as never), size: len ? Number(len) : null };
  }
}
export interface OciManifest {
  schemaVersion: number;
  mediaType?: string;
  config: { mediaType: string; digest: string; size: number };
  layers: { mediaType: string; digest: string; size: number }[];
}
export interface OciConfig {
  architecture?: string;
  os?: string;
  config?: {
    ExposedPorts?: Record<string, unknown>;
    Env?: string[];
    Entrypoint?: string[] | null;
    Cmd?: string[] | null;
    WorkingDir?: string;
    User?: string;
    Volumes?: Record<string, unknown>;
  };
}
