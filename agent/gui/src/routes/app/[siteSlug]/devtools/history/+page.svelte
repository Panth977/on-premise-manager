<script lang="ts">
  /**
   * Dev tools › History (§21 #97 g): every devtools.query activity of the site — filters kind / connection / mine /
   * ok-failed; time, actor, connection, op, redacted statement (expand), duration, rows / status, error; "Open in
   * workbench" (the statement is the REDACTED text of the activity).
   */
  import { untrack } from 'svelte';
  import { page } from '$app/state';
  import { goto } from '$app/navigation';
  import { DEVTOOL_KINDS, type DevToolHistoryItem, type DevToolKind } from '@opm/shared/devtools';
  import { appHref } from '$lib/nav';
  import { toasts } from '$lib/state/toast.svelte';
  import { fmtDateTime, relTime } from '$lib/format';
  import PageHead from '$lib/ui/PageHead.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Select from '$lib/ui/Select.svelte';
  import SegmentedControl from '$lib/ui/SegmentedControl.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import DevGate from '$lib/devtools/DevGate.svelte';
  import { dev } from '$lib/devtools/api';
  import { devStore } from '$lib/devtools/state.svelte';
  import { DEVTOOL_KIND_LABEL, KIND_ICON, KIND_SHORT, loadJson, pageOfKind, saveJson } from '$lib/devtools/kinds';
  import { copyText } from '$lib/devtools/grid';
  import { fromHistory } from '$lib/devtools/http/http';

  const slug = $derived(page.params.siteSlug ?? '*');
  let kind = $state<string>('');
  let connId = $state<string>(page.url.searchParams.get('c') ?? '');
  let mine = $state(false);
  type OkF = 'all' | 'ok' | 'failed';
  let okf = $state<OkF>('all');
  let items = $state<DevToolHistoryItem[] | null>(null);
  let cursor = $state<string | null>(null);
  let busy = $state(false);
  let open = $state<Record<string, boolean>>({});
  async function load(more = false) {
    if (slug === '*') return;
    busy = true;
    try {
      const r = await dev.history({ siteSlug: slug, query: { kind: (kind || undefined) as DevToolKind | undefined, connectionId: connId || undefined, mine: mine || undefined, cursor: more ? (cursor ?? undefined) : undefined, limit: 100 } });
      items = more ? [...(items ?? []), ...r.items] : r.items;
      cursor = r.cursor;
    } catch (e) {
      toasts.err(e, 'History could not be loaded');
      items ??= [];
    } finally {
      busy = false;
    }
  }
  $effect(() => {
    void slug;
    void kind;
    void connId;
    void mine;
    untrack(() => load());
  });
  const shown = $derived((items ?? []).filter((h) => okf === 'all' || (okf === 'ok' ? h.ok : !h.ok)));
  const connOpts = $derived([{ value: '', label: 'All connections' }, ...devStore.conns.filter((c) => !kind || c.kind === kind).map((c) => ({ value: c.id, label: c.name, icon: KIND_ICON[c.kind], hint: KIND_SHORT[c.kind] }))]);
  const kindOpts = [{ value: '', label: 'All kinds' }, ...DEVTOOL_KINDS.map((k) => ({ value: k, label: DEVTOOL_KIND_LABEL[k], icon: KIND_ICON[k] }))];

  /** open the statement in its workbench (SQL: a new editor tab; HTTP: a request tab; Redis: the console history) */
  function openIn(h: DevToolHistoryItem) {
    const p = pageOfKind(h.kind);
    if (!devStore.byId(h.connectionId)) return toasts.push('bad', 'The connection no longer exists', h.connectionName);
    if (p === 'sql') {
      const k = `opm.dev.sql.${slug}.${h.connectionId}`;
      const st = loadJson<{ tabs: { id: string; title: string; text: string; saved: null; params: string[] }[]; active: string } | null>(k, null) ?? { tabs: [], active: '' };
      const n = Math.max(0, ...st.tabs.map((t) => Number(/^t(\d+)$/.exec(t.id)?.[1] ?? 0))) + 1;
      st.tabs.push({ id: `t${n}`, title: `history ${new Date(h.ts).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}`, text: h.statement, saved: null, params: [] });
      st.active = `t${n}`;
      saveJson(k, st);
    } else if (p === 'redis') {
      const k = `opm.dev.redis.hist.${slug}.${h.connectionId}`;
      saveJson(k, [...loadJson<string[]>(k, []).filter((x) => x !== h.statement), h.statement]);
    } else if (p === 'http') {
      const k = `opm.dev.http.${slug}.${h.connectionId}`;
      const st = loadJson<{ tabs: { id: string; title: string; spec: unknown; saved: null }[]; active: string } | null>(k, null) ?? { tabs: [], active: '' };
      const n = Math.max(0, ...st.tabs.map((t) => Number(/^r(\d+)$/.exec(t.id)?.[1] ?? 0))) + 1;
      st.tabs.push({ id: `r${n}`, title: `history ${new Date(h.ts).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}`, spec: fromHistory(h.statement), saved: null });
      st.active = `r${n}`;
      saveJson(k, st);
    } else void copyText(h.statement);
    goto(appHref(slug, `devtools/${p}?c=${encodeURIComponent(h.connectionId)}`));
  }
</script>

<DevGate what="the dev tools history">
  {#snippet children(_info, site)}
    <PageHead title="History" eyebrow="Dev tools · {site}" icon="history" sub="Every query, command, request and publish run from the dev tools on this site (devtools.query activities: redacted statement, no result data)." />
    <div class="filters" data-testid="dh-filters">
      <div class="f"><Select value={kind} options={kindOpts} onchange={(v) => ((kind = v), (connId = ''))} size="sm" ariaLabel="Kind" testid="dh-kind" /></div>
      <div class="f wide"><Select value={connId} options={connOpts} onchange={(v) => (connId = v)} size="sm" ariaLabel="Connection" testid="dh-conn" searchable /></div>
      <SegmentedControl options={[{ value: 'all' as OkF, label: 'All' }, { value: 'ok' as OkF, label: 'OK' }, { value: 'failed' as OkF, label: 'Failed' }]} value={okf} onchange={(v) => (okf = v)} label="Outcome" size="sm" testid="dh-ok" />
      <label class="switch small"><input type="checkbox" bind:checked={mine} data-testid="dh-mine" />Only mine</label>
      <span class="grow"></span>
      <Btn size="sm" variant="ghost" icon="refresh" busy={busy} onclick={() => load()}>Refresh</Btn>
      <a class="btn sm ghost" href={appHref(site, 'activities')}><Icon name="activity" size={13} />All activities</a>
    </div>
    <div class="table-wrap boxed">
      <table class="table hist" data-testid="dh-table">
        <thead><tr><th>Time</th><th>Who</th><th>Connection</th><th>Statement</th><th class="r">Result</th><th></th></tr></thead>
        <tbody>
          {#if items === null}
            <tr><td colspan="6"><span class="skel" style="width:100%"></span></td></tr>
          {:else}
            {#each shown as h (h.activityId)}
              <tr class:failed={!h.ok} data-testid="dh-row">
                <td class="nowrap" title={fmtDateTime(h.ts)}>{relTime(h.ts)}</td>
                <td class="small who">{h.actor ?? 'system'}{#if h.via}<div class="faint xs">via {h.via}</div>{/if}</td>
                <td class="nowrap"><span class="cn"><Icon name={KIND_ICON[h.kind]} size={14} />{h.connectionName}</span><div class="faint xs"><span class="op">{h.op}</span></div></td>
                <td class="stc">
                  <button class="st mono" class:open={open[h.activityId]} onclick={() => (open = { ...open, [h.activityId]: !open[h.activityId] })} aria-expanded={!!open[h.activityId]} title="Expand">{h.statement}</button>
                  {#if h.error}<div class="bad-text small">{h.error}</div>{/if}
                </td>
                <td class="r nowrap small">
                  <span class="badge {h.ok ? 'green' : 'red'}">{h.ok ? 'ok' : 'failed'}</span>
                  <div class="faint xs">{h.durationMs} ms{h.rows !== null ? ` · ${h.rows} rows` : ''}{h.status !== null ? ` · ${h.status}` : ''}</div>
                </td>
                <td class="r"><Btn size="xs" variant="ghost" icon="arrowRight" onclick={() => openIn(h)} data-testid="dh-open">Open</Btn></td>
              </tr>
            {:else}
              <tr><td colspan="6" class="faint">No dev tools activity matches.</td></tr>
            {/each}
          {/if}
        </tbody>
      </table>
    </div>
    {#if cursor}<div class="more"><Btn size="sm" variant="ghost" busy={busy} onclick={() => load(true)}>Load more</Btn></div>{/if}
  {/snippet}
</DevGate>

<style>
  .filters {
    display: flex;
    gap: 8px;
    align-items: center;
    flex-wrap: wrap;
    margin-bottom: 12px;
  }
  .f {
    width: 170px;
  }
  .f.wide {
    width: 240px;
  }
  .hist td {
    vertical-align: top;
  }
  .cn {
    display: inline-flex;
    gap: 6px;
    align-items: center;
    font-weight: var(--fw-medium);
  }
  .op {
    text-transform: uppercase;
    font-size: 10px;
    letter-spacing: 0.04em;
  }
  .who {
    max-width: 180px;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .stc {
    min-width: 260px;
    max-width: 560px;
  }
  .st {
    all: unset;
    cursor: pointer;
    display: -webkit-box;
    -webkit-line-clamp: 2;
    line-clamp: 2;
    -webkit-box-orient: vertical;
    overflow: hidden;
    white-space: pre-wrap;
    word-break: break-word;
    font-size: 12px;
  }
  .st.open {
    display: block;
    -webkit-line-clamp: unset;
    line-clamp: unset;
  }
  tr.failed td {
    background: color-mix(in srgb, var(--red-soft) 35%, transparent);
  }
  .r {
    text-align: right;
  }
  .more {
    display: flex;
    justify-content: center;
    margin-top: 10px;
  }
  @media (max-width: 760px) {
    .hist thead {
      display: none;
    }
    .hist tr {
      display: grid;
      grid-template-columns: 1fr auto;
      border-bottom: 1px solid var(--border);
    }
    .hist td {
      border: 0;
      padding: 4px 12px;
    }
    .stc {
      grid-column: 1 / -1;
      min-width: 0;
    }
    .f,
    .f.wide {
      width: calc(50% - 4px);
    }
  }
</style>
