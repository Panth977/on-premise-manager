<script lang="ts">
  /**
   * CodeMirror 6 editor (lazy bundle, see load.ts). `value` is the initial / external text: when it changes from
   * outside (another tab opened, a saved query loaded) the editor takes it; typing reports through `onchange`.
   * `handle` (bindable) gives the page runTarget / markError / insert / setSchema.
   */
  import { onDestroy, onMount, untrack } from 'svelte';
  import { loadCodeMirror, type EditorHandle, type EditorLang, type EditorSchema } from './load';

  interface Props {
    value: string;
    lang: EditorLang;
    schema?: EditorSchema | null;
    readonly?: boolean;
    placeholder?: string;
    singleLine?: boolean;
    lineNumbers?: boolean;
    placeholders?: boolean;
    onchange?: (v: string) => void;
    onrun?: () => void;
    onrunall?: () => void;
    onsave?: () => void;
    handle?: EditorHandle | null;
    testid?: string;
    ariaLabel?: string;
    /** CSS height of the editor box ("100%", "220px") */
    height?: string;
    minHeight?: string;
  }
  let { value, lang, schema = null, readonly = false, placeholder, singleLine = false, lineNumbers = true, placeholders = false, onchange, onrun, onrunall, onsave, handle = $bindable(null), testid, ariaLabel, height = '100%', minHeight }: Props = $props();

  let host: HTMLDivElement;
  let err = $state<string | null>(null);
  let h: EditorHandle | null = null;
  let last = '';

  onMount(() => {
    let dead = false;
    loadCodeMirror()
      .then((cm) => {
        if (dead) return;
        last = value;
        h = cm.createEditor({
          parent: host,
          doc: value,
          lang,
          schema,
          readOnly: readonly,
          placeholder,
          singleLine,
          lineNumbers,
          placeholders,
          ariaLabel,
          onChange: (t) => {
            last = t;
            onchange?.(t);
          },
          onRun: onrun ? () => onrun?.() : undefined,
          onRunAll: onrunall ? () => onrunall?.() : undefined,
          onSave: onsave ? () => onsave?.() : undefined,
        });
        handle = h;
      })
      .catch((e) => (err = `The editor could not be loaded (${e instanceof Error ? e.message : String(e)}).`));
    return () => {
      dead = true;
    };
  });
  onDestroy(() => {
    h?.destroy();
    h = null;
  });
  $effect(() => {
    const v = value;
    untrack(() => {
      if (h && v !== last) {
        last = v;
        h.setText(v);
      }
    });
  });
  $effect(() => {
    const s = schema;
    untrack(() => h?.setSchema(s));
  });
  $effect(() => {
    const r = readonly;
    untrack(() => h?.setReadOnly(r));
  });
</script>

<div class="cmhost" class:single={singleLine} style:height={singleLine ? undefined : height} style:min-height={minHeight} data-testid={testid} data-ready={handle ? '1' : '0'} bind:this={host}>
  {#if !handle && !err}<div class="cm-loading" aria-busy="true"><span class="spin"></span>Loading editor…</div>{/if}
  {#if err}<div class="cm-loading bad-text">{err}</div>{/if}
</div>

<style>
  .cmhost {
    position: relative;
    min-width: 0;
    overflow: hidden;
    background: var(--surface);
  }
  .cmhost.single {
    height: auto;
    overflow: visible;
  }
  .cmhost :global(.cm-editor) {
    height: 100%;
  }
  .cm-loading {
    display: flex;
    gap: 8px;
    align-items: center;
    padding: 10px 12px;
    color: var(--text-3);
    font-size: var(--fs-sm);
  }
  .single .cm-loading {
    padding: 4px 8px;
  }
</style>
