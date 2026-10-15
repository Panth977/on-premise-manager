<script lang="ts">
  /**
   * Typed confirmation of a destructive dev tools action (§21 #97 e): the agent answered 422 confirmRequired with the
   * reasons; the operator types the connection name exactly (devToolConfirmOk) and the action is re-sent with confirm.
   */
  import { devToolConfirmOk } from '@opm/shared/devtools';
  import Modal from '$lib/ui/Modal.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Btn from '$lib/ui/Btn.svelte';

  interface Props {
    open: boolean;
    connectionName: string;
    reasons: string[];
    /** what will run (statement / command / topic), shown in a code box */
    what?: string;
    actionLabel?: string;
    onconfirm: (typed: string) => unknown | Promise<unknown>;
    oncancel?: () => void;
  }
  let { open = $bindable(false), connectionName, reasons, what, actionLabel = 'Run anyway', onconfirm, oncancel }: Props = $props();
  let typed = $state('');
  let busy = $state(false);
  const ok = $derived(devToolConfirmOk(connectionName, typed));
  $effect(() => {
    if (open) typed = '';
  });
  async function go() {
    if (!ok || busy) return;
    busy = true;
    try {
      const t = typed;
      open = false;
      await onconfirm(t);
    } finally {
      busy = false;
    }
  }
</script>

<Modal bind:open title="Destructive — confirm with the connection name" testid="dt-confirm" onclose={() => oncancel?.()}>
  <div class="stack">
    <div class="banner bad">
      <Icon name="alert" />
      <div class="grow">
        <strong>This cannot be undone.</strong> The agent flagged:
        <ul class="reasons" data-testid="dt-confirm-reasons">
          {#each reasons as r (r)}<li>{r}</li>{/each}
        </ul>
      </div>
    </div>
    {#if what}<pre class="what mono">{what}</pre>{/if}
    <div class="field">
      <label for="dt-confirm-input">Type <strong class="mono">{connectionName}</strong> to confirm</label>
      <input id="dt-confirm-input" class="input mono" bind:value={typed} autocomplete="off" spellcheck="false" data-testid="dt-confirm-input" onkeydown={(e) => e.key === 'Enter' && go()} />
    </div>
    <p class="hint">The confirmation is recorded in the activity of this query.</p>
  </div>
  {#snippet footer()}
    <Btn variant="ghost" onclick={() => ((open = false), oncancel?.())}>Cancel</Btn>
    <Btn variant="danger" disabled={!ok} {busy} onclick={go} data-testid="dt-confirm-go">{actionLabel}</Btn>
  {/snippet}
</Modal>

<style>
  .reasons {
    margin: 4px 0 0;
    padding-left: 18px;
  }
  .what {
    margin: 0;
    max-height: 160px;
    overflow: auto;
    padding: 8px 10px;
    border-radius: var(--r-md);
    background: var(--code-bg);
    border: 1px solid var(--border);
    font-size: var(--fs-sm);
    white-space: pre-wrap;
    word-break: break-word;
  }
</style>
