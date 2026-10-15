<script lang="ts">
  import Modal from './Modal.svelte';
  import Button from './Button.svelte';
  type Props = {
    open: boolean;
    current: number | null;
    what?: string;
    /** discard my edits, load the server state */
    onreload: () => Promise<void> | void;
    /** reload the server state, re-apply my edited sections, save again */
    onkeep: () => Promise<void> | void;
  };
  let { open = $bindable(), current, what = 'version', onreload, onkeep }: Props = $props();
  let busy = $state<'' | 'reload' | 'keep'>('');
  async function run(k: 'reload' | 'keep') {
    busy = k;
    try {
      await (k === 'reload' ? onreload() : onkeep());
      open = false;
    } finally {
      busy = '';
    }
  }
</script>

<Modal bind:open title="Someone else changed this{current !== null ? ` (now ${what} ${current})` : ''}" size="md" busy={!!busy} testid="conflict-modal">
  <div class="stack">
    <p class="muted">Your save was refused because the data changed on the server after you opened it.</p>
    <ul class="muted small">
      <li><b>Reload</b> — discard your edits and show the current state.</li>
      <li><b>Keep my edits</b> — load the current state, put your edited sections on top of it and save again.</li>
    </ul>
  </div>
  {#snippet footer()}
    <Button onclick={() => run('reload')} loading={busy === 'reload'} disabled={!!busy} data-testid="conflict-reload">Reload</Button>
    <Button variant="primary" onclick={() => run('keep')} loading={busy === 'keep'} disabled={!!busy} data-testid="conflict-keep">Keep my edits</Button>
  {/snippet}
</Modal>

<style>
  ul {
    margin: 0;
    padding-left: 18px;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
</style>
