/**
 * §21 #96 volume manager — shared helpers (agent GUI + mock; the Go agent mirrors the path rules in
 * agent/internal/agent/volops.go and the mismatch patterns in the same file).
 */
import { VOLUME_COPIES_DIR } from './types.js';

/** images whose data must not be copied while the server runs: "stop containers during the copy" defaults ON */
const DB_IMAGE = /(^|[/_-])(postgres|postgresql|postgis|timescale(db)?|custom-pg|pg|mysql|mariadb|mongo(db)?|redis|valkey|keydb|influx(db)?|clickhouse|elasticsearch|opensearch|cassandra|couchdb|questdb|victoria-?metrics|minio|etcd|rabbitmq|nats|mosquitto|emqx)([:@/_-]|\d|$)/i;
export function isDatabaseImage(image: string | null | undefined): boolean {
  return !!image && DB_IMAGE.test(image);
}

export interface DataDirMismatch {
  kind: 'custom-pg' | 'postgres';
  /** the matching log line */
  line: string;
}
/**
 * A crash loop caused by an old data directory (a newer database image refuses it): the custom-pg banner
 * "DATA DIRECTORY / IMAGE VERSION MISMATCH" or PostgreSQL's "database files are incompatible with server".
 */
export function dataDirMismatch(logText: string): DataDirMismatch | null {
  // eslint-disable-next-line no-control-regex
  const lines = logText.replace(/\x1b\[[0-9;]*m/g, '').split('\n');
  for (const l of lines) {
    if (/DATA DIRECTORY\s*\/\s*IMAGE VERSION MISMATCH/i.test(l)) return { kind: 'custom-pg', line: l.trim() };
  }
  for (const l of lines) {
    if (/database files are incompatible with server/i.test(l)) return { kind: 'postgres', line: l.trim() };
  }
  return null;
}

function stamp(d: Date): string {
  const p = (n: number) => String(n).padStart(2, '0');
  return `${d.getUTCFullYear()}${p(d.getUTCMonth() + 1)}${p(d.getUTCDate())}-${p(d.getUTCHours())}${p(d.getUTCMinutes())}${p(d.getUTCSeconds())}`;
}
/** <agentHome>/volume-copies/<volume>-<yyyymmdd-hhmmss> (UTC) — the default "Copy to path" target */
export function defaultCopyPath(agentHome: string, volumeSlug: string, now: Date = new Date()): string {
  return `${agentHome.replace(/\/+$/, '')}/${VOLUME_COPIES_DIR}/${volumeSlug}-${stamp(now)}`;
}

/**
 * Early (GUI) check of a copy target; the agent decides (it also resolves symlinks and knows every volume path).
 * Returns a problem or null.
 */
export function copyPathProblem(path: string, agentHome: string): string | null {
  const p = path.trim();
  if (!p) return null; // default path
  if (p.includes('\0')) return 'invalid character';
  let abs = p;
  if (p.startsWith('./')) abs = `${agentHome.replace(/\/+$/, '')}/${p.slice(2)}`;
  else if (!p.startsWith('/')) return 'use an absolute path, or ./… relative to the agent home';
  const parts: string[] = [];
  for (const seg of abs.split('/')) {
    if (!seg || seg === '.') continue;
    if (seg === '..') parts.pop();
    else parts.push(seg);
  }
  const norm = '/' + parts.join('/');
  if (norm === '/') return 'not the root folder';
  for (const sys of SYSTEM_DIRS) if (norm === sys || norm.startsWith(sys + '/')) return `not inside ${sys}`;
  const home = agentHome.replace(/\/+$/, '');
  if (norm === home || (norm.startsWith(home + '/') && !norm.startsWith(`${home}/${VOLUME_COPIES_DIR}/`))) return `inside the agent home only under ./${VOLUME_COPIES_DIR}/`;
  if (norm === `${home}/${VOLUME_COPIES_DIR}`) return `a folder inside ./${VOLUME_COPIES_DIR}/`;
  return null;
}
/** host folders a copy never goes to (the agent refuses them; /var/lib/docker and /run too) */
export const SYSTEM_DIRS = ['/bin', '/boot', '/dev', '/etc', '/lib', '/lib32', '/lib64', '/libx32', '/proc', '/root/.ssh', '/run', '/sbin', '/sys', '/usr', '/var/lib/docker', '/var/run'] as const;

/** typed confirmation of empty / restore = the volume slug */
export function volumeConfirmOk(volumeSlug: string, typed: string): boolean {
  return typed.trim() === volumeSlug;
}
