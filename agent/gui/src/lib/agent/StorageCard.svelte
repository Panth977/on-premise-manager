<script lang="ts">
  /**
   * §21 #119 Agent › Storage: every disk OPM uses (the agent home, Docker's data folder, volumes with their own
   * location) — total / used / free and the free-space floor the agent keeps — the size of the agent home, and
   * "Move agent home…": check a new folder (another disk), type MOVE; the agent stops this site's containers, moves the
   * folder while it is stopped, restarts from there and re-applies the containers. The old copy stays until deleted.
   */
  import { onMount } from 'svelte';
  import type { MoveHomeCheck, StorageView } from '@opm/shared/remote-gui';
  import { api, errMessage } from '$lib/api';
  import { bytes } from '$lib/format';
  import { toasts } from '$lib/state/toast.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Modal from '$lib/ui/Modal.svelte';
  import Progress from '$lib/ui/Progress.svelte';
  import Time from '$lib/ui/Time.svelte';

  let v = $state<StorageView | null>(null);
  let err = $state<string | null>(null);
  async function load() {
    try {
      v = await api.agentStorage({});
      err = null;
    } catch (e) {
      err = errMessage(e);
    }
  }
  onMount(() => {
    void load();
    const t = setInterval(() => !document.hidden && void load(), 20_000);
    return () => clearInterval(t);
  });

  let open = $state(false);
  let to = $state('');
  let check = $state<MoveHomeCheck | null>(null);
  let checking = $state(false);
  let typed = $state('');
  let busy = $state(false);
  let moving = $state(false);
  async function runCheck() {
    checking = true;
    try {
      check = await api.checkMoveHome({ body: { to } });
    } catch (e) {
      toasts.err(e, 'Check failed');
    } finally {
      checking = false;
    }
  }
  async function move() {
    busy = true;
    try {
      await api.moveHome({ body: { to, typed } });
      open = false;
      moving = true;
      toasts.ok('Moving the agent home', 'The agent stops, moves the folder and starts again from the new place. This page reconnects by itself.');
      waitBack();
    } catch (e) {
      toasts.err(e, 'Not moved');
    } finally {
      busy = false;
    }
  }
  function waitBack() {
    const t = setInterval(async () => {
      try {
        const x = await api.agentStorage({});
        if (x.move && (x.move.status === 'done' || x.move.status === 'failed')) {
          clearInterval(t);
          moving = false;
          v = x;
          if (x.move.status === 'done') toasts.ok('Agent home moved', `Now at ${x.home}. The containers are applied again.`);
          else toasts.err(new Error(x.move.error ?? 'failed'), 'The move failed — the agent stays in the old folder');
        }
      } catch {
        /* the agent is restarting */
      }
    }, 3000);
  }
  let delOpen = $state(false);
  let delTyped = $state('');
  async function deleteOld() {
    busy = true;
    try {
      await api.deleteOldHome({ body: { to: '', typed: delTyped } });
      delOpen = false;
      toasts.ok('Old copy deleted');
      await load();
    } catch (e) {
      toasts.err(e, 'Not deleted');
    } finally {
      busy = false;
    }
  }
  const pct = (f: { usedBytes: number; totalBytes: number }) => (f.totalBytes ? f.usedBytes / f.totalBytes : 0);
</script>

<section class="card card-pad c wide" data-testid="storage-card">
  <h3><Icon name="database" size={15} />Storage</h3>
  {#if err}<p class="small bad-text">{err}</p>{/if}
  {#if v}
    <div class="fss">
      {#each v.filesystems as f (f.path)}
        {@const low = f.freeBytes < f.floorBytes * 2}
        <div class="fs" data-testid="storage-fs">
          <div class="row between small"><b>{f.roles.join(' · ')}</b><span class:warn-text={low} data-testid="storage-free">{bytes(f.freeBytes)} free of {bytes(f.totalBytes)}</span></div>
          <Progress value={pct(f)} tone={low ? 'bad' : ''} label="used on {f.path}" />
          <div class="tiny muted"><span class="mono">{f.path}</span> · {bytes(f.usedBytes)} used · the agent always leaves {bytes(f.floorBytes)} free{#if low} — <b class="warn-text">nearly full: downloads (agent upgrades, images), snapshots and volume copies are refused below that</b>{/if}</div>
        </div>
      {/each}
    </div>
    <p class="small" data-testid="storage-home">Agent home <span class="mono">{v.home}</span>: {#if v.homeBytes !== null}<b>{bytes(v.homeBytes)}</b> in {v.homeFiles} files{#if v.measuredAt} (measured <Time ts={v.measuredAt} />){/if}{:else}measuring…{/if}. It holds opm.db, downloaded images and files, snapshots and the volumes without their own location.{#if v.dockerRootDir} Docker keeps its images in <span class="mono">{v.dockerRootDir}</span> — moving the agent home does not move those.{/if}</p>
    {#if moving}
      <p class="small"><span class="spin" aria-hidden="true"></span> Moving the agent home… the agent restarts from the new folder.</p>
    {:else if v.move?.status === 'done' && v.move.oldCopy}
      <div class="banner info small" data-testid="storage-old-copy"><Icon name="info" /><div class="grow">Moved from <span class="mono">{v.move.from}</span>. The old copy is kept at <span class="mono">{v.move.oldCopy}</span> until you delete it.</div><Btn size="sm" variant="danger" icon="trash" onclick={() => ((delTyped = ''), (delOpen = true))} data-testid="storage-delete-old">Delete old copy…</Btn></div>
    {:else if v.move?.status === 'failed'}
      <div class="banner bad small"><Icon name="alert" /><div class="grow">The last move to <span class="mono">{v.move.to}</span> failed: {v.move.error}. The agent stayed in <span class="mono">{v.move.from}</span>.</div></div>
    {/if}
    <div class="row">
      <Btn icon="swap" disabled={!v.canMove || moving} title={v.cannotMove ?? ''} onclick={() => ((to = ''), (check = null), (typed = ''), (open = true))} data-testid="storage-move">Move agent home…</Btn>
      {#if v.cannotMove}<span class="tiny muted">{v.cannotMove}</span>{/if}
    </div>
  {/if}
</section>

<Modal bind:open title="Move the agent home" description="Usually onto another, bigger disk. The agent stops this site's containers, moves the folder while it is stopped, starts again from there and recreates the containers with the new paths (a few minutes of downtime on this site)." size="md" testid="move-home-dialog">
  <div class="stack">
    <label class="small" for="mh-to">New folder (absolute; empty or not there yet; its parent must exist)</label>
    <div class="row"><input id="mh-to" class="input mono grow" bind:value={to} placeholder="/data/opm" data-testid="move-to" /><Btn busy={checking} disabled={!to.trim()} onclick={runCheck} data-testid="move-check">Check</Btn></div>
    {#if check}
      <div class="small" data-testid="move-result">
        <div>Agent home: <b>{bytes(check.sizeBytes)}</b> · there: {bytes(check.targetFreeBytes)} free of {bytes(check.targetTotalBytes)} · {check.sameFilesystem ? 'same disk (a rename)' : 'another disk (copied, verified)'}</div>
        {#if check.containers.length}<div class="muted">Stopped during the move: {check.containers.join(', ')}</div>{/if}
        {#each check.errors as e (e)}<div class="bad-text">✕ {e}</div>{/each}
        {#each check.warnings as w (w)}<div class="warn-text">! {w}</div>{/each}
      </div>
      {#if check.ok}
        <label class="small" for="mh-typed">Type <b>MOVE</b> to start</label>
        <input id="mh-typed" class="input" bind:value={typed} autocomplete="off" data-testid="move-typed" />
      {/if}
    {/if}
  </div>
  {#snippet footer()}
    <Btn onclick={() => (open = false)}>Cancel</Btn>
    <Btn variant="primary" {busy} disabled={!check?.ok || check.to !== to.trim() || typed !== 'MOVE'} onclick={move} data-testid="move-start">Move</Btn>
  {/snippet}
</Modal>

<Modal bind:open={delOpen} title="Delete the old agent home?" description={`This removes ${v?.move?.oldCopy ?? ''} for good. The agent no longer uses it.`} size="sm" testid="delete-old-dialog">
  <label class="small" for="mh-del">Type <b>DELETE</b></label>
  <input id="mh-del" class="input" bind:value={delTyped} autocomplete="off" data-testid="delete-old-typed" />
  {#snippet footer()}
    <Btn onclick={() => (delOpen = false)}>Cancel</Btn>
    <Btn variant="danger" {busy} disabled={delTyped !== 'DELETE'} onclick={deleteOld} data-testid="delete-old-ok">Delete</Btn>
  {/snippet}
</Modal>

<style>
  .fss {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .fs {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
</style>
