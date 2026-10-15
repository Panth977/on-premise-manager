<script lang="ts">
  import Icon from './Icon.svelte';
  import { Plus, X, ArrowUp, ArrowDown } from './icons';
  /** A list of strings (command / entrypoint / exec health check). Empty list → null when nullable. */
  type Props = {
    value: string[] | null;
    nullable?: boolean;
    placeholder?: string;
    addLabel?: string;
    disabled?: boolean;
    invalid?: boolean;
    testid?: string;
    onchange?: (v: string[] | null) => void;
  };
  let { value = $bindable(null), nullable = true, placeholder = '', addLabel = 'Add item', disabled = false, invalid = false, testid, onchange }: Props = $props();
  const items = $derived(value ?? []);
  function set(next: string[]) {
    value = next.length === 0 && nullable ? null : next;
    onchange?.(value);
  }
  function upd(i: number, v: string) {
    const n = [...items];
    n[i] = v;
    set(n);
  }
  function del(i: number) {
    set(items.filter((_, j) => j !== i));
  }
  function move(i: number, d: number) {
    const n = [...items];
    const j = i + d;
    if (j < 0 || j >= n.length) return;
    [n[i], n[j]] = [n[j]!, n[i]!];
    set(n);
  }
  function add() {
    set([...items, '']);
    setTimeout(() => {
      const els = document.querySelectorAll<HTMLInputElement>(`[data-sl="${uid}"]`);
      els[els.length - 1]?.focus();
    });
  }
  const uid = Math.random().toString(36).slice(2, 8);
</script>

<div class="sl" data-testid={testid}>
  {#if items.length === 0 && placeholder}<div class="ph mono">{placeholder}</div>{/if}
  {#each items as it, i (i)}
    <div class="r">
      <span class="ix">{i}</span>
      <input class="input mono sm" class:invalid value={it} oninput={(e) => upd(i, e.currentTarget.value)} {disabled} data-sl={uid} aria-label="Item {i + 1}" onkeydown={(e) => e.key === 'Enter' && (e.preventDefault(), add())} />
      <button type="button" class="btn ghost icon sm" aria-label="Move up" disabled={disabled || i === 0} onclick={() => move(i, -1)}><Icon icon={ArrowUp} size={13} /></button>
      <button type="button" class="btn ghost icon sm" aria-label="Move down" disabled={disabled || i === items.length - 1} onclick={() => move(i, 1)}><Icon icon={ArrowDown} size={13} /></button>
      <button type="button" class="btn ghost icon sm" aria-label="Remove item {i + 1}" {disabled} onclick={() => del(i)}><Icon icon={X} size={13} /></button>
    </div>
  {/each}
  {#if !disabled}<button type="button" class="btn sm add" onclick={add}><Icon icon={Plus} size={13} />{addLabel}</button>{/if}
</div>

<style>
  .sl {
    display: flex;
    flex-direction: column;
    gap: 5px;
  }
  .r {
    display: flex;
    align-items: center;
    gap: 4px;
  }
  .ix {
    width: 18px;
    text-align: right;
    color: var(--text-3);
    font-size: var(--fs-xs);
    font-family: var(--font-mono);
    flex: none;
  }
  .ph {
    color: var(--text-3);
    font-size: 12px;
    padding: 4px 0;
  }
  .add {
    align-self: flex-start;
  }
</style>
