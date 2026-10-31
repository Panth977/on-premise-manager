/**
 * Registry browsing for the register-image pickers (GET /v1/registries/{id}/repositories | /tags; not audited):
 *   Docker Hub        → hub.docker.com API (JWT from /v2/users/login; own namespace + orgs; search; tags with dates)
 *   Artifact Registry → artifactregistry.googleapis.com with an OAuth token from the _json_key SA key (RS256 JWT bearer)
 *   other registries  → Docker v2 /v2/_catalog + /v2/<repo>/tags/list (RegistryClient token flow)
 * Nothing is cached persistently; every remote call has a ≤ 10 s timeout. Failures → supported:false + message.
 * Also: the "accessible repositories" hint appended to RegistryAccessError messages.
 */
import { createSign } from 'node:crypto';
import type { RegistryRepositoriesResponse, RegistryRepository, RegistryTag, RegistryTagsResponse } from '@opm/shared';
import { isAnonymous, isDockerHub, isRegistryAccessError, RegistryAccessError, RegistryClient, RegistryError, type RegistryCreds } from './registry.js';

const T = 10_000;
export interface BrowseOpts {
  /** Docker Hub API base (tests point it at a stub) */
  hubApi?: string;
  /** Artifact Registry API base (tests) */
  arApi?: string;
}
const HUB_API = 'https://hub.docker.com';
const AR_API = 'https://artifactregistry.googleapis.com';
const POPULAR = ['library/redis', 'library/postgres', 'library/nginx', 'library/node', 'library/alpine', 'library/busybox', 'library/mongo', 'library/mysql', 'library/python', 'library/traefik'];

const errMsg = (e: unknown) => ((e as Error)?.message ?? String(e)).slice(0, 300);
const matches = (name: string, q: string) => !q || name.toLowerCase().includes(q.toLowerCase());
const iso = (v: unknown): string | null => (typeof v === 'string' && !Number.isNaN(Date.parse(v)) ? new Date(v).toISOString() : null);
const num = (v: unknown): number | null => (typeof v === 'number' && Number.isFinite(v) ? v : null);

async function getJson<R>(url: string, headers: Record<string, string> = {}, init: { method?: string; body?: string } = {}): Promise<R> {
  let res: Response;
  try {
    res = await fetch(url, { method: init.method ?? 'GET', headers, body: init.body, signal: AbortSignal.timeout(T) });
  } catch (e) {
    throw new RegistryError(`${new URL(url).host} not reachable: ${errMsg(e)}`);
  }
  const text = await res.text();
  if (!res.ok) throw new RegistryError(`${init.method ?? 'GET'} ${new URL(url).pathname}: HTTP ${res.status} ${text.replace(/\s+/g, ' ').slice(0, 160)}`.trim(), res.status);
  try {
    return JSON.parse(text) as R;
  } catch {
    throw new RegistryError(`${new URL(url).pathname}: answer is not JSON`);
  }
}
function repo(name: string, o: Partial<RegistryRepository> = {}): RegistryRepository {
  return { name, description: o.description ?? null, official: o.official ?? false, stars: o.stars ?? null, pullCount: o.pullCount ?? null, updatedAt: o.updatedAt ?? null };
}
function sortTags(items: RegistryTag[]): RegistryTag[] {
  return items.sort((a, b) => (a.pushedAt && b.pushedAt ? b.pushedAt.localeCompare(a.pushedAt) : a.pushedAt ? -1 : b.pushedAt ? 1 : a.tag.localeCompare(b.tag)));
}

// ───────────────────────────────────────── Docker Hub
interface HubRepo {
  name?: string;
  namespace?: string;
  description?: string | null;
  star_count?: number;
  pull_count?: number;
  last_updated?: string | null;
}
/** Docker Hub JWT for the account (password or personal access token). */
export async function hubLogin(creds: RegistryCreds, o: BrowseOpts = {}): Promise<string> {
  const api = o.hubApi ?? HUB_API;
  try {
    const j = await getJson<{ token?: string }>(`${api}/v2/users/login`, { 'Content-Type': 'application/json' }, { method: 'POST', body: JSON.stringify({ username: creds.username, password: creds.password }) });
    if (j.token) return j.token;
  } catch (e) {
    // newer endpoint (PATs / organization access tokens)
    try {
      const j = await getJson<{ access_token?: string }>(`${api}/v2/auth/token`, { 'Content-Type': 'application/json' }, { method: 'POST', body: JSON.stringify({ identifier: creds.username, secret: creds.password }) });
      if (j.access_token) return j.access_token;
    } catch {
      /* report the first error */
    }
    throw new RegistryError(`Docker Hub login failed: ${errMsg(e)}`);
  }
  throw new RegistryError('Docker Hub login answered no token');
}
async function hubNamespaceRepos(api: string, jwt: string, ns: string): Promise<RegistryRepository[]> {
  const h = { Authorization: `Bearer ${jwt}` };
  let j: { results?: HubRepo[] };
  try {
    j = await getJson(`${api}/v2/namespaces/${encodeURIComponent(ns)}/repositories?page_size=100`, h);
  } catch {
    j = await getJson(`${api}/v2/repositories/${encodeURIComponent(ns)}/?page_size=100`, h);
  }
  return (j.results ?? []).filter((r) => r.name).map((r) => repo(`${r.namespace ?? ns}/${r.name}`, { description: r.description || null, stars: num(r.star_count), pullCount: num(r.pull_count), updatedAt: iso(r.last_updated) }));
}
/** Repositories the account can see: its namespace + (best effort) its organizations. */
export async function hubAccountRepos(creds: RegistryCreds, o: BrowseOpts = {}, jwt?: string): Promise<RegistryRepository[]> {
  const api = o.hubApi ?? HUB_API;
  const token = jwt ?? (await hubLogin(creds, o));
  const out = await hubNamespaceRepos(api, token, creds.username.toLowerCase());
  try {
    const orgs = await getJson<{ results?: { orgname?: string }[] }>(`${api}/v2/user/orgs/?page_size=25`, { Authorization: `Bearer ${token}` });
    const names = (orgs.results ?? []).map((x) => x.orgname).filter((x): x is string => !!x && x.toLowerCase() !== creds.username.toLowerCase()).slice(0, 5);
    const lists = await Promise.all(names.map((n) => hubNamespaceRepos(api, token, n).catch(() => [])));
    for (const l of lists) for (const r of l) if (!out.some((x) => x.name === r.name)) out.push(r);
  } catch {
    /* orgs are optional */
  }
  return out;
}
async function hubSearch(api: string, q: string): Promise<RegistryRepository[]> {
  const j = await getJson<{ results?: { repo_name?: string; short_description?: string; star_count?: number; pull_count?: number; is_official?: boolean }[] }>(`${api}/v2/search/repositories/?query=${encodeURIComponent(q)}&page_size=25`);
  return (j.results ?? [])
    .filter((r) => r.repo_name)
    .map((r) => {
      const name = r.repo_name!.includes('/') ? r.repo_name! : `library/${r.repo_name}`;
      return repo(name, { description: r.short_description || null, official: !!r.is_official || name.startsWith('library/'), stars: num(r.star_count), pullCount: num(r.pull_count) });
    });
}
async function hubRepositories(creds: RegistryCreds, q: string, o: BrowseOpts): Promise<RegistryRepositoriesResponse> {
  const api = o.hubApi ?? HUB_API;
  const items: RegistryRepository[] = [];
  const messages: string[] = [];
  if (!isAnonymous(creds)) {
    try {
      for (const r of await hubAccountRepos(creds, o)) if (matches(r.name, q)) items.push(r);
    } catch (e) {
      messages.push(errMsg(e));
    }
  }
  if (q) {
    try {
      // the account's own repositories first, then search hits with official images (library/…) ahead of the rest
      const found = (await hubSearch(api, q)).filter((r) => !items.some((x) => x.name === r.name));
      found.sort((a, b) => Number(b.official) - Number(a.official));
      items.push(...found);
    } catch (e) {
      messages.push(`search: ${errMsg(e)}`);
    }
  } else if (isAnonymous(creds)) for (const n of POPULAR) items.push(repo(n, { official: true }));
  if (!items.length && messages.length) return { items: [], source: 'docker-hub', supported: false, message: messages.join('; ') };
  return { items, source: 'docker-hub', supported: true, message: messages.length ? messages.join('; ') : null };
}
async function hubTags(creds: RegistryCreds, repository: string, q: string, o: BrowseOpts): Promise<RegistryTagsResponse> {
  const api = o.hubApi ?? HUB_API;
  const [ns, ...rest] = repository.split('/');
  const name = rest.join('/');
  const h: Record<string, string> = {};
  if (!isAnonymous(creds)) h.Authorization = `Bearer ${await hubLogin(creds, o)}`;
  type HubTag = { name?: string; last_updated?: string | null; tag_last_pushed?: string | null; digest?: string | null; full_size?: number | null; images?: { digest?: string; size?: number }[] };
  let j: { results?: HubTag[] };
  try {
    j = await getJson(`${api}/v2/namespaces/${encodeURIComponent(ns!)}/repositories/${encodeURIComponent(name)}/tags?page_size=100&ordering=last_updated`, h);
  } catch {
    j = await getJson(`${api}/v2/repositories/${encodeURIComponent(ns!)}/${encodeURIComponent(name)}/tags/?page_size=100&ordering=last_updated`, h);
  }
  const items = (j.results ?? [])
    .filter((t) => t.name && matches(t.name, q))
    .map((t) => {
      const digest = t.digest ?? null;
      return { tag: t.name!, pushedAt: iso(t.tag_last_pushed) ?? iso(t.last_updated), digest: digest && /^sha256:[0-9a-f]{64}$/.test(digest) ? digest : null, sizeBytes: num(t.full_size) };
    });
  return { items: sortTags(items), source: 'docker-hub', supported: true, message: null };
}

// ───────────────────────────────────────── Artifact Registry
interface SaKey {
  client_email: string;
  private_key: string;
  project_id?: string;
  token_uri?: string;
}
/** host "<location>-docker.pkg.dev" → location; null when not Artifact Registry. */
export function arLocation(url: string): string | null {
  const m = /^https:\/\/([a-z0-9-]+)-docker\.pkg\.dev\/?$/.exec(url);
  return m ? m[1]! : null;
}
/** OAuth access token (scope cloud-platform) from a service-account key via the JWT bearer grant (RS256, node:crypto). */
export async function googleAccessToken(keyJson: string): Promise<{ token: string; projectId: string | null }> {
  let k: SaKey;
  try {
    k = JSON.parse(keyJson) as SaKey;
  } catch {
    throw new RegistryError('the _json_key password is not a service-account key JSON');
  }
  if (!k.client_email || !k.private_key) throw new RegistryError('the service-account key has no client_email / private_key');
  const aud = k.token_uri ?? 'https://oauth2.googleapis.com/token';
  const now = Math.floor(Date.now() / 1000);
  const enc = (o: unknown) => Buffer.from(JSON.stringify(o)).toString('base64url');
  const unsigned = `${enc({ alg: 'RS256', typ: 'JWT' })}.${enc({ iss: k.client_email, scope: 'https://www.googleapis.com/auth/cloud-platform', aud, iat: now, exp: now + 3600 })}`;
  let sig: string;
  try {
    sig = createSign('RSA-SHA256').update(unsigned).sign(k.private_key).toString('base64url');
  } catch (e) {
    throw new RegistryError(`cannot sign with the service-account key: ${errMsg(e)}`);
  }
  const body = new URLSearchParams({ grant_type: 'urn:ietf:params:oauth:grant-type:jwt-bearer', assertion: `${unsigned}.${sig}` }).toString();
  const j = await getJson<{ access_token?: string }>(aud, { 'Content-Type': 'application/x-www-form-urlencoded' }, { method: 'POST', body });
  if (!j.access_token) throw new RegistryError('Google token endpoint answered no access_token');
  return { token: j.access_token, projectId: k.project_id ?? null };
}
async function arRepositories(creds: RegistryCreds, loc: string, q: string, o: BrowseOpts): Promise<RegistryRepositoriesResponse> {
  const api = o.arApi ?? AR_API;
  const { token, projectId } = await googleAccessToken(creds.password);
  if (!projectId) throw new RegistryError('the service-account key has no project_id');
  const h = { Authorization: `Bearer ${token}` };
  const repos = await getJson<{ repositories?: { name: string; format?: string; description?: string; updateTime?: string }[] }>(`${api}/v1/projects/${projectId}/locations/${loc}/repositories?pageSize=100`, h);
  const docker = (repos.repositories ?? []).filter((r) => !r.format || r.format === 'DOCKER').slice(0, 20);
  const lists = await Promise.all(
    docker.map(async (r) => {
      const rid = r.name.split('/').pop()!;
      const p = await getJson<{ packages?: { name: string; displayName?: string; updateTime?: string }[] }>(`${api}/v1/${r.name}/packages?pageSize=200`, h).catch(() => ({ packages: [] }));
      return (p.packages ?? []).map((pk) => repo(`${projectId}/${rid}/${decodeURIComponent(pk.name.split('/').pop()!)}`, { description: r.description || null, updatedAt: iso(pk.updateTime) }));
    }),
  );
  const items = lists.flat().filter((r) => matches(r.name, q));
  return { items, source: 'artifact-registry', supported: true, message: null };
}
async function arTags(creds: RegistryCreds, loc: string, repository: string, q: string, o: BrowseOpts): Promise<RegistryTagsResponse> {
  const api = o.arApi ?? AR_API;
  const [project, rid, ...pkgParts] = repository.split('/');
  if (!project || !rid || !pkgParts.length) throw new RegistryError(`Artifact Registry repositories look like <project>/<repository>/<image> (got ${repository})`);
  const { token } = await googleAccessToken(creds.password);
  const h = { Authorization: `Bearer ${token}` };
  const pkg = `${api}/v1/projects/${project}/locations/${loc}/repositories/${rid}/packages/${encodeURIComponent(pkgParts.join('/'))}`;
  const [tags, versions] = await Promise.all([
    getJson<{ tags?: { name: string; version?: string }[] }>(`${pkg}/tags?pageSize=500`, h),
    getJson<{ versions?: { name: string; createTime?: string; updateTime?: string }[] }>(`${pkg}/versions?pageSize=500`, h).catch(() => ({ versions: [] })),
  ]);
  const when = new Map((versions.versions ?? []).map((v) => [decodeURIComponent(v.name.split('/').pop()!), iso(v.updateTime) ?? iso(v.createTime)]));
  const items = (tags.tags ?? [])
    .map((t) => {
      const digest = t.version ? decodeURIComponent(t.version.split('/').pop()!) : null;
      return { tag: decodeURIComponent(t.name.split('/').pop()!), pushedAt: digest ? (when.get(digest) ?? null) : null, digest: digest && /^sha256:[0-9a-f]{64}$/.test(digest) ? digest : null, sizeBytes: null };
    })
    .filter((t) => matches(t.tag, q));
  return { items: sortTags(items), source: 'artifact-registry', supported: true, message: null };
}

// ───────────────────────────────────────── generic Docker v2
export async function v2Catalog(creds: RegistryCreds, q = ''): Promise<RegistryRepositoriesResponse> {
  const cl = new RegistryClient(creds, { timeoutMs: T });
  const res = await cl.getV2('_catalog?n=200', 'registry:catalog:*');
  const text = await res.text();
  if (!res.ok) throw new RegistryError(`GET /v2/_catalog: HTTP ${res.status} ${text.replace(/\s+/g, ' ').slice(0, 160)}`.trim(), res.status);
  const j = JSON.parse(text) as { repositories?: string[] };
  return { items: (j.repositories ?? []).filter((n) => matches(n, q)).map((n) => repo(n)), source: 'catalog', supported: true, message: null };
}
export async function v2Tags(creds: RegistryCreds, repository: string, q = ''): Promise<RegistryTagsResponse> {
  const cl = new RegistryClient(creds, { timeoutMs: T });
  const res = await cl.getV2(`${repository}/tags/list?n=500`, `repository:${repository}:pull`);
  const text = await res.text();
  if (!res.ok) throw new RegistryError(`GET /v2/${repository}/tags/list: HTTP ${res.status} ${text.replace(/\s+/g, ' ').slice(0, 160)}`.trim(), res.status);
  const j = JSON.parse(text) as { tags?: string[] | null };
  const items = (j.tags ?? []).filter((t) => matches(t, q)).map((tag) => ({ tag, pushedAt: null, digest: null, sizeBytes: null }));
  return { items: sortTags(items), source: 'registry-v2', supported: true, message: null };
}

// ───────────────────────────────────────── entry points (never throw: supported:false + message)
export async function listRepositories(creds: RegistryCreds, q = '', o: BrowseOpts = {}): Promise<RegistryRepositoriesResponse> {
  try {
    if (isDockerHub(creds.url)) return await hubRepositories(creds, q, o);
    const loc = arLocation(creds.url);
    if (loc && creds.username === '_json_key') {
      try {
        return await arRepositories(creds, loc, q, o);
      } catch (e) {
        return { items: [], source: 'artifact-registry', supported: false, message: errMsg(e) };
      }
    }
    return await v2Catalog(creds, q);
  } catch (e) {
    return { items: [], source: 'none', supported: false, message: `this registry cannot list repositories (${errMsg(e)}); type the repository name` };
  }
}
export async function listTags(creds: RegistryCreds, repository: string, q = '', o: BrowseOpts = {}): Promise<RegistryTagsResponse> {
  const first: string[] = [];
  try {
    if (isDockerHub(creds.url)) return await hubTags(creds, repository, q, o);
    const loc = arLocation(creds.url);
    if (loc && creds.username === '_json_key') return await arTags(creds, loc, repository, q, o);
  } catch (e) {
    first.push(errMsg(e));
  }
  try {
    const r = await v2Tags(creds, repository, q);
    return first.length ? { ...r, message: first.join('; ') } : r;
  } catch (e) {
    return { items: [], source: 'none', supported: false, message: [...first, errMsg(e)].join('; ') };
  }
}

/** Up to 10 repositories the account can pull (Docker Hub namespace/orgs, else v2 _catalog); [] when unknown. */
export async function accessibleRepositories(creds: RegistryCreds, o: BrowseOpts = {}): Promise<string[]> {
  try {
    if (isDockerHub(creds.url)) return isAnonymous(creds) ? [] : (await hubAccountRepos(creds, o)).map((r) => r.name).slice(0, 10);
    const r = await listRepositories(creds, '', o);
    return r.supported ? r.items.map((x) => x.name).slice(0, 10) : [];
  } catch {
    return [];
  }
}
/** Append "accessible repositories: …" to a RegistryAccessError message (when the listing works). */
export async function withAccessHint<R>(creds: RegistryCreds, fn: () => Promise<R>, o: BrowseOpts = {}): Promise<R> {
  try {
    return await fn();
  } catch (e) {
    if (isRegistryAccessError(e)) throw new RegistryAccessError(await accessHint(creds, e.message, o), e.repository);
    throw e;
  }
}
export async function accessHint(creds: RegistryCreds, message: string, o: BrowseOpts = {}): Promise<string> {
  const list = await accessibleRepositories(creds, o);
  if (!list.length) return message;
  const who = isAnonymous(creds) ? 'this registry' : `account ${creds.username}`;
  return `${message}; repositories ${who} can access: ${list.join(', ')}`;
}
