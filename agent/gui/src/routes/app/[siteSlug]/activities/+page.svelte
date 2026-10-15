<script lang="ts">
  import { untrack } from 'svelte';
  import { base } from '$app/paths';
  import { page } from '$app/state';
  import { api, errMessage } from '$lib/api';
  import { overview } from '$lib/state/app.svelte';
  import { ACTIVITY_TYPES, type Activity } from '@opm/shared/types';
  import { fmtDateTime, jsonPretty } from '$lib/format';
  import PageHead from '$lib/ui/PageHead.svelte';
  import Empty from '$lib/ui/Empty.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Time from '$lib/ui/Time.svelte';
  import Drawer from '$lib/ui/Drawer.svelte';
  import Stale from '$lib/ui/Stale.svelte';
  import CopyId from '$lib/ui/CopyId.svelte';
  import Combobox from '$lib/ui/Combobox.svelte';
  import { agentActivityMeta } from '$lib/agent/activity';
  import AnsiLog from '$lib/ui/AnsiLog.svelte';
  import { logTailPrefix } from '$lib/ansi.svelte';

  /** §21 #87 / #93: update.failed data.failures[] (containerSlug, error, hint?, exitCode?, logTail[]) */
  type FailData = { containerSlug: string; error?: string; hint?: string | null; exitCode?: number; restartCount?: number; oomKilled?: boolean; keptAs?: string | null; logTail?: string[] };
  function failuresOf(a: Activity | null): FailData[] {
    const d = a?.data as { failures?: unknown } | null | undefined;
    return Array.isArray(d?.failures) ? (d.failures as FailData[]).filter((f) => f && typeof f.containerSlug === 'string') : [];
  }

  const slug = $derived(page.params.siteSlug ?? '*');
  let type = $state('');
  let actor = $state('');
  let items = $state<Activity[]>([]);
  let cursor = $state<string | null>(null);
  let loading = $state(false);
  let loaded = $state(false);
  let error = $state<string | null>(null);
  let sel = $state<Activity | null>(null);
  let selOpen = $state(false);
  let gen = 0;

  async function load(more = false) {
    const my = ++gen;
    loading = true;
    try {
      const r = await api.siteActivities({ siteSlug: slug, query: { cursor: more ? (cursor ?? undefined) : undefined, limit: 100, type: type.trim() || undefined, actor: actor.trim() || undefined } });
      if (my !== gen) return;
      items = more ? [...items, ...r.items.filter((a) => !items.some((x) => x.activityId === a.activityId))] : r.items;
      cursor = r.cursor;
      error = null;
      loaded = true;
    } catch (e) {
      if (my === gen) error = errMessage(e);
    } finally {
      if (my === gen) loading = false;
    }
  }
  $effect(() => {
    void slug;
    untrack(() => {
      loaded = false;
      items = [];
      load();
    });
  });
  let t: ReturnType<typeof setTimeout>;
  function filterChanged() {
    clearTimeout(t);
    t = setTimeout(() => load(), 300);
  }
  const prefixes = [...new Set(ACTIVITY_TYPES.map((x) => x.split('.')[0] + '.'))];
  const typeOpts = [...prefixes.map((p) => ({ value: p, hint: 'prefix', group: 'prefixes' })), ...ACTIVITY_TYPES.map((t) => ({ value: t, group: 'types' }))];
  let recent = $state<string[]>([]);
  $effect(() => {
    api
      .recentActors({ query: { limit: 20 } })
      .then((r) => (recent = r.actors.map((x) => x.email)))
      .catch(() => (recent = []));
  });
  const actorOpts = $derived([...new Set([...recent, ...items.map((a) => a.actor).filter((x): x is string => !!x)])].map((e) => ({ value: e })));
  const stale = $derived(slug === '*' ? (overview.data?.sites.filter((s) => !s.reachable && s.enrolled) ?? []) : []);
  const selMeta = $derived(sel ? agentActivityMeta(sel.type, sel.data as Record<string, unknown>) : null);
  const originTone = { agent: 'accent', cloud: 'info', system: '' } as const;
</script>

<PageHead icon="activity" eyebrow={slug === '*' ? 'All sites (merged by time)' : `Site ${slug}`} title="Activities" sub="The append-only, hash-chained activity log. Every action carries the declared email.">
  {#snippet actions()}
    <a class="btn" href="{base}/app/{slug}/activities/download" data-testid="download-bundle"><Icon name="download" size={14} />Download bundle…</a>
  {/snippet}
</PageHead>

{#each stale as s (s.siteSlug)}
  <div class="banner warn small" style="margin-bottom:10px"><Icon name="wifiOff" /><div class="grow">Site <strong>{s.siteSlug}</strong> does not answer; its entries are missing from the merged list.</div><Stale lastSeenAt={s.lastSeenAt} /></div>
{/each}

<section class="card">
  <div class="card-head filters">
    <div class="row grow">
      <Combobox size="sm" mono allowCustom width="220px" label="Type prefix" placeholder="Type or prefix (update.)" value={type} options={typeOpts} onchange={(v) => ((type = v), filterChanged())} testid="filter-type" />
      <Combobox size="sm" allowCustom width="240px" label="Actor" placeholder="Actor email" value={actor} options={actorOpts} onchange={(v) => ((actor = v), filterChanged())} testid="filter-actor" />
      {#if type || actor}<Btn size="sm" variant="ghost" onclick={() => ((type = ''), (actor = ''), load())}>Clear</Btn>{/if}
    </div>
    <Btn size="sm" icon="refresh" busy={loading} onclick={() => load()} aria-label="Refresh" />
  </div>
  {#if !loaded && error}
    <div class="card-body"><div class="banner bad" role="alert"><Icon name="alert" /><div class="grow">{error}</div><Btn size="sm" onclick={() => load()}>Retry</Btn></div></div>
  {:else if !loaded}
    <div class="card-body stack" aria-busy="true">{#each [1, 2, 3, 4, 5] as i (i)}<span class="skel"></span>{/each}</div>
  {:else if items.length === 0}
    <Empty icon="activity" title="No activities" text={type || actor ? 'Nothing matches the filters.' : 'Nothing is logged yet. Logins, commits, merges, updates and snapshots appear here as they happen.'} />
  {:else}
    <div class="table-wrap">
      <table class="table" data-testid="activities">
        <thead><tr><th>Site</th><th class="num">Seq</th><th>Time</th><th>Actor</th><th>Origin</th><th>Type</th><th>Summary</th></tr></thead>
        <tbody>
          {#each items as a (a.activityId)}
            {@const am = agentActivityMeta(a.type, a.data as Record<string, unknown>)}
            <tr class="clickable" onclick={() => ((sel = a), (selOpen = true))} tabindex="0" onkeydown={(e) => e.key === 'Enter' && ((sel = a), (selOpen = true))}>
              <td class="mono small">{a.siteSlug}</td>
              <td class="num mono small">{a.seq}</td>
              <td class="small"><Time ts={a.ts} /></td>
              <td class="small">{a.actor ?? '—'}</td>
              <td><span class="badge {originTone[a.origin]}">{a.origin}</span></td>
              <td class="small">
                {#if am}
                  <span class="badge {am.tone} at" data-testid="act-type-{a.type}"><Icon name={am.icon} size={12} />{am.label}</span>
                  <div class="mono tiny faint">{a.type}</div>
                {:else}<span class="mono">{a.type}</span>{/if}
              </td>
              <td class="small">{a.summary}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
    <div class="card-body row between">
      <span class="muted small">{items.length} shown</span>
      {#if cursor}<Btn size="sm" busy={loading} onclick={() => load(true)} data-testid="load-more">Load more</Btn>{:else}<span class="faint small">end of the log</span>{/if}
    </div>
  {/if}
</section>

<Drawer bind:open={selOpen} title={sel ? `${sel.type}` : ''} subtitle={sel ? `${sel.siteSlug} · seq ${sel.seq}` : ''} testid="activity-drawer">
  {#if sel}
    <div class="stack">
      <p>{sel.summary}</p>
      <dl class="kv">
        <dt>Activity id</dt><dd><CopyId value={sel.activityId} /></dd>
        <dt>Time</dt><dd>{fmtDateTime(sel.ts)} <span class="faint">({sel.ts})</span></dd>
        <dt>Actor</dt><dd>{sel.actor ?? 'system'}</dd>
        <dt>Origin</dt><dd>{sel.origin}</dd>
        <dt>Uploaded</dt><dd>{sel.uploadedVia ? `${sel.uploadedVia} · ${fmtDateTime(sel.uploadedAt)}` : 'not yet in the cloud'}</dd>
        <dt>prevHash</dt><dd><code class="h">{sel.prevHash ?? 'null (first entry)'}</code></dd>
        <dt>hash</dt><dd><code class="h">{sel.hash}</code></dd>
      </dl>
      {#if selMeta}
        <div class="section-title">{selMeta.label}</div>
        <dl class="kv" data-testid="activity-facts">
          {#each selMeta.facts as [k, v] (k)}<dt>{k}</dt><dd class:mono={/^(Version|Upgrade id|Request|Run|Failed version|Restored)$/.test(k) || k.includes('.')}>{v}</dd>{/each}
        </dl>
      {/if}
      {#each failuresOf(sel) as f (f.containerSlug)}
        <div class="section-title">Failed container · {f.containerSlug}</div>
        <div class="stack fail" data-testid="activity-failure-{f.containerSlug}">
          {#if f.error}<div class="field-err">{f.error}</div>{/if}
          {#if f.hint}<div class="banner warn small" data-testid="activity-hint-{f.containerSlug}"><Icon name="info" /><div class="grow"><strong>Likely cause:</strong> {f.hint}</div></div>{/if}
          {#if f.exitCode !== undefined}<div class="small muted">exit code {f.exitCode}{f.oomKilled ? ' · OOM killed' : ''}{f.restartCount !== undefined ? ` · ${f.restartCount} restarts` : ''}{f.keptAs ? ` · kept as ${f.keptAs}` : ''}</div>{/if}
          {#if f.logTail?.length}<AnsiLog lines={f.logTail} prefix={logTailPrefix} toggles testid="activity-logs-{f.containerSlug}" />{/if}
        </div>
      {/each}
      <div class="section-title">data</div>
      <pre class="code">{jsonPretty(sel.data)}</pre>
    </div>
  {/if}
</Drawer>

<style>
  .at {
    white-space: nowrap;
  }
  .filters {
    gap: 8px;
  }
  .h {
    font-size: 11px;
    overflow-wrap: anywhere;
  }
</style>
