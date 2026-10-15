<script lang="ts">
  /**
   * §21 #101 CodeMirror for one file of a volume's content: the language by extension (content.ts languageExtension),
   * {{ }} blocks highlighted when the file renders expressions, fills its container's height. Keyed by the parent per
   * file (a new file = a new editor, so undo history never crosses files).
   */
  import { onMount, onDestroy } from 'svelte';
  import type { EditorView } from '@codemirror/view';
  import { BLOCK_RE } from '../files/FileTemplateEditor.svelte';
  import { languageExtension } from './content';
  import { highlightSpec, panelTheme } from '../../ui/cm-theme';

  type Props = { value: string; path: string; expressions?: boolean; readonly?: boolean; label?: string; testid?: string; oninput?: (v: string) => void };
  let { value, path, expressions = false, readonly = false, label = 'File content', testid = 'vc-code', oninput }: Props = $props();

  let host = $state<HTMLDivElement | null>(null);
  let view: EditorView | null = null;
  let ready = $state(false);
  let reconf: ((on: boolean) => void) | null = null;
  let exprOn = false;

  onMount(async () => {
    const [cmView, { EditorState, Compartment }, { defaultKeymap, history, historyKeymap, indentWithTab }, { syntaxHighlighting, HighlightStyle, bracketMatching, indentOnInput, foldGutter }, { searchKeymap, highlightSelectionMatches }, { tags: t }, lang] = await Promise.all([
      import('@codemirror/view'),
      import('@codemirror/state'),
      import('@codemirror/commands'),
      import('@codemirror/language'),
      import('@codemirror/search'),
      import('@lezer/highlight'),
      languageExtension(path),
    ]);
    const { EditorView, keymap, lineNumbers, highlightActiveLine, highlightActiveLineGutter, drawSelection, Decoration, MatchDecorator, ViewPlugin } = cmView;
    type DecoSet = import('@codemirror/view').DecorationSet;
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
    // theme-aware syntax colors (tokens.css light + dark), readable on --code-bg in both themes
    const hl = HighlightStyle.define(highlightSpec(t));
    const exprComp = new Compartment();
    exprOn = expressions;
    reconf = (on) => view?.dispatch({ effects: exprComp.reconfigure(on ? blocks : []) });
    const theme = EditorView.theme({
      '&': { fontSize: '12.5px', backgroundColor: 'var(--code-bg)', color: 'var(--text)', height: '100%' },
      '.cm-scroller': { fontFamily: 'var(--font-mono)', lineHeight: '1.55', overflow: 'auto' },
      '.cm-content': { caretColor: 'var(--text)', padding: '8px 0' },
      '.cm-gutters': { backgroundColor: 'var(--surface-2)', color: 'var(--text-3)', border: 'none', borderRight: '1px solid var(--border)' },
      '.cm-activeLine': { backgroundColor: 'color-mix(in srgb, var(--accent-soft) 45%, transparent)' },
      '.cm-activeLineGutter': { backgroundColor: 'var(--surface-hover)' },
      '&.cm-focused': { outline: 'none' },
      '.cm-selectionBackground, &.cm-focused .cm-selectionBackground, ::selection': { backgroundColor: 'var(--accent-soft-2) !important' },
      '.cm-cursor': { borderLeftColor: 'var(--text)' },
      '.cm-opm-expr, .cm-opm-expr *': { color: 'var(--accent) !important', backgroundColor: 'color-mix(in srgb, var(--accent-soft) 70%, transparent)', borderRadius: '3px' },
      ...panelTheme,
    });
    view = new EditorView({
      parent: host!,
      state: EditorState.create({
        doc: value,
        extensions: [
          lineNumbers(),
          highlightActiveLineGutter(),
          foldGutter(),
          history(),
          drawSelection(),
          indentOnInput(),
          bracketMatching(),
          highlightActiveLine(),
          highlightSelectionMatches(),
          syntaxHighlighting(hl),
          keymap.of([...defaultKeymap, ...historyKeymap, ...searchKeymap, indentWithTab]),
          lang,
          exprComp.of(exprOn ? blocks : []),
          theme,
          EditorState.readOnly.of(readonly),
          EditorView.editable.of(!readonly),
          EditorView.contentAttributes.of({ 'aria-label': label, 'data-testid': `${testid}-content` }),
          EditorView.updateListener.of((u) => {
            if (u.docChanged && !external) oninput?.(u.state.doc.toString());
          }),
        ],
      }),
    });
    ready = true;
  });
  $effect(() => {
    const on = expressions;
    if (on !== exprOn) {
      exprOn = on;
      if (ready) reconf?.(on);
    }
  });
  // §21 #107 the value changed outside the editor (Discard / restore / rebase of the change set) → show it
  let external = false;
  $effect(() => {
    const v = value;
    if (!ready || !view) return;
    const cur = view.state.doc.toString();
    if (v === cur) return;
    external = true;
    try {
      view.dispatch({ changes: { from: 0, to: cur.length, insert: v } });
    } finally {
      external = false;
    }
  });
  onDestroy(() => view?.destroy());
</script>

<div class="cce" class:ro={readonly} data-testid={testid}>
  <div bind:this={host} class="host" class:hidden={!ready}></div>
  {#if !ready}
    <textarea class="textarea mono fallback" {value} {readonly} aria-label={label} oninput={(e) => oninput?.(e.currentTarget.value)}></textarea>
  {/if}
</div>

<style>
  .cce {
    height: 100%;
    min-height: 0;
    display: flex;
    flex-direction: column;
    background: var(--code-bg);
  }
  .host {
    flex: 1;
    min-height: 0;
  }
  .host :global(.cm-editor) {
    height: 100%;
  }
  .hidden {
    display: none;
  }
  .fallback {
    flex: 1;
    border: 0;
    border-radius: 0;
    background: var(--code-bg);
    width: 100%;
    resize: none;
  }
</style>
