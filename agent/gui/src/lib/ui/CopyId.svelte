<script lang="ts">
  import Icon from './Icon.svelte';
  import { toasts } from '../state/toast.svelte';
  let { value, short = 0, label = 'id' }: { value: string | null | undefined; short?: number; label?: string } = $props();
  let copied = $state(false);
  async function copy() {
    if (!value) return;
    try {
      await navigator.clipboard.writeText(value);
    } catch {
      // insecure context (plain http on a LAN IP): fallback
      const ta = document.createElement('textarea');
      ta.value = value;
      document.body.appendChild(ta);
      ta.select();
      document.execCommand('copy');
      ta.remove();
    }
    copied = true;
    toasts.ok(`Copied ${label}`);
    setTimeout(() => (copied = false), 1500);
  }
</script>

{#if value}
  <span class="cid">
    <code title={value}>{short && value.length > short ? value.slice(0, short) + '…' : value}</code>
    <button class="btn ghost xs icon" type="button" aria-label="Copy {label}" title="Copy" onclick={copy}><Icon name={copied ? 'check' : 'copy'} size={12} /></button>
  </span>
{:else}<span class="faint">—</span>{/if}

<style>
  .cid {
    display: inline-flex;
    align-items: center;
    gap: 2px;
    max-width: 100%;
  }
  code {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
</style>
