<script lang="ts">
  import Icon from './Icon.svelte';
  import { Search, X } from './icons';
  let { value = $bindable(''), placeholder = 'Search', width = '240px', testid = 'search' }: { value?: string; placeholder?: string; width?: string; testid?: string } = $props();
</script>

<div class="si" style="--w:{width}">
  <Icon icon={Search} size={14} class="s" />
  <input class="input" type="search" bind:value {placeholder} aria-label={placeholder} data-testid={testid} />
  {#if value}<button type="button" class="clr" aria-label="Clear search" onclick={() => (value = '')}><Icon icon={X} size={12} /></button>{/if}
</div>

<style>
  .si {
    position: relative;
    width: var(--w);
    max-width: 100%;
  }
  .si :global(.s) {
    position: absolute;
    left: 10px;
    top: 50%;
    transform: translateY(-50%);
    color: var(--text-3);
    pointer-events: none;
  }
  input {
    padding-left: 30px;
    padding-right: 26px;
  }
  input::-webkit-search-cancel-button {
    display: none;
  }
  .clr {
    position: absolute;
    right: 6px;
    top: 50%;
    transform: translateY(-50%);
    border: 0;
    background: var(--surface-active);
    border-radius: 50%;
    width: 18px;
    height: 18px;
    display: grid;
    place-items: center;
    cursor: pointer;
    color: var(--text-2);
  }
</style>
