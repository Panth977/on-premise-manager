<script lang="ts">
  /**
   * Saved queries / requests of the site ("collections", §21 #97): folders by `collection`, open (new tab), rename /
   * move (modal), delete (confirm). Shared by every operator of the site (createdBy shown).
   */
  import type { DevToolKind, DevToolSaved } from '@opm/shared/devtools';
  import { toasts } from '$lib/state/toast.svelte';
  import { relTime } from '$lib/format';
  import Icon from '$lib/ui/Icon.svelte';
  import Confirm from '$lib/ui/Confirm.svelte';
  import SaveDialog from './SaveDialog.svelte';
  import { dev } from './api';

  interface Props {
    site: string;
    kinds: DevToolKind[];
    connectionId: string;
    onopen: (s: DevToolSaved) => void;
    /** bump to reload */
    version?: number;
    preview?: (s: DevToolSaved) => string;
  }
  let { site, kinds, connectionId, onopen, version = 0, preview }: Props = $props();
  let items = $state<DevToolSaved[] | null>(null);
  let q = $state('');
  let closed = $state<Record<string, boolean>>({});
  let edit = $state<DevToolSaved | null>(null);
  let editOpen = $state(false);
  let del = $state<DevToolSaved | null>(null);
  let delOpen = $state(false);
  async function load() {
    try {
      const all = await Promise.all(kinds.map((k) => dev.listSaved({ siteSlug: site, query: { kind: k, connectionId } })));
      items = all.flatMap((r) => r.items);
    } catch (e) {
      toasts.err(e, 'Saved items could not be loaded');
      items = [];
    }
  }
  $effect(() => {
    void version;
    void connectionId;
    load();
  });
  const groups = $derived.by(() => {
    const m = new Map<string, DevToolSaved[]>();
    for (const s of (items ?? []).filter((x) => !q.trim() || `${x.collection} ${x.name} ${x.content}`.toLowerCase().includes(q.trim().toLowerCase())).sort((a, b) => a.name.localeCompare(b.name)))
      m.set(s.collection, [...(m.get(s.collection) ?? []), s]);
    return [...m.entries()].sort(([a], [b]) => (a === '' ? 1 : b === '' ? -1 : a.localeCompare(b)));
  });
  async function remove() {
    if (!del) return;
    try {
      await dev.deleteSaved({ siteSlug: site, savedId: del.id });
      toasts.ok(`Deleted ${del.name}`);
      await load();
    } catch (e) {
      toasts.err(e, 'Delete failed');
    }
  }
</script>

<div class="sp" data-testid="saved-panel">
  <div class="th"><input class="input sm grow" placeholder="Search saved…" bind:value={q} aria-label="Search saved items" /></div>
  <div class="body">
    {#if items === null}
      <div class="pad"><span class="skel" style="width:70%"></span></div>
    {:else if !groups.length}
      <p class="faint small pad">{q.trim() ? 'No match.' : 'Nothing saved yet. Use Save (Ctrl/⌘-S) to keep the current tab for everyone on this site.'}</p>
    {:else}
      {#each groups as [col, list] (col)}
        <button class="folder" onclick={() => (closed = { ...closed, [col]: !closed[col] })} aria-expanded={!closed[col]}>
          <Icon name={closed[col] ? 'chevronRight' : 'chevronDown'} size={12} /><Icon name={closed[col] ? 'folder' : 'folderOpen'} size={13} /><span>{col || 'Unsorted'}</span><span class="cnt">{list.length}</span>
        </button>
        {#if !closed[col]}
          {#each list as s (s.id)}
            <div class="it" data-testid="saved-{s.name}">
              <button class="open" onclick={() => onopen(s)} title={s.content.slice(0, 400)}>
                <span class="nm">{s.name}</span>
                <span class="pv mono">{preview ? preview(s) : s.content.replace(/\s+/g, ' ').slice(0, 90)}</span>
                <span class="by faint">{s.createdBy ?? '—'} · {relTime(s.updatedAt)}{s.connectionId ? '' : ' · any connection'}</span>
              </button>
              <span class="acts">
                <button class="btn ghost icon xs" aria-label="Rename / move {s.name}" title="Rename / move" onclick={() => ((edit = s), (editOpen = true))}><Icon name="edit" size={12} /></button>
                <button class="btn ghost icon xs" aria-label="Delete {s.name}" title="Delete" onclick={() => ((del = s), (delOpen = true))} data-testid="saved-del-{s.name}"><Icon name="trash" size={12} /></button>
              </span>
            </div>
          {/each}
        {/if}
      {/each}
    {/if}
  </div>
</div>
{#if edit}
  <SaveDialog bind:open={editOpen} {site} kind={edit.kind} connectionId={edit.connectionId} content={edit.content} existing={edit} renameOnly onsaved={() => load()} collections={[...new Set((items ?? []).map((x) => x.collection).filter(Boolean))]} />
{/if}
<Confirm bind:open={delOpen} title="Delete {del?.name}?" confirmLabel="Delete" danger onconfirm={remove} testid="saved-del-confirm">
  <p>Removes it for everyone on site <span class="mono">{site}</span>.</p>
</Confirm>

<style>
  .sp {
    display: flex;
    flex-direction: column;
    height: 100%;
    min-height: 0;
  }
  .th {
    display: flex;
    padding: 8px;
    border-bottom: 1px solid var(--border);
  }
  .th .input {
    height: 28px;
  }
  .body {
    overflow: auto;
    flex: 1;
    padding: 4px;
  }
  .pad {
    padding: 8px 10px;
    margin: 0;
  }
  .folder {
    all: unset;
    box-sizing: border-box;
    width: 100%;
    display: flex;
    gap: 5px;
    align-items: center;
    padding: 4px 6px;
    font-weight: var(--fw-semibold);
    font-size: 13px;
    cursor: pointer;
    border-radius: var(--r-sm);
  }
  .folder:hover {
    background: var(--surface-hover);
  }
  .cnt {
    margin-left: auto;
    font-size: 11px;
    color: var(--text-3);
  }
  .it {
    display: flex;
    align-items: flex-start;
    margin-left: 14px;
    border-radius: var(--r-sm);
  }
  .it:hover {
    background: var(--surface-hover);
  }
  .open {
    all: unset;
    box-sizing: border-box;
    flex: 1;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 1px;
    padding: 5px 6px;
    cursor: pointer;
  }
  .nm {
    font-size: 13px;
    font-weight: var(--fw-medium);
  }
  .pv {
    font-size: 11px;
    color: var(--text-2);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .by {
    font-size: 10.5px;
  }
  .acts {
    display: flex;
    opacity: 0;
    padding-top: 4px;
  }
  .it:hover .acts,
  .it:focus-within .acts {
    opacity: 1;
  }
  @media (hover: none) {
    .acts {
      opacity: 1;
    }
  }
</style>
