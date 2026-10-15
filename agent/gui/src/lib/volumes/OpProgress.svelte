<script lang="ts">
  /**
   * §21 #96: progress + result of a volume operation (empty / copy / restore) — polls getVolumeOp every 700 ms until it
   * finishes. Steps (stop → snapshot → copy / empty / restore → start), bytes, the result (path, size, snapshot), warnings.
   */
  import { siteHref } from '$lib/nav';
  import { base } from '$app/paths';
  import type { VolumeOp } from '@opm/shared/types';
  import { fastForwardLabel } from '@opm/shared/intent-wait';
  import { api, errMessage } from '$lib/api';
  import { bytes, duration } from '$lib/format';
  import Drawer from '$lib/ui/Drawer.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Progress from '$lib/ui/Progress.svelte';
  import CopyId from '$lib/ui/CopyId.svelte';
  import { OP_LABEL, STEP_LABEL } from './vol';

  interface Props {
    open: boolean;
    op: VolumeOp | null;
    ondone?: (op: VolumeOp) => void;
  }
  let { open = $bindable(false), op = $bindable(null), ondone }: Props = $props();
  let err = $state<string | null>(null);
  let reported = '';

  $effect(() => {
    const cur = op;
    if (!open || !cur || cur.finishedAt) return;
    const id = cur.opId;
    const site = cur.siteSlug;
    const t = setInterval(async () => {
      try {
        const r = await api.getVolumeOp({ siteSlug: site, opId: id });
        if (op?.opId === id) op = r;
        err = null;
      } catch (e) {
        err = errMessage(e);
      }
    }, 700);
    return () => clearInterval(t);
  });
  $effect(() => {
    if (op?.finishedAt && reported !== op.opId) {
      reported = op.opId;
      ondone?.(op);
    }
  });
  const frac = $derived(op?.progress.bytesTotal ? Math.min(1, op.progress.bytesDone / Math.max(1, op.progress.bytesTotal)) : op?.finishedAt ? 1 : 0);
  const ICON: Record<string, string> = { pending: 'clock', running: 'refresh', done: 'check', skipped: 'minus', failed: 'x' };
</script>

<Drawer bind:open title={op ? `${OP_LABEL[op.kind]} · volume ${op.volumeSlug}` : 'Volume action'} subtitle={op ? `site ${op.siteSlug} · by ${op.actor}` : undefined} testid="volop-drawer">
  {#if op}
    <div class="stack" data-testid="volop" data-phase={op.phase}>
      {#if op.finishedAt}
        <div class="banner {op.ok ? 'ok' : 'bad'}" data-testid="volop-result">
          <Icon name={op.ok ? 'check' : 'alert'} />
          <div class="grow">
            {#if op.ok}
              <strong>{OP_LABEL[op.kind]} finished</strong> in {duration(op.durationMs ?? 0)}.
              {#if op.kind === 'copy' && op.target}<div class="small">{bytes(op.resultBytes)} copied to <code class="pathc">{op.target.volumeSlug ? `volume ${op.target.volumeSlug}` : op.target.path}</code></div>{/if}
              {#if op.kind === 'empty'}<div class="small">{bytes(op.resultBytes)} deleted; the folder itself, its owner and mode stay.</div>{/if}
              {#if op.kind === 'restore'}<div class="small">{bytes(op.resultBytes)} restored from {op.source?.kind === 'copy' ? op.source.path : 'the uploaded archive'}.</div>{/if}
            {:else}
              <strong>{OP_LABEL[op.kind]} failed</strong>: {op.error}
              {#if op.stopped.length}<div class="small">The containers that were stopped ({op.stopped.join(', ')}) were started again.</div>{/if}
            {/if}
            {#if op.snapshotId}<div class="small">Snapshot first: <a href={siteHref('backups')} data-testid="volop-snapshot">{op.snapshotId}</a> (Backups → roll back / download)</div>{/if}
            {#if op.fastForward}<div class="tiny muted">The wait was {fastForwardLabel(op.fastForward)}.</div>{/if}
          </div>
        </div>
      {:else}
        <div class="banner info small"><span class="spin"></span><div class="grow">Running — you can close this panel; the action continues on the server (Docker › Volumes › History).</div></div>
      {/if}
      <div>
        <div class="row between small"><span>{bytes(op.progress.bytesDone)}{op.progress.bytesTotal !== null ? ` of ${bytes(op.progress.bytesTotal)}` : ''}</span><span class="muted">{op.progress.files} files</span></div>
        <Progress value={frac} tone={op.finishedAt ? (op.ok ? 'ok' : 'bad') : ''} label="Bytes" />
      </div>
      <ol class="steps" data-testid="volop-steps">
        {#each op.steps as s (s.name)}
          <li class="st {s.state}" data-testid="volop-step-{s.name}" data-state={s.state}>
            <span class="ic">{#if s.state === 'running'}<span class="spin"></span>{:else}<Icon name={ICON[s.state]} size={14} />{/if}</span>
            <span class="grow"><strong>{STEP_LABEL[s.name]}</strong>{#if s.detail}<span class="small muted"> · {s.detail}</span>{/if}</span>
            <span class="tiny faint">{s.state}</span>
          </li>
        {/each}
      </ol>
      {#if op.warnings.length}
        <div class="banner warn small"><Icon name="alert" /><div class="grow"><strong>Warnings</strong><ul>{#each op.warnings as w, i (i)}<li>{w}</li>{/each}</ul></div></div>
      {/if}
      {#if err}<div class="banner bad small"><Icon name="alert" /><div class="grow">{err}</div></div>{/if}
      <div class="tiny faint">Operation <CopyId value={op.opId} short={12} label="operation id" /></div>
    </div>
  {/if}
  {#snippet footer()}
    <Btn onclick={() => (open = false)} data-testid="volop-close">{op?.finishedAt ? 'Close' : 'Hide'}</Btn>
  {/snippet}
</Drawer>

<style>
  .steps {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .st {
    display: flex;
    gap: 10px;
    align-items: center;
    padding: 8px 10px;
    border: 1px solid var(--border);
    border-radius: var(--r-md);
  }
  .st.running {
    border-color: var(--accent);
    background: var(--accent-soft);
  }
  .st.done .ic {
    color: var(--green);
  }
  .st.failed {
    border-color: var(--red);
  }
  .st.failed .ic {
    color: var(--red);
  }
  .st.skipped,
  .st.pending {
    opacity: 0.7;
  }
  .ic {
    width: 18px;
    display: grid;
    place-items: center;
    flex: none;
  }
  .pathc {
    overflow-wrap: anywhere;
  }
  ul {
    margin: 4px 0 0;
    padding-left: 18px;
  }
</style>
