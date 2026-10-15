/**
 * Mock Redis of the dev tools (§21 #97): one keyspace (db 0) with keys of every type + TTLs, SCAN with cursor / MATCH /
 * TYPE, key values per type with paging, a command interpreter for the console and the value editors, INFO / SLOWLOG.
 */
import type { RedisInfoResponse, RedisKeyRow, RedisKeyValueResponse, RedisReply, RedisScanResponse } from '@opm/shared/devtools';

type V =
  | { type: 'string'; v: string }
  | { type: 'hash'; v: Map<string, string> }
  | { type: 'list'; v: string[] }
  | { type: 'set'; v: Set<string> }
  | { type: 'zset'; v: Map<string, number> }
  | { type: 'stream'; v: { id: string; fields: [string, string][] }[] };
interface Entry {
  val: V;
  /** absolute expiry ms, null = none */
  exp: number | null;
}
const db = new Map<string, Entry>();
const now = () => Date.now();
const SLOW: RedisInfoResponse['slowlog'] = [];
let cmdCount = 18_442;

(function seed() {
  const T = now();
  const set = (k: string, val: V, ttlSec: number | null = null) => db.set(k, { val, exp: ttlSec === null ? null : T + ttlSec * 1000 });
  for (let i = 1; i <= 40; i++)
    set(`user:${i}`, { type: 'hash', v: new Map([['name', ['Asha Shah', 'Ravi Patel', 'Mira Rao', 'Karan Iyer'][i % 4]!], ['email', `user${i}@acme.example`], ['role', i % 7 ? 'viewer' : 'admin'], ['logins', String(i * 3)], ['prefs', JSON.stringify({ theme: i % 2 ? 'dark' : 'light' })]]) });
  for (let i = 0; i < 180; i++) set(`session:${(0x3f9a00 + i * 7919).toString(16)}`, { type: 'string', v: JSON.stringify({ userId: (i % 40) + 1, ip: `10.0.1.${i % 250}`, createdAt: new Date(T - i * 60_000).toISOString(), scopes: ['read', i % 3 ? 'write' : 'admin'] }) }, 600 + i * 17);
  set('config:feature-flags', { type: 'string', v: JSON.stringify({ heatmap: true, exports: false, newDashboard: { rollout: 0.25, sites: ['pune', 'delhi'] } }, null, 0) });
  set('config:maintenance', { type: 'string', v: 'off' });
  set('counter:visits', { type: 'string', v: '48213' });
  set('counter:api:errors', { type: 'string', v: '17' }, 86_400);
  set('cache:html:/home', { type: 'string', v: '<!doctype html><html><head><title>Northwind</title></head><body>' + 'x'.repeat(4000) + '</body></html>' }, 300);
  set('cache:device:SENSOR-1:latest', { type: 'string', v: JSON.stringify({ pm25: 41.2, pm10: 88, temp: 29.4, ts: new Date(T).toISOString() }) }, 120);
  set('queue:emails', { type: 'list', v: Array.from({ length: 64 }, (_, i) => JSON.stringify({ to: `user${i}@acme.example`, template: i % 2 ? 'digest' : 'alert', attempt: 0 })) });
  set('queue:exports', { type: 'list', v: ['report-2026-10-07.csv', 'report-2026-10-08.csv'] });
  set('tags:popular', { type: 'set', v: new Set(['pm25', 'noise', 'odour', 'dust', 'weather', 'aqi', 'ahmedabad', 'pune']) });
  set('leaderboard:daily', { type: 'zset', v: new Map(Array.from({ length: 30 }, (_, i) => [`device:AQ-${(1000 + i * 37).toString(36).toUpperCase()}`, Math.round((500 - i * 13.7) * 10) / 10] as [string, number])) });
  set('events:stream', { type: 'stream', v: Array.from({ length: 25 }, (_, i) => ({ id: `${T - (25 - i) * 60_000}-0`, fields: [['type', i % 3 ? 'reading' : 'alert'], ['device', `AQ-${(1000 + (i % 5) * 37).toString(36).toUpperCase()}`], ['value', String(20 + i)]] as [string, string][] })) });
  set('lock:report:daily', { type: 'string', v: 'worker-2' }, 45);
  for (let i = 0; i < 12; i++) SLOW.push({ id: 300 - i, at: new Date(T - i * 431_000).toISOString(), durationUs: 12_000 + ((i * 7919) % 90_000), command: ['KEYS *', 'HGETALL user:7', 'ZRANGE leaderboard:daily 0 -1 WITHSCORES', 'LRANGE queue:emails 0 -1', 'SMEMBERS tags:popular'][i % 5]! });
})();

function alive(k: string): Entry | null {
  const e = db.get(k);
  if (!e) return null;
  if (e.exp !== null && e.exp <= now()) {
    db.delete(k);
    return null;
  }
  return e;
}
function ttlOf(e: Entry) {
  return e.exp === null ? -1 : Math.max(0, Math.ceil((e.exp - now()) / 1000));
}
function lenOf(e: Entry): number {
  const v = e.val;
  return v.type === 'string' ? new TextEncoder().encode(v.v).length : v.type === 'list' || v.type === 'stream' ? v.v.length : v.type === 'set' ? v.v.size : v.v.size;
}
function sizeOf(k: string, e: Entry): number {
  const v = e.val;
  const raw = v.type === 'string' ? v.v.length : v.type === 'hash' ? [...v.v].reduce((a, [f, x]) => a + f.length + x.length + 16, 0) : v.type === 'list' ? v.v.reduce((a, x) => a + x.length + 11, 0) : v.type === 'set' ? [...v.v].reduce((a, x) => a + x.length + 16, 0) : v.type === 'zset' ? [...v.v.keys()].reduce((a, x) => a + x.length + 24, 0) : v.v.length * 64;
  return 56 + k.length + raw;
}
function globRe(p: string) {
  let s = '';
  for (let i = 0; i < p.length; i++) {
    const c = p[i]!;
    if (c === '\\' && i + 1 < p.length) s += '\\' + p[++i];
    else if (c === '*') s += '.*';
    else if (c === '?') s += '.';
    else if (c === '[') {
      const j = p.indexOf(']', i);
      if (j < 0) s += '\\[';
      else {
        s += '[' + p.slice(i + 1, j).replace(/^!/, '^') + ']';
        i = j;
      }
    } else s += c.replace(/[.+^${}()|\\]/g, '\\$&');
  }
  return new RegExp('^' + s + '$', 's');
}
function keysSorted() {
  return [...db.keys()].filter((k) => alive(k)).sort();
}
export function redisScan(pattern: string, cursor: string, count: number, type: string | null): RedisScanResponse {
  const all = keysSorted();
  const start = Number(cursor) || 0;
  const n = Math.min(Math.max(count || 200, 1), 1000);
  const re = globRe(pattern || '*');
  const chunk = all.slice(start, start + n);
  const keys: RedisKeyRow[] = [];
  for (const k of chunk) {
    const e = alive(k)!;
    if (!re.test(k)) continue;
    if (type && e.val.type !== type) continue;
    keys.push({ key: k, type: e.val.type, ttlSec: ttlOf(e), sizeBytes: sizeOf(k, e), length: lenOf(e) });
  }
  const next = start + n >= all.length ? '0' : String(start + n);
  return { cursor: next, keys, scanned: chunk.length, dbSize: all.length };
}
export function redisKey(key: string, cursor: string | null, count: number): RedisKeyValueResponse | null {
  const e = alive(key);
  if (!e) return null;
  const off = Number(cursor) || 0;
  const n = Math.min(Math.max(count || 100, 1), 1000);
  const v = e.val;
  let value: unknown;
  let total = 0;
  if (v.type === 'string') return { key, type: 'string', ttlSec: ttlOf(e), length: lenOf(e), value: v.v, next: null, truncated: false, encoding: /^\d+$/.test(v.v) ? 'int' : v.v.length > 44 ? 'raw' : 'embstr' };
  if (v.type === 'hash') {
    const all = [...v.v];
    total = all.length;
    value = all.slice(off, off + n);
  } else if (v.type === 'list') {
    total = v.v.length;
    value = v.v.slice(off, off + n);
  } else if (v.type === 'set') {
    const all = [...v.v].sort();
    total = all.length;
    value = all.slice(off, off + n);
  } else if (v.type === 'zset') {
    const all = [...v.v].sort((a, b) => b[1] - a[1]);
    total = all.length;
    value = all.slice(off, off + n);
  } else {
    total = v.v.length;
    value = v.v.slice(off, off + n).map((x) => ({ id: x.id, fields: Object.fromEntries(x.fields) }));
  }
  return { key, type: v.type, ttlSec: ttlOf(e), length: total, value, next: off + n < total ? String(off + n) : null, truncated: false, encoding: v.type === 'hash' ? (total > 128 ? 'hashtable' : 'listpack') : v.type === 'list' ? 'quicklist' : v.type === 'set' ? 'listpack' : v.type === 'zset' ? 'skiplist' : 'stream' };
}
export function redisInfo(): RedisInfoResponse {
  const keys = keysSorted();
  const expires = keys.filter((k) => alive(k)?.exp !== null).length;
  return {
    serverVersion: '7.2.5',
    sections: {
      server: { redis_version: '7.2.5', redis_mode: 'standalone', os: 'Linux 6.8.0-45-generic x86_64', arch_bits: '64', tcp_port: '6379', uptime_in_seconds: '1209611', uptime_in_days: '14', executable: '/usr/local/bin/redis-server', config_file: '' },
      clients: { connected_clients: '7', blocked_clients: '0', maxclients: '10000' },
      memory: { used_memory: '2183920', used_memory_human: '2.08M', used_memory_peak_human: '3.91M', maxmemory: '268435456', maxmemory_human: '256.00M', maxmemory_policy: 'allkeys-lru', mem_fragmentation_ratio: '1.62' },
      persistence: { loading: '0', rdb_changes_since_last_save: '41', rdb_last_save_time: String(Math.floor(now() / 1000) - 812), aof_enabled: '0' },
      stats: { total_connections_received: '1288', total_commands_processed: String(cmdCount), instantaneous_ops_per_sec: '23', keyspace_hits: '91234', keyspace_misses: '3120', expired_keys: '5521', evicted_keys: '0' },
      replication: { role: 'master', connected_slaves: '0' },
      cpu: { used_cpu_sys: '81.21', used_cpu_user: '133.40' },
      keyspace: { db0: `keys=${keys.length},expires=${expires},avg_ttl=412000` },
    },
    dbs: [{ db: 0, keys: keys.length, expires }],
    slowlog: SLOW,
  };
}

// ───────────────────────────── commands
export class RedisErr extends Error {}
const ok = (): RedisReply => ({ type: 'status', value: 'OK' });
const int = (n: number): RedisReply => ({ type: 'integer', value: n });
const str = (s: string | null): RedisReply => (s === null ? { type: 'nil', value: null } : { type: 'string', value: s });
const arr = (xs: RedisReply[]): RedisReply => ({ type: 'array', value: xs });
const wrongType = () => new RedisErr('WRONGTYPE Operation against a key holding the wrong kind of value');

function need(argv: string[], n: number) {
  if (argv.length < n) throw new RedisErr(`ERR wrong number of arguments for '${argv[0]!.toLowerCase()}' command`);
}
function getOf<T extends V['type']>(k: string, type: T, create?: () => Extract<V, { type: T }>): Extract<V, { type: T }> | null {
  const e = alive(k);
  if (!e) {
    if (!create) return null;
    const v = create();
    db.set(k, { val: v, exp: null });
    return v;
  }
  if (e.val.type !== type) throw wrongType();
  return e.val as Extract<V, { type: T }>;
}
function dropEmpty(k: string) {
  const e = db.get(k);
  if (e && e.val.type !== 'string' && lenOf(e) === 0) db.delete(k);
}

export function redisExec(argv: string[]): RedisReply {
  cmdCount++;
  const c = (argv[0] ?? '').toUpperCase();
  const a = argv.slice(1);
  switch (c) {
    case 'PING':
      return a[0] !== undefined ? str(a[0]) : { type: 'status', value: 'PONG' };
    case 'ECHO':
      need(argv, 2);
      return str(a[0]!);
    case 'TIME': {
      const t = now();
      return arr([str(String(Math.floor(t / 1000))), str(String((t % 1000) * 1000))]);
    }
    case 'DBSIZE':
      return int(keysSorted().length);
    case 'INFO': {
      const i = redisInfo();
      const sec = a[0]?.toLowerCase();
      const text = Object.entries(i.sections)
        .filter(([k]) => !sec || sec === 'all' || sec === k)
        .map(([k, v]) => `# ${k[0]!.toUpperCase()}${k.slice(1)}\r\n${Object.entries(v).map(([x, y]) => `${x}:${y}`).join('\r\n')}`)
        .join('\r\n\r\n');
      return { type: 'string', value: text };
    }
    case 'KEYS': {
      need(argv, 2);
      const re = globRe(a[0]!);
      return arr(keysSorted().filter((k) => re.test(k)).map((k) => str(k)));
    }
    case 'SCAN': {
      need(argv, 2);
      let pattern = '*';
      let count = 10;
      let type: string | null = null;
      for (let i = 1; i < a.length; i += 2) {
        const o = a[i]!.toUpperCase();
        if (o === 'MATCH') pattern = a[i + 1] ?? '*';
        if (o === 'COUNT') count = Number(a[i + 1]) || 10;
        if (o === 'TYPE') type = a[i + 1] ?? null;
      }
      const r = redisScan(pattern, a[0]!, count, type);
      return arr([str(r.cursor), arr(r.keys.map((k) => str(k.key)))]);
    }
    case 'EXISTS':
      need(argv, 2);
      return int(a.filter((k) => alive(k)).length);
    case 'TYPE': {
      need(argv, 2);
      const e = alive(a[0]!);
      return { type: 'status', value: e ? e.val.type : 'none' };
    }
    case 'TTL':
    case 'PTTL': {
      need(argv, 2);
      const e = alive(a[0]!);
      const t = !e ? -2 : ttlOf(e);
      return int(c === 'PTTL' && t > 0 ? t * 1000 : t);
    }
    case 'EXPIRE':
    case 'PEXPIRE': {
      need(argv, 3);
      const e = alive(a[0]!);
      if (!e) return int(0);
      const n = Number(a[1]);
      if (!Number.isFinite(n)) throw new RedisErr('ERR value is not an integer or out of range');
      if (n <= 0) db.delete(a[0]!);
      else e.exp = now() + (c === 'PEXPIRE' ? n : n * 1000);
      return int(1);
    }
    case 'PERSIST': {
      need(argv, 2);
      const e = alive(a[0]!);
      if (!e || e.exp === null) return int(0);
      e.exp = null;
      return int(1);
    }
    case 'RENAME':
    case 'RENAMENX': {
      need(argv, 3);
      const e = alive(a[0]!);
      if (!e) throw new RedisErr('ERR no such key');
      if (c === 'RENAMENX' && alive(a[1]!)) return int(0);
      db.delete(a[0]!);
      db.set(a[1]!, e);
      return c === 'RENAMENX' ? int(1) : ok();
    }
    case 'DEL':
    case 'UNLINK': {
      need(argv, 2);
      let n = 0;
      for (const k of a) if (alive(k) && db.delete(k)) n++;
      return int(n);
    }
    case 'FLUSHDB':
    case 'FLUSHALL':
      db.clear();
      return ok();
    case 'GET': {
      need(argv, 2);
      const v = getOf(a[0]!, 'string');
      return str(v ? v.v : null);
    }
    case 'STRLEN': {
      const v = getOf(a[0]!, 'string');
      return int(v ? v.v.length : 0);
    }
    case 'SET': {
      need(argv, 3);
      const e = alive(a[0]!);
      let exp: number | null = null;
      let keepTtl = false;
      for (let i = 2; i < a.length; i++) {
        const o = a[i]!.toUpperCase();
        if (o === 'EX') exp = now() + Number(a[++i]) * 1000;
        else if (o === 'PX') exp = now() + Number(a[++i]);
        else if (o === 'KEEPTTL') keepTtl = true;
        else if (o === 'NX' && e) return str(null);
        else if (o === 'XX' && !e) return str(null);
      }
      db.set(a[0]!, { val: { type: 'string', v: a[1]! }, exp: keepTtl && e ? e.exp : exp });
      return ok();
    }
    case 'SETEX': {
      need(argv, 4);
      db.set(a[0]!, { val: { type: 'string', v: a[2]! }, exp: now() + Number(a[1]) * 1000 });
      return ok();
    }
    case 'MGET':
      return arr(a.map((k) => str(getOf(k, 'string')?.v ?? null)));
    case 'INCR':
    case 'DECR':
    case 'INCRBY':
    case 'DECRBY': {
      need(argv, 2);
      const v = getOf(a[0]!, 'string', () => ({ type: 'string', v: '0' }))!;
      const n = Number(v.v);
      if (!/^-?\d+$/.test(v.v)) throw new RedisErr('ERR value is not an integer or out of range');
      const d = c === 'INCR' ? 1 : c === 'DECR' ? -1 : c === 'INCRBY' ? Number(a[1]) : -Number(a[1]);
      v.v = String(n + d);
      return int(n + d);
    }
    case 'APPEND': {
      const v = getOf(a[0]!, 'string', () => ({ type: 'string', v: '' }))!;
      v.v += a[1] ?? '';
      return int(v.v.length);
    }
    case 'HGET': {
      need(argv, 3);
      return str(getOf(a[0]!, 'hash')?.v.get(a[1]!) ?? null);
    }
    case 'HMGET':
      return arr(a.slice(1).map((f) => str(getOf(a[0]!, 'hash')?.v.get(f) ?? null)));
    case 'HGETALL': {
      need(argv, 2);
      const h = getOf(a[0]!, 'hash');
      return { type: 'map', value: h ? [...h.v].flatMap(([f, v]) => [str(f), str(v)]) : [] };
    }
    case 'HKEYS':
      return arr([...(getOf(a[0]!, 'hash')?.v.keys() ?? [])].map((x) => str(x)));
    case 'HVALS':
      return arr([...(getOf(a[0]!, 'hash')?.v.values() ?? [])].map((x) => str(x)));
    case 'HLEN':
      return int(getOf(a[0]!, 'hash')?.v.size ?? 0);
    case 'HEXISTS':
      return int(getOf(a[0]!, 'hash')?.v.has(a[1]!) ? 1 : 0);
    case 'HSET':
    case 'HMSET': {
      need(argv, 4);
      if ((a.length - 1) % 2) throw new RedisErr(`ERR wrong number of arguments for '${c.toLowerCase()}' command`);
      const h = getOf(a[0]!, 'hash', () => ({ type: 'hash', v: new Map() }))!;
      let n = 0;
      for (let i = 1; i < a.length; i += 2) {
        if (!h.v.has(a[i]!)) n++;
        h.v.set(a[i]!, a[i + 1]!);
      }
      return c === 'HMSET' ? ok() : int(n);
    }
    case 'HDEL': {
      need(argv, 3);
      const h = getOf(a[0]!, 'hash');
      let n = 0;
      for (const f of a.slice(1)) if (h?.v.delete(f)) n++;
      dropEmpty(a[0]!);
      return int(n);
    }
    case 'LPUSH':
    case 'RPUSH': {
      need(argv, 3);
      const l = getOf(a[0]!, 'list', () => ({ type: 'list', v: [] }))!;
      for (const x of a.slice(1)) c === 'LPUSH' ? l.v.unshift(x) : l.v.push(x);
      return int(l.v.length);
    }
    case 'LPOP':
    case 'RPOP': {
      const l = getOf(a[0]!, 'list');
      const x = l ? (c === 'LPOP' ? l.v.shift() : l.v.pop()) : undefined;
      dropEmpty(a[0]!);
      return str(x ?? null);
    }
    case 'LLEN':
      return int(getOf(a[0]!, 'list')?.v.length ?? 0);
    case 'LINDEX': {
      const l = getOf(a[0]!, 'list');
      const i = Number(a[1]);
      return str(l ? (l.v.at(i) ?? null) : null);
    }
    case 'LRANGE': {
      need(argv, 4);
      const l = getOf(a[0]!, 'list')?.v ?? [];
      const n = l.length;
      let s = Number(a[1]);
      let e = Number(a[2]);
      if (s < 0) s = Math.max(0, n + s);
      if (e < 0) e = n + e;
      return arr(l.slice(s, e + 1).map((x) => str(x)));
    }
    case 'LSET': {
      need(argv, 4);
      const l = getOf(a[0]!, 'list');
      if (!l) throw new RedisErr('ERR no such key');
      const i = Number(a[1]);
      const j = i < 0 ? l.v.length + i : i;
      if (j < 0 || j >= l.v.length) throw new RedisErr('ERR index out of range');
      l.v[j] = a[2]!;
      return ok();
    }
    case 'LREM': {
      need(argv, 4);
      const l = getOf(a[0]!, 'list');
      if (!l) return int(0);
      let cnt = Number(a[1]);
      let n = 0;
      const out: string[] = [];
      const src = cnt < 0 ? [...l.v].reverse() : l.v;
      for (const x of src) {
        if (x === a[2] && (cnt === 0 || n < Math.abs(cnt))) {
          n++;
          continue;
        }
        out.push(x);
      }
      l.v = cnt < 0 ? out.reverse() : out;
      cnt = n;
      dropEmpty(a[0]!);
      return int(cnt);
    }
    case 'SADD': {
      need(argv, 3);
      const s = getOf(a[0]!, 'set', () => ({ type: 'set', v: new Set() }))!;
      let n = 0;
      for (const x of a.slice(1))
        if (!s.v.has(x)) {
          s.v.add(x);
          n++;
        }
      return int(n);
    }
    case 'SREM': {
      const s = getOf(a[0]!, 'set');
      let n = 0;
      for (const x of a.slice(1)) if (s?.v.delete(x)) n++;
      dropEmpty(a[0]!);
      return int(n);
    }
    case 'SMEMBERS':
      return { type: 'set', value: [...(getOf(a[0]!, 'set')?.v ?? [])].sort().map((x) => str(x)) };
    case 'SCARD':
      return int(getOf(a[0]!, 'set')?.v.size ?? 0);
    case 'SISMEMBER':
      return int(getOf(a[0]!, 'set')?.v.has(a[1]!) ? 1 : 0);
    case 'ZADD': {
      need(argv, 4);
      const z = getOf(a[0]!, 'zset', () => ({ type: 'zset', v: new Map() }))!;
      let n = 0;
      let i = 1;
      while (i < a.length && /^(NX|XX|GT|LT|CH)$/i.test(a[i]!)) i++;
      for (; i + 1 < a.length; i += 2) {
        const sc = Number(a[i]);
        if (Number.isNaN(sc)) throw new RedisErr('ERR value is not a valid float');
        if (!z.v.has(a[i + 1]!)) n++;
        z.v.set(a[i + 1]!, sc);
      }
      return int(n);
    }
    case 'ZREM': {
      const z = getOf(a[0]!, 'zset');
      let n = 0;
      for (const x of a.slice(1)) if (z?.v.delete(x)) n++;
      dropEmpty(a[0]!);
      return int(n);
    }
    case 'ZINCRBY': {
      const z = getOf(a[0]!, 'zset', () => ({ type: 'zset', v: new Map() }))!;
      const v = (z.v.get(a[2]!) ?? 0) + Number(a[1]);
      z.v.set(a[2]!, v);
      return str(String(v));
    }
    case 'ZCARD':
      return int(getOf(a[0]!, 'zset')?.v.size ?? 0);
    case 'ZSCORE': {
      const s = getOf(a[0]!, 'zset')?.v.get(a[1]!);
      return s === undefined ? str(null) : { type: 'double', value: s };
    }
    case 'ZRANGE':
    case 'ZREVRANGE': {
      need(argv, 4);
      const z = [...(getOf(a[0]!, 'zset')?.v ?? [])].sort((x, y) => x[1] - y[1]);
      if (c === 'ZREVRANGE' || a.some((x) => x.toUpperCase() === 'REV')) z.reverse();
      const n = z.length;
      let s = Number(a[1]);
      let e = Number(a[2]);
      if (s < 0) s = Math.max(0, n + s);
      if (e < 0) e = n + e;
      const ws = a.some((x) => x.toUpperCase() === 'WITHSCORES');
      return arr(z.slice(s, e + 1).flatMap(([m, sc]) => (ws ? [str(m), { type: 'double', value: sc } as RedisReply] : [str(m)])));
    }
    case 'XADD': {
      need(argv, 5);
      const st = getOf(a[0]!, 'stream', () => ({ type: 'stream', v: [] }))!;
      const id = a[1] === '*' ? `${now()}-0` : a[1]!;
      const fields: [string, string][] = [];
      for (let i = 2; i + 1 < a.length; i += 2) fields.push([a[i]!, a[i + 1]!]);
      st.v.push({ id, fields });
      return str(id);
    }
    case 'XLEN':
      return int(getOf(a[0]!, 'stream')?.v.length ?? 0);
    case 'XRANGE':
    case 'XREVRANGE': {
      const st = [...(getOf(a[0]!, 'stream')?.v ?? [])];
      if (c === 'XREVRANGE') st.reverse();
      const cnt = a[3]?.toUpperCase() === 'COUNT' ? Number(a[4]) : st.length;
      return arr(st.slice(0, cnt).map((x) => arr([str(x.id), arr(x.fields.flatMap(([f, v]) => [str(f), str(v)]))])));
    }
    case 'XDEL': {
      const st = getOf(a[0]!, 'stream');
      if (!st) return int(0);
      const before = st.v.length;
      st.v = st.v.filter((x) => !a.slice(1).includes(x.id));
      return int(before - st.v.length);
    }
    case 'MEMORY': {
      if ((a[0] ?? '').toUpperCase() === 'USAGE') {
        const e = alive(a[1] ?? '');
        return e ? int(sizeOf(a[1]!, e)) : str(null);
      }
      throw new RedisErr('ERR unknown subcommand');
    }
    case 'OBJECT': {
      const e = alive(a[1] ?? '');
      if (!e) return str(null);
      return str(redisKey(a[1]!, null, 1)?.encoding ?? 'raw');
    }
    case 'CONFIG': {
      const sub = (a[0] ?? '').toUpperCase();
      if (sub === 'GET') return arr([str(a[1] ?? ''), str(a[1] === 'maxmemory' ? '268435456' : a[1] === 'maxmemory-policy' ? 'allkeys-lru' : '')]);
      if (sub === 'SET') return ok();
      throw new RedisErr(`ERR unknown subcommand '${a[0] ?? ''}'`);
    }
    case 'CLIENT':
      if ((a[0] ?? '').toUpperCase() === 'LIST') return { type: 'string', value: 'id=7 addr=172.18.0.1:51234 laddr=172.18.0.5:6379 fd=8 name=opm-devtools age=12 idle=0 db=0 cmd=client|list\nid=3 addr=172.18.0.7:40210 fd=9 name=northwind-backend age=1209 idle=1 db=0 cmd=get' };
      return { type: 'status', value: 'OK' };
    case 'SLOWLOG':
      return arr(SLOW.slice(0, 10).map((s) => arr([int(s.id), int(Math.floor(Date.parse(s.at) / 1000)), int(s.durationUs), arr(s.command.split(' ').map((x) => str(x)))])));
    case 'EVAL':
    case 'EVALSHA':
      return str(null);
    default:
      throw new RedisErr(`ERR unknown command '${argv[0] ?? ''}', with args beginning with: ${a
        .slice(0, 3)
        .map((x) => `'${x}'`)
        .join(' ')}`);
  }
}
