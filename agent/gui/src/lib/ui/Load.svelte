<script lang="ts">
  /** Loading skeleton → error (message + Retry) → content. Keeps showing old data while refreshing. */
  import type { Snippet } from 'svelte';
  import { errMessage } from '../api';
  import Icon from './Icon.svelte';
  interface Props {
    loading: boolean;
    loaded: boolean;
    error: unknown;
    retry: () => unknown;
    rows?: number;
    children: Snippet;
  }
  let { loading, loaded, error, retry, rows = 4, children }: Props = $props();
</script>

{#if !loaded && error}
  <div class="state" role="alert" data-testid="error-state">
    <Icon name="alert" size={22} />
    <div class="grow">
      <strong>Could not load</strong>
      <p class="muted small">{errMessage(error)}</p>
    </div>
    <button class="btn sm" onclick={() => retry()}><Icon name="refresh" size={14} />Retry</button>
  </div>
{:else if !loaded}
  <div class="skels" aria-busy="true" aria-label="Loading" data-testid="skeleton">
    {#each Array(rows) as _, i (i)}
      <span class="skel" style="width:{92 - ((i * 17) % 35)}%"></span>
    {/each}
  </div>
{:else}
  {#if error}
    <div class="banner warn small" style="margin-bottom:10px">
      <Icon name="alert" />
      <div class="grow">Refresh failed: {errMessage(error)} — showing the last data.</div>
      <button class="btn xs" onclick={() => retry()} disabled={loading}>Retry</button>
    </div>
  {/if}
  {@render children()}
{/if}

<style>
  .state {
    display: flex;
    gap: 12px;
    align-items: center;
    padding: 18px;
    border: 1px dashed color-mix(in srgb, var(--red) 45%, var(--border));
    border-radius: var(--r-lg);
    color: var(--red-text);
    background: color-mix(in srgb, var(--red-soft) 50%, transparent);
  }
  .state strong {
    color: var(--text);
  }
  .state p {
    margin: 2px 0 0;
  }
  .skels {
    display: flex;
    flex-direction: column;
    gap: 12px;
    padding: 16px;
  }
</style>
