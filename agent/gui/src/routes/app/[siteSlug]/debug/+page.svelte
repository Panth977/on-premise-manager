<script lang="ts">
  import MultiSelect from '$lib/ui/MultiSelect.svelte';
  import SegmentedControl from '$lib/ui/SegmentedControl.svelte';
  import { untrack } from 'svelte';
  import { page } from '$app/state';
  import { api, errMessage } from '$lib/api';
  import { poll } from '$lib/state/resource.svelte';
  import { overview } from '$lib/state/app.svelte';
  import type { DebugLine } from '@opm/shared/types';
  import PageHead from '$lib/ui/PageHead.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Empty from '$lib/ui/Empty.svelte';
  import Stale from '$lib/ui/Stale.svelte';
  import { ansiPrefs, textHtml } from '$lib/ansi.svelte';
  import LogViewer from '$lib/tools/LogViewer.svelte';
  import PageTabs, { tabOf } from '$lib/ui/PageTabs.svelte';
  import ChooseSite from '$lib/ui/ChooseSite.svelte';

  const TABS = [
    { k: 'log', label: 'Debug log', icon: 'bug' },
    { k: 'agent', label: 'Live agent log', icon: 'logs' },
  ];
  const tab = $derived(tabOf(page.url, ['log', 'agent']));

  const MODULES = ['cloudlink', 'mesh', 'stager', 'distrib', 'expr', 'driver', 'edge', 'health', 'coordinator', 'snapshot', 'pipe', 'transfer', 'tunnel', 'auth', 'gui'];
  const slug = $derived(page.params.siteSlug ?? '*');
  let level = $state('info');
  /** modules filter (custom MultiSelect): none = all; one = server-side filter; several = filtered here */
  let modules = $state<string[]>([]);
  const module = $derived(modules.length === 1 ? modules[0] : '');
  let q = $state('');
  let since = $state('');
  let limit = $state(500);
  let auto = $state(true);
  let lines = $state<DebugLine[]>([]);
  const shown = $derived(modules.length > 1 ? lines.filter((l) => modules.includes(l.module)) : lines);
  let loaded = $state(false);
  let busy = $state(false);
  let error = $state<string | null>(null);
  let gen = 0;

  async function load() {
    const my = ++gen;
    busy = true;
    try {
      const r = await api.siteDebug({ siteSlug: slug, query: { level, module: module || undefined, q: q || undefined, since: since ? new Date(since).toISOString() : undefined, limit } });
      if (my !== gen) return;
      lines = r.items;
      loaded = true;
      error = null;
    } catch (e) {
      if (my === gen) error = errMessage(e);
    } finally {
      if (my === gen) busy = false;
    }
  }
  $effect(() => {
    void slug;
    void level;
    void module;
    void modules.length;
    void since;
    void limit;
    untrack(() => load());
  });
  $effect(() => {
    if (!auto || tab !== 'log') return;
    return poll(() => load(), 5000);
  });
  let t: ReturnType<typeof setTimeout>;
  const onQ = (v: string) => {
    q = v;
    clearTimeout(t);
    t = setTimeout(load, 300);
  };
  const stale = $derived(slug === '*' ? (overview.data?.sites.filter((s) => !s.reachable && s.enrolled) ?? []) : []);
  const lvTone = { debug: 'faint', info: 'info', warn: 'warn', error: 'bad' } as const;
</script>

<PageHead icon="bug" eyebrow={slug === '*' ? 'Agent · all sites (merged)' : `Agent · site ${slug}`} title="Debug" sub="The agent debug log (JSON lines, newest first) and the live agent log stream. Secret values are never written." />

<PageTabs tabs={TABS} label="Debug sections" testid="debug-tabs" />
{#if tab === 'agent'}
  {#if slug === '*'}
    <ChooseSite what="the live agent log" text={'"All sites" is selected; the live log streams from one agent. (The Debug log tab merges every site.)'} />
  {:else}
    <LogViewer site={slug} src="agent" />
  {/if}
{:else}

{#each stale as s (s.siteSlug)}
  <div class="banner warn small" style="margin-bottom:10px"><Icon name="wifiOff" /><div class="grow">Site <strong>{s.siteSlug}</strong> does not answer; its lines are missing.</div><Stale lastSeenAt={s.lastSeenAt} /></div>
{/each}

<section class="card">
  <div class="card-head">
    <div class="row grow">
      <SegmentedControl size="sm" label="Minimum level" options={[{ value: 'debug', label: '≥ debug' }, { value: 'info', label: '≥ info' }, { value: 'warn', label: '≥ warn' }, { value: 'error', label: 'error' }]} value={level} onchange={(v) => (level = v)} testid="min-level" />
      <div class="modsel"><MultiSelect size="sm" bind:values={modules} options={MODULES.map((m) => ({ value: m, label: m }))} placeholder="all modules" allLabel="all modules" ariaLabel="Modules" testid="debug-module" /></div>
      <input class="input sm" style="width:200px" placeholder="Search" value={q} oninput={(e) => onQ(e.currentTarget.value)} aria-label="Search" />
      <input class="input sm" style="width:auto" type="datetime-local" bind:value={since} aria-label="Since" title="Since" />
      <label class="switch"><input type="checkbox" bind:checked={auto} />auto-refresh</label>
    </div>
    <Btn size="sm" icon="refresh" busy={busy} onclick={load} aria-label="Refresh" />
  </div>
  <div class="card-body">
    {#if error && !loaded}
      <div class="banner bad" role="alert"><Icon name="alert" /><div class="grow">{error}</div><Btn size="sm" onclick={load}>Retry</Btn></div>
    {:else if !loaded}
      <span class="skel" style="height:240px"></span>
    {:else if shown.length === 0}
      <Empty icon="bug" title="No lines" text="Nothing matches the filters." />
    {:else}
      {#if error}<div class="banner warn small" style="margin-bottom:8px">Refresh failed: {error}</div>{/if}
      <div class="lines" data-testid="debug-lines">
        {#each shown as l, i (i)}
          <div class="ln">
            <span class="ts">{l.ts.slice(5, 19).replace('T', ' ')}</span>
            {#if l.site}<span class="site">{l.site}</span>{/if}
            <span class="lv {lvTone[l.level]}">{l.level}</span>
            <span class="mod">{l.module}</span>
            <span class="msg ansi-line">{@html textHtml(l.msg, ansiPrefs.mode)}</span>
            {#if Object.keys(l.fields).length}<span class="f">{JSON.stringify(l.fields)}</span>{/if}
          </div>
        {/each}
      </div>
      <div class="row between" style="margin-top:10px">
        <span class="muted small">{shown.length} lines</span>
        {#if lines.length >= limit && limit < 5000}<Btn size="sm" onclick={() => (limit = Math.min(5000, limit + 500))} data-testid="load-older">Load older</Btn>{:else}<span class="faint small">{limit >= 5000 ? 'max 5000 lines' : 'oldest line reached'}</span>{/if}
      </div>
    {/if}
  </div>
</section>
{/if}

<style>
  .modsel {
    width: 220px;
  }
  .lines {
    font-family: var(--font-mono);
    font-size: 12px;
    line-height: 1.55;
    background: var(--log-bg);
    color: var(--log-text);
    border-radius: var(--r-md);
    padding: 8px 10px;
    max-height: 64vh;
    overflow: auto;
  }
  .ln {
    display: flex;
    gap: 8px;
    white-space: nowrap;
  }
  .ts {
    color: #73737e;
  }
  .site {
    color: #93c5fd;
  }
  .lv {
    width: 40px;
    flex: none;
    font-weight: 700;
  }
  .lv.faint {
    color: #73737e;
  }
  .lv.info {
    color: #60a5fa;
  }
  .lv.warn {
    color: #fbbf24;
  }
  .lv.bad {
    color: #f87171;
  }
  .mod {
    color: #a78bfa;
    width: 84px;
    flex: none;
  }
  .f {
    color: #a8a8b3;
  }
</style>
