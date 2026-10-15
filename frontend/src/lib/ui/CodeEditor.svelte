<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import type { EditorView } from '@codemirror/view';
  import { highlightSpec, panelTheme } from './cm-theme';

  type Props = {
    value: string;
    lang?: 'html' | 'json' | 'text';
    readonly?: boolean;
    height?: string;
    minHeight?: string;
    placeholder?: string;
    invalid?: boolean;
    label?: string;
    testid?: string;
    oninput?: (v: string) => void;
  };
  let { value = $bindable(''), lang = 'text', readonly = false, height, minHeight = '160px', placeholder = '', invalid = false, label = 'Code editor', testid = 'code-editor', oninput }: Props = $props();

  let host = $state<HTMLDivElement | null>(null);
  let view: EditorView | null = null;
  let ready = $state(false);
  let lastEmitted = value;

  onMount(async () => {
    const [{ EditorView, keymap, lineNumbers, highlightActiveLine, highlightActiveLineGutter, drawSelection, placeholder: ph }, { EditorState }, { defaultKeymap, history, historyKeymap, indentWithTab }, { syntaxHighlighting, HighlightStyle, bracketMatching, indentOnInput, foldGutter }, { searchKeymap, highlightSelectionMatches }, { tags }] = await Promise.all([
      import('@codemirror/view'),
      import('@codemirror/state'),
      import('@codemirror/commands'),
      import('@codemirror/language'),
      import('@codemirror/search'),
      import('@lezer/highlight'),
    ]);
    const langExt = lang === 'html' ? (await import('@codemirror/lang-html')).html() : lang === 'json' ? (await import('@codemirror/lang-json')).json() : [];
    const theme = EditorView.theme({
      '&': { fontSize: '12.5px', backgroundColor: 'var(--code-bg)', color: 'var(--text)', height: height ?? 'auto' },
      '.cm-scroller': { fontFamily: 'var(--font-mono)', lineHeight: '1.55', minHeight, overflow: 'auto' },
      '.cm-content': { caretColor: 'var(--text)', padding: '8px 0' },
      '.cm-gutters': { backgroundColor: 'var(--surface-2)', color: 'var(--text-3)', border: 'none', borderRight: '1px solid var(--border)' },
      '.cm-activeLine': { backgroundColor: 'color-mix(in srgb, var(--accent-soft) 45%, transparent)' },
      '.cm-activeLineGutter': { backgroundColor: 'var(--surface-hover)' },
      '&.cm-focused': { outline: 'none' },
      '.cm-selectionBackground, &.cm-focused .cm-selectionBackground, ::selection': { backgroundColor: 'var(--accent-soft-2) !important' },
      '.cm-cursor': { borderLeftColor: 'var(--text)' },
      '.cm-placeholder': { color: 'var(--text-3)' },
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
          syntaxHighlighting(HighlightStyle.define(highlightSpec(tags))),
          keymap.of([...defaultKeymap, ...historyKeymap, ...searchKeymap, indentWithTab]),
          langExt,
          theme,
          EditorView.lineWrapping,
          EditorState.readOnly.of(readonly),
          EditorView.editable.of(!readonly),
          ph(placeholder),
          EditorView.contentAttributes.of({ 'aria-label': label, 'data-testid': `${testid}-content` }),
          EditorView.updateListener.of((u) => {
            if (!u.docChanged) return;
            const v = u.state.doc.toString();
            lastEmitted = v;
            value = v;
            oninput?.(v);
          }),
        ],
      }),
    });
    ready = true;
  });

  // external value change (e.g. "Read from registry", reset) → replace the document
  $effect(() => {
    const v = value;
    if (!view || v === lastEmitted) return;
    const cur = view.state.doc.toString();
    if (cur !== v) view.dispatch({ changes: { from: 0, to: cur.length, insert: v } });
    lastEmitted = v;
  });

  onDestroy(() => view?.destroy());
</script>

<div class="ce" class:invalid class:ro={readonly} data-testid={testid}>
  <div bind:this={host} class="host" class:hidden={!ready}></div>
  {#if !ready}
    <textarea class="textarea mono fallback" style="min-height:{minHeight}" bind:value {readonly} aria-label={label} {placeholder}></textarea>
  {/if}
</div>

<style>
  .ce {
    border: 1px solid var(--border-strong);
    border-radius: var(--r-md);
    overflow: hidden;
    background: var(--code-bg);
  }
  .ce:focus-within {
    border-color: var(--accent);
    box-shadow: var(--focus-ring);
  }
  .ce.invalid {
    border-color: var(--red);
  }
  .hidden {
    display: none;
  }
  .fallback {
    border: 0;
    border-radius: 0;
    background: var(--code-bg);
  }
</style>
