/**
 * Expression autocomplete (SPEC §10): the paths a field may use, from the resolve context, and the token under the
 * caret inside an open "{{ … " block.
 */
import { CONTAINERS_ENV_FIELDS, FIELD_ROOTS, FILTERS, type ExprContext, type ExprField, type ExprValue } from '@opm/shared/expr';

export interface Suggestion {
  /** text inserted for the token (a path, or a filter name) */
  insert: string;
  hint: string;
  group: string;
}

const short = (v: ExprValue | undefined): string => {
  if (v === undefined) return '';
  if (v === null) return 'null';
  const s = typeof v === 'string' ? v : JSON.stringify(v);
  return s.length > 40 ? s.slice(0, 39) + '…' : s;
};

/** every path allowed in `field`, with a short preview of the value */
export function exprPaths(field: ExprField, ctx: ExprContext | null, envNames: string[] = []): Suggestion[] {
  const roots = FIELD_ROOTS[field];
  const out: Suggestion[] = [];
  const add = (insert: string, hint: string, group: string) => out.push({ insert, hint, group });
  for (const r of roots) {
    switch (r) {
      case 'container':
        for (const n of envNames) add(`container.env.${n}`, 'env of this container', 'container');
        add('container.slug', ctx?.container?.slug ?? 'container name', 'container');
        break;
      case 'var':
        for (const [k, v] of Object.entries(ctx?.var ?? {})) add(`var.${k}`, short(v), 'variables');
        break;
      case 'secret':
        for (const k of Object.keys(ctx?.secret ?? {})) add(`secret.${k}`, 'secret (masked)', 'secrets');
        break;
      case 'file':
        for (const [k, f] of Object.entries(ctx?.file ?? {})) add(`file.${k}.path`, f.path ?? 'not mounted here', 'files');
        break;
      case 'site':
        for (const [s, info] of Object.entries(ctx?.site ?? {})) {
          add(`site.${s}.host`, info.host ?? 'auto · not reported yet', 'sites');
          for (const [cs, ports] of Object.entries(info.port)) for (const [p, n] of Object.entries(ports)) add(`site.${s}.port.${cs}.${p}`, n === null ? 'no host port' : String(n), 'sites');
        }
        break;
      case 'self':
        add('self.slug', ctx?.self.slug ?? '', 'this site');
        add('self.host', ctx?.self.host ?? 'auto · not reported yet', 'this site');
        break;
      case 'deployment':
        add('deployment.id', ctx?.deployment.id ?? '', 'deployment');
        break;
      case 'containers': {
        // §21 #88: other containers of this site — names first, then their internal ports
        const cs = Object.entries(ctx?.containers ?? {});
        for (const [c, info] of cs) add(`containers.${c}.host`, info.enabled ? `${info.host} (site network)` : `${info.host} · disabled`, 'containers');
        for (const [c, info] of cs) for (const [p, n] of Object.entries(info.port)) add(`containers.${c}.port.${p}`, n === null ? 'does not resolve' : `${n} (internal)`, 'containers');
        // §21 #99: another container's env — dev-tool fields only
        if (CONTAINERS_ENV_FIELDS.includes(field)) for (const [c, info] of cs) for (const [n, e] of Object.entries(info.env ?? {})) add(`containers.${c}.env.${n}`, e.secret ? 'secret (masked)' : e.value === null ? 'env (no value)' : short(e.value), 'containers');
        break;
      }
    }
  }
  return out;
}

export interface Token {
  /** start index of the token in the text */
  from: number;
  /** caret index (end of the token) */
  to: number;
  text: string;
  /** "path" = a path position; "filter" = right after "|" */
  kind: 'path' | 'filter';
  /** the block has no "}}" after the caret */
  unclosed: boolean;
}

/** the token under the caret when the caret is inside "{{ … }}" (or an unclosed "{{"), else null */
export function tokenAt(text: string, caret: number): Token | null {
  const before = text.slice(0, caret);
  const open = before.lastIndexOf('{{');
  if (open < 0) return null;
  const close = before.lastIndexOf('}}');
  if (close > open) return null;
  const inside = before.slice(open + 2);
  if (/["\n\r]/.test(inside.replace(/"(?:[^"\\]|\\.)*"/g, ''))) return null; // inside a string literal
  const m = /[A-Za-z0-9._-]*$/.exec(inside)!;
  const tokText = m[0];
  const pre = inside.slice(0, inside.length - tokText.length).trimEnd();
  const kind: Token['kind'] = pre.endsWith('|') ? 'filter' : 'path';
  if (kind === 'path' && pre.trim() !== '') return null; // e.g. after a complete path, before "|"
  const after = text.slice(caret);
  const nextClose = after.indexOf('}}');
  const nextOpen = after.indexOf('{{');
  return { from: caret - tokText.length, to: caret, text: tokText, kind, unclosed: nextClose < 0 || (nextOpen >= 0 && nextOpen < nextClose) };
}

export function suggestFor(tok: Token, paths: Suggestion[]): Suggestion[] {
  const q = tok.text.toLowerCase();
  if (tok.kind === 'filter') return FILTERS.filter((f) => f.startsWith(q)).map((f) => ({ insert: f === 'default' ? 'default("")' : f === 'join' ? 'join(",")' : f, hint: 'filter', group: 'filters' }));
  return paths.filter((p) => p.insert.toLowerCase().startsWith(q) || (q.length > 1 && p.insert.toLowerCase().includes(q))).slice(0, 60);
}
