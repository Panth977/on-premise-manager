<script lang="ts">
  /**
   * Dev tools › Connections (§7.6, §21 #97): presets of the head (read-only, "Duplicate to edit locally", problem
   * marks) and manual connections (create / edit / delete), with the remembered pooled state, Test, Disconnect and a
   * link into the workbench of each.
   */
  import { DEVTOOL_KINDS, devToolTargetText, type DevToolConnection, type DevToolKind, type TestConnectionResponse } from '@opm/shared/devtools';
  import { toasts } from '$lib/state/toast.svelte';
  import { relTime } from '$lib/format';
  import PageHead from '$lib/ui/PageHead.svelte';
  import Icon from '$lib/ui/Icon.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import Select from '$lib/ui/Select.svelte';
  import Confirm from '$lib/ui/Confirm.svelte';
  import DevGate from '$lib/devtools/DevGate.svelte';
  import ConnectionDrawer from '$lib/devtools/ConnectionDrawer.svelte';
  import { dev } from '$lib/devtools/api';
  import { devStore } from '$lib/devtools/state.svelte';
  import { DEVTOOL_KIND_LABEL, KIND_ICON, KIND_SHORT, connHref, stateText, stateTone } from '$lib/devtools/kinds';

  let drawer = $state(false);
  let editing = $state<DevToolConnection | null>(null);
  let newKind = $state<DevToolKind>('postgres');
  let del = $state<DevToolConnection | null>(null);
  let delOpen = $state(false);
  let busy = $state<string | null>(null);
  let tests = $state<Record<string, TestConnectionResponse>>({});

  function add(k: string) {
    editing = null;
    newKind = k as DevToolKind;
    drawer = true;
  }
  async function test(site: string, c: DevToolConnection) {
    busy = `t:${c.id}`;
    try {
      const r = await dev.testConnection({ siteSlug: site, connectionId: c.id });
      tests = { ...tests, [c.id]: r };
      await devStore.refreshConns();
    } catch (e) {
      toasts.err(e, `Test of ${c.name} failed`);
    } finally {
      busy = null;
    }
  }
  async function disconnect(site: string, c: DevToolConnection) {
    busy = `d:${c.id}`;
    try {
      await dev.disconnect({ siteSlug: site, connectionId: c.id });
      toasts.ok(`${c.name}: disconnected`);
      await devStore.refreshConns();
    } catch (e) {
      toasts.err(e, 'Disconnect failed');
    } finally {
      busy = null;
    }
  }
  async function duplicate(site: string, c: DevToolConnection) {
    busy = `c:${c.id}`;
    try {
      const d = await dev.duplicateConnection({ siteSlug: site, connectionId: c.id, body: { name: `${c.name} (local)` } });
      devStore.upsert(d);
      toasts.ok(`Duplicated as ${d.name}`);
      editing = d;
      drawer = true;
    } catch (e) {
      toasts.err(e, 'Duplicate failed');
    } finally {
      busy = null;
    }
  }
  async function remove(site: string) {
    const c = del;
    if (!c) return;
    try {
      await dev.deleteConnection({ siteSlug: site, connectionId: c.id });
      devStore.remove(c.id);
      toasts.ok(`Deleted ${c.name}`);
    } catch (e) {
      toasts.err(e, 'Delete failed');
    }
  }
  const kindOpts = DEVTOOL_KINDS.map((k) => ({ value: k, label: DEVTOOL_KIND_LABEL[k], icon: KIND_ICON[k] }));
</script>

<DevGate what="Dev tools">
  {#snippet children(info, site)}
    <PageHead title="Dev tools" eyebrow="Connections · {site}" icon="plug" sub="Service GUIs whose queries the agent runs on the site server — every query is logged to the site's activities (redacted, never result data).">
      {#snippet actions()}
        <div class="addsel"><Select value={null} placeholder="Add connection…" options={kindOpts} onchange={add} ariaLabel="Add a connection of kind" testid="add-conn" /></div>
      {/snippet}
    </PageHead>
    <p class="hint lim" data-testid="dev-limits">
      Limits on this site: timeout {info.limits.statementTimeoutSec} s · {info.limits.maxRows.toLocaleString()} rows per page ({info.limits.bufferRows.toLocaleString()} kept for paging / export) · {info.limits.maxConcurrent} running at once · idle connections close after {Math.round(info.limits.idleTimeoutSec / 60)} min.
    </p>
    <div class="table-wrap boxed">
      <table class="table conns" data-testid="conn-table">
        <thead>
          <tr><th>Connection</th><th>Target</th><th>State</th><th class="right">Actions</th></tr>
        </thead>
        <tbody>
          {#each devStore.conns as c (c.id)}
            {@const t = tests[c.id]}
            <tr data-testid="conn-row-{c.id}" class:problem={!!c.problem}>
              <td>
                <div class="nm">
                  <span class="ki" title={DEVTOOL_KIND_LABEL[c.kind]}><Icon name={KIND_ICON[c.kind]} size={16} /></span>
                  <div class="nmt">
                    <a class="strong" href={connHref(site, c)} data-testid="conn-open-{c.id}">{c.name}</a>
                    <div class="row tight wrap">
                      <span class="faint xs">{KIND_SHORT[c.kind]}</span>
                      {#if c.source === 'preset'}<span class="badge violet" title="From the cloud configuration (template / deployment); read-only here">preset · from revision seq {c.presetRevSeq ?? '?'}</span>{:else}<span class="badge outline" title={c.createdBy ? `created by ${c.createdBy}` : undefined}>manual</span>{/if}
                      {#if c.readOnly}<span class="badge amber"><Icon name="lock" size={11} />read-only</span>{/if}
                    </div>
                    {#if c.description}<span class="faint xs desc">{c.description}</span>{/if}
                  </div>
                </div>
                {#if c.problem}<div class="bad-text small prob" data-testid="conn-problem-{c.id}"><Icon name="alert" size={13} /> {c.problem}</div>{/if}
              </td>
              <td>
                <div class="mono small">{devToolTargetText(c.target)}</div>
                {#if c.resolvedTarget && c.resolvedTarget !== devToolTargetText(c.target)}<div class="faint xs mono">{c.resolvedTarget}</div>{/if}
              </td>
              <td>
                <span class="badge {stateTone(c)}" title={c.state.lastError ?? undefined} data-testid="conn-state-{c.id}"><i class="dot"></i>{c.state.status}</span>
                <div class="faint xs st">{stateText(c).replace(/^(connected|idle|error)( · )?/, '')}{c.state.lastUsedAt ? `${stateText(c).includes('·') ? ' · ' : ''}used ${relTime(c.state.lastUsedAt)}` : ''}{c.state.runningQueries ? ` · ${c.state.runningQueries} running` : ''}</div>
                {#if t}<div class="small {t.ok ? 'ok-text' : 'bad-text'}" data-testid="conn-test-{c.id}">{t.ok ? `ok · ${t.latencyMs} ms` : t.message}</div>{/if}
              </td>
              <td class="right">
                <div class="acts">
                  <a class="btn sm" href={connHref(site, c)}><Icon name="play" size={13} />Open</a>
                  <Btn size="sm" variant="ghost" icon="plug" busy={busy === `t:${c.id}`} onclick={() => test(site, c)} data-testid="conn-testbtn-{c.id}">Test</Btn>
                  {#if c.state.status === 'connected'}<Btn size="sm" variant="ghost" icon="power" busy={busy === `d:${c.id}`} onclick={() => disconnect(site, c)} aria-label="Disconnect {c.name}" title="Disconnect (close the pooled client)"></Btn>{/if}
                  {#if c.source === 'preset'}
                    <Btn size="sm" variant="ghost" icon="copy" busy={busy === `c:${c.id}`} onclick={() => duplicate(site, c)} data-testid="conn-dup-{c.id}">Duplicate to edit locally</Btn>
                    <Btn size="sm" variant="ghost" icon="eye" onclick={() => ((editing = c), (drawer = true))} data-testid="conn-view-{c.id}" aria-label="View {c.name}" title="View"></Btn>
                  {:else}
                    <Btn size="sm" variant="ghost" icon="edit" onclick={() => ((editing = c), (drawer = true))} data-testid="conn-edit-{c.id}" aria-label="Edit {c.name}" title="Edit"></Btn>
                    <Btn size="sm" variant="ghost" icon="trash" onclick={() => ((del = c), (delOpen = true))} data-testid="conn-del-{c.id}" aria-label="Delete {c.name}" title="Delete"></Btn>
                  {/if}
                </div>
              </td>
            </tr>
          {:else}
            <tr><td colspan="4" class="faint">No connection on {site} yet — add one, or define presets in the cloud configuration.</td></tr>
          {/each}
        </tbody>
      </table>
    </div>
    <ConnectionDrawer bind:open={drawer} {site} {info} conn={editing} kind={newKind} onsaved={(c) => (devStore.upsert(c), devStore.refreshConns())} />
    <Confirm bind:open={delOpen} title="Delete {del?.name}?" confirmLabel="Delete" danger onconfirm={() => remove(site)} testid="conn-del-confirm">
      <p>The connection and its stored credential are removed from the agent of <span class="mono">{site}</span>. Saved queries and the history stay.</p>
    </Confirm>
  {/snippet}
</DevGate>

<style>
  .addsel {
    width: 210px;
  }
  .lim {
    margin: -6px 0 12px;
  }
  .conns td {
    vertical-align: top;
  }
  .nm {
    display: flex;
    gap: 10px;
    align-items: flex-start;
    min-width: 220px;
  }
  .ki {
    width: 30px;
    height: 30px;
    border-radius: var(--r-md);
    background: var(--accent-soft);
    color: var(--accent-text);
    display: grid;
    place-items: center;
    flex: none;
  }
  .nmt {
    display: flex;
    flex-direction: column;
    gap: 3px;
    min-width: 0;
  }
  .row.wrap {
    flex-wrap: wrap;
    gap: 4px 6px;
  }
  .desc {
    max-width: 380px;
  }
  .prob {
    margin-top: 6px;
  }
  tr.problem td {
    background: color-mix(in srgb, var(--red-soft) 40%, transparent);
  }
  .st {
    margin-top: 3px;
  }
  .acts {
    display: inline-flex;
    gap: 4px;
    flex-wrap: wrap;
    justify-content: flex-end;
  }
  @media (max-width: 760px) {
    .conns thead {
      display: none;
    }
    .conns tr {
      display: grid;
      grid-template-columns: 1fr;
      border-bottom: 1px solid var(--border);
    }
    .conns td {
      border: 0;
      padding: 6px 12px;
    }
    .conns td.right {
      text-align: left;
      padding-bottom: 12px;
    }
    .acts {
      justify-content: flex-start;
    }
    .addsel {
      width: 100%;
    }
  }
</style>
