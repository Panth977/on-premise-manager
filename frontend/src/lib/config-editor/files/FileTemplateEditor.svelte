<script lang="ts" module>
  export interface PathSuggestion {
    label: string;
    detail: string;
    kind: string;
  }
  export interface EditorMark {
    line: number;
    col: number;
  }
  /** a {{ }} block on one line (strings may hold "}}" — e.g. the escape {{ "{{" }}) */
  export const BLOCK_RE = /\{\{(?:[^"}\n]|"(?:[^"\\\n]|\\.)*"|\}(?!\}))*\}\}/g;
</script>

<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import type { EditorView } from '@codemirror/view';
  import type { StateEffectType } from '@codemirror/state';
  import { FILTERS } from '@opm/shared';
  import { highlightSpec, panelTheme } from '../../ui/cm-theme';

  /**
   * Code editor for file content with expressions: highlights {{ }} blocks, autocompletes paths after "{{ " and filters
   * after "|", marks error positions (line:col), and jumps to a position (jumpTo). `expressions` false = plain text.
   */
  type Props = {
    value: string;
    lang?: 'html' | 'json' | 'text';
    expressions?: boolean;
    paths?: PathSuggestion[];
    marks?: EditorMark[];
    readonly?: boolean;
    minHeight?: string;
    invalid?: boolean;
    label?: string;
    testid?: string;
  };
  let { value = $bindable(''), lang = 'text', expressions = true, paths = [], marks = [], readonly = false, minHeight = '320px', invalid = false, label = 'File content', testid = 'file-content' }: Props = $props();

  let host = $state<HTMLDivElement | null>(null);
  let view: EditorView | null = null;
  let ready = $state(false);
  let lastEmitted = value;
  // read by the CodeMirror callbacks (not reactive there)
  let pathList: PathSuggestion[] = [];
  let exprOn = true;
  let reconf: ((on: boolean) => void) | null = null;
  let setMarks: StateEffectType<{ from: number; to: number }[]> | null = null;

  /** UTF-16 offset of a 1-based line + code-point column */
  function offsetOf(line: number, col: number): number {
    if (!view) return 0;
    const doc = view.state.doc;
    const l = doc.line(Math.max(1, Math.min(doc.lines, line)));
    const pre = Array.from(l.text).slice(0, Math.max(0, col - 1)).join('');
    return l.from + Math.min(pre.length, l.length);
  }
  /** move the cursor to line:col (1-based, code points), scroll it into view and focus */
  export function jumpTo(line: number, col: number) {
    if (!view) return;
    const pos = offsetOf(line, col);
    view.dispatch({ selection: { anchor: pos }, scrollIntoView: true });
    view.focus();
  }

  onMount(async () => {
    const [cmView, { EditorState, StateField, StateEffect, Compartment, RangeSetBuilder }, { defaultKeymap, history, historyKeymap, indentWithTab }, { syntaxHighlighting, HighlightStyle, bracketMatching, indentOnInput }, { searchKeymap, highlightSelectionMatches }, { autocompletion, completionKeymap }, { tags }] = await Promise.all([
      import('@codemirror/view'),
      import('@codemirror/state'),
      import('@codemirror/commands'),
      import('@codemirror/language'),
      import('@codemirror/search'),
      import('@codemirror/autocomplete'),
      import('@lezer/highlight'),
    ]);
    const { EditorView, keymap, lineNumbers, highlightActiveLine, highlightActiveLineGutter, drawSelection, Decoration, MatchDecorator, ViewPlugin } = cmView;
    type DecoSet = import('@codemirror/view').DecorationSet;
    const langExt = lang === 'html' ? (await import('@codemirror/lang-html')).html() : lang === 'json' ? (await import('@codemirror/lang-json')).json() : [];

    // {{ }} blocks
    const blockMark = Decoration.mark({ class: 'cm-opm-expr' });
    const matcher = new MatchDecorator({ regexp: BLOCK_RE, decoration: () => blockMark });
    const blocks = ViewPlugin.fromClass(
      class {
        decorations: DecoSet;
        constructor(v: EditorView) {
          this.decorations = matcher.createDeco(v);
        }
        update(u: import('@codemirror/view').ViewUpdate) {
          this.decorations = matcher.updateDeco(u, this.decorations);
        }
      },
      { decorations: (p) => p.decorations },
    );

    // error marks
    const effect = StateEffect.define<{ from: number; to: number }[]>();
    setMarks = effect;
    const errMark = Decoration.mark({ class: 'cm-opm-err' });
    const errField = StateField.define<DecoSet>({
      create: () => Decoration.none,
      update(set, tr) {
        set = set.map(tr.changes);
        for (const e of tr.effects)
          if (e.is(effect)) {
            const b = new RangeSetBuilder<import('@codemirror/view').Decoration>();
            for (const r of [...e.value].sort((a, z) => a.from - z.from)) if (r.to > r.from) b.add(r.from, r.to, errMark);
            set = b.finish();
          }
        return set;
      },
      provide: (f) => EditorView.decorations.from(f),
    });

    // autocomplete: paths after "{{ " (and "(", ","), filters after "|"
    const FILTER_DETAIL: Record<string, string> = { sha256: 'filter · SHA-256 hex', sha512: 'filter · SHA-512 hex', mosquitto_passwd: 'filter · Mosquitto password-file hash ($7$)' };
    const filterOpts = FILTERS.map((f) => ({ label: f === 'default' ? 'default("")' : f === 'join' ? 'join(",")' : f, type: 'function', detail: FILTER_DETAIL[f] ?? 'filter' }));
    const source = (ctx: import('@codemirror/autocomplete').CompletionContext) => {
      if (!exprOn) return null;
      const line = ctx.state.doc.lineAt(ctx.pos);
      const before = line.text.slice(0, ctx.pos - line.from);
      const o = before.lastIndexOf('{{');
      if (o < 0 || before.lastIndexOf('}}') > o) return null;
      const inner = before.slice(o + 2);
      if ((inner.match(/(^|[^\\])"/g) ?? []).length % 2 === 1) return null; // inside a string
      const token = /[A-Za-z0-9_.\-]*$/.exec(inner)![0];
      const pre = inner.slice(0, inner.length - token.length).trimEnd();
      const filter = pre.endsWith('|');
      if (!filter && pre.length > 0 && !pre.endsWith('(') && !pre.endsWith(',')) return null;
      if (!ctx.explicit && !token && !/[\s|({]$/.test(before)) return null;
      const after = line.text.slice(ctx.pos - line.from);
      const close = after.indexOf('}}');
      const nextOpen = after.indexOf('{{');
      const closed = close >= 0 && (nextOpen < 0 || close < nextOpen);
      const opts = filter
        ? filterOpts
        : pathList.map((p) => ({ label: p.label, detail: p.detail, type: 'variable', section: p.kind, apply: closed || pre.endsWith('(') || pre.endsWith(',') ? p.label : `${p.label} }}` }));
      return { from: ctx.pos - token.length, options: opts, validFor: /^[A-Za-z0-9_.\-]*$/ };
    };

    const exprComp = new Compartment();
    const exprExt = () => (exprOn ? [blocks, errField] : [errField]);
    reconf = (on) => view?.dispatch({ effects: exprComp.reconfigure(on ? [blocks, errField] : [errField]) });

    const theme = EditorView.theme({
      '&': { fontSize: '12.5px', backgroundColor: 'var(--code-bg)', color: 'var(--text)' },
      '.cm-scroller': { fontFamily: 'var(--font-mono)', lineHeight: '1.55', maxHeight: '60vh', overflow: 'auto' },
      '.cm-content, .cm-gutter': { minHeight },
      '.cm-content': { caretColor: 'var(--text)', padding: '8px 0' },
      '.cm-gutters': { backgroundColor: 'var(--surface-2)', color: 'var(--text-3)', border: 'none', borderRight: '1px solid var(--border)' },
      '.cm-activeLine': { backgroundColor: 'color-mix(in srgb, var(--accent-soft) 45%, transparent)' },
      '.cm-activeLineGutter': { backgroundColor: 'var(--surface-hover)' },
      '&.cm-focused': { outline: 'none' },
      '.cm-selectionBackground, &.cm-focused .cm-selectionBackground, ::selection': { backgroundColor: 'var(--accent-soft-2) !important' },
      '.cm-cursor': { borderLeftColor: 'var(--text)' },
      '.cm-opm-expr, .cm-opm-expr *': { color: 'var(--accent) !important', backgroundColor: 'color-mix(in srgb, var(--accent-soft) 70%, transparent)', borderRadius: '3px' },
      ...panelTheme,
      '.cm-opm-err': { textDecoration: 'underline wavy var(--red)', textUnderlineOffset: '3px', backgroundColor: 'color-mix(in srgb, var(--red) 12%, transparent)' },
      '.cm-tooltip.cm-tooltip-autocomplete': { backgroundColor: 'var(--surface)', border: '1px solid var(--border-strong)', borderRadius: 'var(--r-md)', boxShadow: 'var(--shadow-md, 0 6px 20px rgba(0,0,0,.15))' },
      '.cm-tooltip-autocomplete > ul > li': { fontFamily: 'var(--font-mono)', fontSize: '12px', padding: '2px 8px' },
      '.cm-tooltip-autocomplete > ul > li[aria-selected]': { backgroundColor: 'var(--accent-soft)', color: 'var(--text)' },
      '.cm-completionDetail': { color: 'var(--text-3)', fontStyle: 'normal', marginLeft: '10px' },
      '.cm-completionSection': { color: 'var(--text-3)', fontSize: '11px', padding: '2px 8px' },
    });

    exprOn = expressions;
    view = new EditorView({
      parent: host!,
      state: EditorState.create({
        doc: value,
        extensions: [
          lineNumbers(),
          highlightActiveLineGutter(),
          history(),
          drawSelection(),
          indentOnInput(),
          bracketMatching(),
          highlightActiveLine(),
          highlightSelectionMatches(),
          syntaxHighlighting(HighlightStyle.define(highlightSpec(tags))),
          autocompletion({ override: [source], icons: false }),
          keymap.of([...completionKeymap, ...defaultKeymap, ...historyKeymap, ...searchKeymap, indentWithTab]),
          langExt,
          exprComp.of(exprExt()),
          theme,
          EditorView.lineWrapping,
          EditorState.readOnly.of(readonly),
          EditorView.editable.of(!readonly),
          EditorView.contentAttributes.of({ 'aria-label': label, 'data-testid': `${testid}-content` }),
          EditorView.updateListener.of((u) => {
            if (!u.docChanged) return;
            const v = u.state.doc.toString();
            lastEmitted = v;
            value = v;
          }),
        ],
      }),
    });
    ready = true;
  });

  $effect(() => {
    pathList = paths;
  });
  $effect(() => {
    const on = expressions;
    if (on !== exprOn) {
      exprOn = on;
      if (ready) reconf?.(on);
    }
  });
  // external value change → replace the document
  $effect(() => {
    const v = value;
    if (!ready || !view || v === lastEmitted) return;
    const cur = view.state.doc.toString();
    if (cur !== v) view.dispatch({ changes: { from: 0, to: cur.length, insert: v } });
    lastEmitted = v;
  });
  // error marks (one character, or to the end of the {{ }} block that starts there)
  $effect(() => {
    const ms = marks;
    void value;
    if (!ready || !view || !setMarks) return;
    const doc = view.state.doc;
    const ranges = ms
      .filter((m) => m.line >= 1 && m.line <= doc.lines)
      .map((m) => {
        const from = offsetOf(m.line, m.col);
        const l = doc.lineAt(from);
        const rest = l.text.slice(from - l.from);
        const end = rest.indexOf('}}');
        return { from, to: Math.min(l.to, end >= 0 ? from + end + 2 : from + 1) };
      })
      .map((r) => (r.to <= r.from ? { from: Math.max(doc.lineAt(r.from).from, r.from - 1), to: r.from } : r));
    view.dispatch({ effects: setMarks.of(ranges) });
  });

  onDestroy(() => view?.destroy());
</script>

<div class="fte" class:invalid class:ro={readonly} data-testid={testid}>
  <div bind:this={host} class:hidden={!ready}></div>
  {#if !ready}
    <textarea class="textarea mono fallback" style="min-height:{minHeight}" bind:value {readonly} aria-label={label}></textarea>
  {/if}
</div>

<style>
  .fte {
    border: 1px solid var(--border-strong);
    border-radius: var(--r-md);
    overflow: hidden;
    background: var(--code-bg);
  }
  .fte:focus-within {
    border-color: var(--accent);
    box-shadow: var(--focus-ring);
  }
  .fte.invalid {
    border-color: var(--red);
  }
  .hidden {
    display: none;
  }
  .fallback {
    border: 0;
    border-radius: 0;
    background: var(--code-bg);
    width: 100%;
  }
</style>
