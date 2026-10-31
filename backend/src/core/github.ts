/**
 * §21 #106 GitHub REST client (fetch; no git binary): repos, user, Git Data API (blobs / trees / commits / refs),
 * contents (first commit of an EMPTY repository — the Git Data API answers 409 "Git Repository is empty" there),
 * rate limits. Errors are GithubError with the HTTP status and, for rate limits, the time to resume.
 * The token is never logged or put into an error message.
 */
import { createHash } from 'node:crypto';

export const GITHUB_API_DEFAULT = 'https://api.github.com';

export class GithubError extends Error {
  constructor(
    message: string,
    public readonly status: number | null,
    /** rate limited (primary or secondary): resume at this time */
    public readonly retryAt: number | null = null,
    /** 401 / 403 without rate limiting: the token or its permissions are wrong (pause the mirror) */
    public readonly auth = false,
  ) {
    super(message);
  }
  get rateLimited() {
    return this.retryAt !== null;
  }
}

export interface RateInfo {
  limit: number;
  remaining: number;
  resetAt: string;
}
export interface GitPerson {
  name: string;
  email: string;
  date: string;
}
export interface TreeEntry {
  path: string;
  mode: '100644' | '100755' | '040000';
  type: 'blob' | 'tree';
  sha: string;
}

/** git object id of a blob: sha1("blob <n>\0" + bytes) — what `git hash-object` prints. */
export function gitBlobSha(bytes: Buffer): string {
  return createHash('sha1').update(`blob ${bytes.length}\0`).update(bytes).digest('hex');
}

export class GithubClient {
  lastRate: RateInfo | null = null;
  constructor(
    private readonly token: string,
    public readonly owner: string,
    public readonly repo: string,
    private readonly base = GITHUB_API_DEFAULT,
    private readonly fetchImpl: typeof fetch = fetch,
  ) {}

  private get r() {
    return `/repos/${encodeURIComponent(this.owner)}/${encodeURIComponent(this.repo)}`;
  }

  async req<T>(method: string, path: string, body?: unknown, opts: { ok404?: boolean; ok409?: boolean } = {}): Promise<{ status: number; json: T | null; headers: Headers }> {
    let res: Response;
    try {
      res = await this.fetchImpl(this.base.replace(/\/$/, '') + path, {
        method,
        headers: {
          Accept: 'application/vnd.github+json',
          Authorization: `Bearer ${this.token}`,
          'X-GitHub-Api-Version': '2022-11-28',
          'User-Agent': 'opm-github-mirror',
          ...(body !== undefined ? { 'Content-Type': 'application/json' } : {}),
        },
        body: body !== undefined ? JSON.stringify(body) : undefined,
        signal: AbortSignal.timeout(120_000),
      });
    } catch (e) {
      throw new GithubError(`GitHub ${method} ${path.split('?')[0]}: ${(e as Error).message}`, null);
    }
    const lim = Number(res.headers.get('x-ratelimit-limit'));
    const rem = Number(res.headers.get('x-ratelimit-remaining'));
    const reset = Number(res.headers.get('x-ratelimit-reset'));
    if (Number.isFinite(lim) && Number.isFinite(rem) && Number.isFinite(reset) && res.headers.get('x-ratelimit-limit') !== null) this.lastRate = { limit: lim, remaining: rem, resetAt: new Date(reset * 1000).toISOString() };
    const text = await res.text();
    let json: T | null = null;
    try {
      json = text ? (JSON.parse(text) as T) : null;
    } catch {
      json = null;
    }
    if (res.ok) return { status: res.status, json, headers: res.headers };
    if (res.status === 404 && opts.ok404) return { status: 404, json, headers: res.headers };
    if (res.status === 409 && opts.ok409) return { status: 409, json, headers: res.headers };
    const msg = (json as { message?: string } | null)?.message ?? text.slice(0, 200);
    // rate limits: 429, or 403 with remaining 0 / retry-after / "secondary rate limit"
    const retryAfter = Number(res.headers.get('retry-after'));
    const secondary = /secondary rate limit|abuse/i.test(msg);
    if (res.status === 429 || (res.status === 403 && (rem === 0 || Number.isFinite(retryAfter) && res.headers.get('retry-after') !== null || secondary))) {
      let at = Date.now() + 60_000;
      if (res.headers.get('retry-after') !== null && Number.isFinite(retryAfter)) at = Date.now() + retryAfter * 1000;
      else if (rem === 0 && Number.isFinite(reset)) at = reset * 1000 + 1000;
      throw new GithubError(`GitHub rate limit (${res.status}): ${msg}`, res.status, at);
    }
    throw new GithubError(`GitHub ${method} ${path.split('?')[0]} → ${res.status}: ${msg}`, res.status, null, res.status === 401 || res.status === 403);
  }

  // ── repo / user
  async getRepo() {
    const r = await this.req<{ full_name: string; private: boolean; default_branch: string | null; html_url: string; size: number; permissions?: { push?: boolean; pull?: boolean; admin?: boolean } }>('GET', this.r, undefined, { ok404: true });
    return r.status === 404 ? null : r.json!;
  }
  async getUser(): Promise<string | null> {
    try {
      const r = await this.req<{ login: string }>('GET', '/user');
      return r.json?.login ?? null;
    } catch (e) {
      if (e instanceof GithubError && (e.status === 403 || e.status === 404)) return null; // fine-grained tokens may not read /user
      throw e;
    }
  }
  async rateLimit(): Promise<RateInfo | null> {
    const r = await this.req<{ resources?: { core?: { limit: number; remaining: number; reset: number } } }>('GET', '/rate_limit');
    const c = r.json?.resources?.core;
    return c ? { limit: c.limit, remaining: c.remaining, resetAt: new Date(c.reset * 1000).toISOString() } : this.lastRate;
  }
  /** true when the repository has no commit (the Git Data API refuses it) */
  async isEmpty(): Promise<boolean> {
    const r = await this.req<unknown>('GET', `${this.r}/git/matching-refs/heads/`, undefined, { ok409: true, ok404: true });
    if (r.status === 409) return true;
    return Array.isArray(r.json) && r.json.length === 0;
  }
  /** first commit of an empty repository (Contents API) */
  async putContents(path: string, content: Buffer, message: string, branch: string, person?: Omit<GitPerson, 'date'>) {
    await this.req('PUT', `${this.r}/contents/${path.split('/').map(encodeURIComponent).join('/')}`, { message, content: content.toString('base64'), branch, ...(person ? { committer: person, author: person } : {}) });
  }

  // ── git data
  async getBlobExists(sha: string): Promise<boolean> {
    const r = await this.req('GET', `${this.r}/git/blobs/${sha}`, undefined, { ok404: true });
    return r.status !== 404;
  }
  async createBlob(bytes: Buffer): Promise<string> {
    const r = await this.req<{ sha: string }>('POST', `${this.r}/git/blobs`, { content: bytes.toString('base64'), encoding: 'base64' });
    return r.json!.sha;
  }
  async createTree(entries: TreeEntry[]): Promise<string> {
    const r = await this.req<{ sha: string }>('POST', `${this.r}/git/trees`, { tree: entries });
    return r.json!.sha;
  }
  async createCommit(c: { message: string; tree: string; parents: string[]; author: GitPerson; committer: GitPerson }): Promise<string> {
    const r = await this.req<{ sha: string }>('POST', `${this.r}/git/commits`, c);
    return r.json!.sha;
  }
  async getCommit(sha: string): Promise<{ sha: string; message: string; tree: { sha: string }; parents: { sha: string }[] } | null> {
    const r = await this.req<{ sha: string; message: string; tree: { sha: string }; parents: { sha: string }[] }>('GET', `${this.r}/git/commits/${sha}`, undefined, { ok404: true });
    return r.status === 404 ? null : r.json;
  }
  /** head sha of a branch (null = no such branch) */
  async getRef(branch: string): Promise<string | null> {
    const r = await this.req<{ object?: { sha: string }; ref?: string } | unknown[]>('GET', `${this.r}/git/ref/heads/${branch.split('/').map(encodeURIComponent).join('/')}`, undefined, { ok404: true, ok409: true });
    if (r.status === 404 || r.status === 409) return null;
    const j = r.json as { object?: { sha: string } } | null;
    return j?.object?.sha ?? null;
  }
  async createRef(branch: string, sha: string) {
    await this.req('POST', `${this.r}/git/refs`, { ref: `refs/heads/${branch}`, sha });
  }
  async updateRef(branch: string, sha: string, force: boolean) {
    await this.req('PATCH', `${this.r}/git/refs/heads/${branch.split('/').map(encodeURIComponent).join('/')}`, { sha, force });
  }
  async deleteRef(branch: string): Promise<boolean> {
    try {
      const r = await this.req('DELETE', `${this.r}/git/refs/heads/${branch.split('/').map(encodeURIComponent).join('/')}`, undefined, { ok404: true, ok409: true });
      return r.status !== 404 && r.status !== 409;
    } catch (e) {
      if (e instanceof GithubError && e.status === 422) return false; // "Reference does not exist"
      throw e;
    }
  }
  /** create or move a branch to sha (force: allow a non-fast-forward move) */
  async setRef(branch: string, sha: string, force: boolean) {
    const cur = await this.getRef(branch);
    if (cur === sha) return;
    if (cur === null) await this.createRef(branch, sha);
    else await this.updateRef(branch, sha, force);
  }
}
