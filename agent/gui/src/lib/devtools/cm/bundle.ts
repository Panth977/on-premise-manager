/**
 * The CodeMirror 6 bundle of the dev tools (§21 #97). NOT part of the main SPA bundle (bundleStrategy 'single' would
 * inline a dynamic import): scripts/vendor-codemirror.mjs builds this file with esbuild into
 * static/assets/vendor/codemirror.js (embedded with the GUI, no CDN), and lib/devtools/cm/load.ts imports it at run
 * time, only on the dev tools pages. The main bundle only imports TYPES from here.
 *
 * One factory, `createEditor`, for every editor of the dev tools: SQL (PostgreSQL / SQLite / CQL dialects with
 * schema-aware completion, hover cards and a small linter), JSON (folding + parse lint), plain text. Keys: Mod-Enter run
 * (selection, else the statement under the cursor), Shift-Mod-Enter run all, Mod-F find / replace, Mod-/ comment,
 * Alt-click / Mod-D multi-cursor, Ctrl-Shift-[ ] fold.
 */
import { EditorState, Compartment, StateEffect, StateField, RangeSetBuilder, type Extension } from '@codemirror/state';
import {
  EditorView,
  keymap,
  lineNumbers,
  highlightActiveLine,
  highlightActiveLineGutter,
  drawSelection,
  dropCursor,
  rectangularSelection,
  crosshairCursor,
  highlightSpecialChars,
  placeholder as cmPlaceholder,
  hoverTooltip,
  Decoration,
  ViewPlugin,
  MatchDecorator,
  type DecorationSet,
  type ViewUpdate,
} from '@codemirror/view';
import { defaultKeymap, history, historyKeymap, indentWithTab, toggleComment } from '@codemirror/commands';
import { bracketMatching, foldGutter, foldKeymap, indentOnInput, syntaxHighlighting, HighlightStyle, codeFolding } from '@codemirror/language';
import { autocompletion, closeBrackets, closeBracketsKeymap, completionKeymap, type Completion, type CompletionContext } from '@codemirror/autocomplete';
import { searchKeymap, highlightSelectionMatches, search } from '@codemirror/search';
import { linter, lintGutter, type Diagnostic } from '@codemirror/lint';
import { sql, PostgreSQL, SQLite, Cassandra, type SQLNamespace } from '@codemirror/lang-sql';
import { json, jsonParseLinter } from '@codemirror/lang-json';
import { tags as t } from '@lezer/highlight';
import { statementAt } from '../sqlRanges';

export type EditorLang = 'postgres' | 'sqlite' | 'cassandra' | 'json' | 'text';

/** what the SQL editor knows about the database (from the schema endpoint) */
export interface EditorSchema {
  defaultNamespace: string | null;
  namespaces: {
    name: string;
    system: boolean;
    tables: { name: string; kind: string; rowEstimate: number | null; comment: string | null; columns: { name: string; type: string; nullable: boolean; primaryKey: boolean; default: string | null; role?: string | null }[] }[];
    functions: { name: string; arguments: string; returns: string; kind: string }[];
  }[];
}

export interface EditorOptions {
  parent: HTMLElement;
  doc: string;
  lang: EditorLang;
  schema?: EditorSchema | null;
  readOnly?: boolean;
  placeholder?: string;
  /** a one-line editor (no gutters, Enter = onRun) */
  singleLine?: boolean;
  lineNumbers?: boolean;
  /** highlight {{name}} / {{ secret.x }} placeholders (HTTP client) */
  placeholders?: boolean;
  ariaLabel?: string;
  onChange?: (text: string) => void;
  onRun?: () => void;
  onRunAll?: () => void;
  onSave?: () => void;
}

export interface EditorHandle {
  view: EditorView;
  getText(): string;
  setText(text: string): void;
  /** the selection when there is one, else the statement under the cursor (SQL) / the whole text */
  runTarget(): { text: string; from: number };
  all(): { text: string; from: number };
  setSchema(s: EditorSchema | null): void;
  setReadOnly(ro: boolean): void;
  markError(from: number, to: number, message: string): void;
  clearError(): void;
  insert(text: string): void;
  focus(): void;
  select(from: number, to: number): void;
  destroy(): void;
}

// ───────────────────────────── theme (tokens of the GUI → light + dark follow the page)
const theme = EditorView.theme({
  '&': { color: 'var(--text)', backgroundColor: 'var(--surface)', fontSize: '13px', height: '100%' },
  '.cm-scroller': { fontFamily: 'var(--font-mono)', lineHeight: '1.55' },
  '.cm-content': { caretColor: 'var(--accent)', padding: '6px 0' },
  '.cm-cursor, .cm-dropCursor': { borderLeftColor: 'var(--accent)', borderLeftWidth: '2px' },
  '&.cm-focused': { outline: 'none' },
  '&.cm-focused .cm-selectionBackground, .cm-selectionBackground, .cm-content ::selection': { backgroundColor: 'color-mix(in srgb, var(--accent) 22%, transparent) !important' },
  '.cm-activeLine': { backgroundColor: 'color-mix(in srgb, var(--accent) 5%, transparent)' },
  '.cm-gutters': { backgroundColor: 'var(--surface-2)', color: 'var(--text-3)', border: 'none', borderRight: '1px solid var(--border)' },
  '.cm-activeLineGutter': { backgroundColor: 'color-mix(in srgb, var(--accent) 9%, transparent)', color: 'var(--text-2)' },
  '.cm-foldPlaceholder': { backgroundColor: 'var(--accent-soft)', border: 'none', color: 'var(--accent-text)', padding: '0 4px' },
  '.cm-tooltip': { backgroundColor: 'var(--surface)', color: 'var(--text)', border: '1px solid var(--border)', borderRadius: '8px', boxShadow: 'var(--shadow-md)', overflow: 'hidden' },
  '.cm-tooltip-autocomplete > ul': { fontFamily: 'var(--font-mono)', fontSize: '12.5px', maxHeight: '18em' },
  '.cm-tooltip-autocomplete > ul > li': { padding: '2px 8px !important' },
  '.cm-tooltip-autocomplete > ul > li[aria-selected]': { backgroundColor: 'var(--accent-soft)', color: 'var(--text)' },
  '.cm-completionDetail': { color: 'var(--text-3)', fontStyle: 'normal', marginLeft: '10px' },
  '.cm-completionMatchedText': { textDecoration: 'none', color: 'var(--accent-text)', fontWeight: '600' },
  '.cm-completionInfo': { padding: '6px 9px', maxWidth: '320px' },
  '.cm-panels': { backgroundColor: 'var(--surface-2)', color: 'var(--text)', borderColor: 'var(--border)' },
  '.cm-panels.cm-panels-bottom': { borderTop: '1px solid var(--border)' },
  '.cm-search': { fontSize: '12.5px', display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: '4px', padding: '6px 8px' },
  '.cm-search input, .cm-search button, .cm-search label': { fontSize: '12.5px' },
  '.cm-textfield': { backgroundColor: 'var(--surface)', color: 'var(--text)', border: '1px solid var(--border-strong)', borderRadius: '6px', padding: '3px 6px' },
  '.cm-button': { backgroundImage: 'none', backgroundColor: 'var(--surface)', color: 'var(--text)', border: '1px solid var(--border-strong)', borderRadius: '6px', padding: '2px 8px' },
  '.cm-searchMatch': { backgroundColor: 'color-mix(in srgb, var(--amber) 30%, transparent)', outline: '1px solid color-mix(in srgb, var(--amber) 60%, transparent)' },
  '.cm-searchMatch.cm-searchMatch-selected': { backgroundColor: 'color-mix(in srgb, var(--amber) 55%, transparent)' },
  '.cm-selectionMatch': { backgroundColor: 'color-mix(in srgb, var(--accent) 12%, transparent)' },
  '.cm-matchingBracket': { backgroundColor: 'color-mix(in srgb, var(--green) 25%, transparent)', outline: 'none' },
  '.cm-diagnostic': { fontFamily: 'var(--font-sans)', fontSize: '12.5px' },
  '.cm-lintRange-warning': { backgroundImage: 'none', textDecoration: 'underline wavy var(--amber)', textUnderlineOffset: '3px' },
  '.cm-lintRange-error': { backgroundImage: 'none', textDecoration: 'underline wavy var(--red)', textUnderlineOffset: '3px' },
  '.cm-err-mark': { backgroundColor: 'color-mix(in srgb, var(--red) 22%, transparent)', borderBottom: '2px solid var(--red)' },
  '.cm-err-line': { backgroundColor: 'color-mix(in srgb, var(--red) 7%, transparent)' },
  '.cm-ph': { color: 'var(--green-text)', backgroundColor: 'var(--green-soft)', borderRadius: '3px' },
  '.cm-ph-opm': { color: 'var(--violet-text)', backgroundColor: 'var(--violet-soft)', borderRadius: '3px' },
  '.cm-placeholder': { color: 'var(--text-3)' },
  '.dt-hover': { padding: '8px 10px', fontSize: '12.5px', maxWidth: '360px', fontFamily: 'var(--font-sans)' },
  '.dt-hover .h': { fontWeight: '600', fontFamily: 'var(--font-mono)' },
  '.dt-hover .k': { color: 'var(--text-3)', fontSize: '11.5px' },
  '.dt-hover table': { borderCollapse: 'collapse', marginTop: '4px', fontFamily: 'var(--font-mono)', fontSize: '12px' },
  '.dt-hover td': { padding: '1px 8px 1px 0', verticalAlign: 'top' },
  '.dt-hover .pk': { color: 'var(--amber-text)' },
});
const singleTheme = EditorView.theme({
  '.cm-content': { padding: '0' },
  '.cm-line': { padding: '0 2px' },
  '&': { height: 'auto' },
  '.cm-scroller': { overflow: 'hidden', lineHeight: '30px' },
});
const highlight = HighlightStyle.define([
  { tag: [t.keyword, t.operatorKeyword, t.modifier], color: 'var(--violet-text)', fontWeight: '600' },
  { tag: [t.typeName, t.standard(t.name)], color: 'var(--blue-text)' },
  { tag: [t.string, t.special(t.string)], color: 'var(--green-text)' },
  { tag: [t.number, t.bool, t.null, t.atom], color: 'var(--amber-text)' },
  { tag: [t.comment, t.lineComment, t.blockComment], color: 'var(--text-3)', fontStyle: 'italic' },
  { tag: [t.propertyName], color: 'var(--blue-text)' },
  { tag: [t.special(t.name), t.variableName], color: 'var(--text)' },
  { tag: [t.punctuation, t.bracket, t.operator], color: 'var(--text-2)' },
  { tag: t.invalid, color: 'var(--red-text)' },
]);

// ───────────────────────────── schema helpers
function dialectOf(lang: EditorLang) {
  return lang === 'sqlite' ? SQLite : lang === 'cassandra' ? Cassandra : PostgreSQL;
}
function colDetail(c: EditorSchema['namespaces'][number]['tables'][number]['columns'][number]) {
  return `${c.type}${c.primaryKey ? ' · PK' : ''}${c.nullable ? '' : ' · not null'}`;
}
function sqlNamespace(s: EditorSchema): SQLNamespace {
  const out: Record<string, SQLNamespace> = {};
  for (const ns of s.namespaces) {
    const children: Record<string, SQLNamespace> = {};
    for (const tb of ns.tables)
      children[tb.name] = {
        self: { label: tb.name, type: tb.kind === 'view' || tb.kind === 'materialized-view' ? 'interface' : 'class', detail: `${tb.kind}${tb.rowEstimate !== null ? ` · ~${tb.rowEstimate} rows` : ''}`, info: tb.comment ?? undefined },
        children: tb.columns.map((c) => ({ label: c.name, type: c.primaryKey ? 'constant' : 'property', detail: colDetail(c), boost: c.primaryKey ? 2 : 0 }) as Completion),
      };
    out[ns.name] = { self: { label: ns.name, type: 'namespace', detail: ns.system ? 'system' : 'schema' }, children };
  }
  return out;
}
function langExt(lang: EditorLang, schema: EditorSchema | null | undefined): Extension {
  if (lang === 'json') {
    const lint = jsonParseLinter();
    return [json(), linter((v) => (v.state.doc.toString().trim() ? lint(v) : []), { delay: 400 })];
  }
  if (lang === 'text') return [];
  const fns: Completion[] = [];
  for (const ns of schema?.namespaces ?? []) if (!ns.system) for (const f of ns.functions) fns.push({ label: f.name, type: 'function', detail: `(${f.arguments}) → ${f.returns}`, apply: `${f.name}(` });
  // keywords rank below schema names (a column "id" must beat IDENTITY / OIDS / …)
  const lang_ = sql({ dialect: dialectOf(lang), schema: schema ? sqlNamespace(schema) : undefined, defaultSchema: schema?.defaultNamespace ?? undefined, upperCaseKeywords: true, keywordCompletion: (label, type) => ({ label, type, boost: -30 }) });
  const cols = schema ? columnSource(schema) : null;
  return [lang_, cols ? lang_.language.data.of({ autocomplete: cols }) : [], schema ? sqlLint(lang, schema) : [], fns.length ? lang_.language.data.of({ autocomplete: (ctx: CompletionContext) => {
    const w = ctx.matchBefore(/\w+/);
    if (!w || (w.from === w.to && !ctx.explicit)) return null;
    return { from: w.from, options: fns, validFor: /^\w*$/ };
  } }) : []];
}

const IDENT = /^[A-Za-z_][A-Za-z0-9_$]*$/;
/**
 * Columns of the tables the current statement references (FROM / JOIN / UPDATE / INTO), ranked first in unqualified
 * positions (SELECT list, WHERE, ORDER BY, GROUP BY, SET …). Qualified "t." completion stays with lang-sql.
 */
function columnSource(s: EditorSchema) {
  return (ctx: CompletionContext) => {
    const w = ctx.matchBefore(/[\w$]*/);
    if (!w) return null;
    if (w.from > 0 && ctx.state.sliceDoc(w.from - 1, w.from) === '.') return null;
    if (w.from === w.to && !ctx.explicit) return null;
    const text = ctx.state.doc.toString();
    const st = statementAt(text, ctx.pos);
    const body = maskLiterals(st ? text.slice(Math.max(0, st.from - 1), Math.max(st.end, ctx.pos)) : text);
    const before = maskLiterals(text.slice(Math.max(0, (st?.from ?? 0) - 1), w.from));
    // right after FROM / JOIN / INTO / UPDATE / TABLE a table name is expected, not a column
    if (/\b(from|join|into|update|table)\s+$/i.test(before)) return null;
    const seen = new Set<string>();
    const options: Completion[] = [];
    for (const m of body.matchAll(/\b(?:from|join|update|into)\s+((?:"[^"]+"|[A-Za-z_][\w$]*)(?:\.(?:"[^"]+"|[A-Za-z_][\w$]*))?)/gi)) {
      const parts = m[1]!.split('.');
      const hit = parts.length === 2 ? findTable(s, parts[1]!, parts[0]) : findTable(s, parts[0]!);
      if (!hit) continue;
      for (const c of hit.tb.columns) {
        if (seen.has(c.name)) continue;
        seen.add(c.name);
        options.push({ label: c.name, type: c.primaryKey ? 'constant' : 'property', detail: `${c.type}${c.primaryKey ? ' · PK' : ''} · ${hit.tb.name}`, boost: c.primaryKey ? 60 : 50 });
      }
    }
    if (!options.length) return null;
    return { from: w.from, options, validFor: /^[\w$]*$/ };
  };
}
function findTable(s: EditorSchema, name: string, ns?: string) {
  const n = name.replace(/^"|"$/g, '');
  const nsList = ns ? s.namespaces.filter((x) => x.name === ns.replace(/^"|"$/g, '')) : s.namespaces;
  for (const x of nsList) {
    const tb = x.tables.find((t) => t.name === n) ?? x.tables.find((t) => t.name.toLowerCase() === n.toLowerCase());
    if (tb) return { ns: x, tb };
  }
  return null;
}

/** linter: unknown tables after FROM / JOIN / INTO / UPDATE / TABLE, unknown columns of a known alias.column */
function sqlLint(lang: EditorLang, s: EditorSchema): Extension {
  const words = /\b(from|join|into|update|table)\s+((?:"[^"]+"|[A-Za-z_][\w$]*)(?:\.(?:"[^"]+"|[A-Za-z_][\w$]*))?)/gi;
  return linter(
    (view) => {
      const text = view.state.doc.toString();
      const masked = maskLiterals(text);
      const out: Diagnostic[] = [];
      const aliases = new Map<string, ReturnType<typeof findTable>>();
      for (const m of masked.matchAll(words)) {
        const ref = m[2]!;
        const kw = m[1]!.toLowerCase();
        if (kw === 'table' && !/\b(create|alter|drop|truncate)\s+table\s*$/i.test(masked.slice(Math.max(0, m.index! - 20), m.index! + 5 + m[1]!.length))) continue;
        if (/^(if|only|lateral|select|values|set|not)$/i.test(ref)) continue;
        if (/\bcreate\s+table\s*$/i.test(masked.slice(Math.max(0, m.index! - 8), m.index! + m[1]!.length))) continue;
        const parts = ref.split('.');
        const hit = parts.length === 2 ? findTable(s, parts[1]!, parts[0]) : findTable(s, parts[0]!);
        const from = m.index! + m[0].length - ref.length;
        if (!hit) {
          // a CTE name or a function call is not a table
          if (new RegExp(`\\b${escapeRe(parts[parts.length - 1]!)}\\s+as\\s*\\(`, 'i').test(masked)) continue;
          if (masked[from + ref.length] === '(') continue;
          out.push({ from, to: from + ref.length, severity: 'warning', message: `Unknown ${lang === 'cassandra' ? 'table' : 'table or view'} "${ref}" (not in the loaded schema — refresh the schema if it was just created)` });
          continue;
        }
        const after = /^\s+(?:as\s+)?([A-Za-z_][\w$]*)/i.exec(masked.slice(from + ref.length));
        const alias = after && !/^(where|join|on|left|right|inner|outer|full|cross|group|order|limit|set|values|using|natural|union|returning|select)$/i.test(after[1]!) ? after[1]! : null;
        aliases.set((parts[parts.length - 1] ?? '').toLowerCase(), hit);
        if (alias) aliases.set(alias.toLowerCase(), hit);
      }
      for (const m of masked.matchAll(/\b([A-Za-z_][\w$]*)\.([A-Za-z_][\w$]*)\b/g)) {
        const hit = aliases.get(m[1]!.toLowerCase());
        if (!hit) continue;
        if (s.namespaces.some((n) => n.name === m[1])) continue;
        const col = m[2]!;
        if (!hit.tb.columns.some((c) => c.name.toLowerCase() === col.toLowerCase())) {
          const from = m.index! + m[1]!.length + 1;
          out.push({ from, to: from + col.length, severity: 'warning', message: `${hit.tb.name} has no column "${col}"` });
        }
      }
      return out;
    },
    { delay: 500 },
  );
}
function escapeRe(s: string) {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}
/** same length, string literals and comments blanked (offsets kept) */
function maskLiterals(text: string): string {
  return text.replace(/--[^\n]*|\/\*[\s\S]*?\*\/|'(?:[^']|'')*'/g, (m) => m.replace(/[^\n]/g, ' '));
}

function hover(getSchema: () => EditorSchema | null) {
  return hoverTooltip((view, pos) => {
    const s = getSchema();
    if (!s) return null;
    const line = view.state.doc.lineAt(pos);
    const off = pos - line.from;
    const text = line.text;
    let a = off;
    let b = off;
    while (a > 0 && /[\w$]/.test(text[a - 1]!)) a--;
    while (b < text.length && /[\w$]/.test(text[b]!)) b++;
    if (a === b) return null;
    const word = text.slice(a, b);
    const qual = a > 1 && text[a - 1] === '.' ? /([\w$]+)$/.exec(text.slice(0, a - 1))?.[1] : undefined;
    const nsHit = !qual && s.namespaces.find((n) => n.name === word);
    const tbl = findTable(s, word, qual && s.namespaces.some((n) => n.name === qual) ? qual : undefined);
    let html: string | null = null;
    if (tbl) {
      const rows = tbl.tb.columns
        .slice(0, 14)
        .map((c) => `<tr><td class="${c.primaryKey ? 'pk' : ''}">${c.primaryKey ? '🔑 ' : ''}${esc(c.name)}</td><td class="k">${esc(c.type)}${c.nullable ? '' : ' not null'}</td></tr>`)
        .join('');
      html = `<div class="h">${esc(tbl.ns.name)}.${esc(tbl.tb.name)}</div><div class="k">${esc(tbl.tb.kind)}${tbl.tb.rowEstimate !== null ? ` · ~${tbl.tb.rowEstimate.toLocaleString()} rows` : ''} · ${tbl.tb.columns.length} columns</div>${tbl.tb.comment ? `<div>${esc(tbl.tb.comment)}</div>` : ''}<table>${rows}</table>${tbl.tb.columns.length > 14 ? `<div class="k">… ${tbl.tb.columns.length - 14} more</div>` : ''}`;
    } else if (nsHit) {
      html = `<div class="h">${esc(nsHit.name)}</div><div class="k">${nsHit.system ? 'system ' : ''}namespace · ${nsHit.tables.length} tables · ${nsHit.functions.length} functions</div>`;
    } else {
      const owners: string[] = [];
      let col: EditorSchema['namespaces'][number]['tables'][number]['columns'][number] | null = null;
      const qt = qual ? findTable(s, qual) : null;
      const scan = qt ? [{ ns: qt.ns, tables: [qt.tb] }] : s.namespaces.filter((n) => !n.system).map((n) => ({ ns: n, tables: n.tables }));
      for (const x of scan)
        for (const tb of x.tables) {
          const c = tb.columns.find((cc) => cc.name === word);
          if (c) {
            col ??= c;
            owners.push(`${x.ns.name}.${tb.name}`);
          }
        }
      if (col) html = `<div class="h">${col.primaryKey ? '🔑 ' : ''}${esc(col.name)}</div><div>${esc(col.type)}${col.nullable ? ' · nullable' : ' · not null'}${col.default ? ` · default ${esc(col.default)}` : ''}${col.role ? ` · ${esc(col.role)}` : ''}</div><div class="k">column of ${owners.slice(0, 4).map(esc).join(', ')}${owners.length > 4 ? ` +${owners.length - 4}` : ''}</div>`;
      else {
        const fn = s.namespaces.flatMap((n) => n.functions).find((f) => f.name === word);
        if (fn) html = `<div class="h">${esc(fn.name)}(${esc(fn.arguments)})</div><div class="k">${esc(fn.kind)} → ${esc(fn.returns)}</div>`;
      }
    }
    if (!html) return null;
    const h = html;
    return {
      pos: line.from + a,
      end: line.from + b,
      above: true,
      create() {
        const dom = document.createElement('div');
        dom.className = 'dt-hover';
        dom.innerHTML = h;
        return { dom };
      },
    };
  });
}
function esc(s: string) {
  return s.replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' })[c]!);
}

// ───────────────────────────── error mark (statement error position)
const setErr = StateEffect.define<{ from: number; to: number } | null>();
const errField = StateField.define<DecorationSet>({
  create: () => Decoration.none,
  update(v, tr) {
    v = v.map(tr.changes);
    for (const e of tr.effects)
      if (e.is(setErr)) {
        if (!e.value) v = Decoration.none;
        else {
          const b = new RangeSetBuilder<Decoration>();
          const line = tr.state.doc.lineAt(e.value.from);
          b.add(line.from, line.from, Decoration.line({ class: 'cm-err-line' }));
          if (e.value.to > e.value.from) b.add(e.value.from, e.value.to, Decoration.mark({ class: 'cm-err-mark' }));
          v = b.finish();
        }
      }
    if (tr.docChanged) v = Decoration.none;
    return v;
  },
  provide: (f) => EditorView.decorations.from(f),
});

// ───────────────────────────── {{ placeholders }}
const phDeco = new MatchDecorator({
  regexp: /\{\{\s*([^{}]+?)\s*\}\}/g,
  decoration: (m) => Decoration.mark({ class: /^(var|secret|site|self|deployment|containers|container|file)(\.|\[|$)|\|/.test(m[1]!.trim()) ? 'cm-ph-opm' : 'cm-ph' }),
});
const placeholdersExt = ViewPlugin.fromClass(
  class {
    decorations: DecorationSet;
    constructor(view: EditorView) {
      this.decorations = phDeco.createDeco(view);
    }
    update(u: ViewUpdate) {
      this.decorations = phDeco.updateDeco(u, this.decorations);
    }
  },
  { decorations: (v) => v.decorations },
);

export function createEditor(o: EditorOptions): EditorHandle {
  let schema = o.schema ?? null;
  const langC = new Compartment();
  const roC = new Compartment();
  const isSql = o.lang === 'postgres' || o.lang === 'sqlite' || o.lang === 'cassandra';
  const runKeys = keymap.of([
    { key: 'Mod-Enter', preventDefault: true, run: () => (o.onRun?.(), !!o.onRun) },
    { key: 'Shift-Mod-Enter', preventDefault: true, run: () => (o.onRunAll?.(), !!o.onRunAll) },
    { key: 'Mod-s', preventDefault: true, run: () => (o.onSave?.(), !!o.onSave) },
    { key: 'Mod-/', run: toggleComment },
    ...(o.singleLine ? [{ key: 'Enter', preventDefault: true, run: () => (o.onRun?.(), true) }] : []),
  ]);
  const exts: Extension[] = [
    runKeys,
    o.singleLine ? [] : o.lineNumbers === false ? [] : [lineNumbers(), highlightActiveLineGutter(), foldGutter({ markerDOM: (open) => {
      const e = document.createElement('span');
      e.textContent = open ? '▾' : '▸';
      e.style.cssText = 'color:var(--text-3);cursor:pointer;padding:0 2px';
      return e;
    } })],
    highlightSpecialChars(),
    history(),
    drawSelection(),
    dropCursor(),
    EditorState.allowMultipleSelections.of(true),
    indentOnInput(),
    syntaxHighlighting(highlight),
    bracketMatching(),
    closeBrackets(),
    autocompletion({ activateOnTyping: true, icons: false }),
    rectangularSelection(),
    crosshairCursor(),
    o.singleLine ? [] : highlightActiveLine(),
    highlightSelectionMatches(),
    codeFolding(),
    search({ top: false }),
    keymap.of([...closeBracketsKeymap, ...defaultKeymap, ...searchKeymap, ...historyKeymap, ...foldKeymap, ...completionKeymap, ...(o.singleLine ? [] : [indentWithTab])]),
    theme,
    o.singleLine ? [singleTheme, EditorState.transactionFilter.of((tr) => (tr.newDoc.lines > 1 ? [] : tr))] : [],
    langC.of(langExt(o.lang, schema)),
    isSql ? [hover(() => schema), lintGutter()] : o.lang === 'json' ? lintGutter() : [],
    errField,
    o.placeholders ? placeholdersExt : [],
    o.placeholder ? cmPlaceholder(o.placeholder) : [],
    roC.of([EditorState.readOnly.of(!!o.readOnly), EditorView.editable.of(!o.readOnly)]),
    EditorView.contentAttributes.of({ 'aria-label': o.ariaLabel ?? 'Editor', spellcheck: 'false', autocapitalize: 'off', autocorrect: 'off' }),
    EditorView.updateListener.of((u) => {
      if (u.docChanged) o.onChange?.(u.state.doc.toString());
    }),
    EditorView.lineWrapping,
  ];
  const view = new EditorView({ parent: o.parent, state: EditorState.create({ doc: o.doc, extensions: exts }) });
  return {
    view,
    getText: () => view.state.doc.toString(),
    setText(text) {
      if (text === view.state.doc.toString()) return;
      view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: text } });
    },
    runTarget() {
      const sel = view.state.selection.main;
      const text = view.state.doc.toString();
      if (!sel.empty) return { text: view.state.sliceDoc(sel.from, sel.to), from: sel.from };
      if (!isSql) return { text, from: 0 };
      const r = statementAt(text, sel.head);
      return r ? { text: r.text, from: r.from } : { text: '', from: 0 };
    },
    all() {
      return { text: view.state.doc.toString(), from: 0 };
    },
    setSchema(s) {
      schema = s;
      view.dispatch({ effects: langC.reconfigure(langExt(o.lang, s)) });
    },
    setReadOnly(ro) {
      view.dispatch({ effects: roC.reconfigure([EditorState.readOnly.of(ro), EditorView.editable.of(!ro)]) });
    },
    markError(from, to, _message) {
      const len = view.state.doc.length;
      const f = Math.max(0, Math.min(len, from));
      let e = Math.max(f, Math.min(len, to));
      if (e === f) {
        const w = /^[\w$"]+/.exec(view.state.sliceDoc(f, Math.min(len, f + 64)));
        e = f + (w ? w[0].length : Math.min(1, len - f));
      }
      view.dispatch({ effects: setErr.of({ from: f, to: e }), selection: { anchor: f }, scrollIntoView: true });
    },
    clearError() {
      view.dispatch({ effects: setErr.of(null) });
    },
    insert(text) {
      const sel = view.state.selection.main;
      view.dispatch({ changes: { from: sel.from, to: sel.to, insert: text }, selection: { anchor: sel.from + text.length } });
      view.focus();
    },
    focus: () => view.focus(),
    select(from, to) {
      view.dispatch({ selection: { anchor: from, head: to }, scrollIntoView: true });
      view.focus();
    },
    destroy: () => view.destroy(),
  };
}

/** the IDENT rule (exported for tests of the bundle) */
export const isIdent = (s: string) => IDENT.test(s);
