// VM e2e helpers: results table, waits, docker / compose, agent GUI session (cookie jar + actor token).
import { execFileSync } from 'node:child_process';
import { createHash, randomBytes } from 'node:crypto';
import { appendFileSync, mkdirSync } from 'node:fs';
import { createServer } from 'node:net';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createAgentClient, type AgentClient } from '../../shared/dist/agent-api.js';

export const HERE = dirname(fileURLToPath(import.meta.url));
export const ROOT = resolve(HERE, '../..'); // on-premise-manager
export const RUN = join(ROOT, 'e2e/run/vm');
export const SELFHOST = join(ROOT, 'selfhost');
export const DOCKER_HOST = process.env.DOCKER_HOST || `unix://${process.env.HOME}/.colima/default/docker.sock`;
export const COMPOSE = process.env.OPM_COMPOSE_BIN || '/opt/homebrew/bin/docker-compose';
export const COMPOSE_FILES = ['-f', 'docker-compose.yml', '-f', '../e2e/vm/compose.e2e.yml'];
mkdirSync(RUN, { recursive: true });
const LOG = join(RUN, `harness-${new Date().toISOString().replace(/[:.]/g, '-')}.log`);

export const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
export const sha256Hex = (b: Uint8Array | string) => createHash('sha256').update(b).digest('hex');
export const uniq = (p: string) => `${p}${randomBytes(3).toString('hex')}`;

export function log(...a: unknown[]) {
  const line = `[${new Date().toISOString().slice(11, 19)}] ${a.map((x) => (typeof x === 'string' ? x : JSON.stringify(x))).join(' ')}`;
  console.log(line);
  appendFileSync(LOG, line + '\n');
}

// ───────────────────────────── results
export interface Check { section: string; name: string; ok: boolean; detail: string }
export const checks: Check[] = [];
let section = '';
export function check(name: string, ok: boolean, detail: unknown = ''): boolean {
  const d = typeof detail === 'string' ? detail : JSON.stringify(detail);
  checks.push({ section, name, ok, detail: d.length > 400 ? d.slice(0, 400) + '…' : d });
  log(`${ok ? 'PASS' : 'FAIL'} [${section}] ${name}${d ? ` — ${d.slice(0, 300)}` : ''}`);
  return ok;
}
export class Fatal extends Error {}
/** a check that stops the current step when it fails */
export function must(name: string, ok: boolean, detail: unknown = ''): void {
  if (!check(name, ok, detail)) throw new Fatal(name);
}
const aborted = new Set<string>();
/** run a step; it is skipped when a step it needs aborted */
export async function step(id: string, title: string, needs: string[], fn: () => Promise<void>): Promise<void> {
  section = `${id} ${title}`;
  log(`\n=== ${section}`);
  const missing = needs.filter((n) => aborted.has(n));
  if (missing.length) {
    aborted.add(id);
    check('skipped', false, `needs step ${missing.join(', ')} which aborted`);
    return;
  }
  try {
    await fn();
  } catch (e) {
    aborted.add(id);
    const err = e as Error & { details?: unknown; status?: number; code?: string };
    if (!(e instanceof Fatal)) check('step threw', false, `${err.status ?? ''} ${err.code ?? ''} ${err.message} ${err.details ? JSON.stringify(err.details) : ''} ${(err.stack ?? '').split('\n').slice(1, 3).join(' ')}`);
  }
}

export async function waitFor<T>(what: string, f: () => Promise<T | null | undefined | false>, ms: number, every = 1000): Promise<T> {
  const t0 = Date.now();
  let last: unknown = null;
  for (;;) {
    try {
      const v = await f();
      if (v) return v as T;
    } catch (e) {
      last = e;
    }
    if (Date.now() - t0 > ms) throw new Error(`timeout (${Math.round(ms / 1000)} s) waiting for ${what}${last ? `: ${(last as Error).message}` : ''}`);
    await sleep(every);
  }
}

// ───────────────────────────── processes
export function sh(cmd: string, args: string[], opts: { cwd?: string; input?: string; allowFail?: boolean; timeoutMs?: number } = {}): { code: number; out: string } {
  try {
    const out = execFileSync(cmd, args, { cwd: opts.cwd, input: opts.input, encoding: 'utf8', stdio: ['pipe', 'pipe', 'pipe'], env: { ...process.env, DOCKER_HOST }, timeout: opts.timeoutMs ?? 300_000, maxBuffer: 64 * 1024 * 1024 });
    return { code: 0, out: out.trim() };
  } catch (e) {
    const x = e as { status?: number; stdout?: string; stderr?: string };
    if (!opts.allowFail) throw new Error(`${cmd} ${args.join(' ')} → exit ${x.status}: ${(x.stderr ?? '').slice(-800)} ${(x.stdout ?? '').slice(-400)}`);
    return { code: x.status ?? 1, out: `${x.stdout ?? ''}${x.stderr ?? ''}`.trim() };
  }
}
export const compose = (args: string[], o: { input?: string; allowFail?: boolean; timeoutMs?: number } = {}) => sh(COMPOSE, [...COMPOSE_FILES, ...args], { cwd: SELFHOST, ...o });
/** `opm-cloud <cmd>` inside the running container */
export const opmCloud = (args: string[], o: { allowFail?: boolean; timeoutMs?: number } = {}) => compose(['exec', '-T', 'opm-cloud', 'opm-cloud', ...args], o);
export const docker = (args: string[], o: { allowFail?: boolean } = {}) => sh('docker', args, o);

export function freePort(): Promise<number> {
  return new Promise((res, rej) => {
    const s = createServer();
    s.once('error', rej);
    s.listen(0, '127.0.0.1', () => {
      const p = (s.address() as { port: number }).port;
      s.close(() => res(p));
    });
  });
}

// ───────────────────────────── agent GUI session (opm_session_<site> cookie + Actor token)
export class AgentSession {
  actorToken: string | null = null;
  jar = new Map<string, string>();
  readonly api: AgentClient;
  constructor(public readonly base: string) {
    this.api = createAgentClient(base, () => this.actorToken, { fetch: this.fetch });
  }
  get cookie(): string {
    return [...this.jar].map(([k, v]) => `${k}=${v}`).join('; ');
  }
  fetch = async (input: RequestInfo | URL, init: RequestInit = {}): Promise<Response> => {
    const headers = new Headers(init.headers);
    if (this.jar.size) headers.set('Cookie', this.cookie);
    const r = await fetch(input, { ...init, headers });
    for (const sc of r.headers.getSetCookie()) {
      const [kv] = sc.split(';');
      const i = kv!.indexOf('=');
      const k = kv!.slice(0, i).trim();
      const v = kv!.slice(i + 1).trim();
      if (k !== 'opm_session' && !k.startsWith('opm_session_')) continue;
      if (v === '' || /max-age=0/i.test(sc)) this.jar.delete(k);
      else this.jar.set(k, v);
    }
    return r;
  };
  async login(accessKey: string, actor: string) {
    const r = await this.api.login({ body: { accessKey } });
    if (!this.jar.size) throw new Error(`login ok (session ${r.sessionId}) but no session cookie`);
    this.actorToken = (await this.api.declareActor({ body: { email: actor } })).actorToken;
    return r;
  }
  /** raw request with the session cookie + actor token */
  raw(path: string, init: RequestInit = {}) {
    const headers = new Headers(init.headers);
    if (this.actorToken) headers.set('Authorization', `Actor ${this.actorToken}`);
    return this.fetch(this.base + path, { ...init, headers });
  }
}
