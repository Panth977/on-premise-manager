/** Redis helpers of the dev tools (§21 #97): reply rendering, key tree, type colours, value formatting. */
import type { RedisKeyRow, RedisReply } from '@opm/shared/devtools';

export const TYPE_TONE: Record<string, string> = { string: 'blue', hash: 'violet', list: 'green', set: 'amber', zset: 'red', stream: 'accent', 'ReJSON-RL': 'blue' };
export const TYPE_SHORT: Record<string, string> = { string: 'STR', hash: 'HASH', list: 'LIST', set: 'SET', zset: 'ZSET', stream: 'STRM', 'ReJSON-RL': 'JSON' };

export function ttlText(t: number): string {
  if (t === -1) return 'no expiry';
  if (t === -2) return 'gone';
  if (t < 60) return `${t} s`;
  if (t < 3600) return `${Math.floor(t / 60)} m ${t % 60} s`;
  if (t < 86_400) return `${Math.floor(t / 3600)} h ${Math.floor((t % 3600) / 60)} m`;
  return `${Math.floor(t / 86_400)} d ${Math.floor((t % 86_400) / 3600)} h`;
}

export interface KeyNode {
  name: string;
  path: string;
  children: Map<string, KeyNode>;
  key: RedisKeyRow | null;
  count: number;
}
/** keys split on ":" into folders (a key can also be a folder: "user" and "user:1") */
export function keyTree(keys: RedisKeyRow[], sep = ':'): KeyNode {
  const root: KeyNode = { name: '', path: '', children: new Map(), key: null, count: 0 };
  for (const k of keys) {
    const parts = k.key.split(sep);
    let n = root;
    n.count++;
    for (let i = 0; i < parts.length; i++) {
      const last = i === parts.length - 1;
      const id = last ? `\u0000${parts[i]}` : parts[i]!;
      let c = n.children.get(id);
      if (!c) {
        c = { name: parts[i]!, path: parts.slice(0, i + 1).join(sep), children: new Map(), key: null, count: 0 };
        n.children.set(id, c);
      }
      if (last) c.key = k;
      else c.count++;
      n = c;
    }
  }
  return root;
}
export function sortedChildren(n: KeyNode): KeyNode[] {
  return [...n.children.values()].sort((a, b) => (a.key && !b.key ? 1 : !a.key && b.key ? -1 : a.name.localeCompare(b.name, undefined, { numeric: true })));
}

/** redis-cli like text of a reply (copy / plain view) */
export function replyText(r: RedisReply | null, indent = ''): string {
  if (!r) return '(nil)';
  const v = r.value;
  switch (r.type) {
    case 'nil':
      return '(nil)';
    case 'integer':
      return `(integer) ${v}`;
    case 'double':
      return `(double) ${v}`;
    case 'bool':
      return `(boolean) ${v}`;
    case 'status':
      return String(v);
    case 'error':
      return `(error) ${v}`;
    case 'string':
      return JSON.stringify(String(v));
    case 'array':
    case 'set':
    case 'map': {
      const list = (Array.isArray(v) ? v : []) as RedisReply[];
      if (!list.length) return r.type === 'map' ? '(empty hash)' : '(empty array)';
      if (r.type === 'map') {
        const out: string[] = [];
        for (let i = 0; i + 1 < list.length; i += 2) out.push(`${indent}${i / 2 + 1}# ${replyText(list[i]!)} => ${replyText(list[i + 1]!, indent + '   ')}`);
        return out.join('\n');
      }
      return list.map((x, i) => `${i ? indent : ''}${i + 1}) ${replyText(x as RedisReply, indent + '   ')}`).join('\n');
    }
    default:
      return typeof v === 'string' ? v : JSON.stringify(v);
  }
}
/** a console line may hold a credential → not kept in the local history */
export function sensitiveLine(line: string): boolean {
  return /^\s*(auth|hello\b.*\bauth|config\s+set\s+\S*(pass|auth)|acl\s+setuser|migrate\b.*\bauth)/i.test(line);
}
export function quoteArg(a: string): string {
  return a === '' || /[\s"'\\]/.test(a) ? JSON.stringify(a) : a;
}
export function isJsonText(s: string): boolean {
  const t = s.trim();
  if (!/^[[{]/.test(t)) return false;
  try {
    JSON.parse(t);
    return true;
  } catch {
    return false;
  }
}
