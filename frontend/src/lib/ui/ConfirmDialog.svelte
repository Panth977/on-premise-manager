<script lang="ts">
  import type { Snippet } from 'svelte';
  import Modal from './Modal.svelte';
  import Button from './Button.svelte';
  import { toast } from '../toast.svelte';

  type Props = {
    open: boolean;
    title: string;
    message?: string;
    body?: Snippet;
    confirmLabel?: string;
    cancelLabel?: string;
    danger?: boolean;
    /** typed confirm: the primary button is enabled only when the typed text matches exactly */
    typed?: string | null;
    typedLabel?: string;
    disabled?: boolean;
    /** return false to keep the dialog open; a thrown error shows an error toast and keeps it open */
    onconfirm: () => unknown | Promise<unknown>;
    oncancel?: () => void;
    testid?: string;
  };
  let { open = $bindable(), title, message, body, confirmLabel = 'Confirm', cancelLabel = 'Cancel', danger = false, typed = null, typedLabel, disabled = false, onconfirm, oncancel, testid = 'confirm-dialog' }: Props = $props();
  let busy = $state(false);
  let text = $state('');
  $effect(() => {
    if (open) text = '';
  });
  const ok = $derived(!disabled && (typed === null || typed === undefined || text === typed));
  async function go(e?: Event) {
    e?.preventDefault();
    if (!ok || busy) return;
    busy = true;
    try {
      const r = await onconfirm();
      if (r !== false) open = false;
    } catch (err) {
      toast.error(err);
    } finally {
      busy = false;
    }
  }
  const fid = `cf-${Math.random().toString(36).slice(2, 8)}`;
</script>

<Modal bind:open {title} size="sm" {busy} onclose={oncancel} {testid}>
  <form id={fid} onsubmit={go} class="stack">
    {#if message}<p class="msg">{message}</p>{/if}
    {@render body?.()}
    {#if typed}
      <label class="stack tight">
        <span class="small muted">{typedLabel ?? 'Type'} <code class="inline">{typed}</code> to confirm</span>
        <input class="input mono" bind:value={text} autocomplete="off" spellcheck="false" data-autofocus data-testid="typed-confirm" />
      </label>
    {/if}
  </form>
  {#snippet footer()}
    <Button onclick={() => { open = false; oncancel?.(); }} disabled={busy}>{cancelLabel}</Button>
    <Button type="submit" form={fid} variant={danger ? 'danger' : 'primary'} loading={busy} disabled={!ok} data-testid="confirm-ok">{confirmLabel}</Button>
  {/snippet}
</Modal>

<style>
  .msg {
    color: var(--text-2);
    white-space: pre-line;
  }
</style>
