<script lang="ts">
  /**
   * §21 #96 guided "Copy data volume aside, then empty it" (the data-directory / image-version mismatch of a database
   * container): 1. copy the volume to <agentHome>/volume-copies/… with its containers stopped, 2. empty it (60 s wait +
   * typed name), 3. the containers start again with an empty data directory and initialise it. The old data stays in
   * the copy (Docker › Volumes › Copies: restore it, or migrate from it).
   */
  import type { VolumeOp } from '@opm/shared/types';
  import { api, errMessage } from '$lib/api';
  import { bytes } from '$lib/format';
  import Drawer from '$lib/ui/Drawer.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Progress from '$lib/ui/Progress.svelte';
  import Select from '$lib/ui/Select.svelte';
  import EmptyFlow from './EmptyFlow.svelte';

  interface Props {
    open: boolean;
    siteSlug: string;
    container: string;
    /** volumes that container mounts */
    volumes: { volumeSlug: string; kind: 'dir' | 'file'; users: string[]; mountPath: string }[];
    reason: string;
    ondone?: () => void;
  }
  let { open = $bindable(false), siteSlug, container, volumes, reason, ondone }: Props = $props();
  let vslug = $state<string | null>(null);
  let step = $state<'pick' | 'copying' | 'copied' | 'emptying' | 'done' | 'failed'>('pick');
  let copyOp = $state<VolumeOp | null>(null);
  let emptyOp = $state<VolumeOp | null>(null);
  let err = $state<string | null>(null);
  let emptyOpen = $state(false);
  const v = $derived(volumes.find((x) => x.volumeSlug === vslug) ?? null);
  $effect(() => {
    if (open) {
      vslug = volumes[0]?.volumeSlug ?? null;
      step = 'pick';
      copyOp = emptyOp = null;
      err = null;
    }
  });
  async function poll(op: VolumeOp, set: (o: VolumeOp) => void): Promise<VolumeOp> {
    let cur = op;
    while (!cur.finishedAt && open) {
      await new Promise((r) => setTimeout(r, 700));
      cur = await api.getVolumeOp({ siteSlug, opId: op.opId });
      set(cur);
    }
    return cur;
  }
  async function copyAside() {
    if (!v) return;
    err = null;
    step = 'copying';
    try {
      const op = await api.copyVolume({ siteSlug, volumeSlug: v.volumeSlug, body: { target: { kind: 'path', path: null }, stopContainers: true, preserve: true } });
      copyOp = op;
      const fin = await poll(op, (o) => (copyOp = o));
      if (fin.ok) step = 'copied';
      else ((step = 'failed'), (err = fin.error));
    } catch (e) {
      step = 'failed';
      err = errMessage(e);
    }
  }
  async function emptied(op: VolumeOp) {
    emptyOp = op;
    step = 'emptying';
    const fin = await poll(op, (o) => (emptyOp = o));
    if (fin.ok) {
      step = 'done';
      ondone?.();
    } else ((step = 'failed'), (err = fin.error));
  }
  const frac = (o: VolumeOp | null) => (o?.progress.bytesTotal ? o.progress.bytesDone / Math.max(1, o.progress.bytesTotal) : o?.finishedAt ? 1 : 0.05);
</script>

<Drawer bind:open title="Copy the data aside, then empty it" subtitle="container {container} · site {siteSlug}" testid="aside-drawer">
  <div class="stack">
    <div class="banner warn small"><Icon name="alert" /><div class="grow"><strong>{container}</strong> keeps restarting: <span class="mono small">{reason}</span><div class="small">The image is newer than the data directory it finds. Keep the old data aside (to migrate or roll back later) and let the container start with an empty directory.</div></div></div>
    <ol class="guide">
      <li class:on={step === 'pick' || step === 'copying'} class:ok={['copied', 'emptying', 'done'].includes(step)}>
        <strong>1. Copy the volume aside</strong> — to <span class="mono">volume-copies/</span> on the server, with its containers stopped.
        {#if volumes.length > 1 && step === 'pick'}
          <div style="margin-top:6px"><Select bind:value={vslug} options={volumes.map((x) => ({ value: x.volumeSlug, label: x.volumeSlug, hint: x.mountPath }))} testid="aside-volume" mono /></div>
        {:else if v}<div class="small muted">Volume <strong class="mono">{v.volumeSlug}</strong> (mounted at {v.mountPath}).</div>{/if}
        {#if copyOp}
          <div class="small" style="margin-top:6px">{bytes(copyOp.progress.bytesDone)}{copyOp.progress.bytesTotal !== null ? ` of ${bytes(copyOp.progress.bytesTotal)}` : ''}{#if copyOp.finishedAt && copyOp.ok}&nbsp;→ <span class="mono" data-testid="aside-copy-path">{copyOp.target?.path}</span>{/if}</div>
          <Progress value={frac(copyOp)} tone={copyOp.finishedAt ? (copyOp.ok ? 'ok' : 'bad') : ''} label="Copy" />
        {/if}
      </li>
      <li class:on={step === 'copied' || step === 'emptying'} class:ok={step === 'done'}>
        <strong>2. Empty the volume</strong> — 60 s wait (hold ⏩ to fast-forward) and its name typed; the containers stop and start again.
        {#if emptyOp}<Progress value={frac(emptyOp)} tone={emptyOp.finishedAt ? (emptyOp.ok ? 'ok' : 'bad') : ''} label="Empty" />{/if}
      </li>
      <li class:ok={step === 'done'}><strong>3. The container starts fresh</strong> and initialises an empty data directory. Restore or migrate from the copy (Docker › Volumes › Copies) when you are ready.</li>
    </ol>
    {#if step === 'done'}<div class="banner ok small" data-testid="aside-done"><Icon name="check" /><div class="grow">Done. The old data is at <span class="mono">{copyOp?.target?.path}</span>.</div></div>{/if}
    {#if err}<div class="banner bad small"><Icon name="alert" /><div class="grow">{err}</div></div>{/if}
  </div>
  {#snippet footer()}
    <Btn onclick={() => (open = false)}>{step === 'done' ? 'Close' : 'Cancel'}</Btn>
    {#if step === 'pick' || step === 'failed'}
      <Btn variant="primary" icon="copy" disabled={!v} onclick={copyAside} data-testid="aside-copy">Copy aside</Btn>
    {:else if step === 'copied'}
      <Btn variant="danger" icon="eraser" onclick={() => (emptyOpen = true)} data-testid="aside-empty">Empty {v?.volumeSlug}…</Btn>
    {/if}
  {/snippet}
</Drawer>
{#if v}
  <EmptyFlow bind:open={emptyOpen} {siteSlug} volumeSlug={v.volumeSlug} kind={v.kind} users={v.users} snapshotDefault={false} onstarted={emptied} />
{/if}

<style>
  .guide {
    margin: 0;
    padding: 0;
    list-style: none;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .guide li {
    padding: 10px 12px;
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    opacity: 0.75;
  }
  .guide li.on {
    border-color: var(--accent);
    opacity: 1;
  }
  .guide li.ok {
    border-color: color-mix(in srgb, var(--green) 50%, var(--border));
    opacity: 1;
  }
</style>
