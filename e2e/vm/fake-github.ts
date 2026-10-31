/**
 * §21 #106 a fake GitHub (local HTTP server) for the mirror tests: repos, user, rate_limit, Git Data API (blobs /
 * trees / commits / refs) and contents PUT. Objects are REAL git objects (sha1 like git: blob / tree / commit), so a
 * pushed tree can be compared with `git write-tree` and a branch can be checked out into files.
 * Behaviour copied from GitHub where it matters: an EMPTY repository answers 409 "Git Repository is empty." to the
 * Git Data API (only the Contents API can create the first commit); non-fast-forward ref updates without force → 422;
 * rate limits (403 + x-ratelimit-remaining 0 / 429 + retry-after); permissions (no write → 403; unknown token → 401).
 */
import { createServer, type IncomingMessage, type Server, type ServerResponse } from 'node:http';
import { createHash } from 'node:crypto';

export interface FakeRepo {
  owner: string;
  name: string;
  private: boolean;
  objects: Map<string, { type: 'blob' | 'tree' | 'commit'; body: Buffer }>;
  refs: Map<string, string>; // "heads/<branch>" → sha
  defaultBranch: string | null;
}
export interface FakeToken {
  login: string;
  /** repos this token may see ("owner/name"); "*" = all */
  repos: string[] | '*';
  write: boolean;
  /** fine-grained tokens without user read: GET /user → 403 */
  noUser?: boolean;
}
type Fault = { match: RegExp; method?: string; status: number; times: number; headers?: Record<string, string>; body?: string };

const sha1 = (type: string, body: Buffer) => createHash('sha1').update(`${type} ${body.length}\0`).update(body).digest('hex');

export class FakeGithub {
  server: Server | null = null;
  base = '';
  repos = new Map<string, FakeRepo>();
  tokens = new Map<string, FakeToken>();
  faults: Fault[] = [];
  /** count of requests by "METHOD /pattern" kind (blobs, trees, commits, refs …) */
  calls: { method: string; path: string; status: number }[] = [];
  rate = { limit: 5000, remaining: 5000 };

  addRepo(owner: string, name: string, opts: { private?: boolean } = {}): FakeRepo {
    const r: FakeRepo = { owner, name, private: opts.private ?? true, objects: new Map(), refs: new Map(), defaultBranch: null };
    this.repos.set(`${owner}/${name}`.toLowerCase(), r);
    return r;
  }
  addToken(token: string, t: FakeToken) {
    this.tokens.set(token, t);
  }
  /** fail the next `times` requests matching path regex (+ method) with status */
  fail(match: RegExp, status: number, times = 1, opts: { method?: string; headers?: Record<string, string>; body?: string } = {}) {
    this.faults.push({ match, status, times, ...opts });
  }
  repo(owner: string, name: string): FakeRepo {
    return this.repos.get(`${owner}/${name}`.toLowerCase())!;
  }

  async start(): Promise<string> {
    this.server = createServer((req, res) => void this.handle(req, res).catch((e) => this.send(res, 500, { message: String(e?.stack ?? e) })));
    await new Promise<void>((r) => this.server!.listen(0, '127.0.0.1', () => r()));
    const a = this.server.address() as { port: number };
    this.base = `http://127.0.0.1:${a.port}`;
    return this.base;
  }
  async stop() {
    await new Promise<void>((r) => (this.server ? this.server.close(() => r()) : r()));
  }

  // ── object helpers (also used by the tests)
  put(r: FakeRepo, type: 'blob' | 'tree' | 'commit', body: Buffer): string {
    const sha = sha1(type, body);
    r.objects.set(sha, { type, body });
    return sha;
  }
  /** files of a commit / branch: path → bytes (+ mode) */
  checkout(r: FakeRepo, branchOrSha: string): Map<string, { bytes: Buffer; mode: string }> {
    const sha = r.refs.get(`heads/${branchOrSha}`) ?? branchOrSha;
    const c = this.commit(r, sha);
    const out = new Map<string, { bytes: Buffer; mode: string }>();
    const walk = (tsha: string, prefix: string) => {
      for (const e of this.readTree(r, tsha)) {
        if (e.mode === '40000') walk(e.sha, `${prefix}${e.name}/`);
        else out.set(`${prefix}${e.name}`, { bytes: r.objects.get(e.sha)!.body, mode: e.mode });
      }
    };
    walk(c.tree, '');
    return out;
  }
  readTree(r: FakeRepo, sha: string): { mode: string; name: string; sha: string }[] {
    const o = r.objects.get(sha);
    if (!o || o.type !== 'tree') throw new Error(`no tree ${sha}`);
    const out: { mode: string; name: string; sha: string }[] = [];
    let i = 0;
    const b = o.body;
    while (i < b.length) {
      const sp = b.indexOf(0x20, i);
      const nul = b.indexOf(0, sp);
      out.push({ mode: b.subarray(i, sp).toString(), name: b.subarray(sp + 1, nul).toString('utf8'), sha: b.subarray(nul + 1, nul + 21).toString('hex') });
      i = nul + 21;
    }
    return out;
  }
  commit(r: FakeRepo, sha: string): { sha: string; tree: string; parents: string[]; message: string; author: string; committer: string } {
    const o = r.objects.get(sha);
    if (!o || o.type !== 'commit') throw new Error(`no commit ${sha}`);
    const text = o.body.toString('utf8');
    const i = text.indexOf('\n\n');
    const head = text.slice(0, i).split('\n');
    return {
      sha,
      tree: head.find((l) => l.startsWith('tree '))!.slice(5),
      parents: head.filter((l) => l.startsWith('parent ')).map((l) => l.slice(7)),
      author: head.find((l) => l.startsWith('author '))!.slice(7),
      committer: head.find((l) => l.startsWith('committer '))!.slice(10),
      message: text.slice(i + 2),
    };
  }
  /** commits of a branch, newest first */
  log(r: FakeRepo, branch: string) {
    const out: ReturnType<FakeGithub['commit']>[] = [];
    let sha: string | undefined = r.refs.get(`heads/${branch}`);
    while (sha) {
      const c = this.commit(r, sha);
      out.push(c);
      sha = c.parents[0];
    }
    return out;
  }
  private isAncestor(r: FakeRepo, anc: string, of: string): boolean {
    const seen = new Set<string>();
    const stack = [of];
    while (stack.length) {
      const s = stack.pop()!;
      if (s === anc) return true;
      if (seen.has(s)) continue;
      seen.add(s);
      const o = r.objects.get(s);
      if (o?.type === 'commit') stack.push(...this.commit(r, s).parents);
    }
    return false;
  }
  /** build nested trees from flat entries (path with "/") — like GitHub POST /git/trees without base_tree */
  private buildTree(r: FakeRepo, entries: { path: string; mode: string; type: string; sha: string }[]): string {
    type Node = { files: Map<string, { mode: string; sha: string }>; dirs: Map<string, Node> };
    const root: Node = { files: new Map(), dirs: new Map() };
    for (const e of entries) {
      const parts = e.path.split('/');
      let n = root;
      for (const p of parts.slice(0, -1)) {
        if (!n.dirs.has(p)) n.dirs.set(p, { files: new Map(), dirs: new Map() });
        n = n.dirs.get(p)!;
      }
      n.files.set(parts.at(-1)!, { mode: e.mode, sha: e.sha });
    }
    const write = (n: Node): string => {
      const items: { name: string; mode: string; sha: string; key: string }[] = [];
      for (const [name, f] of n.files) items.push({ name, mode: f.mode.replace(/^0+/, ''), sha: f.sha, key: name });
      for (const [name, d] of n.dirs) items.push({ name, mode: '40000', sha: write(d), key: `${name}/` });
      items.sort((a, b) => Buffer.compare(Buffer.from(a.key), Buffer.from(b.key)));
      const body = Buffer.concat(items.map((x) => Buffer.concat([Buffer.from(`${x.mode} ${x.name}\0`), Buffer.from(x.sha, 'hex')])));
      return this.put(r, 'tree', body);
    };
    return write(root);
  }
  private person(p: { name: string; email: string; date?: string }) {
    const ts = Math.floor((p.date ? Date.parse(p.date) : Date.now()) / 1000);
    return `${p.name} <${p.email}> ${ts} +0000`;
  }
  private makeCommit(r: FakeRepo, c: { message: string; tree: string; parents: string[]; author: { name: string; email: string; date?: string }; committer?: { name: string; email: string; date?: string } }): string {
    const lines = [`tree ${c.tree}`, ...c.parents.map((p) => `parent ${p}`), `author ${this.person(c.author)}`, `committer ${this.person(c.committer ?? c.author)}`];
    return this.put(r, 'commit', Buffer.from(`${lines.join('\n')}\n\n${c.message}`, 'utf8'));
  }

  // ── http
  private send(res: ServerResponse, status: number, body: unknown, headers: Record<string, string> = {}) {
    const h = { 'Content-Type': 'application/json', 'x-ratelimit-limit': String(this.rate.limit), 'x-ratelimit-remaining': String(this.rate.remaining), 'x-ratelimit-reset': String(Math.floor(Date.now() / 1000) + 3600), ...headers };
    res.writeHead(status, h);
    res.end(status === 204 ? undefined : JSON.stringify(body));
  }
  private async handle(req: IncomingMessage, res: ServerResponse) {
    const url = new URL(req.url ?? '/', 'http://x');
    const path = decodeURIComponent(url.pathname);
    const method = req.method ?? 'GET';
    const chunks: Buffer[] = [];
    for await (const c of req) chunks.push(c as Buffer);
    const raw = Buffer.concat(chunks).toString('utf8');
    const body = raw ? JSON.parse(raw) : {};
    const done = (status: number, b: unknown, h: Record<string, string> = {}) => {
      this.calls.push({ method, path, status });
      this.send(res, status, b, h);
    };
    const f = this.faults.find((x) => x.times > 0 && x.match.test(path) && (!x.method || x.method === method));
    if (f) {
      f.times--;
      return done(f.status, f.body ? JSON.parse(f.body) : { message: `injected ${f.status}` }, f.headers ?? {});
    }
    const auth = (req.headers.authorization ?? '').replace(/^Bearer\s+/i, '');
    const tok = this.tokens.get(auth);
    if (!tok) return done(401, { message: 'Bad credentials' });
    this.rate.remaining = Math.max(0, this.rate.remaining - 1);
    if (path === '/user') return tok.noUser ? done(403, { message: 'Resource not accessible by personal access token' }) : done(200, { login: tok.login });
    if (path === '/rate_limit') return done(200, { resources: { core: { limit: this.rate.limit, remaining: this.rate.remaining, reset: Math.floor(Date.now() / 1000) + 3600 } } });
    const m = /^\/repos\/([^/]+)\/([^/]+)(\/.*)?$/.exec(path);
    if (!m) return done(404, { message: 'Not Found' });
    const r = this.repos.get(`${m[1]}/${m[2]}`.toLowerCase());
    const can = r && (tok.repos === '*' || tok.repos.map((x) => x.toLowerCase()).includes(`${m[1]}/${m[2]}`.toLowerCase()));
    if (!r || !can) return done(404, { message: 'Not Found' });
    const rest = m[3] ?? '';
    const empty = r.refs.size === 0;
    const write = () => tok.write;
    if (rest === '' && method === 'GET') return done(200, { full_name: `${r.owner}/${r.name}`, private: r.private, default_branch: r.defaultBranch, html_url: `https://github.com/${r.owner}/${r.name}`, size: empty ? 0 : 1, permissions: { pull: true, push: tok.write, admin: false } });
    if (rest.startsWith('/contents/') && method === 'PUT') {
      if (!write()) return done(403, { message: 'Resource not accessible by personal access token' });
      const p = rest.slice('/contents/'.length);
      const branch = body.branch ?? r.defaultBranch ?? 'main';
      const parent = r.refs.get(`heads/${branch}`);
      const blob = this.put(r, 'blob', Buffer.from(body.content, 'base64'));
      const prev = parent ? this.checkout(r, parent) : new Map();
      const entries = [...prev].filter(([k]) => k !== p).map(([k, v]) => ({ path: k, mode: v.mode, type: 'blob', sha: sha1('blob', v.bytes) }));
      for (const [k, v] of prev) if (k !== p) this.put(r, 'blob', v.bytes);
      entries.push({ path: p, mode: '100644', type: 'blob', sha: blob });
      const tree = this.buildTree(r, entries);
      const who = body.author ?? { name: tok.login, email: `${tok.login}@users.noreply.github.com` };
      const sha = this.makeCommit(r, { message: body.message, tree, parents: parent ? [parent] : [], author: who, committer: body.committer ?? who });
      r.refs.set(`heads/${branch}`, sha);
      r.defaultBranch ??= branch;
      return done(201, { content: { path: p }, commit: { sha } });
    }
    if (rest.startsWith('/git/')) {
      if (empty) return done(409, { message: 'Git Repository is empty.' });
      const g = rest.slice(5);
      if (g === 'matching-refs/heads/' || g === 'matching-refs/heads') return done(200, [...r.refs.keys()].filter((k) => k.startsWith('heads/')).map((k) => ({ ref: `refs/${k}`, object: { sha: r.refs.get(k) } })));
      if (g === 'blobs' && method === 'POST') {
        if (!write()) return done(403, { message: 'Resource not accessible by personal access token' });
        const bytes = body.encoding === 'base64' ? Buffer.from(body.content, 'base64') : Buffer.from(body.content, 'utf8');
        return done(201, { sha: this.put(r, 'blob', bytes) });
      }
      let mm = /^blobs\/([0-9a-f]{40})$/.exec(g);
      if (mm && method === 'GET') return r.objects.get(mm[1]!)?.type === 'blob' ? done(200, { sha: mm[1], size: r.objects.get(mm[1]!)!.body.length }) : done(404, { message: 'Not Found' });
      if (g === 'trees' && method === 'POST') {
        if (!write()) return done(403, { message: 'Resource not accessible by personal access token' });
        for (const e of body.tree as { sha: string; path: string }[]) if (!r.objects.has(e.sha)) return done(422, { message: `tree.sha ${e.sha} is not a valid blob (${e.path})` });
        return done(201, { sha: this.buildTree(r, body.tree) });
      }
      if (g === 'commits' && method === 'POST') {
        if (!write()) return done(403, { message: 'Resource not accessible by personal access token' });
        if (!r.objects.has(body.tree)) return done(422, { message: 'Tree SHA does not exist' });
        for (const p of body.parents ?? []) if (!r.objects.has(p)) return done(422, { message: 'Parent SHA does not exist' });
        return done(201, { sha: this.makeCommit(r, body) });
      }
      mm = /^commits\/([0-9a-f]{40})$/.exec(g);
      if (mm && method === 'GET') {
        if (r.objects.get(mm[1]!)?.type !== 'commit') return done(404, { message: 'Not Found' });
        const c = this.commit(r, mm[1]!);
        return done(200, { sha: c.sha, message: c.message, tree: { sha: c.tree }, parents: c.parents.map((sha) => ({ sha })) });
      }
      mm = /^ref\/heads\/(.+)$/.exec(g);
      if (mm && method === 'GET') {
        const sha = r.refs.get(`heads/${mm[1]}`);
        return sha ? done(200, { ref: `refs/heads/${mm[1]}`, object: { sha, type: 'commit' } }) : done(404, { message: 'Not Found' });
      }
      if (g === 'refs' && method === 'POST') {
        if (!write()) return done(403, { message: 'Resource not accessible by personal access token' });
        const name = String(body.ref).replace(/^refs\//, '');
        if (r.refs.has(name)) return done(422, { message: 'Reference already exists' });
        if (r.objects.get(body.sha)?.type !== 'commit') return done(422, { message: 'Object does not exist' });
        // git: a ref cannot be both a file and a directory
        for (const k of r.refs.keys()) if (k.startsWith(`${name}/`) || name.startsWith(`${k}/`)) return done(422, { message: `Reference cannot be created: ${k} exists` });
        r.refs.set(name, body.sha);
        return done(201, { ref: `refs/${name}`, object: { sha: body.sha } });
      }
      mm = /^refs\/heads\/(.+)$/.exec(g);
      if (mm && method === 'PATCH') {
        if (!write()) return done(403, { message: 'Resource not accessible by personal access token' });
        const cur = r.refs.get(`heads/${mm[1]}`);
        if (!cur) return done(422, { message: 'Reference does not exist' });
        if (!body.force && !this.isAncestor(r, cur, body.sha)) return done(422, { message: 'Update is not a fast forward' });
        r.refs.set(`heads/${mm[1]}`, body.sha);
        return done(200, { ref: `refs/heads/${mm[1]}`, object: { sha: body.sha } });
      }
      if (mm && method === 'DELETE') {
        if (!write()) return done(403, { message: 'Resource not accessible by personal access token' });
        if (!r.refs.delete(`heads/${mm[1]}`)) return done(422, { message: 'Reference does not exist' });
        return done(204, null);
      }
    }
    return done(404, { message: 'Not Found' });
  }
}
