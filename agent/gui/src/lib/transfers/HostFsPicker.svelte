<script lang="ts">
  /**
   * §21 #100 Send › Path: the server filesystem picker (GET …/transfers/fs, one directory level per call, lazy).
   * A directory pane with breadcrumbs, a typed path box, up, hidden toggle and inline expandable folders (a lazy tree).
   * Files only (folders cannot be sent); blocked (safety policy), unreadable and > 2 GiB entries are not selectable.
   */
  import { untrack } from 'svelte';
  import { api, errCode, errMessage, isApiError } from '$lib/api';
  import { bytes } from '$lib/format';
  import { fsClean, fsCrumbs, fsParent } from '@opm/shared/transfers';
  import type { HostFsEntry, HostFsListResponse } from '@opm/shared/types';
  import Modal from '$lib/ui/Modal.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Time from '$lib/ui/Time.svelte';
  import Toggle from '$opm/primitives/Toggle.svelte';

  const MAX = 2 * 1024 * 1024 * 1024;
  let { open = $bindable(false), site, onpick }: { open: boolean; site: string; onpick: (path: string, entry: HostFsEntry) => void } = $props();

  type Node = { loading: boolean; data: HostFsListResponse | null; error: string | null; code: string | null };
  let nodes = $state<Record<string, Node>>({});
  let expanded = $state<Record<string, boolean>>({});
  let cwd = $state('/');
  let typed = $state('/');
  let typedErr = $state<string | null>(null);
  let hidden = $state(false);
  let selected = $state<HostFsEntry | null>(null);
  let agentUser = $state<string | null>(null);
  let lastSite = '';

  const key = (p: string) => `${hidden ? 'h' : '-'}${p}`;

  async function load(p: string, force = false): Promise<Node> {
    const k = key(p);
    if (!force && nodes[k] && !nodes[k].error) return nodes[k];
    nodes[k] = { loading: true, data: nodes[k]?.data ?? null, error: null, code: null };
    try {
      const d = await api.hostFs({ siteSlug: site, query: { path: p, hidden } });
      agentUser = d.agentUser;
      nodes[k] = { loading: false, data: d, error: null, code: null };
    } catch (e) {
      const code = errCode(e);
      const msg = code === 'forbidden' ? `The agent (user ${agentUser ?? 'of the agent'}) cannot list this folder.` : code === 'not-found' ? `${p}: no such folder on ${site}.` : errMessage(e);
      nodes[k] = { loading: false, data: null, error: msg, code };
      if (isApiError(e) && code === 'invalid' && (e.details as { type?: string }).type === 'file') nodes[k].code = 'is-file';
    }
    return nodes[k];
  }

  function go(p: string) {
    const c = fsClean(p) ?? '/';
    cwd = c;
    typed = c;
    typedErr = null;
    void load(c);
  }

  async function submitTyped(e?: Event) {
    e?.preventDefault();
    const c = fsClean(typed.trim());
    if (!c) return (typedErr = 'An absolute path (start with /).');
    typedErr = null;
    const n = await load(c, true);
    if (n.code === 'is-file') {
      const parent = fsParent(c) ?? '/';
      delete nodes[key(c)];
      cwd = parent;
      const pn = await load(parent);
      const ent = pn.data?.entries.find((x) => x.path === c) ?? null;
      typed = c;
      if (ent && selectable(ent) === null) selected = ent;
      else if (ent) typedErr = `${c}: ${selectable(ent)}`;
      else typedErr = `${c} is a file; it is not listed here${hidden ? '' : ' (hidden files are off)'}.`;
      return;
    }
    if (n.code === 'not-found') {
      typedErr = n.error;
      delete nodes[key(c)];
      return;
    }
    cwd = c;
  }

  /** null = can be picked, else why not */
  function selectable(e: HostFsEntry): string | null {
    const kind = e.type === 'symlink' ? e.targetType : e.type;
    if (kind === 'missing') return 'broken link';
    if (e.blocked) return e.blocked;
    if (!e.readable) return 'no permission';
    if (kind === 'dir') return 'folders cannot be sent';
    if (kind !== 'file') return 'not a regular file';
    if ((e.sizeBytes ?? 0) > MAX) return 'larger than 2 GiB';
    return null;
  }
  const isDir = (e: HostFsEntry) => e.type === 'dir' || (e.type === 'symlink' && e.targetType === 'dir');
  /** where a folder entry leads (a symlink to a folder → its target) */
  function dirTarget(e: HostFsEntry): string {
    if (e.type === 'symlink' && e.linkTarget) return fsClean(e.linkTarget.startsWith('/') ? e.linkTarget : `${fsParent(e.path) ?? '/'}/${e.linkTarget}`) ?? e.path;
    return e.path;
  }
  function activate(e: HostFsEntry) {
    if (isDir(e)) {
      // looking inside a blocked area is allowed (listing is read-only); only unreadable folders stop here
      if (e.readable) go(dirTarget(e));
      return;
    }
    if (selectable(e) === null) {
      selected = e;
      typed = e.path;
      typedErr = null;
    }
  }
  async function toggle(e: HostFsEntry) {
    const t = dirTarget(e);
    expanded[t] = !expanded[t];
    if (expanded[t]) await load(t);
  }

  $effect(() => {
    if (!open) return;
    const s = site;
    untrack(() => {
      if (s !== lastSite) {
        lastSite = s;
        nodes = {};
        expanded = {};
        selected = null;
        agentUser = null;
        go('/');
      } else void load(cwd, true);
    });
  });
  function setHidden(v: boolean) {
    hidden = v;
    void load(cwd);
    for (const [p, on] of Object.entries(expanded)) if (on) void load(p);
  }

  const cur = $derived(nodes[key(cwd)]);
  const crumbs = $derived(fsCrumbs(cwd));
  function pick() {
    if (!selected) return;
    onpick(selected.path, selected);
    open = false;
  }
  function onKey(ev: KeyboardEvent) {
    if (ev.key === 'Backspace' && !(ev.target instanceof HTMLInputElement)) {
      const p = fsParent(cwd);
      if (p) {
        ev.preventDefault();
        go(p);
      }
    }
  }
</script>

{#snippet rows(list: HostFsEntry[], depth: number)}
  {#each list as e (e.path)}
    {@const why = selectable(e)}
    {@const dir = isDir(e)}
    {@const t = dir ? dirTarget(e) : e.path}
    <li class="ent" class:dim={!e.readable || !!e.blocked || (!dir && why !== null)} class:sel={selected?.path === e.path} data-testid="fs-entry" data-path={e.path}>
      <span class="ind" style="width:{depth * 16}px"></span>
      {#if dir && e.readable}
        <button class="exp" type="button" aria-label="{expanded[t] ? 'Collapse' : 'Expand'} {e.name}" aria-expanded={!!expanded[t]} onclick={() => toggle(e)} data-testid="fs-expand"><Icon name={expanded[t] ? 'chevronDown' : 'chevronRight'} size={14} /></button>
      {:else}
        <span class="exp"></span>
      {/if}
      <button class="name" type="button" onclick={() => activate(e)} ondblclick={() => (selectable(e) === null ? pick() : undefined)} disabled={!e.readable && dir} title={why ?? e.path}>
        <Icon name={!e.readable ? 'lock' : e.blocked ? 'shield' : dir ? 'folder' : e.type === 'symlink' ? 'link' : 'file'} size={15} />
        <span class="nm mono">{e.name}</span>
        {#if e.type === 'symlink'}<span class="tiny faint mono lt">→ {e.linkTarget ?? '?'}</span>{/if}
      </button>
      <span class="sz small nowrap">{dir ? '' : bytes(e.sizeBytes)}</span>
      <span class="mt small"><Time ts={e.mtime} /></span>
      {#if e.type === 'symlink' && e.targetType === 'missing'}<span class="why tiny">broken link</span>{:else if !e.readable}<span class="why tiny" data-testid="fs-denied">no permission</span>{:else if e.blocked}<span class="why tiny" data-testid="fs-blocked" title={e.blocked}>blocked · {e.blocked}</span>{:else if !dir && why}<span class="why tiny">{why}</span>{/if}
    </li>
    {#if dir && expanded[t]}
      {@const n = nodes[key(t)]}
      {#if !n || n.loading}
        <li class="ent sub muted small"><span class="ind" style="width:{(depth + 1) * 16 + 22}px"></span>Loading…</li>
      {:else if n.error}
        <li class="ent sub small bad-text"><span class="ind" style="width:{(depth + 1) * 16 + 22}px"></span>{n.error}</li>
      {:else if n.data}
        {#if n.data.entries.length === 0}<li class="ent sub muted small"><span class="ind" style="width:{(depth + 1) * 16 + 22}px"></span>Empty folder</li>{/if}
        {@render rows(n.data.entries, depth + 1)}
      {/if}
    {/if}
  {/each}
{/snippet}

<Modal bind:open title="Pick a file on {site}" description="The whole filesystem of the server, read-only. Folders cannot be sent — pick a file (or tar it on the server first). At most 2 GiB." size="lg" testid="fs-picker">
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="picker" onkeydown={onKey}>
    <form class="pathrow" onsubmit={submitTyped}>
      <Btn size="sm" icon="arrowUp" disabled={cwd === '/'} onclick={() => go(fsParent(cwd) ?? '/')} aria-label="Up one folder" data-testid="fs-up" />
      <input class="input mono grow" bind:value={typed} aria-label="Path" placeholder="/var/log" spellcheck="false" autocomplete="off" data-testid="fs-path" aria-invalid={typedErr ? 'true' : undefined} />
      <Btn size="sm" type="submit" data-testid="fs-go">Go</Btn>
    </form>
    {#if typedErr}<div class="field-err" role="alert" data-testid="fs-path-err">{typedErr}</div>{/if}
    <div class="bar">
      <nav class="crumbs" aria-label="Folder">
        {#each crumbs as c, i (c.path)}
          {#if i > 1}<span class="faint">/</span>{/if}
          <button type="button" class="crumb mono" class:here={c.path === cwd} onclick={() => go(c.path)} data-testid="fs-crumb">{c.name}</button>
        {/each}
      </nav>
      <Toggle checked={hidden} onchange={setHidden} label={!hidden && cur?.data?.hiddenCount ? `Hidden (${cur.data.hiddenCount})` : 'Hidden'} testid="fs-hidden" />
      <Btn size="xs" variant="ghost" icon="refresh" aria-label="Reload" onclick={() => load(cwd, true)} />
    </div>
    {#if cur?.data?.blocked}<div class="banner warn small" data-testid="fs-dir-blocked"><Icon name="shield" /><div class="grow">Files here cannot be sent: {cur.data.blocked}</div></div>{/if}
    {#if cur?.data?.truncated}<div class="banner info small" data-testid="fs-truncated"><Icon name="info" /><div class="grow">Showing the first {cur.data.entries.length.toLocaleString()} entries of this folder. Type the path of the file to reach any other.</div></div>{/if}
    <div class="list" role="region" aria-label="Folder contents" data-testid="fs-list">
      <div class="lhead tiny faint"><span>Name</span><span class="hr">Size</span><span class="hm">Modified</span></div>
      {#if !cur || (cur.loading && !cur.data)}
        <p class="muted small pad">Loading {cwd}…</p>
      {:else if cur.error}
        <div class="pad" data-testid="fs-error"><p class="bad-text small"><Icon name="lock" size={14} /> {cur.error}</p><Btn size="xs" icon="arrowUp" onclick={() => go(fsParent(cwd) ?? '/')}>Back to {fsParent(cwd) ?? '/'}</Btn></div>
      {:else if cur.data}
        {#if cur.data.entries.length === 0}<p class="muted small pad">Empty folder{!hidden && cur.data.hiddenCount ? ` (${cur.data.hiddenCount} hidden)` : ''}.</p>{/if}
        <ul>{@render rows(cur.data.entries, 0)}</ul>
      {/if}
    </div>
  </div>
  {#snippet footer()}
    <div class="foot grow">
      {#if selected}<span class="small"><Icon name="file" size={13} /> <strong class="mono" data-testid="fs-selected">{selected.path}</strong> · {bytes(selected.sizeBytes)}</span>{:else}<span class="muted small">Pick a file (click; double-click to use it).</span>{/if}
      {#if agentUser}<span class="tiny faint" data-testid="fs-user">Reading as <span class="mono">{agentUser}</span></span>{/if}
    </div>
    <Btn onclick={() => (open = false)}>Cancel</Btn>
    <Btn variant="primary" icon="arrowRight" disabled={!selected} onclick={pick} data-testid="fs-pick">Next</Btn>
  {/snippet}
</Modal>

<style>
  .picker {
    display: flex;
    flex-direction: column;
    gap: 8px;
    min-width: 0;
  }
  .pathrow {
    display: flex;
    gap: 6px;
    align-items: center;
  }
  .pathrow .input {
    min-width: 0;
  }
  .bar {
    display: flex;
    gap: 8px;
    align-items: center;
    flex-wrap: wrap;
  }
  .crumbs {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 2px;
    flex: 1;
    min-width: 0;
  }
  .crumb {
    border: 0;
    background: none;
    color: var(--accent);
    padding: 2px 4px;
    border-radius: var(--r-sm);
    cursor: pointer;
    font-size: var(--fs-sm);
    max-width: 180px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .crumb:hover {
    background: var(--surface-2);
  }
  .crumb.here {
    color: var(--text);
    font-weight: 600;
  }
  .list {
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    height: min(52vh, 440px);
    overflow: auto;
    background: var(--surface);
  }
  .lhead {
    position: sticky;
    top: 0;
    z-index: 1;
    display: grid;
    grid-template-columns: 1fr 84px 92px;
    gap: 8px;
    padding: 6px 10px 6px 38px;
    background: var(--surface-2);
    border-bottom: 1px solid var(--border);
    text-transform: uppercase;
    letter-spacing: 0.04em;
  }
  .hr {
    text-align: right;
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 2px 0;
  }
  .ent {
    display: grid;
    grid-template-columns: auto 22px minmax(0, 1fr) 84px 92px;
    grid-template-areas: 'ind exp name sz mt' 'ind exp why why why';
    align-items: center;
    column-gap: 8px;
    padding: 1px 10px 1px 4px;
    border-radius: var(--r-sm);
  }
  .ent:hover {
    background: var(--surface-2);
  }
  .ent.sel {
    background: var(--accent-soft);
    outline: 1px solid var(--accent);
  }
  .ent.sub {
    display: flex;
    padding: 4px 10px;
  }
  .ind {
    grid-area: ind;
    display: inline-block;
  }
  .exp {
    grid-area: exp;
    width: 22px;
    height: 22px;
    display: grid;
    place-items: center;
    border: 0;
    background: none;
    color: var(--text-3);
    border-radius: var(--r-sm);
    cursor: pointer;
    padding: 0;
  }
  .exp:hover {
    background: var(--border);
  }
  .name {
    grid-area: name;
    display: flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
    border: 0;
    background: none;
    color: inherit;
    text-align: left;
    padding: 5px 2px;
    cursor: pointer;
    font: inherit;
  }
  .name:disabled {
    cursor: not-allowed;
  }
  .nm {
    flex: 0 1 auto;
    min-width: 3ch;
    max-width: 100%;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: var(--fs-sm);
  }
  .lt {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    flex: 0 100 auto;
    min-width: 0;
  }
  .sz {
    grid-area: sz;
    text-align: right;
  }
  .mt {
    grid-area: mt;
    color: var(--text-3);
  }
  .why {
    grid-area: why;
    color: var(--text-3);
    padding: 0 0 4px 2px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .dim .name {
    opacity: 0.6;
  }
  .pad {
    padding: 12px;
  }
  .foot {
    display: flex;
    flex-direction: column;
    gap: 2px;
    min-width: 0;
    margin-right: auto;
    overflow: hidden;
  }
  .foot strong {
    word-break: break-all;
  }
  @media (max-width: 600px) {
    .lhead {
      grid-template-columns: 1fr 70px;
    }
    .lhead .hm,
    .ent .mt {
      display: none;
    }
    .ent {
      grid-template-columns: auto 22px minmax(0, 1fr) 70px;
      grid-template-areas: 'ind exp name sz' 'ind exp why why';
    }
  }
</style>
