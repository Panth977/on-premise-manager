<script lang="ts">
  /** Confirmation modal. Destructive = red primary. typed = the primary button is enabled only when the text matches. */
  import type { Snippet } from 'svelte';
  import Modal from './Modal.svelte';
  import Btn from './Btn.svelte';
  interface Props {
    open: boolean;
    title: string;
    message?: string;
    confirmLabel?: string;
    danger?: boolean;
    typed?: string;
    onconfirm: () => unknown | Promise<unknown>;
    children?: Snippet;
    testid?: string;
  }
  let { open = $bindable(false), title, message, confirmLabel = 'Confirm', danger = false, typed, onconfirm, children, testid }: Props = $props();
  let text = $state('');
  let busy = $state(false);
  const id = $props.id();
  $effect(() => {
    if (open) text = '';
  });
  const okEnabled = $derived(!typed || text === typed);
  async function go() {
    if (!okEnabled || busy) return;
    busy = true;
    try {
      const r = await onconfirm();
      if (r !== false) open = false;
    } finally {
      busy = false;
    }
  }
</script>

<Modal bind:open {title} size="sm" {testid}>
  {#if message}<p>{message}</p>{/if}
  {#if children}{@render children()}{/if}
  {#if typed}
    <div class="field" style="margin-top:12px">
      <label for="{id}-typed">Type <code>{typed}</code> to confirm</label>
      <input id="{id}-typed" class="input mono" bind:value={text} autocomplete="off" spellcheck="false" onkeydown={(e) => e.key === 'Enter' && go()} />
    </div>
  {/if}
  {#snippet footer()}
    <Btn onclick={() => (open = false)} disabled={busy}>Cancel</Btn>
    <Btn variant={danger ? 'danger' : 'primary'} {busy} disabled={!okEnabled} onclick={go} data-testid="confirm-ok">{confirmLabel}</Btn>
  {/snippet}
</Modal>
