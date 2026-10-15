/**
 * §21 #100 transfers helpers shared by both GUIs and mirrored by the agent (Go `agent/internal/agent/hostfs.go`):
 * - the server-filesystem safety policy (`hostFsBlockReason`, vectors `testdata/hostfs-vectors.json`);
 * - the store-and-forward state shown in the Transfers module of both GUIs (`transferState`);
 * - path helpers for the fs picker (`fsCrumbs`, `fsParent`, `fsClean`).
 */
import type { Transfer } from './types.js';

/** pseudo / runtime filesystems: listed, never sent (their "files" are kernel views, devices or sockets) */
export const HOST_FS_PSEUDO = ['/proc', '/sys', '/dev', '/run'] as const;
/** credential stores of the host: listed, never sent (a cloud reader of the deployment must not get them) */
export const HOST_FS_SECRETS = ['/etc/shadow', '/etc/shadow-', '/etc/gshadow', '/etc/gshadow-', '/etc/sudoers', '/etc/security/opasswd'] as const;

/** lexical clean of an absolute "/" path (no symlink resolution): "//a/./b/../c/" → "/a/c"; "" or relative → null */
export function fsClean(p: string): string | null {
  if (typeof p !== 'string' || !p.startsWith('/') || p.includes('\0')) return null;
  const out: string[] = [];
  for (const seg of p.split('/')) {
    if (seg === '' || seg === '.') continue;
    if (seg === '..') out.pop();
    else out.push(seg);
  }
  return '/' + out.join('/');
}

/** "/" → null; "/a/b" → "/a" */
export function fsParent(p: string): string | null {
  const c = fsClean(p);
  if (!c || c === '/') return null;
  const i = c.lastIndexOf('/');
  return i <= 0 ? '/' : c.slice(0, i);
}

/** breadcrumbs: "/var/log" → [{name:"/",path:"/"},{name:"var",path:"/var"},{name:"log",path:"/var/log"}] */
export function fsCrumbs(p: string): { name: string; path: string }[] {
  const c = fsClean(p) ?? '/';
  const out = [{ name: '/', path: '/' }];
  let acc = '';
  for (const seg of c.split('/').filter(Boolean)) {
    acc += '/' + seg;
    out.push({ name: seg, path: acc });
  }
  return out;
}

const within = (p: string, dir: string) => dir !== '' && (p === dir || p.startsWith(dir.endsWith('/') ? dir : dir + '/'));

/**
 * Why a server path cannot be sent to the cloud (null = allowed). `p` must be the REAL path (symlinks resolved — the
 * agent checks both the requested and the resolved path). `protectedPaths` = the agent's own files: its home (site key,
 * database, certificates, secrets, blobs …), its agent.json and its install-token file.
 */
export function hostFsBlockReason(p: string, protectedPaths: readonly string[]): string | null {
  const c = fsClean(p);
  if (!c) return 'not an absolute path';
  for (const d of protectedPaths) {
    const pc = d ? fsClean(d) : null;
    if (pc && pc !== '/' && within(c, pc)) return 'OPM agent data (site key, database, secrets) — never sent';
  }
  for (const d of HOST_FS_PSEUDO) if (within(c, d)) return `${d} is a kernel / runtime filesystem — not a real file`;
  if ((HOST_FS_SECRETS as readonly string[]).includes(c)) return 'host password / sudo database — never sent';
  if (/^\/etc\/ssh\/ssh_host_[^/]*_key$/.test(c)) return 'SSH host private key — never sent';
  return null;
}

export type TransferStateKey =
  | 'browser-uploading' // cloud GUI: the operator's browser is still uploading parts
  | 'queued'
  | 'uploading'
  | 'stored'
  | 'pending'
  | 'downloading'
  | 'on-site'
  | 'saved-on-server'
  | 'downloaded'
  | 'expired';
export interface TransferState {
  key: TransferStateKey;
  label: string;
  tone: 'info' | 'ok' | 'accent' | 'warn' | '';
  /** 0..1 while bytes move, else null */
  progress: number | null;
  /** one short line under the badge (retry / error / who has it) */
  hint: string | null;
}

const pct = (done: number, size: number) => (size > 0 ? Math.max(0, Math.min(1, done / size)) : 0);

/**
 * The state of a transfer as one badge, from the point of view of the GUI that shows it (`side`):
 * - agent: the agent API's Transfer (with `delivery`);
 * - cloud: the cloud Transfer (to-site uses `fetchedAt`).
 */
export function transferState(t: Transfer, side: 'agent' | 'cloud'): TransferState {
  const d = t.delivery;
  const retry = d && d.attempts > 0 ? `retry ${d.attempts}${d.lastError ? ` — ${d.lastError}` : ''}` : null;
  if (t.status === 'expired') return { key: 'expired', label: 'deleted', tone: '', progress: null, hint: null };
  if (t.direction === 'to-cloud') {
    if (t.status === 'downloaded') return { key: 'downloaded', label: 'stored · downloaded', tone: 'accent', progress: null, hint: side === 'agent' ? 'the operations team downloaded it' : null };
    if (t.status === 'ready') return { key: 'stored', label: side === 'agent' ? 'stored in the cloud' : 'stored', tone: 'ok', progress: null, hint: null };
    // uploading
    if (side === 'cloud') return { key: 'uploading', label: 'arriving from the site', tone: 'info', progress: null, hint: 'the site uploads it in the background' };
    if (d && d.phase === 'uploading') return { key: 'uploading', label: 'uploading', tone: 'info', progress: pct(d.bytesDone, t.sizeBytes), hint: retry };
    return { key: 'queued', label: 'queued', tone: 'warn', progress: d && d.bytesDone > 0 ? pct(d.bytesDone, t.sizeBytes) : null, hint: retry ?? 'stored on the site; uploads as soon as the cloud is reachable' };
  }
  // to-site
  if (t.status === 'uploading') return { key: 'browser-uploading', label: 'uploading from the browser', tone: 'info', progress: null, hint: null };
  if (t.status === 'saved-on-server') return { key: 'saved-on-server', label: 'saved on server', tone: 'accent', progress: null, hint: t.savedPath };
  if (t.status === 'downloaded') return { key: 'downloaded', label: 'downloaded', tone: 'accent', progress: null, hint: side === 'cloud' ? 'downloaded in the agent GUI' : null };
  // ready
  if (side === 'cloud') {
    if (t.fetchedAt) return { key: 'on-site', label: 'on the site', tone: 'ok', progress: null, hint: null };
    return { key: 'pending', label: 'stored · waiting for the site', tone: 'warn', progress: null, hint: 'the agent fetches it the next time it is online' };
  }
  if (d && d.phase === 'downloading') return { key: 'downloading', label: 'downloading', tone: 'info', progress: pct(d.bytesDone, t.sizeBytes), hint: retry };
  if (d && d.phase === 'pending') return { key: 'pending', label: 'pending', tone: 'warn', progress: null, hint: retry ?? 'in the cloud; fetched as soon as the cloud is reachable' };
  return { key: 'on-site', label: 'on this site', tone: 'ok', progress: null, hint: null };
}
