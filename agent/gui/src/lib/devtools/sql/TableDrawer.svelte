<script lang="ts">
  /** Table detail (§21 #97): columns, indexes, constraints, DDL (tableDetail) + Select first 100 rows / Copy / Insert. */
  import type { TableDetailResponse } from '@opm/shared/devtools';
  import { errMessage } from '$lib/api';
  import { bytes } from '$lib/format';
  import { toasts } from '$lib/state/toast.svelte';
  import Drawer from '$lib/ui/Drawer.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import Editor from '../cm/Editor.svelte';
  import { dev } from '../api';
  import { copyText } from '../grid';

  interface Props {
    open: boolean;
    site: string;
    connectionId: string;
    kind: 'postgres' | 'sqlite' | 'cassandra';
    namespace: string;
    table: string;
    qualified: string;
    onselect: () => void;
    oninsert: (t: string) => void;
  }
  let { open = $bindable(false), site, connectionId, kind, namespace, table, qualified, onselect, oninsert }: Props = $props();
  let d = $state<TableDetailResponse | null>(null);
  let err = $state<string | null>(null);
  $effect(() => {
    if (!open) return;
    const k = `${namespace}.${table}`;
    d = null;
    err = null;
    dev
      .tableDetail({ siteSlug: site, connectionId, query: { namespace, table } })
      .then((r) => {
        if (`${namespace}.${table}` === k) d = r;
      })
      .catch((e) => (err = errMessage(e)));
  });
  async function copy(t: string) {
    if (await copyText(t)) toasts.ok('Copied');
  }
</script>

<Drawer bind:open title={qualified} subtitle={d ? `${d.columns.length} columns${d.rowEstimate !== null ? ` · ~${d.rowEstimate.toLocaleString()} rows` : ''}${d.sizeBytes !== null ? ` · ${bytes(d.sizeBytes)}` : ''}` : 'Loading…'} wide testid="table-drawer">
  <div class="stack">
    <div class="row gap-s wrap">
      <Btn variant="primary" size="sm" icon="play" onclick={() => ((open = false), onselect())} data-testid="td-select">Select first 100 rows</Btn>
      <Btn size="sm" variant="ghost" icon="copy" onclick={() => copy(qualified)}>Copy name</Btn>
      <Btn size="sm" variant="ghost" icon="arrowRight" onclick={() => ((open = false), oninsert(qualified))}>Insert into editor</Btn>
    </div>
    {#if err}<div class="banner bad">{err}</div>{/if}
    {#if d}
      <section>
        <h3 class="sec">Columns</h3>
        <div class="table-wrap boxed">
          <table class="table compact" data-testid="td-columns">
            <thead><tr><th>Name</th><th>Type</th><th>Null</th><th>Default</th>{#if kind === 'cassandra'}<th>Role</th>{/if}</tr></thead>
            <tbody>
              {#each d.columns as c (c.name)}
                <tr><td class="mono">{c.primaryKey ? '🔑 ' : ''}{c.name}</td><td class="mono faint">{c.type}</td><td>{c.nullable ? 'yes' : 'no'}</td><td class="mono small">{c.default ?? ''}</td>{#if kind === 'cassandra'}<td class="small">{c.role ?? ''}</td>{/if}</tr>
              {/each}
            </tbody>
          </table>
        </div>
      </section>
      {#if d.indexes.length}
        <section>
          <h3 class="sec">Indexes</h3>
          <div class="table-wrap boxed">
            <table class="table compact" data-testid="td-indexes">
              <tbody>
                {#each d.indexes as i (i.name)}
                  <tr><td class="mono">{i.name}{#if i.primary}<span class="badge accent">primary</span>{:else if i.unique}<span class="badge outline">unique</span>{/if}</td><td class="mono small faint">{i.definition}</td></tr>
                {/each}
              </tbody>
            </table>
          </div>
        </section>
      {/if}
      {#if d.constraints.length}
        <section>
          <h3 class="sec">Constraints</h3>
          <div class="table-wrap boxed">
            <table class="table compact">
              <tbody>
                {#each d.constraints as c (c.name)}
                  <tr><td class="mono">{c.name}</td><td class="small">{c.type}</td><td class="mono small faint">{c.definition}</td></tr>
                {/each}
              </tbody>
            </table>
          </div>
        </section>
      {/if}
      {#if d.ddl}
        <section>
          <div class="row gap-s"><h3 class="sec grow">DDL</h3><Btn size="xs" variant="ghost" icon="copy" onclick={() => copy(d!.ddl!)}>Copy</Btn></div>
          <div class="ddl"><Editor value={d.ddl} lang={kind} readonly height="260px" testid="td-ddl" ariaLabel="DDL" /></div>
        </section>
      {/if}
    {:else if !err}
      <span class="skel" style="width:100%;height:200px"></span>
    {/if}
  </div>
</Drawer>

<style>
  .sec {
    margin: 4px 0 6px;
    font-size: var(--fs-sm);
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--text-3);
  }
  .wrap {
    flex-wrap: wrap;
  }
  .compact :global(td),
  .compact :global(th) {
    padding: 5px 10px;
  }
  .ddl {
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    overflow: hidden;
  }
  .badge {
    margin-left: 6px;
  }
</style>
