/**
 * §21 #100 mock of GET /_opm/api/sites/{site}/transfers/fs — a small fake server filesystem that exercises every picker
 * state: blocked entries (agent data, /proc, /etc/shadow, SSH host keys), an unreadable folder (/root), hidden files,
 * symlinks (to a folder, to a file, dangling), a file > 2 GiB and a folder with 6000 entries (truncated at 5000).
 */
import { HOST_FS_MAX_ENTRIES, type HostFsEntry, type HostFsListResponse } from '@opm/shared/types';
import { fsClean, fsParent, hostFsBlockReason } from '@opm/shared/transfers';

type F = { size: number; mtimeAgo?: number; unreadable?: boolean };
type L = { link: string };
type Dir = { [name: string]: Dir | F | L };
const UNREADABLE = new Set(['/root', '/var/lib/docker']);
const PROTECTED = ['/var/lib/opm', '/etc/opm/agent.json', '/etc/opm/install-token'];
const GiB = 1024 ** 3;

const many: Dir = {};
for (let i = 0; i < 6000; i++) many[`sample-${String(i).padStart(4, '0')}.json`] = { size: 1200 + i, mtimeAgo: 3600 + i };

const ROOT: Dir = {
  bin: { link: 'usr/bin' },
  etc: {
    hosts: { size: 221 },
    passwd: { size: 1904 },
    shadow: { size: 1022 },
    opm: { 'agent.json': { size: 812 }, 'install-token': { size: 40 } },
    ssh: { 'ssh_host_ed25519_key': { size: 411 }, 'ssh_host_ed25519_key.pub': { size: 99 }, sshd_config: { size: 3301 } },
  },
  home: { op: { '.bashrc': { size: 3771 }, '.ssh': { authorized_keys: { size: 402 } }, 'notes.txt': { size: 912 }, 'report.pdf': { size: 1_822_331 } } },
  proc: { cpuinfo: { size: 0 }, kcore: { size: 140 * GiB }, '1': { status: { size: 0 } } },
  root: { 'secret.txt': { size: 12 } },
  srv: {
    backups: { 'pg-dump-2026-10-08.sql.gz': { size: 412_336_120, mtimeAgo: 900 }, 'pg-dump-2026-10-07.sql.gz': { size: 409_118_007, mtimeAgo: 86400 } },
    many,
    'opm-drop': { 'plant-layout.pdf': { size: 2_481_334 } },
  },
  tmp: { '.X0-lock': { size: 11 }, 'core.12345': { size: 88_112_004 } },
  usr: { bin: { docker: { size: 71_331_002 }, bash: { size: 1_396_520 } } },
  var: {
    log: {
      syslog: { size: 2_311_822, mtimeAgo: 30 },
      'kern.log': { size: 411_022 },
      'huge-archive.log': { size: 3 * GiB, mtimeAgo: 7200 },
      'locked.log': { size: 2048, unreadable: true },
      app: { 'app.log': { size: 18_204_113, mtimeAgo: 12 }, 'app-logs-2026-10-05.tar.gz': { size: 18_204_113, mtimeAgo: 86400 } },
      'current.log': { link: '/var/log/app/app.log' },
      'gone.log': { link: '/var/log/app/rotated-away.log' },
    },
    lib: { opm: { 'site.key': { size: 119 }, 'opm.db': { size: 4_118_528 } }, docker: { volumes: {} } },
  },
};

const isLink = (n: unknown): n is L => !!n && typeof n === 'object' && 'link' in (n as object) && typeof (n as L).link === 'string';
const isFile = (n: unknown): n is F => !!n && typeof n === 'object' && 'size' in (n as object) && typeof (n as F).size === 'number';

function lookup(path: string, follow = true, depth = 0): Dir | F | L | null {
  if (depth > 8) return null;
  let cur: Dir | F | L = ROOT;
  let at = '';
  for (const seg of path.split('/').filter(Boolean)) {
    if (isLink(cur) && follow) cur = lookup(resolve(at, (cur as L).link), true, depth + 1) ?? ({} as Dir);
    if (isFile(cur) || isLink(cur)) return null;
    const nx: Dir | F | L | undefined = (cur as Dir)[seg];
    if (!nx) return null;
    cur = nx;
    at += '/' + seg;
  }
  if (isLink(cur) && follow) return lookup(resolve(fsParent(at) ?? '/', cur.link), true, depth + 1);
  return cur;
}
const resolve = (dir: string, target: string) => fsClean(target.startsWith('/') ? target : `${dir}/${target}`) ?? '/';

export function hostFsList(rawPath: string, hidden: boolean, fail: (code: 'invalid' | 'not-found' | 'forbidden', message: string, details?: Record<string, unknown>) => never): HostFsListResponse {
  const path = fsClean(rawPath || '/');
  if (!path) fail('invalid', 'path must be absolute', { errors: [{ path: 'path', code: 'V_FORMAT', message: 'absolute path', severity: 'error' }] });
  const p = path as string;
  const node = lookup(p);
  if (!node) fail('not-found', `no such folder: ${p}`);
  if (isFile(node)) fail('invalid', `${p} is not a directory`, { type: 'file' });
  if (UNREADABLE.has(p)) fail('forbidden', `permission denied: ${p} (the agent runs as opm)`);
  const now = Date.now();
  const all: HostFsEntry[] = Object.entries(node as Dir).map(([name, n]) => {
    const ep = p === '/' ? `/${name}` : `${p}/${name}`;
    const base = { name, path: ep, hidden: name.startsWith('.'), mode: '', blocked: hostFsBlockReason(ep, PROTECTED) };
    if (isLink(n)) {
      const t = lookup(resolve(p, n.link));
      const targetType = t === null ? 'missing' : isFile(t) ? 'file' : 'dir';
      return { ...base, type: 'symlink', targetType, linkTarget: n.link, sizeBytes: isFile(t) ? t.size : null, mtime: new Date(now - 86400_000).toISOString(), mode: 'lrwxrwxrwx', readable: true, blocked: base.blocked ?? (t !== null ? hostFsBlockReason(resolve(p, n.link), PROTECTED) : null) } as HostFsEntry;
    }
    if (isFile(n)) return { ...base, type: 'file', targetType: null, linkTarget: null, sizeBytes: n.size, mtime: new Date(now - (n.mtimeAgo ?? 86400 * 3) * 1000).toISOString(), mode: n.unreadable ? '-rw-------' : '-rw-r--r--', readable: !n.unreadable } as HostFsEntry;
    return { ...base, type: 'dir', targetType: null, linkTarget: null, sizeBytes: null, mtime: new Date(now - 86400_000 * 2).toISOString(), mode: UNREADABLE.has(ep) ? 'drwx------' : 'drwxr-xr-x', readable: !UNREADABLE.has(ep) } as HostFsEntry;
  });
  const visible = all.filter((e) => hidden || !e.hidden);
  const isD = (e: HostFsEntry) => e.type === 'dir' || (e.type === 'symlink' && e.targetType === 'dir');
  visible.sort((a, b) => Number(isD(b)) - Number(isD(a)) || a.name.toLowerCase().localeCompare(b.name.toLowerCase()));
  return {
    path: p,
    parent: fsParent(p),
    entries: visible.slice(0, HOST_FS_MAX_ENTRIES),
    truncated: visible.length > HOST_FS_MAX_ENTRIES,
    hiddenCount: hidden ? 0 : all.length - visible.length,
    agentUser: 'opm',
    blocked: hostFsBlockReason(p, PROTECTED),
  };
}

/** a file of the fake tree (sendFromPath): size, or a refusal */
export function hostFsFile(path: string): { size: number } | { code: 'invalid' | 'not-found' | 'forbidden' | 'too-large'; message: string } {
  const p = fsClean(path);
  if (!p) return { code: 'invalid', message: 'path must be absolute' };
  const reason = hostFsBlockReason(p, PROTECTED);
  if (reason) return { code: 'forbidden', message: reason };
  const n = lookup(p);
  if (!n) return { code: 'not-found', message: `no such file: ${p}` };
  if (!isFile(n)) return { code: 'invalid', message: 'not a regular file (folders cannot be sent)' };
  if (n.unreadable) return { code: 'forbidden', message: `permission denied: ${p}` };
  if (n.size > 2 * GiB) return { code: 'too-large', message: 'at most 2 GiB' };
  return { size: n.size };
}
