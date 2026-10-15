<script lang="ts">
  /**
   * §21 #105 rebase of a browser change set onto a moved head (J6): the merge engine (shared `rebaseChanges`) re-applies
   * my clean changes; this dialog shows only the CONFLICTS (both I and the new head changed the same item): Keep the
   * head · Take mine · Custom (scalars). "Apply" returns the choices; the caller re-runs rebaseChanges with them.
   */
  import type { MergeChoice, MergeItem } from '@opm/shared/branch';
  import Btn from '../primitives/Btn.svelte';
  import Icon from '../primitives/Icon.svelte';
  import SegmentedControl from '../primitives/SegmentedControl.svelte';
  import { show } from './labels';
  interface Props {
    open?: boolean;
    conflicts: readonly MergeItem[];
    /** my changes re-applied without a conflict */
    clean?: number;
    headLabel?: string;
    onapply: (choices: MergeChoice[]) => void | Promise<void>;
    ondiscard?: () => void;
    onclose?: () => void;
    testid?: string;
  }
  let { open = $bindable(false), conflicts, clean = 0, headLabel = 'the head', onapply, ondiscard, onclose, testid = 'rebase-dialog' }: Props = $props();
  let dlg = $state<HTMLDialogElement>();
  let picks = $state<Record<string, MergeChoice>>({});
  let busy = $state(false);
  const unresolved = $derived(conflicts.filter((c) => !picks[c.key]).length);
  $effect(() => {
    if (!dlg) return;
    if (open && !dlg.open) {
      picks = {};
      dlg.showModal();
    } else if (!open && dlg.open) dlg.close();
  });
  function close() {
    if (busy) return;
    open = false;
    onclose?.();
  }
  function decide(i: MergeItem, take: 'source' | 'target' | 'custom') {
    picks = { ...picks, [i.key]: take === 'custom' ? { key: i.key, take, value: i.source.has ? i.source.value : null } : { key: i.key, take } };
  }
  function setCustom(i: MergeItem, raw: string) {
    const v: unknown = raw === 'true' ? true : raw === 'false' ? false : raw !== '' && !Number.isNaN(Number(raw)) && typeof i.source.value === 'number' ? Number(raw) : raw;
    picks = { ...picks, [i.key]: { key: i.key, take: 'custom', value: v } };
  }
  async function apply() {
    if (unresolved) return;
    busy = true;
    try {
      await onapply(Object.values(picks));
      open = false;
    } finally {
      busy = false;
    }
  }
</script>

<dialog bind:this={dlg} class="rd" data-testid={testid} aria-labelledby="{testid}-title" oncancel={(e) => { e.preventDefault(); close(); }}>
  <div class="stack">
    <header class="row between">
      <h3 id="{testid}-title">Rebase your changes onto {headLabel}</h3>
      <Btn size="sm" variant="ghost" icon="x" aria-label="Close" onclick={close} disabled={busy} data-testid="{testid}-close" />
    </header>
    <p class="small muted m0">
      Someone committed meanwhile. {clean} change{clean === 1 ? '' : 's'} of yours {clean === 1 ? 'applies' : 'apply'} cleanly;
      {conflicts.length} conflict{conflicts.length === 1 ? '' : 's'} — the same item was changed in {headLabel} too. Choose per item.
    </p>
    <ul class="list">
      {#each conflicts as i (i.key)}
        {@const p = picks[i.key]}
        <li data-testid="rebase-item-{i.key}">
          <div class="small strong">{i.label}</div>
          <div class="sides">
            <div class="slot"><div class="tiny strong">{headLabel}</div><pre class="val">{show(i.target.value, i.target.has)}</pre></div>
            <div class="slot mine"><div class="tiny strong">Mine</div><pre class="val">{show(i.source.value, i.source.has)}</pre></div>
          </div>
          <div class="row gap-s wrap">
            <SegmentedControl size="sm" ariaLabel="Resolve {i.label}" value={p?.take} options={[{ value: 'target', label: 'Keep the head' }, { value: 'source', label: 'Take mine' }, ...(i.scalar ? [{ value: 'custom' as const, label: 'Custom' }] : [])]} onchange={(v) => decide(i, v as 'target' | 'source' | 'custom')} testid="rebase-resolve-{i.key}" />
            {#if p?.take === 'custom'}<input class="input sm mono" value={p.value === null ? '' : String(p.value ?? '')} oninput={(e) => setCustom(i, (e.currentTarget as HTMLInputElement).value)} aria-label="Custom value" data-testid="rebase-custom-{i.key}" />{/if}
          </div>
        </li>
      {/each}
    </ul>
    <footer class="row end">
      {#if ondiscard}<Btn variant="ghost" onclick={() => { open = false; ondiscard?.(); }} disabled={busy} data-testid="{testid}-discard"><Icon name="trash" size={13} /> Discard my changes</Btn>{/if}
      <Btn variant="primary" {busy} disabled={unresolved > 0} title={unresolved ? `${unresolved} conflict${unresolved === 1 ? '' : 's'} need a decision` : undefined} onclick={apply} data-testid="{testid}-apply">Apply</Btn>
    </footer>
  </div>
</dialog>

<style>
  .rd {
    width: min(640px, calc(100vw - 24px));
    max-height: calc(100vh - 32px);
    border: 1px solid var(--border);
    border-radius: var(--r-xl);
    padding: 16px;
    background: var(--surface);
    color: var(--text);
    box-shadow: var(--shadow-lg, 0 20px 50px rgba(0, 0, 0, 0.25));
  }
  .rd::backdrop {
    background: var(--overlay);
  }
  h3 {
    margin: 0;
    font-size: var(--fs-lg);
  }
  .m0 {
    margin: 0;
  }
  .list {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 12px;
    max-height: 50vh;
    overflow: auto;
  }
  .list li {
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    padding: 8px 10px;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .sides {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 8px;
  }
  @media (max-width: 480px) {
    .sides {
      grid-template-columns: 1fr;
    }
  }
  .slot {
    border: 1px solid var(--border);
    border-radius: var(--r-sm, 6px);
    padding: 4px 8px;
    min-width: 0;
  }
  .slot.mine {
    border-color: color-mix(in srgb, var(--accent) 45%, var(--border));
  }
  .val {
    margin: 2px 0 0;
    font-family: var(--font-mono, monospace);
    font-size: var(--fs-xs, 12px);
    white-space: pre-wrap;
    word-break: break-word;
    max-height: 140px;
    overflow: auto;
  }
  footer {
    gap: 8px;
  }
</style>
