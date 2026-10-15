<script lang="ts">
  /**
   * §21 #101 "Apply cloud version" of a persist-to-cloud volume: GET plan → review dialog (fast-forward / up-to-date short
   * paths, and a clean merge when the base is known and nothing conflicts: cloud-only changes taken, live-only kept; else
   * the 3-way choice Overwrite / Keep live / Per file with a per-file list + diff) → restart checkbox → one confirm (no
   * intent wait: an apply is a merge, §21 #104 (e)) → apply → result. 409 conflict ("live changed since the plan") →
   * re-plan. Keep live = a confirm (nothing written; the push is marked kept-live).
   */
  import { untrack } from 'svelte';
  import { base } from '$app/paths';
  import type { VolumePlanItem, VolumePushPlan, VolumePushResolution, VolumeSyncApplyResponse, VolumeSyncStatus } from '@opm/shared/volume-content';
  import { formatContentBytes } from '@opm/shared/volume-content';
  import { errCode, errMessage } from '$lib/api';
  import { toasts } from '$lib/state/toast.svelte';
  import Modal from '$lib/ui/Modal.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Confirm from '$lib/ui/Confirm.svelte';
  import SegmentedControl from '$lib/ui/SegmentedControl.svelte';
  import FileDiff from './FileDiff.svelte';
  import { shortHash, vsync } from './sync';

  interface Props {
    open: boolean;
    siteSlug: string;
    status: VolumeSyncStatus;
    ondone: () => void;
  }
  let { open = $bindable(false), siteSlug, status, ondone }: Props = $props();
  const volumeSlug = $derived(status.volumeSlug);
  const push = $derived(status.push);

  let plan = $state<VolumePushPlan | null>(null);
  let planErr = $state<string | null>(null);
  let replanned = $state<string | null>(null);
  /** set by a 409 before the dialog reopens (the open-effect moves it into `replanned`) */
  let pendingReplan: string | null = null;
  let resolution = $state<VolumePushResolution>('per-file');
  let choices = $state<Record<string, 'cloud' | 'live'>>({});
  let restart = $state(true);
  let openDiff = $state<string | null>(null);
  let filter = $state<'all' | 'conflict' | 'cloud' | 'live'>('all');
  let confirmOpen = $state(false);
  let keepOpen = $state(false);
  let result = $state<VolumeSyncApplyResponse | null>(null);
  let resultOpen = $state(false);

  async function loadPlan() {
    plan = null;
    planErr = null;
    openDiff = null;
    try {
      const p = await vsync.planVolumeSync({ siteSlug, volumeSlug });
      plan = p;
      restart = p.restart;
      choices = {};
      resolution = 'per-file';
      filter = 'all';
    } catch (e) {
      planErr = errMessage(e);
    }
  }
  $effect(() => {
    if (open)
      untrack(() => {
        replanned = pendingReplan;
        pendingReplan = null;
        void loadPlan();
      });
  });

  const items = $derived((plan?.items ?? []).filter((i) => i.kind !== 'same'));
  const shown = $derived(filter === 'all' ? items : items.filter((i) => i.kind === filter));
  const conflicts = $derived(items.filter((i) => i.kind === 'conflict'));
  const undecided = $derived(resolution === 'per-file' ? conflicts.filter((i) => !choices[i.path]).length : 0);
  /** the base is known and no file changed on both sides: cloud-only changes are taken, live-only changes kept */
  const clean = $derived(!!plan && plan.baseKnown && !plan.fastForward && !plan.upToDate && plan.counts.conflict === 0);
  const shortPath = $derived(!!plan && (plan.fastForward || plan.upToDate || clean));
  /** what happens to one path with the chosen resolution */
  function outcome(i: VolumePlanItem): 'cloud' | 'live' | null {
    if (!plan) return null;
    if (plan.fastForward || (!clean && resolution === 'overwrite')) return 'cloud';
    if (clean) return i.kind === 'cloud' ? 'cloud' : 'live';
    if (resolution === 'keep-live') return 'live';
    if (i.kind === 'cloud') return 'cloud';
    if (i.kind === 'live') return 'live';
    return choices[i.path] ?? null;
  }
  const OP: Record<string, string> = { none: 'unchanged', added: 'added', removed: 'removed', changed: 'changed' };
  const KIND_LABEL: Record<string, string> = { cloud: 'cloud changed', live: 'live changed', conflict: 'conflict', same: 'same' };
  const KIND_TONE: Record<string, string> = { cloud: 'accent', live: 'warn', conflict: 'bad', same: '' };
  function setAll(side: 'cloud' | 'live') {
    const c: Record<string, 'cloud' | 'live'> = { ...choices };
    for (const i of conflicts) c[i.path] = side;
    choices = c;
  }
  const writes = $derived(items.filter((i) => outcome(i) === 'cloud').length);
  const canContinue = $derived(!!plan && (shortPath || (resolution !== 'keep-live' && undecided === 0)));

  function next() {
    if (!plan) return;
    if (resolution === 'keep-live' && !shortPath) {
      keepOpen = true;
      return;
    }
    open = false;
    confirmOpen = true;
  }
  async function keepLive() {
    if (!plan) return;
    try {
      await vsync.keepLiveVolumeSync({ siteSlug, volumeSlug, body: { pushId: plan.pushId } });
      toasts.ok(`Kept the live content of ${volumeSlug}`, 'Nothing was written. A snapshot of the live volume goes to the cloud.');
      open = false;
      ondone();
    } catch (e) {
      toasts.err(e, 'Keep live failed');
      throw e;
    }
  }
  /** what the apply sends: fast-forward / up to date = the cloud tree; a clean merge = per file with nothing to choose */
  const sentResolution = $derived<VolumePushResolution>(!plan ? resolution : plan.fastForward || plan.upToDate ? 'overwrite' : clean ? 'per-file' : resolution);
  const modeLabel = $derived(!plan ? '' : plan.upToDate ? 'up to date' : plan.fastForward ? 'fast-forward' : clean ? 'clean merge' : resolution === 'overwrite' ? 'overwrite (cloud wins)' : 'per file');
  async function apply() {
    if (!plan) return;
    const p = plan;
    try {
      const r = await vsync.applyVolumeSync({
        siteSlug,
        volumeSlug,
        body: { pushId: p.pushId, resolution: sentResolution, choices: sentResolution === 'per-file' && !shortPath ? { ...choices } : undefined, restart, liveTreeHash: p.liveTreeHash },
      });
      result = r;
      resultOpen = true;
      ondone();
    } catch (e) {
      if (errCode(e) === 'conflict') {
        // the live tree changed since the plan: plan again (the person reviews the new plan)
        pendingReplan = errMessage(e);
        open = true;
        return;
      }
      toasts.err(e, 'Apply failed');
      throw e;
    }
  }
  const res = $derived(result?.result ?? null);
</script>

<Modal bind:open title="Apply cloud version to {volumeSlug}" size="lg" testid="apply-flow">
  <div class="stack">
    {#if replanned}
      <div class="banner warn small" data-testid="apply-replanned"><Icon name="rotate" /><div class="grow"><strong>The live volume changed since the plan</strong> — nothing was written. This is the new plan; review it again.<div class="tiny muted">{replanned}</div></div></div>
    {/if}
    {#if push}
      <div class="pushline small" data-testid="apply-push">
        <span>Version <strong data-testid="apply-version">{push.versionSeq != null ? `seq ${push.versionSeq}` : push.versionId.slice(-8)}</strong> sent by <strong>{push.createdBy}</strong></span>
        {#if push.note}<span class="muted">“{push.note}”</span>{/if}
      </div>
    {/if}
    {#if planErr}
      <div class="banner bad small" data-testid="apply-plan-err"><Icon name="alert" /><div class="grow">Could not compute the plan: {planErr}</div><Btn size="sm" onclick={loadPlan}>Retry</Btn></div>
    {:else if !plan}
      <div class="row muted small"><span class="spin"></span>Comparing the live volume with the cloud version…</div>
    {:else}
      <div class="summary" data-testid="plan-summary">
        <div class="tile"><span class="tl">Cloud changed</span><b>{plan.counts.cloud}</b></div>
        <div class="tile"><span class="tl">Live changed</span><b>{plan.counts.live}</b></div>
        <div class="tile" class:bad={plan.counts.conflict > 0}><span class="tl">Conflicts</span><b data-testid="plan-conflicts">{plan.counts.conflict}</b></div>
        <div class="tile"><span class="tl">Base</span><b class="mono small">{plan.baseKnown ? shortHash(plan.baseTreeHash) : 'unknown'}</b></div>
      </div>
      {#if plan.upToDate}
        <div class="banner ok small" data-testid="plan-uptodate"><Icon name="check" /><div class="grow"><strong>Already up to date.</strong> The live volume equals the cloud version — nothing is written; applying only marks the push as applied.</div></div>
      {:else if plan.fastForward}
        <div class="banner info small" data-testid="plan-ff"><Icon name="fastForward" /><div class="grow"><strong>Clean fast-forward.</strong> The live volume has not changed since the snapshot this version is based on — the {items.length} change{items.length === 1 ? '' : 's'} below {items.length === 1 ? 'is' : 'are'} taken from the cloud, nothing on the site is lost.</div></div>
      {:else if clean}
        <div class="banner info small" data-testid="plan-clean"><Icon name="check" /><div class="grow"><strong>No conflicts.</strong> The {plan.counts.cloud} cloud change{plan.counts.cloud === 1 ? '' : 's'} {plan.counts.cloud === 1 ? 'is' : 'are'} taken{#if plan.counts.live}, and the {plan.counts.live} file{plan.counts.live === 1 ? '' : 's'} changed on the site {plan.counts.live === 1 ? 'is' : 'are'} kept{/if} — no file changed on both sides.</div></div>
      {:else}
        {#if !plan.baseKnown}
          <div class="banner warn small" data-testid="plan-nobase"><Icon name="alert" /><div class="grow">The cloud does not know which snapshot this version is based on — every difference counts as a conflict.</div></div>
        {/if}
        <div class="field">
          <span class="label">The live volume changed too — how to apply?</span>
          <SegmentedControl
            label="Resolution"
            value={resolution}
            options={[
              { value: 'overwrite' as const, label: 'Overwrite' },
              { value: 'keep-live' as const, label: 'Keep live' },
              { value: 'per-file' as const, label: 'Per file' },
            ]}
            onchange={(v) => (resolution = v)}
            testid="apply-resolution"
          />
          <span class="hint" data-testid="resolution-hint">
            {#if resolution === 'overwrite'}The cloud version wins: every live change below is replaced or removed. A snapshot of the volume is taken first (Backups).
            {:else if resolution === 'keep-live'}Nothing is written. The push is marked “kept live” and a snapshot of the live volume goes to the cloud.
            {:else}Cloud-only changes are taken, live-only changes are kept, and you choose for every conflict.{/if}
          </span>
        </div>
      {/if}
      {#if items.length}
        <div class="listhead">
          <div class="filters" role="group" aria-label="Show">
            {#each [['all', `All ${items.length}`], ['conflict', `Conflicts ${plan.counts.conflict}`], ['cloud', `Cloud ${plan.counts.cloud}`], ['live', `Live ${plan.counts.live}`]] as [k, l] (k)}
              <button class="chipbtn" class:on={filter === k} aria-pressed={filter === k} onclick={() => (filter = k as typeof filter)} data-testid="plan-filter-{k}">{l}</button>
            {/each}
          </div>
          {#if resolution === 'per-file' && !shortPath && conflicts.length}
            <div class="row gap-s small"><span class="muted">All conflicts:</span><Btn size="xs" onclick={() => setAll('cloud')} data-testid="all-cloud">Take cloud</Btn><Btn size="xs" onclick={() => setAll('live')} data-testid="all-live">Keep live</Btn></div>
          {/if}
        </div>
        <ul class="items" data-testid="plan-items">
          {#each shown as i (i.path)}
            {@const o = outcome(i)}
            <li class="it" class:need={resolution === 'per-file' && !shortPath && i.kind === 'conflict' && !choices[i.path]} data-testid="plan-item-{i.path}" data-kind={i.kind} data-outcome={o ?? ''}>
              <div class="ih">
                <button class="pth mono" onclick={() => (openDiff = openDiff === i.path ? null : i.path)} aria-expanded={openDiff === i.path} data-testid="diff-btn-{i.path}"><Icon name={openDiff === i.path ? 'chevronDown' : 'chevronRight'} size={13} /><span class="ellipsis">{i.path}</span></button>
                <span class="badge {KIND_TONE[i.kind]}">{KIND_LABEL[i.kind]}</span>
                <span class="ops tiny muted">live {OP[i.liveOp]} · cloud {OP[i.cloudOp]}{i.cloud ? ` · ${formatContentBytes(i.cloud.size)}` : ''}</span>
                <span class="grow"></span>
                {#if resolution === 'per-file' && !shortPath && i.kind === 'conflict'}
                  <SegmentedControl size="sm" label="Choice for {i.path}" value={choices[i.path] ?? ('' as 'cloud' | 'live')} options={[{ value: 'cloud' as const, label: 'Take cloud' }, { value: 'live' as const, label: 'Keep live' }]} onchange={(v) => (choices = { ...choices, [i.path]: v })} testid="choice-{i.path}" />
                {:else if o}
                  <span class="tiny out {o}">{o === 'cloud' ? (i.cloud ? 'takes cloud' : 'deleted') : 'keeps live'}</span>
                {/if}
              </div>
              {#if openDiff === i.path}
                <div class="idiff"><FileDiff {siteSlug} {volumeSlug} item={i} baseKnown={plan.baseKnown} /></div>
              {/if}
            </li>
          {:else}
            <li class="muted small" style="padding:8px">Nothing in this filter.</li>
          {/each}
        </ul>
        {#if undecided}<div class="tiny bad-text" data-testid="apply-undecided">{undecided} conflict{undecided === 1 ? '' : 's'} still need{undecided === 1 ? 's' : ''} a choice.</div>{/if}
      {/if}
      {#if plan.exclude.length}<div class="tiny muted">Never touched (excluded): <span class="mono">{plan.exclude.join('  ')}</span> · <span class="mono">.opm-seeded</span></div>{/if}
      {#if shortPath || resolution !== 'keep-live'}
        <label class="switch" data-testid="apply-restart">
          <input type="checkbox" bind:checked={restart} />
          <span>Restart the containers that use it{#if plan.users.length}: <span class="mono">{plan.users.join(', ')}</span>{:else}<span>&nbsp;(none mounts it)</span>{/if}</span>
        </label>
        <div class="tiny muted">{restart ? 'They are stopped before the files are written and started again after (also when it fails).' : 'Files are written while the containers run — they may need a restart to see them.'} The cloud asked: {plan.restart ? 'restart' : 'no restart'}.</div>
      {/if}
    {/if}
  </div>
  {#snippet footer()}
    <Btn onclick={() => (open = false)} data-testid="apply-close">Cancel</Btn>
    {#if plan}
      {#if !shortPath && resolution === 'keep-live'}
        <Btn variant="primary" icon="check" onclick={() => (keepOpen = true)} data-testid="apply-keep-live">Keep live…</Btn>
      {:else}
        <Btn variant="primary" icon="download" disabled={!canContinue} onclick={next} data-testid="apply-continue">{plan.upToDate ? 'Mark as applied…' : plan.fastForward ? 'Apply (fast-forward)…' : clean ? `Merge ${writes} cloud change${writes === 1 ? '' : 's'}…` : `Apply${writes ? ` ${writes} change${writes === 1 ? '' : 's'}` : ''}…`}</Btn>
      {/if}
    {/if}
  {/snippet}
</Modal>

<Confirm bind:open={keepOpen} title="Keep the live content of {volumeSlug}?" confirmLabel="Keep live" onconfirm={keepLive} testid="keep-live-confirm">
  <p class="small">Nothing is written to the volume. The cloud's push is marked <strong>kept live</strong> and a snapshot of the live volume is uploaded, so the cloud sees the site's content. The cloud can send another version later.</p>
</Confirm>

<Confirm bind:open={confirmOpen} title="Apply cloud version to {volumeSlug}?" confirmLabel={plan?.upToDate ? 'Mark as applied' : 'Apply to the volume'} onconfirm={apply} testid="apply-confirm">
  {#if plan}
    <p class="small" data-testid="apply-what">Site <strong class="mono">{siteSlug}</strong> · {modeLabel} · {writes} file{writes === 1 ? '' : 's'} from the cloud{#if !plan.upToDate && !plan.fastForward && items.length - writes > 0} · {items.length - writes} kept live{/if} · restart {restart ? 'on' : 'off'}</p>
    {#if !plan.upToDate}<p class="small muted">A snapshot of the volume is taken first (Backups), so you can roll back.</p>{/if}
    {#if restart && plan.users.length && !plan.upToDate}<p class="small">{plan.users.join(', ')} {plan.users.length === 1 ? 'is' : 'are'} stopped while the files are written and started again.</p>{/if}
  {/if}
</Confirm>

<Modal bind:open={resultOpen} title={res?.error ? `Apply to ${volumeSlug} failed` : `Applied to ${volumeSlug}`} size="sm" testid="apply-result">
  {#if res}
    <div class="stack">
      {#if res.error}<div class="banner bad small"><Icon name="alert" /><div class="grow">{res.error}</div></div>{:else}<div class="banner ok small"><Icon name="check" /><div class="grow">The cloud version is on the site ({res.resolution}).</div></div>{/if}
      <dl class="kv small">
        <dt>Written</dt><dd data-testid="res-written">{res.written}</dd>
        <dt>Deleted</dt><dd data-testid="res-deleted">{res.deleted}</dd>
        <dt>Kept live</dt><dd data-testid="res-kept">{res.keptLive}</dd>
        <dt>Snapshot first</dt><dd>{#if res.snapshotId}<a href="{base}/app/{siteSlug}/backups" data-testid="res-snapshot">{res.snapshotId}</a> <span class="tiny muted">(Backups)</span>{:else}—{/if}</dd>
        <dt>Restarted</dt><dd data-testid="res-restarted">{res.restarted.length ? res.restarted.join(', ') : 'none'}</dd>
        {#if res.afterVersionId}<dt>Uploaded</dt><dd class="small">snapshot after applying → version <span class="mono">{res.afterVersionId.slice(-8)}</span></dd>{/if}
      </dl>
    </div>
  {/if}
  {#snippet footer()}<Btn variant="primary" onclick={() => (resultOpen = false)} data-testid="apply-result-close">Done</Btn>{/snippet}
</Modal>

<style>
  .pushline {
    display: flex;
    flex-wrap: wrap;
    gap: 4px 10px;
  }
  .summary {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(110px, 1fr));
    gap: 8px;
  }
  .tile {
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    padding: 8px 10px;
    display: flex;
    flex-direction: column;
    gap: 2px;
    min-width: 0;
  }
  .tile.bad {
    border-color: color-mix(in srgb, var(--red) 50%, var(--border));
  }
  .tl {
    font-size: var(--fs-xs);
    color: var(--text-3);
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }
  .tile b {
    font-size: var(--fs-lg);
    font-variant-numeric: tabular-nums;
  }
  .tile b.small {
    font-size: var(--fs-sm);
  }
  .listhead {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    justify-content: space-between;
    align-items: center;
  }
  .filters {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
  }
  .chipbtn {
    all: unset;
    cursor: pointer;
    font-size: var(--fs-xs);
    padding: 2px 10px;
    border: 1px solid var(--border-strong);
    border-radius: 99px;
    color: var(--text-2);
  }
  .chipbtn.on {
    border-color: var(--accent);
    background: var(--accent-soft);
    color: var(--accent-text);
  }
  .chipbtn:focus-visible {
    outline: 2px solid var(--accent);
  }
  .items {
    list-style: none;
    margin: 0;
    padding: 0;
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    max-height: 52vh;
    overflow: auto;
  }
  .it {
    border-bottom: 1px solid var(--border);
    padding: 6px 10px;
  }
  .it:last-child {
    border-bottom: 0;
  }
  .it.need {
    background: color-mix(in srgb, var(--red) 6%, transparent);
  }
  .ih {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 4px 8px;
    min-width: 0;
  }
  .pth {
    all: unset;
    cursor: pointer;
    display: inline-flex;
    align-items: center;
    gap: 4px;
    font-size: var(--fs-sm);
    min-width: 0;
    max-width: 100%;
    color: var(--text);
  }
  .pth:hover {
    color: var(--accent-text);
  }
  .pth:focus-visible {
    outline: 2px solid var(--accent);
  }
  .out.cloud {
    color: var(--accent-text);
  }
  .out.live {
    color: var(--amber-text, var(--text-2));
  }
  .idiff {
    padding: 8px 0 4px;
  }
</style>
