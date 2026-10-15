<script lang="ts">
  /**
   * Frame of a dev tools workbench (SQL / Redis / MQTT / HTTP, §21 #97): the gate (site, switch), the connection of
   * `?c=` (picker grouped by kind, remembered per site + tool), its remembered state, Test / Disconnect /
   * Edit / Duplicate, "Add connection" (drawer pre-set to the kind), the preset problem banner. The workbench is
   * rendered under {#key connection id}.
   */
  import type { Snippet } from 'svelte';
  import { untrack } from 'svelte';
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import type { DevToolConnection, DevToolKind, DevToolsInfo } from '@opm/shared/devtools';
  import { appHref } from '$lib/nav';
  import { toasts } from '$lib/state/toast.svelte';
  import Select from '$lib/ui/Select.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import Empty from '$lib/ui/Empty.svelte';
  import { relTime } from '$lib/format';
  import DevGate from './DevGate.svelte';
  import ConnectionDrawer from './ConnectionDrawer.svelte';
  import { dev } from './api';
  import { devStore } from './state.svelte';
  import { KIND_ICON, KIND_SHORT, PAGE_ICON, PAGE_KINDS, PAGE_LABEL, connOptions, lastConn, stateText, stateTone, type DevPage } from './kinds';

  let { tool, workbench }: { tool: DevPage; workbench: Snippet<[DevToolConnection, DevToolsInfo, string]> } = $props();
  const kinds = $derived(PAGE_KINDS[tool]);
  const slug = $derived(page.params.siteSlug ?? '*');
  const cParam = $derived(page.url.searchParams.get('c'));
  const kindParam = $derived(page.url.searchParams.get('kind') as DevToolKind | null);
  const list = $derived(devStore.site === slug ? devStore.conns.filter((c) => kinds.includes(c.kind)) : []);
  const conn = $derived(list.find((c) => c.id === cParam) ?? null);

  // no / unknown ?c= → the last one used here, else the first (of ?kind= when given)
  $effect(() => {
    const l = list;
    const c = cParam;
    if (!devStore.loaded || devStore.site !== slug || !l.length) return;
    if (c && l.some((x) => x.id === c)) {
      untrack(() => lastConn.set(slug, tool, c));
      return;
    }
    const pref = lastConn.get(slug, tool);
    const pick = (kindParam ? l.find((x) => x.kind === kindParam) : null) ?? l.find((x) => x.id === pref) ?? l.find((x) => !x.problem) ?? l[0]!;
    untrack(() => goto(appHref(slug, `devtools/${tool}?c=${encodeURIComponent(pick.id)}`), { replaceState: true, keepFocus: true, noScroll: true }));
  });

  let drawer = $state(false);
  let editing = $state<DevToolConnection | null>(null);
  let newKind = $state<DevToolKind>('postgres');
  let busy = $state<string | null>(null);
  function add(k?: DevToolKind) {
    editing = null;
    newKind = k ?? (kindParam && kinds.includes(kindParam) ? kindParam : conn?.kind && kinds.includes(conn.kind) ? conn.kind : kinds[0]!);
    drawer = true;
  }
  function select(id: string) {
    if (id === '__add') return add();
    goto(appHref(slug, `devtools/${tool}?c=${encodeURIComponent(id)}`), { keepFocus: true, noScroll: true });
  }
  async function testIt() {
    if (!conn) return;
    busy = 'test';
    try {
      const r = await dev.testConnection({ siteSlug: slug, connectionId: conn.id });
      if (r.ok) toasts.ok(`${conn.name}: connected in ${r.latencyMs} ms`, r.serverVersion ?? undefined);
      else toasts.push('bad', `${conn.name}: test failed`, r.message);
      await devStore.refreshConns();
    } catch (e) {
      toasts.err(e, 'Test failed');
    } finally {
      busy = null;
    }
  }
  async function disconnect() {
    if (!conn) return;
    busy = 'disc';
    try {
      const r = await dev.disconnect({ siteSlug: slug, connectionId: conn.id });
      toasts.ok(r.closed ? `${conn.name}: disconnected (the next use reconnects)` : `${conn.name} was not connected`);
      await devStore.refreshConns();
    } catch (e) {
      toasts.err(e, 'Disconnect failed');
    } finally {
      busy = null;
    }
  }
  async function duplicate() {
    if (!conn) return;
    busy = 'dup';
    try {
      const d = await dev.duplicateConnection({ siteSlug: slug, connectionId: conn.id, body: { name: `${conn.name} (local)` } });
      devStore.upsert(d);
      toasts.ok(`Duplicated as ${d.name}`, 'A manual connection you can edit.');
      await goto(appHref(slug, `devtools/${tool}?c=${encodeURIComponent(d.id)}`));
      editing = d;
      drawer = true;
    } catch (e) {
      toasts.err(e, 'Duplicate failed');
    } finally {
      busy = null;
    }
  }
  function saved(c: DevToolConnection) {
    devStore.upsert(c);
    if (c.id !== conn?.id) goto(appHref(slug, `devtools/${tool}?c=${encodeURIComponent(c.id)}`));
    void devStore.refreshConns();
  }
  const opts = $derived([...connOptions(devStore.conns, kinds), { value: '__add', label: 'Add connection…', icon: 'plus', action: true }]);
  // the remembered state refreshes while the page is open (running queries, idle close)
  $effect(() => {
    if (slug === '*') return;
    const t = setInterval(() => devStore.refreshConns(), 20_000);
    return () => clearInterval(t);
  });
</script>

<DevGate what={PAGE_LABEL[tool]}>
  {#snippet children(info, site)}
    <div class="dev" data-testid="dev-{tool}">
      <header class="bar">
        <span class="tile" aria-hidden="true"><Icon name={PAGE_ICON[tool]} size={18} /></span>
        <div class="ttl">
          <h1 class="page-title" data-testid="page-title">{PAGE_LABEL[tool]}</h1>
          <span class="faint xs">Dev tools · runs on <span class="mono">{site}</span></span>
        </div>
        {#if list.length}
          <div class="pick"><Select value={conn?.id ?? null} options={opts} onchange={select} ariaLabel="Connection" testid="conn-select" placeholder="Choose a connection…" searchable={opts.length > 8} /></div>
        {/if}
        {#if conn}
          <div class="meta" data-testid="conn-meta">
            <span class="badge {stateTone(conn)}" title="{stateText(conn)}{conn.state.lastUsedAt ? ` · used ${relTime(conn.state.lastUsedAt)}` : ''}{conn.resolvedTarget ? ` · ${conn.resolvedTarget}` : ''}" data-testid="conn-state"><i class="dot"></i>{stateText(conn).split(' · ')[0]}</span>
            {#if conn.state.serverVersion}<span class="faint small nowrap ver">{conn.state.serverVersion}</span>{/if}
            {#if kinds.length > 1}<span class="badge outline"><Icon name={KIND_ICON[conn.kind]} size={12} />{KIND_SHORT[conn.kind]}</span>{/if}
            {#if conn.source === 'preset'}<span class="badge violet" title="Defined in the cloud configuration; read-only here" data-testid="conn-preset">preset · from revision seq {conn.presetRevSeq ?? '?'}</span>{/if}
            {#if conn.readOnly}<span class="badge amber" data-testid="conn-readonly-badge"><Icon name="lock" size={11} />read-only</span>{/if}
            {#if conn.state.runningQueries}<span class="badge blue">{conn.state.runningQueries} running</span>{/if}
          </div>
          <div class="acts">
            <Btn size="sm" icon="plug" busy={busy === 'test'} onclick={testIt} data-testid="conn-test-btn">Test</Btn>
            {#if conn.state.status === 'connected'}<Btn size="sm" variant="ghost" icon="power" busy={busy === 'disc'} onclick={disconnect} data-testid="conn-disconnect">Disconnect</Btn>{/if}
            {#if conn.source === 'preset'}
              <Btn size="sm" variant="ghost" icon="copy" busy={busy === 'dup'} onclick={duplicate} data-testid="conn-duplicate">Duplicate to edit locally</Btn>
            {:else}
              <Btn size="sm" variant="ghost" icon="edit" onclick={() => ((editing = conn), (drawer = true))} data-testid="conn-edit">Edit</Btn>
            {/if}
          </div>
        {/if}
      </header>
      {#if conn?.problem}
        <div class="banner bad" data-testid="conn-problem"><Icon name="alert" /><div class="grow"><strong>This preset cannot be used:</strong> {conn.problem}. Fix it in the cloud configuration, or duplicate it and edit the copy.</div></div>
      {/if}
      {#if !list.length}
        <Empty icon={PAGE_ICON[tool]} title="No {kinds.map((k) => KIND_SHORT[k]).join(' / ')} connection on {site} yet" text="Add one here (stored on this site's agent), or define a preset in the cloud configuration of the deployment (it arrives with the next revision).">
          <div class="row gap-s" style="justify-content:center;flex-wrap:wrap">
            {#each kinds as k (k)}<Btn variant="primary" icon="plus" onclick={() => add(k)} data-testid="add-conn-{k}">Add {KIND_SHORT[k]} connection</Btn>{/each}
          </div>
        </Empty>
      {:else if conn}
        {#key conn.id}
          {@render workbench(conn, info, site)}
        {/key}
      {:else}
        <span class="skel" style="width:100%;height:300px"></span>
      {/if}
    </div>
    <ConnectionDrawer bind:open={drawer} {site} {info} conn={editing} kind={newKind} {kinds} onsaved={saved} />
  {/snippet}
</DevGate>

<style>
  .dev {
    display: flex;
    flex-direction: column;
    gap: 10px;
    min-width: 0;
  }
  .bar {
    display: flex;
    align-items: center;
    gap: 10px 12px;
    flex-wrap: wrap;
    min-width: 0;
  }
  .tile {
    width: 34px;
    height: 34px;
    border-radius: var(--r-md);
    border: 1px solid var(--border);
    background: var(--surface);
    display: grid;
    place-items: center;
    color: var(--accent-text);
    flex: none;
  }
  .ttl {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }
  .ttl h1 {
    margin: 0;
    font-size: var(--fs-lg);
    line-height: 1.2;
  }
  .pick {
    width: 280px;
    max-width: 100%;
  }
  .meta {
    display: flex;
    gap: 6px;
    align-items: center;
    flex-wrap: wrap;
    min-width: 0;
  }
  .acts {
    display: flex;
    gap: 6px;
    margin-left: auto;
    flex-wrap: wrap;
  }
  @media (max-width: 1500px) {
    .ver {
      display: none;
    }
  }
  @media (max-width: 700px) {
    .pick {
      width: 100%;
      order: 3;
    }
    .acts {
      margin-left: 0;
    }
  }
</style>
