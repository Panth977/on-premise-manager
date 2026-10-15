<script lang="ts">
  import CopyButton from './CopyButton.svelte';
  let { id, short = 0, label = '' }: { id: string; short?: number; label?: string } = $props();
  const shown = $derived(short && id.length > short ? id.slice(0, short) + '…' : id);
</script>

<span class="idc" title={id}>
  {#if label}<span class="faint">{label}</span>{/if}
  <code class="mono">{shown}</code>
  <CopyButton text={id} tip="Copy {id}" />
</span>

<style>
  .idc {
    display: inline-flex;
    align-items: center;
    gap: 2px;
    max-width: 100%;
    min-width: 0;
  }
  code {
    font-size: 12px;
    color: var(--text-2);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .idc :global(.btn) {
    height: 22px;
    width: 22px;
  }
</style>
