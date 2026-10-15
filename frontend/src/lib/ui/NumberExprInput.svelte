<script lang="ts">
  import type { ExprContext } from '@opm/shared';
  import ExprInput from './ExprInput.svelte';
  /** hostPort / imagePort: a number, one whole {{ }} block, or (nullable) empty = null */
  type Props = {
    value: number | string | null;
    field: 'hostPort' | 'imagePort';
    ctx?: ExprContext | null;
    nullable?: boolean;
    placeholder?: string;
    disabled?: boolean;
    invalid?: boolean;
    testid?: string;
    size?: 'md' | 'sm';
    ariaLabel?: string;
    onchange?: (v: number | string | null) => void;
  };
  let { value = $bindable(null), field, ctx = null, nullable = true, placeholder = '', disabled = false, invalid = false, testid, size = 'md', ariaLabel, onchange }: Props = $props();
  let text = $state(value === null || value === undefined ? '' : String(value));
  $effect(() => {
    const ext = value === null || value === undefined ? '' : String(value);
    if (ext !== conv(text)?.toString() && !(ext === '' && text === '')) text = ext;
  });
  function conv(t: string): number | string | null {
    const s = t.trim();
    if (s === '') return nullable ? null : '';
    if (/^\d+$/.test(s)) return Number(s);
    return t;
  }
  function changed(t: string) {
    text = t;
    value = conv(t);
    onchange?.(value);
  }
</script>

<ExprInput value={text} {field} {ctx} {placeholder} {disabled} {invalid} {testid} {size} {ariaLabel} numeric oninput={changed} />
