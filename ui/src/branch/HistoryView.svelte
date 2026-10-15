<script lang="ts">
  /**
   * Commit history of one branch (§21 #104 §3.4): list (seq · kind · first line · author · time · source · stats), row →
   * detail (full message, links, changes), pick two → compare (MergeView diffOnly), Revert… / Reset to this commit…
   * (CommitDialog with the plan; history is never rewritten — both make a NEW commit).
   */
  import type { BranchRef, CommitDetail, CommitSummary, MergeItem, MergePlan } from '@opm/shared/branch';
  import { commitSummary, pointLabel } from '@opm/shared/branch';
  import Btn from '../primitives/Btn.svelte';
  import Icon from '../primitives/Icon.svelte';
  import CommitDialog from './CommitDialog.svelte';
  import MergeView from './MergeView.svelte';
  import { useBranchHost, pointOf, type MergeResult } from './host';
  import { COMMIT_KIND_LABEL, COMMIT_KIND_TONE, groupItems, relTime, EFFECT_LABEL } from './labels';

  interface Props {
    branch: BranchRef;
    label: string;
    onchanged?: (r: MergeResult) => void;
    /** reload trigger (increment from the app after an outside change) */
    version?: number;
    testid?: string;
  }
  let { branch, label, onchanged, version = 0, testid = 'history' }: Props = $props();
  const host = useBranchHost();
  let commits = $state<CommitSummary[]>([]);
  let cursor = $state<string | null>(null);
  let loading = $state(false);
  let error = $state<string | null>(null);
  let selected = $state<string | null>(null);
  let detail = $state<CommitDetail | null>(null);
  let detailError = $state<string | null>(null);
  let pickA = $state<string | null>(null);
  let pickB = $state<string | null>(null);
  let cmp = $state<MergePlan | null>(null);
  let cmpError = $state<string | null>(null);
  let dlg = $state<{ mode: 'revert' | 'reset'; commit: CommitSummary; items: MergeItem[] } | null>(null);
  let dlgOpen = $state(false);
  let busy = $state(false);

  async function load(more = false) {
    loading = true;
    error = null;
    try {
      const r = await host.commits(branch, more ? cursor : null);
      commits = more ? [...commits, ...r.items] : r.items;
      cursor = r.cursor ?? null;
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      loading = false;
    }
  }
  $effect(() => {
    void version;
    void branch;
    void load(false);
  });
  async function open(c: CommitSummary) {
    selected = selected === c.commitId ? null : c.commitId;
    detail = null;
    detailError = null;
    if (!selected) return;
    try {
      detail = await host.commit(branch, c.commitId);
    } catch (e) {
      detailError = e instanceof Error ? e.message : String(e);
    }
  }
  function pick(id: string, on: boolean) {
    if (!on) {
      if (pickA === id) pickA = pickB;
      if (pickA === pickB || pickB === id) pickB = null;
      return;
    }
    if (!pickA) pickA = id;
    else if (!pickB) pickB = id;
    else {
      pickA = pickB;
      pickB = id;
    }
    cmp = null;
  }
  async function compare() {
    if (!pickA || !pickB) return;
    const a = commits.find((c) => c.commitId === pickA)!;
    const b = commits.find((c) => c.commitId === pickB)!;
    const [older, newer] = a.seq < b.seq ? [a, b] : [b, a];
    cmpError = null;
    try {
      cmp = await host.compare({ source: pointOf(branch, newer.commitId), target: pointOf(branch, older.commitId), diffOnly: true });
    } catch (e) {
      cmpError = e instanceof Error ? e.message : String(e);
    }
  }
  async function start(mode: 'revert' | 'reset', c: CommitSummary) {
    const plan = mode === 'revert' ? host.revertPlan : host.resetPlan;
    let items: MergeItem[] = [];
    try {
      items = plan ? await plan.call(host, branch, c.commitId) : [];
    } catch (e) {
      detailError = e instanceof Error ? e.message : String(e);
      return;
    }
    dlg = { mode, commit: c, items };
    dlgOpen = true;
  }
  async function submit(message: string) {
    if (!dlg) return;
    busy = true;
    try {
      const head = commits[0]?.commitId ?? null;
      const conflicts = dlg.items.filter((i) => i.kind === 'conflict');
      const r = dlg.mode === 'revert' ? await host.revert!(branch, dlg.commit.commitId, message, head, conflicts.map((i) => ({ key: i.key, take: 'source' as const }))) : await host.reset!(branch, dlg.commit.commitId, message, head);
      dlgOpen = false;
      host.changed?.(r);
      onchanged?.(r);
      await load(false);
    } finally {
      busy = false;
    }
  }
  const stats = (c: CommitSummary) => `+${c.stats.added} ~${c.stats.changed} −${c.stats.removed}`;
  const isHead = (c: CommitSummary) => commits[0]?.commitId === c.commitId;
</script>

<div class="hv" data-testid={testid}>
  <div class="row between wrap">
    <p class="small muted m0">{label}: {commits.length}{cursor ? '+' : ''} commit{commits.length === 1 ? '' : 's'} · pick two to compare</p>
    <div class="row gap-s">
      <Btn size="sm" disabled={!pickA || !pickB} onclick={compare} icon="diff" data-testid="history-compare">Compare</Btn>
      <Btn size="sm" variant="ghost" icon="refresh" onclick={() => load(false)} data-testid="history-refresh">Refresh</Btn>
    </div>
  </div>
  {#if error}<div class="banner bad"><Icon name="alert" /><div class="grow">{error}</div><Btn size="sm" onclick={() => load(false)}>Retry</Btn></div>{/if}
  {#if cmpError}<div class="banner bad small"><div class="grow">{cmpError}</div></div>{/if}
  {#if cmp}
    <div class="card card-pad" data-testid="history-compare-result">
      <div class="row between"><strong class="small">Compare</strong><Btn size="xs" variant="ghost" icon="x" aria-label="Close compare" onclick={() => (cmp = null)} /></div>
      <MergeView plan={cmp} diffOnly testid="history-diff" />
    </div>
  {/if}
  {#if !commits.length && !loading && !error}<div class="card card-pad muted" data-testid="history-empty">No commits yet.</div>{/if}
  <ul class="list card">
    {#each commits as c (c.commitId)}
      <li data-testid="history-row-{c.seq}" class:sel={selected === c.commitId}>
        <div class="line">
          <input type="checkbox" class="cb" checked={pickA === c.commitId || pickB === c.commitId} onchange={(e) => pick(c.commitId, (e.currentTarget as HTMLInputElement).checked)} aria-label="Pick seq {c.seq} to compare" data-testid="history-pick-{c.seq}" />
          <button type="button" class="open" onclick={() => open(c)} aria-expanded={selected === c.commitId} data-testid="history-open-{c.seq}">
            <span class="mono seq">#{c.seq}</span>
            <span class="badge {COMMIT_KIND_TONE[c.kind]}">{COMMIT_KIND_LABEL[c.kind]}</span>
            {#if isHead(c)}<span class="badge green">head</span>{/if}
            <span class="msg ellipsis">{commitSummary(c.message)}</span>
          </button>
          <span class="meta tiny muted">
            {#if c.source}<span class="chip tiny" title="source">from {pointLabel(c.source.label, c.source.seq)}</span>{/if}
            <span class="mono">{stats(c)}</span>
            <span>{c.author.email}</span>
            <span title={c.createdAt}>{relTime(c.createdAt)}</span>
          </span>
        </div>
        {#if selected === c.commitId}
          <div class="detail" data-testid="history-detail">
            {#if detailError}<div class="banner bad small"><div class="grow">{detailError}</div></div>{/if}
            {#if c.message.trim().split(/\r?\n/).slice(1).join('\n').trim()}<pre class="full" data-testid="history-message-body">{c.message.trim().split(/\r?\n/).slice(1).join('\n').trim()}</pre>{/if}
            <p class="tiny muted m0">
              {c.author.name ?? c.author.email}{c.author.siteSlug ? ` · on site ${c.author.siteSlug}` : ''} · {c.createdAt} · <span class="mono">{c.hash.slice(0, 19)}…</span>
              {#if c.revertOf} · reverts #{c.revertOf.seq}{/if}
              {#if c.resetTo} · reset to #{c.resetTo.seq}{/if}
              {#if c.rollbackOf} · rollback to snapshot {c.rollbackOf.snapshotId}{/if}
              {#if c.source} · {c.kind === 'create' ? 'created from' : 'from'} {pointLabel(c.source.label, c.source.seq)}{c.source.picks === 'all' ? '' : ` (${c.source.picks.length} picked, ${c.source.skipped} skipped)`}{/if}
            </p>
            {#if detail}
              {#if detail.changes.length}
                <div class="changes">
                  {#each groupItems(detail.changes) as g (g.id)}
                    <div class="tiny strong">{g.title}</div>
                    <ul>{#each g.items as i (i.key)}<li class="small"><span class="badge {i.effect === 'remove' ? 'red' : i.effect === 'add' ? 'green' : 'blue'}">{EFFECT_LABEL[i.effect]}</span> {i.label}</li>{/each}</ul>
                  {/each}
                </div>
              {:else}<p class="tiny muted">No config changes.</p>{/if}
            {:else if !detailError}<p class="tiny muted">Loading…</p>{/if}
            {#if host.canWrite}
              <div class="row gap-s">
                {#if host.revert && c.kind !== 'create' && c.parentId}<Btn size="sm" icon="undo" onclick={() => start('revert', c)} data-testid="revert">Revert…</Btn>{/if}
                {#if host.reset && !isHead(c)}<Btn size="sm" icon="history" onclick={() => start('reset', c)} data-testid="reset">Reset to this commit…</Btn>{/if}
              </div>
            {/if}
          </div>
        {/if}
      </li>
    {/each}
  </ul>
  {#if cursor}<Btn size="sm" busy={loading} onclick={() => load(true)} data-testid="history-more">Load more</Btn>{/if}
  {#if dlg}
    <CommitDialog
      bind:open={dlgOpen}
      title={dlg.mode === 'revert' ? `Revert #${dlg.commit.seq}` : `Reset ${label} to #${dlg.commit.seq}`}
      primaryLabel={dlg.mode === 'revert' ? (host.scope === 'agent' ? 'Revert & apply' : 'Revert') : host.scope === 'agent' ? 'Reset & apply' : 'Reset'}
      intro={dlg.mode === 'revert' ? `A new commit undoes the changes of #${dlg.commit.seq} “${commitSummary(dlg.commit.message)}”.${dlg.items.some((i) => i.kind === 'conflict') ? ' Later commits changed some of the same items — the revert takes the older value there.' : ''}` : `A new commit sets the config back to #${dlg.commit.seq}. History is kept.`}
      items={dlg.items}
      suggestPrefix={dlg.mode === 'revert' ? `Revert "${commitSummary(dlg.commit.message)}" — ` : `Reset to #${dlg.commit.seq} — `}
      author={host.actor()}
      danger={dlg.mode === 'reset'}
      {busy}
      onsubmit={submit}
      testid="history-commit-dialog" />
  {/if}
</div>

<style>
  .hv {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .m0 {
    margin: 0;
  }
  .wrap {
    flex-wrap: wrap;
    gap: 8px;
  }
  .list {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .list > li {
    border-top: 1px solid var(--border);
    padding: 8px 12px;
  }
  .list > li:first-child {
    border-top: 0;
  }
  .list > li.sel {
    background: var(--surface-hover);
  }
  .line {
    display: flex;
    gap: 8px;
    align-items: center;
    flex-wrap: wrap;
  }
  .cb {
    width: 16px;
    height: 16px;
    accent-color: var(--accent);
  }
  .open {
    flex: 1;
    min-width: 0;
    display: flex;
    gap: 8px;
    align-items: center;
    background: none;
    border: 0;
    padding: 0;
    color: var(--text);
    font: inherit;
    cursor: pointer;
    text-align: left;
  }
  .msg {
    min-width: 0;
  }
  .seq {
    color: var(--text-muted, #777);
  }
  .meta {
    display: inline-flex;
    gap: 8px;
    align-items: center;
    flex-wrap: wrap;
  }
  .detail {
    margin: 8px 0 0 24px;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .full {
    margin: 0;
    white-space: pre-wrap;
    font-family: inherit;
    font-size: var(--fs-sm);
  }
  .changes ul {
    list-style: none;
    padding: 0;
    margin: 2px 0 6px;
  }
  @media (max-width: 600px) {
    .detail {
      margin-left: 0;
    }
    .meta {
      flex-basis: 100%;
      padding-left: 24px;
    }
  }
</style>
